package winbet

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/common"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy: WinBet 策略
//
// 策略核心：通过价格移动速度决定开单，目标是在一个周期中尽可能多开对冲单，每一对对冲单锁定小利润。
// 当周期交易上千次时，小利润累积变得可观。
//
// 策略逻辑：
// - 监控 UP/DOWN 价格变化速度
// - 当某一侧速度超过阈值时，触发交易：
//   - Entry: 买入速度更快的一侧（FAK 订单，立即成交）
//   - Hedge: 买入对侧（GTC 限价单，等待成交）
//
// 新架构特性：
// 1. 订单更新回调：通过 TradingService.OnOrderUpdate() 注册，实时跟踪订单状态
// 2. 成本基础跟踪：Position 支持多次成交累加，自动计算平均价格和盈亏
// 3. 订单跟踪：跟踪订单状态，处理订单失败等情况
// 4. 周期管理：OnCycle() 统一处理周期切换，无需手动对比 slug
// 5. 订单执行模式：支持顺序（sequential）或并发（parallel）执行
type Strategy struct {
	TradingService       *services.TradingService
	BinanceFuturesKlines *services.BinanceFuturesKlines
	Config               `yaml:",inline" json:",inline"`

	// 库存计算器（用于库存偏斜机制）
	inventoryCalculator *common.InventoryCalculator

	autoMerge common.AutoMergeController

	// 未对冲的 Entry 订单（当 Hedge 订单失败时记录）
	unhedgedEntries map[string]*domain.Order

	mu sync.Mutex // 保护共享状态
	// 避免在周期切换/重复 Subscribe 时重复注册 handler（OrderEngine handler 列表不去重）
	orderUpdateOnce sync.Once

	// 价格样本：用于计算速度
	samples map[domain.TokenType][]sample

	// 周期状态管理
	firstSeenAt          time.Time         // 首次看到价格的时间
	lastTriggerAt        time.Time         // 上次触发时间（用于冷却）
	tradedThisCycle      bool              // 本周期是否已交易（兼容旧逻辑）
	tradesCountThisCycle int               // 本周期已完成的交易次数（Entry + Hedge 都成交才算完成）
	pendingTrades        map[string]string // 未完成的交易：key=entryOrderID, value=hedgeOrderID（Entry 已成交但 Hedge 未成交）

	// 方向级别的去重：避免同一方向在短时间内重复触发
	lastTriggerSide   domain.TokenType
	lastTriggerSideAt time.Time

	// 日志限流：避免短时间内重复打印相同的日志
	lastCooldownLogSide   domain.TokenType
	lastCooldownLogAt     time.Time
	cooldownLogThrottleMs int64 // 日志限流时间（毫秒），默认 5 秒

	// 价格日志限流：避免价格更新太频繁导致日志刷屏
	lastPriceLogToken      domain.TokenType
	lastPriceLogAt         time.Time
	lastPriceLogPriceCents int
	priceLogThrottleMs     int64 // 价格日志限流时间（毫秒），默认 1 秒

	// 市场跳过日志限流：避免市场过滤日志刷屏
	lastMarketSkipLogAt time.Time // 上次市场跳过日志打印时间

	// 价格事件日志限流：避免价格事件日志刷屏
	lastPriceEventLogAt time.Time // 上次价格事件日志打印时间
	lastWarmupLogAt     time.Time // 上次预热期日志打印时间

	// 订单簿价格日志：实时打印 UP/DOWN 的 bid/ask
	lastOrderBookLogAt     time.Time
	orderBookLogThrottleMs int64 // 订单簿价格日志限流时间（毫秒），默认 2 秒

	// 订单跟踪：利用本地订单状态管理（新架构特性）
	lastEntryOrderID     string                   // 最后下单的 Entry 订单ID
	lastHedgeOrderID     string                   // 最后下单的 Hedge 订单ID
	lastEntryOrderStatus domain.OrderStatus       // Entry 订单状态
	pendingOrders        map[string]*domain.Order // 待确认的订单（通过订单ID跟踪）

	// 出场（平仓）节流：避免短时间重复下 SELL
	lastExitAt         time.Time
	lastExitCheckAt    time.Time
	lastBalanceCheckAt time.Time // 上次持仓平衡检查时间

	// 分批止盈状态：key=positionID，value=已触发的 level 索引集合
	partialTPDone map[string]map[int]bool

	// 追踪止盈状态：key=positionID
	trailing map[string]*trailState

	// Binance bias 状态（每周期）
	cycleStartMs int64
	biasReady    bool
	biasToken    domain.TokenType
	biasReason   string

	// 市场过滤：只处理当前配置的市场（防止误交易）
	marketSlugPrefix string

	// 全局配置约束（从全局配置读取）
	minOrderSize float64 // 最小订单金额（USDC）
	minShareSize float64 // 限价单最小 share 数量

	// 市场精度信息（从配置文件加载）
	currentPrecision *MarketPrecisionInfo

	// UI 相关状态（独立于周期切换）
	uiCtx     context.Context    // UI 独立的 context，不会被周期切换取消
	uiCancel  context.CancelFunc // UI context 的取消函数
	uiOnce    sync.Once          // 确保 UI 只启动一次
	uiRunning bool               // UI 是否正在运行
	uiMu      sync.Mutex         // 保护 UI 状态
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }

func (s *Strategy) Defaults() error { return nil }

func (s *Strategy) Validate() error { return s.Config.Validate() }

// Initialize 初始化策略
//
// 初始化步骤：
// 1. 初始化内部数据结构（samples, pendingOrders）
// 2. 读取全局配置，验证市场配置
// 3. 设置市场过滤前缀（防止误交易）
// 4. 设置全局约束（minOrderSize, minShareSize）
// 5. 注册订单更新回调（新架构特性）
func (s *Strategy) Initialize() error {
	// 1. 初始化内部数据结构
	if s.samples == nil {
		s.samples = make(map[domain.TokenType][]sample)
	}
	if s.pendingOrders == nil {
		s.pendingOrders = make(map[string]*domain.Order)
	}
	if s.partialTPDone == nil {
		s.partialTPDone = make(map[string]map[int]bool)
	}
	if s.trailing == nil {
		s.trailing = make(map[string]*trailState)
	}
	if s.pendingTrades == nil {
		s.pendingTrades = make(map[string]string)
	}

	// 2. 读取全局 market 配置：用于过滤 slug（防止误处理非目标市场）
	gc := config.Get()
	if gc == nil {
		return fmt.Errorf("[%s] 全局配置未加载：拒绝启动（避免误交易到非目标市场）", ID)
	}
	sp, err := gc.Market.Spec()
	if err != nil {
		return fmt.Errorf("[%s] 读取 market 配置失败：%w（拒绝启动，避免误交易）", ID, err)
	}

	// 3. 验证 timeframe（当前仅支持 15m / 1h）
	if sp.Timeframe != "15m" && sp.Timeframe != "1h" {
		return fmt.Errorf("[%s] 当前仅支持 timeframe=15m/1h（收到 %q）", ID, sp.Timeframe)
	}

	// 4. 设置市场过滤前缀（优先用配置里显式指定的 slugPrefix；否则用 spec 推导）
	prefix := strings.TrimSpace(gc.Market.SlugPrefix)
	if prefix == "" {
		prefix = sp.SlugPrefix()
	}
	s.marketSlugPrefix = strings.ToLower(strings.TrimSpace(prefix))
	if s.marketSlugPrefix == "" {
		return fmt.Errorf("[%s] marketSlugPrefix 为空：拒绝启动（避免误交易）", ID)
	}

	// 5. 设置全局约束（从全局配置读取）
	s.minOrderSize = gc.MinOrderSize
	s.minShareSize = gc.MinShareSize
	if s.minOrderSize <= 0 {
		s.minOrderSize = 1.1 // 默认值
	}
	if s.minShareSize <= 0 {
		s.minShareSize = 5.0 // 默认值
	}

	// 6. 初始化日志限流（避免短时间内重复打印相同的日志）
	if s.cooldownLogThrottleMs <= 0 {
		s.cooldownLogThrottleMs = 5000 // 默认 5 秒
	}
	s.lastCooldownLogSide = ""
	s.lastCooldownLogAt = time.Time{}

	// 7. 初始化价格日志限流（避免价格更新太频繁导致日志刷屏）
	if s.priceLogThrottleMs <= 0 {
		s.priceLogThrottleMs = 1000 // 默认 1 秒
	}
	s.lastPriceLogToken = ""
	s.lastPriceLogAt = time.Time{}
	s.lastPriceLogPriceCents = 0
	s.lastMarketSkipLogAt = time.Time{}
	s.lastPriceEventLogAt = time.Time{}
	s.lastWarmupLogAt = time.Time{}

	// 7.5 初始化订单簿价格日志限流（避免频繁调用 API）
	if s.orderBookLogThrottleMs <= 0 {
		s.orderBookLogThrottleMs = 2000 // 默认 2 秒
	}
	s.lastOrderBookLogAt = time.Time{}

	// 8. 从配置读取市场精度信息（系统级配置）
	if gc.Market.Precision != nil {
		s.currentPrecision = &MarketPrecisionInfo{
			TickSize:     gc.Market.Precision.TickSize,
			MinOrderSize: gc.Market.Precision.MinOrderSize,
			NegRisk:      gc.Market.Precision.NegRisk,
		}
		log.Infof("✅ [%s] 从配置加载市场精度信息: tick_size=%s min_order_size=%s neg_risk=%v",
			ID, s.currentPrecision.TickSize, s.currentPrecision.MinOrderSize, s.currentPrecision.NegRisk)
	} else {
		log.Warnf("⚠️ [%s] 配置中未设置市场精度信息，将使用默认值", ID)
	}

	// 6. 注册订单更新回调（新架构特性：利用本地订单状态管理）
	// 当订单状态更新时（通过 WebSocket 或 API 同步），立即更新本地状态
	if s.TradingService != nil {
		s.orderUpdateOnce.Do(func() {
			handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
			s.TradingService.OnOrderUpdate(handler)
			log.Infof("✅ [%s] 已注册订单更新回调（利用本地订单状态管理）", ID)
		})

		// 初始化库存计算器（用于库存偏斜机制）
		s.inventoryCalculator = common.NewInventoryCalculator(s.TradingService)
		if s.Config.InventoryThreshold > 0 {
			log.Infof("✅ [%s] 库存偏斜机制已启用，阈值=%.2f shares", ID, s.Config.InventoryThreshold)
		}
	}

	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	log.Infof("✅ [%s] 策略已订阅价格变化事件 (session=%s)", ID, session.Name)

	// ✅ 添加启动诊断信息
	if s.TradingService != nil {
		currentMarket := s.TradingService.GetCurrentMarket()
		log.Infof("📊 [%s] 当前市场: %s, 市场过滤前缀: %s", ID, currentMarket, s.marketSlugPrefix)
		log.Infof("📊 [%s] 配置参数: windowSeconds=%d minMoveCents=%d minVelocityCentsPerSec=%.3f warmupMs=%d",
			ID, s.WindowSeconds, s.MinMoveCents, s.MinVelocityCentsPerSec, s.WarmupMs)
	} else {
		log.Warnf("⚠️ [%s] TradingService 为 nil", ID)
	}

	// 在 Subscribe 时也注册订单更新回调（兜底方案，确保回调已注册）
	// 因为此时 TradingService 肯定已经注入，且周期切换时会重新调用 Subscribe
	if s.TradingService != nil {
		s.orderUpdateOnce.Do(func() {
			handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
			s.TradingService.OnOrderUpdate(handler)
			log.Infof("✅ [%s] 已注册订单更新回调（Subscribe 兜底）", ID)
		})
	} else {
		log.Warnf("⚠️ [%s] TradingService 为 nil，无法注册订单更新回调", ID)
	}
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	// 启动UI（如果启用）
	// 注意：UI 使用独立的 context，不会被周期切换取消
	// 使用 sync.Once 确保 UI 只启动一次
	if s.Config.EnableUI && s.TradingService != nil {
		s.uiOnce.Do(func() {
			// 创建独立的 UI context，不会被周期切换取消
			// 这个 context 只有在策略真正关闭时（通过 Shutdown）才会被取消
			s.uiMu.Lock()
			s.uiCtx, s.uiCancel = context.WithCancel(context.Background())
			s.uiRunning = true
			s.uiMu.Unlock()

			// UI启动信息（会被重定向到文件）
			log.Infof("✅ [%s] UI已启用，正在启动UI（独立context，不受周期切换影响）...", ID)

			// 在独立的 goroutine 中启动 UI，避免阻塞 Run 方法
			// UI 会使用独立的 context，不会被周期切换取消
			// 注意：bubbletea 在 goroutine 中运行时可能无法捕获终端信号
			// 我们通过频繁检查 context（每50ms）来确保能够快速响应关闭信号
			go func() {
				// 等待一小段时间，确保其他初始化完成
				// 但不要等待太久，避免用户等待
				time.Sleep(200 * time.Millisecond)

				defer func() {
					s.uiMu.Lock()
					s.uiRunning = false
					s.uiMu.Unlock()
					// UI 退出时记录日志（会被重定向到文件）
					log.Infof("✅ [%s] UI已退出", ID)
				}()

				if err := s.RunUI(s.uiCtx); err != nil {
					// 错误已经记录在 RunUI 中
					log.Errorf("❌ [%s] UI运行失败: %v", ID, err)
				}
			}()
		})
	} else {
		if !s.Config.EnableUI {
			log.Debugf("⚠️ [%s] UI未启用: enableUI=false", ID)
		} else if s.TradingService == nil {
			log.Warnf("⚠️ [%s] UI未启动: TradingService为nil", ID)
		}
	}

	// 等待当前周期的 context 取消（周期切换时会取消）
	// UI 在独立的 goroutine 中运行，不会阻塞这里
	<-ctx.Done()
	return ctx.Err()
}

// OnCycle 周期切换回调（框架层统一调用）
//
// 新架构特性：
// - 无需手动对比 slug，框架会自动处理周期切换
// - 统一在这里重置周期相关的状态
//
// 重置内容：
// 1. 价格样本（samples）
// 2. 周期状态（firstSeenAt, tradedThisCycle, tradesCountThisCycle）
// 3. 方向去重状态（lastTriggerSide, lastTriggerSideAt）
// 4. Binance bias 状态（cycleStartMs, biasReady, biasToken, biasReason）
// 5. 订单跟踪（lastEntryOrderID, lastHedgeOrderID, pendingOrders）
//
// 注意：不清 lastTriggerAt，避免周期切换瞬间重复触发
func (s *Strategy) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 重置价格样本
	s.samples = make(map[domain.TokenType][]sample)

	// 重置周期状态
	s.firstSeenAt = time.Now()
	s.tradedThisCycle = false
	s.tradesCountThisCycle = 0 // 重置交易计数
	if s.pendingTrades == nil {
		s.pendingTrades = make(map[string]string)
	} else {
		// 清空未完成的交易记录
		for k := range s.pendingTrades {
			delete(s.pendingTrades, k)
		}
	}

	// 重置方向去重状态
	s.lastTriggerSide = ""
	s.lastTriggerSideAt = time.Time{}

	// 重置日志限流状态
	s.lastCooldownLogSide = ""
	s.lastCooldownLogAt = time.Time{}
	s.lastMarketSkipLogAt = time.Time{}

	// 重置 Binance bias 状态
	s.cycleStartMs = 0
	s.biasReady = false
	s.biasToken = ""
	s.biasReason = ""

	// 重置订单跟踪（周期切换时清理）
	s.lastEntryOrderID = ""
	s.lastHedgeOrderID = ""
	s.lastEntryOrderStatus = ""
	s.pendingOrders = make(map[string]*domain.Order)
	s.lastExitAt = time.Time{}
	s.lastExitCheckAt = time.Time{}
	s.lastBalanceCheckAt = time.Time{}
	s.partialTPDone = make(map[string]map[int]bool)
	s.trailing = make(map[string]*trailState)

	// 市场精度信息从配置文件加载，无需在运行时获取

	// 注意：不清 lastTriggerAt，避免周期切换瞬间重复触发
}

// OnOrderUpdate 订单更新回调（新架构特性：利用本地订单状态管理）
//
// 功能：
// - 实时跟踪订单状态变化（通过 WebSocket 或 API 同步）
// - 更新本地订单跟踪状态
// - 处理订单失败/取消（自动取消对应的 Hedge 订单）
// - 更新待确认订单列表
//
// 注意：
// - 只处理当前市场的订单（通过 marketSlugPrefix 过滤）
// - Entry 订单失败时，自动取消对应的 Hedge 订单
// - 仓位成本基础会自动更新（通过 OrderEngine），无需手动处理
func (s *Strategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	if order == nil || order.OrderID == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 只处理当前市场的订单（通过 marketSlugPrefix 过滤）
	if order.MarketSlug != "" && !strings.HasPrefix(strings.ToLower(order.MarketSlug), s.marketSlugPrefix) {
		return nil
	}

	// 更新本地订单跟踪
	if order.IsEntryOrder {
		// Entry 订单更新
		s.lastEntryOrderID = order.OrderID
		s.lastEntryOrderStatus = order.Status
		log.Debugf("📊 [%s] Entry 订单状态更新: orderID=%s status=%s filledSize=%.4f",
			ID, order.OrderID, order.Status, order.FilledSize)

		// Entry 订单失败时，自动取消对应的 Hedge 订单
		if order.Status == domain.OrderStatusFailed || order.Status == domain.OrderStatusCanceled {
			// 如果 Entry 订单失败/取消，从 pendingTrades 移除（如果已记录）
			if s.pendingTrades != nil {
				if _, exists := s.pendingTrades[order.OrderID]; exists {
					delete(s.pendingTrades, order.OrderID)
					log.Debugf("🔄 [%s] Entry 订单失败/取消，移除未完成交易记录: entryOrderID=%s", ID, order.OrderID)
				}
			}

			if order.HedgeOrderID != nil && *order.HedgeOrderID != "" {
				log.Infof("🔄 [%s] Entry 订单失败/取消，取消 Hedge 订单: entryOrderID=%s hedgeOrderID=%s",
					ID, order.OrderID, *order.HedgeOrderID)
				// 异步取消，避免阻塞回调
				go func(hedgeOrderID string) {
					_ = s.TradingService.CancelOrder(context.Background(), hedgeOrderID)
				}(*order.HedgeOrderID)
			}
		}

		// Entry 订单成交时，记录日志（用于顺序下单模式的成交检测）
		if order.Status == domain.OrderStatusFilled {
			log.Infof("✅ [%s] Entry 订单已成交（通过订单更新回调）: orderID=%s filledSize=%.4f",
				ID, order.OrderID, order.FilledSize)

			// 记录到 pendingTrades：Entry 已成交，等待 Hedge 成交
			// 只有当 Hedge 订单也成交后，才算完成一次交易
			if s.pendingTrades == nil {
				s.pendingTrades = make(map[string]string)
			}
			hedgeOrderID := ""
			if order.HedgeOrderID != nil {
				hedgeOrderID = *order.HedgeOrderID
			}
			if hedgeOrderID != "" {
				s.pendingTrades[order.OrderID] = hedgeOrderID
				log.Debugf("📝 [%s] 记录未完成交易: entryOrderID=%s hedgeOrderID=%s (等待 Hedge 成交)",
					ID, order.OrderID, hedgeOrderID)

				// ✅ 启动对冲单重下监控（如果对冲单未成交）- 支持并行和顺序模式
				// 注意：顺序模式在 entry_sequential.go 中已经启动监控，这里主要是为了并行模式
				if s.HedgeReorderTimeoutSeconds > 0 && s.TradingService != nil {
					// 获取市场信息（从订单中获取）
					marketSlug := order.MarketSlug
					if marketSlug == "" {
						log.Warnf("⚠️ [%s] Entry 订单缺少 MarketSlug，无法启动对冲单监控: orderID=%s", ID, order.OrderID)
					} else {
						// 获取对冲单信息
						hedgeOrder, ok := s.TradingService.GetOrder(hedgeOrderID)
						if !ok || hedgeOrder == nil {
							log.Warnf("⚠️ [%s] 无法获取对冲单信息，无法启动监控: hedgeOrderID=%s", ID, hedgeOrderID)
						} else {
							// 构建临时市场对象（需要从其他地方获取完整信息）
							// 这里先构建基本对象，监控函数中会使用 marketSlug
							market := &domain.Market{
								Slug:       marketSlug,
								YesAssetID: order.AssetID,
								NoAssetID:  hedgeOrder.AssetID,
							}

							// 计算 Entry 成交价格（cents）
							entryPriceCents := 0
							if order.FilledPrice != nil {
								entryPriceCents = order.FilledPrice.ToCents()
							} else {
								entryPriceCents = order.Price.ToCents()
							}

							// 启动监控（异步执行，避免阻塞回调）
							go func() {
								monitorCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
								defer cancel()

								s.monitorAndReorderHedge(
									monitorCtx,
									market,
									order.OrderID,
									hedgeOrderID,
									hedgeOrder.AssetID,
									hedgeOrder.Price,
									hedgeOrder.Size,
									time.Now(), // Entry 成交时间
									order.FilledSize,
									entryPriceCents,
									order.TokenType,
								)
							}()
							log.Infof("✅ [%s] 已启动对冲单监控（Entry 成交后，并行模式）: entryOrderID=%s hedgeOrderID=%s", ID, order.OrderID, hedgeOrderID)
						}
					}
				}
			}
		}
	} else if !order.IsEntryOrder && (order.OrderID == s.lastHedgeOrderID || s.pendingOrders[order.OrderID] != nil) {
		// Hedge 订单更新（通过 lastHedgeOrderID 或 pendingOrders 识别）
		s.lastHedgeOrderID = order.OrderID
		log.Debugf("📊 [%s] Hedge 订单状态更新: orderID=%s status=%s filledSize=%.4f",
			ID, order.OrderID, order.Status, order.FilledSize)

		// Hedge 订单成交时，记录 Info 级别日志（重要）
		if order.Status == domain.OrderStatusFilled {
			log.Infof("✅ [%s] Hedge 订单已成交（通过订单更新回调）: orderID=%s filledSize=%.4f",
				ID, order.OrderID, order.FilledSize)

			// ✅ 关键修复：Hedge 订单成交后，才算完成一次交易
			// 查找对应的 Entry 订单，从 pendingTrades 移除并增加交易计数
			if s.pendingTrades != nil {
				for entryOrderID, hedgeOrderID := range s.pendingTrades {
					if hedgeOrderID == order.OrderID {
						// Entry 和 Hedge 都成交了，完成一次交易
						delete(s.pendingTrades, entryOrderID)
						s.tradesCountThisCycle++
						log.Infof("✅ [%s] 交易完成（Entry+Hedge 都成交）: entryOrderID=%s hedgeOrderID=%s, 本周期已完成交易数=%d/%d",
							ID, entryOrderID, order.OrderID, s.tradesCountThisCycle, s.MaxTradesPerCycle)
						break
					}
				}
			}

			// 如果 Hedge 订单成交，检查是否有对应的未对冲 Entry 订单，如果有则移除
			if s.unhedgedEntries != nil {
				for entryOrderID, entryOrder := range s.unhedgedEntries {
					if entryOrder.HedgeOrderID != nil && *entryOrder.HedgeOrderID == order.OrderID {
						log.Infof("✅ [%s] Hedge 订单已成交，移除未对冲记录: entryOrderID=%s hedgeOrderID=%s",
							ID, entryOrderID, order.OrderID)
						delete(s.unhedgedEntries, entryOrderID)
					}
				}
			}

			// ✅ 优化：检查Entry单是否已平仓，如果已平仓则立即平掉Hedge单持仓
			if order.HedgeOrderID != nil && *order.HedgeOrderID != "" {
				entryOrderID := *order.HedgeOrderID
				if entryOrder, ok := s.TradingService.GetOrder(entryOrderID); ok && entryOrder != nil {
					// 检查Entry单是否已平仓（通过持仓检查）
					// 如果Entry单已成交，检查是否有对应的持仓
					if entryOrder.Status == domain.OrderStatusFilled {
						// 检查Entry单对应的持仓是否还存在
						entryTokenType := entryOrder.TokenType
						marketSlug := entryOrder.MarketSlug

						// 异步检查，避免阻塞回调
						go func() {
							checkCtx, checkCancel := context.WithTimeout(context.Background(), 3*time.Second)
							defer checkCancel()

							// 等待一小段时间，让持仓状态更新
							time.Sleep(200 * time.Millisecond)

							positions := s.TradingService.GetOpenPositionsForMarket(marketSlug)
							hasEntryPos := false
							var hedgePos *domain.Position

							for _, p := range positions {
								if p == nil || !p.IsOpen() || p.Size <= 0 {
									continue
								}
								if p.TokenType == entryTokenType {
									hasEntryPos = true
								} else if p.TokenType == opposite(entryTokenType) {
									// 这是Hedge单持仓
									hedgePos = p
								}
							}

							// 如果Entry单已平仓，但Hedge单还有持仓，立即平掉Hedge单
							if !hasEntryPos && hedgePos != nil {
								log.Warnf("🚨 [%s] 【风险检测】Hedge单成交但Entry单已平仓，立即平掉Hedge单持仓: hedgeOrderID=%s entryOrderID=%s",
									ID, order.OrderID, entryOrderID)

								// 获取market对象（从持仓中获取）
								if hedgePos.Market == nil {
									log.Warnf("⚠️ [%s] Hedge持仓缺少Market信息，无法平仓", ID)
									return
								}

								// 获取订单簿价格
								var exitPrice domain.Price
								var exitAssetID string
								if hedgePos.TokenType == domain.TokenTypeUp {
									yesBid, _, _, _, _, err := s.TradingService.GetTopOfBook(checkCtx, hedgePos.Market)
									if err != nil {
										log.Warnf("⚠️ [%s] 获取订单簿价格失败: %v", ID, err)
										return
									}
									exitPrice = yesBid
									exitAssetID = hedgePos.Market.YesAssetID
								} else {
									_, _, noBid, _, _, err := s.TradingService.GetTopOfBook(checkCtx, hedgePos.Market)
									if err != nil {
										log.Warnf("⚠️ [%s] 获取订单簿价格失败: %v", ID, err)
										return
									}
									exitPrice = noBid
									exitAssetID = hedgePos.Market.NoAssetID
								}

								if exitPrice.Pips <= 0 {
									log.Warnf("⚠️ [%s] 订单簿价格无效，无法平掉Hedge单持仓", ID)
									return
								}

								log.Infof("🔧 [%s] 平掉Hedge单持仓: token=%s size=%.4f price=%dc reason=entry_exited_before_hedge",
									ID, hedgePos.TokenType, hedgePos.Size, exitPrice.ToCents())

								// 创建平仓订单
								exitOrder := &domain.Order{
									MarketSlug: marketSlug,
									AssetID:    exitAssetID,
									TokenType:  hedgePos.TokenType,
									Side:       types.SideSell,
									Price:      exitPrice,
									Size:       hedgePos.Size,
									OrderType:  types.OrderTypeFAK,
									Status:     domain.OrderStatusPending,
									CreatedAt:  time.Now(),
								}

								// 提交平仓订单
								if _, err := s.TradingService.PlaceOrder(checkCtx, exitOrder); err != nil {
									log.Errorf("❌ [%s] 平掉Hedge单持仓失败: token=%s err=%v", ID, hedgePos.TokenType, err)
								} else {
									log.Infof("✅ [%s] 已平掉Hedge单持仓: token=%s size=%.4f", ID, hedgePos.TokenType, hedgePos.Size)
								}
							}
						}()
					}
				}
			}
		}

		// Hedge 订单失败时，检查对应的 Entry 订单是否已成交
		if order.Status == domain.OrderStatusFailed || order.Status == domain.OrderStatusCanceled {
			log.Warnf("⚠️ [%s] Hedge 订单失败/取消: orderID=%s status=%s",
				ID, order.OrderID, order.Status)

			// ✅ 修复：如果 Hedge 订单失败/取消，从 pendingTrades 移除（不增加交易计数）
			if s.pendingTrades != nil {
				for entryOrderID, hedgeOrderID := range s.pendingTrades {
					if hedgeOrderID == order.OrderID {
						delete(s.pendingTrades, entryOrderID)
						log.Warnf("⚠️ [%s] Hedge 订单失败/取消，移除未完成交易记录: entryOrderID=%s hedgeOrderID=%s (不增加交易计数)",
							ID, entryOrderID, order.OrderID)
						break
					}
				}
			}

			// ✅ 修复：对冲单（Hedge）在创建时会携带关联的 Entry 订单ID（order.HedgeOrderID）
			// 这里直接按关联 ID 查询（包含已成交订单），避免 GetActiveOrders 只含 openOrders 导致漏判。
			if s.TradingService != nil && order.HedgeOrderID != nil && *order.HedgeOrderID != "" {
				entryID := *order.HedgeOrderID
				if entryOrder, ok := s.TradingService.GetOrder(entryID); ok && entryOrder != nil {
					if entryOrder.Status == domain.OrderStatusFilled {
						// Entry 订单已成交，记录未对冲风险
						log.Errorf("🚨 [%s] 【风险警告】Hedge 订单失败但 Entry 订单已成交！Entry orderID=%s, Hedge orderID=%s",
							ID, entryOrder.OrderID, order.OrderID)
						if s.unhedgedEntries == nil {
							s.unhedgedEntries = make(map[string]*domain.Order)
						}
						s.unhedgedEntries[entryOrder.OrderID] = entryOrder
					}
				}
			}
		}
	} else {
		// 其他订单（可能是手动订单或其他策略的订单）
		// 检查是否是当前市场的订单，如果是，记录日志
		log.Debugf("📊 [%s] 收到其他订单更新: orderID=%s status=%s filledSize=%.4f tokenType=%s marketSlug=%s",
			ID, order.OrderID, order.Status, order.FilledSize, order.TokenType, order.MarketSlug)
	}

	// 更新待确认订单列表
	if order.Status == domain.OrderStatusFilled ||
		order.Status == domain.OrderStatusCanceled ||
		order.Status == domain.OrderStatusFailed {
		delete(s.pendingOrders, order.OrderID)
	} else if order.Status == domain.OrderStatusOpen || order.Status == domain.OrderStatusPending {
		s.pendingOrders[order.OrderID] = order
	}

	return nil
}

// OnPriceChanged 处理价格变化事件（策略核心逻辑）
//
// 处理流程：
// 1. 市场过滤：只处理目标市场
// 2. 周期检测：检测周期切换，更新 cycleStartMs
// 3. Binance bias：检查开盘 1m K 线 bias（如果启用）
// 4. 预热检查：检查是否在预热窗口内
// 5. 交易限制：检查冷却时间、交易次数限制
// 6. 速度计算：计算 UP/DOWN 价格变化速度
// 7. 触发判断：判断是否满足触发条件
// 8. 价格优先：如果启用，优先选择价格更高的一侧
// 9. 订单执行：根据配置选择顺序或并发执行
//
// 新架构特性：
// - 订单状态更新会通过 OnOrderUpdate() 回调自动处理
// - 仓位成本基础会通过 OrderEngine 自动更新（Position.AddFill()）
func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	// ✅ 立即记录所有价格事件到达（用于诊断，不限流确保能看到所有事件）
	if e == nil {
		log.Warnf("⚠️ [%s] OnPriceChanged 收到 nil 价格事件", ID)
		return nil
	}

	// ✅ 记录所有价格事件（不限流，确保能看到实时价格更新）
	if e.Market != nil {
		log.Infof("📥 [%s] 收到价格事件: market=%s token=%s price=%.4f (%dc)",
			ID, e.Market.Slug, e.TokenType, e.NewPrice.ToDecimal(), e.NewPrice.ToCents())
	} else {
		log.Warnf("⚠️ [%s] OnPriceChanged 收到价格事件但 Market 为 nil: token=%s price=%.4f (%dc)",
			ID, e.TokenType, e.NewPrice.ToDecimal(), e.NewPrice.ToCents())
	}

	if e == nil || e.Market == nil || s.TradingService == nil {
		if e == nil {
			log.Warnf("⚠️ [%s] 价格事件为 nil", ID)
		} else if e.Market == nil {
			log.Warnf("⚠️ [%s] 价格事件中的 Market 为 nil", ID)
		} else if s.TradingService == nil {
			log.Warnf("⚠️ [%s] TradingService 为 nil", ID)
		}
		return nil
	}
	s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)

	now := e.Timestamp
	if now.IsZero() {
		now = time.Now()
	}

	// 1. 市场过滤：只处理目标 market + 当前周期 market
	if !s.shouldHandleMarketEvent(e.Market) {
		// ✅ 添加调试日志（限流，避免刷屏）
		s.mu.Lock()
		lastSkipLogAt := s.lastMarketSkipLogAt
		s.mu.Unlock()
		if lastSkipLogAt.IsZero() || now.Sub(lastSkipLogAt) >= 5*time.Second {
			log.Infof("🔄 [%s] 跳过价格事件: market=%s token=%s price=%dc (市场过滤, 前缀=%s)",
				ID, e.Market.Slug, e.TokenType, e.NewPrice.ToCents(), s.marketSlugPrefix)
			if s.TradingService != nil {
				currentMarket := s.TradingService.GetCurrentMarket()
				log.Infof("  当前市场: %s", currentMarket)
			}
			s.mu.Lock()
			s.lastMarketSkipLogAt = now
			s.mu.Unlock()
		}
		return nil
	}

	// ✅ 记录通过市场过滤的价格事件
	log.Infof("✅ [%s] 处理价格事件: market=%s token=%s price=%.4f (%dc)",
		ID, e.Market.Slug, e.TokenType, e.NewPrice.ToDecimal(), e.NewPrice.ToCents())

	priceCents := e.NewPrice.ToCents()

	// 显示 WebSocket 实时价格（用于调试，带限流避免刷屏）
	s.maybeLogPriceUpdate(now, e.TokenType, e.NewPrice, e.Market.Slug)

	// ===== 实时订单簿价格日志 =====
	// 打印 UP/DOWN 的 bid/ask 价格（带限流，避免频繁调用 API）
	s.maybeLogOrderBook(now, e.Market)

	// ===== 出场（平仓）逻辑：优先于开仓 =====
	// 仅当启用 TP/SL/超时退出 且 当前 market 存在持仓时才触发（避免每个 tick 都打 orderbook）
	if s.maybeHandleExit(ctx, e.Market, now) {
		return nil
	}

	s.mu.Lock()

	if s.firstSeenAt.IsZero() {
		s.firstSeenAt = now
	}

	// 2. 周期检测：检测周期切换，更新 cycleStartMs
	// 尽量用 market.Timestamp 作为本周期起点（框架会从 slug 解析）
	s.updateCycleStartLocked(e.Market)

	// 3. Binance bias：检查开盘 1m K 线 bias（如果启用）
	// 可选：用"开盘第 1 根 1m K线阴阳"做 bias（hard/soft）
	if s.shouldSkipUntilBiasReadyLocked(now) {
		s.mu.Unlock()
		return nil
	}

	// 4. 预热检查：检查是否在预热窗口内
	if s.WarmupMs > 0 && now.Sub(s.firstSeenAt) < time.Duration(s.WarmupMs)*time.Millisecond {
		warmupRemaining := time.Duration(s.WarmupMs)*time.Millisecond - now.Sub(s.firstSeenAt)
		// ✅ 在预热期内也记录价格事件（限流，避免刷屏）
		s.mu.Lock()
		lastWarmupLogAt := s.lastWarmupLogAt
		s.mu.Unlock()
		if lastWarmupLogAt.IsZero() || time.Since(lastWarmupLogAt) >= 1*time.Second {
			log.Debugf("⏳ [%s] 预热中，跳过价格事件: token=%s price=%.4f (%dc) 剩余=%v",
				ID, e.TokenType, e.NewPrice.ToDecimal(), e.NewPrice.ToCents(), warmupRemaining)
			s.mu.Lock()
			s.lastWarmupLogAt = time.Now()
			s.mu.Unlock()
		}
		return nil
	}

	// ✅ 在速度计算之前，先更新样本（这样速度计算才能使用最新数据）
	if priceCents > 0 && priceCents < 100 {
		s.samples[e.TokenType] = append(s.samples[e.TokenType], sample{ts: now, priceCents: priceCents})
		s.pruneLocked(now)
	}

	// 4.5 周期结束前保护：在周期结束前 N 分钟不开新单（降低风险）
	// ✅ 同时检查持仓平衡，确保在周期结束前持仓数量相等
	var cycleEndTime time.Time
	if s.CycleEndProtectionMinutes > 0 && e.Market != nil && e.Market.Timestamp > 0 {
		// 获取周期时长（从全局配置或市场规格获取）
		cycleDuration := 15 * time.Minute // 默认 15 分钟
		if cfg := config.Get(); cfg != nil {
			if spec, err := cfg.Market.Spec(); err == nil {
				cycleDuration = spec.Duration()
			}
		}

		cycleStartTime := time.Unix(e.Market.Timestamp, 0)
		cycleEndTime = cycleStartTime.Add(cycleDuration)
		protectionTime := time.Duration(s.CycleEndProtectionMinutes) * time.Minute

		if now.After(cycleEndTime.Add(-protectionTime)) {
			// ✅ 在保护窗口内，检查持仓平衡并自动补齐缺失的 leg
			if s.TradingService != nil {
				positions := s.TradingService.GetOpenPositionsForMarket(e.Market.Slug)
				var upSize, downSize float64
				for _, p := range positions {
					if p != nil && p.IsOpen() && p.Size > 0 {
						if p.TokenType == domain.TokenTypeUp {
							upSize += p.Size
						} else if p.TokenType == domain.TokenTypeDown {
							downSize += p.Size
						}
					}
				}

				// 计算持仓差异（忽略小数，差 1 个 shares 无所谓）
				upSizeInt := int(upSize + 0.5)
				downSizeInt := int(downSize + 0.5)
				diff := upSizeInt - downSizeInt

				if diff != 0 && (upSizeInt > 0 || downSizeInt > 0) {
					// 持仓不平衡，需要补齐缺失的 leg
					s.mu.Unlock()
					log.Warnf("⚠️ [%s] 检测到持仓不平衡（周期结束前保护窗口）: UP=%d shares, DOWN=%d shares, 差异=%d",
						ID, upSizeInt, downSizeInt, diff)

					// 异步补齐缺失的 leg（避免阻塞价格更新）
					go s.rebalancePositions(context.Background(), e.Market, diff, upSizeInt, downSizeInt)
					return nil
				} else if diff == 0 && (upSizeInt > 0 || downSizeInt > 0) {
					// 持仓平衡，记录日志
					log.Debugf("✅ [%s] 持仓平衡检查通过: UP=%d shares, DOWN=%d shares",
						ID, upSizeInt, downSizeInt)
				}
			}

			s.mu.Unlock()
			log.Debugf("⏸️ [%s] 跳过：周期结束前保护（距离周期结束 %.1f 分钟）",
				ID, time.Until(cycleEndTime).Minutes())
			return nil
		}
	}

	// ✅ 实时检测订单情况，防止有风险敞口（定期检查持仓平衡）
	// 每 5 秒检查一次持仓平衡（避免频繁检查）
	s.mu.Lock()
	lastBalanceCheck := s.lastBalanceCheckAt
	s.mu.Unlock()
	if lastBalanceCheck.IsZero() || now.Sub(lastBalanceCheck) >= 5*time.Second {
		if s.TradingService != nil {
			positions := s.TradingService.GetOpenPositionsForMarket(e.Market.Slug)
			var upSize, downSize float64
			for _, p := range positions {
				if p != nil && p.IsOpen() && p.Size > 0 {
					if p.TokenType == domain.TokenTypeUp {
						upSize += p.Size
					} else if p.TokenType == domain.TokenTypeDown {
						downSize += p.Size
					}
				}
			}

			// 计算持仓差异（忽略小数，差 1 个 shares 无所谓）
			upSizeInt := int(upSize + 0.5)
			downSizeInt := int(downSize + 0.5)
			diff := upSizeInt - downSizeInt

			if diff != 0 && (upSizeInt > 0 || downSizeInt > 0) {
				// 持仓不平衡，记录警告
				log.Warnf("⚠️ [%s] 检测到持仓不平衡: UP=%d shares, DOWN=%d shares, 差异=%d",
					ID, upSizeInt, downSizeInt, diff)

				// 如果差异较大（>1），异步补齐缺失的 leg
				if abs(diff) > 1 {
					s.mu.Lock()
					s.lastBalanceCheckAt = now
					s.mu.Unlock()
					go s.rebalancePositions(context.Background(), e.Market, diff, upSizeInt, downSizeInt)
				}
			} else {
				s.mu.Lock()
				s.lastBalanceCheckAt = now
				s.mu.Unlock()
			}
		}
	}

	// 5. 交易限制检查：MaxTradesPerCycle 控制（0=不设限）
	// 注意：只有 Entry + Hedge 都成交才算完成一次交易
	// pendingTrades 记录 Entry 已成交但 Hedge 未成交的交易
	// 同时检查是否有正在提交的 Entry 订单（通过 lastEntryOrderID 和订单状态）
	pendingCount := 0
	if s.pendingTrades != nil {
		pendingCount = len(s.pendingTrades)
	}
	// ✅ 修复竞态条件：检查是否有正在提交/处理中的 Entry 订单
	// 如果 lastEntryOrderID 存在且订单状态不是 Filled/Failed/Canceled，说明有订单正在处理中
	submittingCount := 0
	if s.lastEntryOrderID != "" && s.TradingService != nil {
		if ord, ok := s.TradingService.GetOrder(s.lastEntryOrderID); ok && ord != nil {
			// 如果订单状态是 Pending/Open/Partial，说明还在处理中
			if ord.Status == domain.OrderStatusPending ||
				ord.Status == domain.OrderStatusOpen ||
				ord.Status == domain.OrderStatusPartial {
				// 检查这个订单是否已经在 pendingTrades 中
				if s.pendingTrades == nil || s.pendingTrades[s.lastEntryOrderID] == "" {
					submittingCount = 1
					log.Debugf("🔍 [%s] 检测到正在处理中的 Entry 订单: orderID=%s status=%s",
						ID, s.lastEntryOrderID, ord.Status)
				}
			}
		}
	}
	totalTrades := s.tradesCountThisCycle + pendingCount + submittingCount
	if s.MaxTradesPerCycle > 0 && totalTrades >= s.MaxTradesPerCycle {
		s.mu.Unlock()
		log.Debugf("🔄 [%s] 跳过：本周期交易次数已达上限 (已完成=%d, 进行中=%d, 提交中=%d, 总计=%d/%d)",
			ID, s.tradesCountThisCycle, pendingCount, submittingCount, totalTrades, s.MaxTradesPerCycle)
		return nil
	}
	// 5.3 冷却时间检查
	if !s.lastTriggerAt.IsZero() && now.Sub(s.lastTriggerAt) < time.Duration(s.CooldownMs)*time.Millisecond {
		s.mu.Unlock()
		return nil
	}

	// 6. 速度计算：计算 UP/DOWN 价格变化速度
	// 注意：样本已在前面更新（在预热检查之后）
	if priceCents <= 0 || priceCents >= 100 {
		s.mu.Unlock()
		return nil
	}

	// 计算 UP/DOWN 指标，选择"上行更快"的一侧触发
	mUp := s.computeLocked(domain.TokenTypeUp)
	mDown := s.computeLocked(domain.TokenTypeDown)

	// 获取当前价格（用于价格优先选择和价格记录）
	var upPriceCents, downPriceCents int
	upSamples := s.samples[domain.TokenTypeUp]
	downSamples := s.samples[domain.TokenTypeDown]
	if len(upSamples) > 0 {
		upPriceCents = upSamples[len(upSamples)-1].priceCents
	}
	if len(downSamples) > 0 {
		downPriceCents = downSamples[len(downSamples)-1].priceCents
	}

	// 根据 bias 调整阈值（soft）或直接只允许 bias 方向（hard）
	reqMoveUp := s.MinMoveCents
	reqMoveDown := s.MinMoveCents
	reqVelUp := s.MinVelocityCentsPerSec
	reqVelDown := s.MinVelocityCentsPerSec

	if s.UseBinanceOpen1mBias && s.biasToken != "" && s.BiasMode == "soft" {
		if s.biasToken == domain.TokenTypeUp {
			reqMoveDown += s.OppositeBiasMinMoveExtraCents
			reqVelDown *= s.OppositeBiasVelocityMultiplier
		} else if s.biasToken == domain.TokenTypeDown {
			reqMoveUp += s.OppositeBiasMinMoveExtraCents
			reqVelUp *= s.OppositeBiasVelocityMultiplier
		}
	}

	winner := domain.TokenType("")
	winMet := metrics{}
	allowUp := true
	allowDown := true
	if s.UseBinanceOpen1mBias && s.biasToken != "" && s.BiasMode == "hard" {
		allowUp = s.biasToken == domain.TokenTypeUp
		allowDown = s.biasToken == domain.TokenTypeDown
	}

	// 检查 UP 是否满足条件
	upQualified := allowUp && mUp.ok && mUp.delta >= reqMoveUp && mUp.velocity >= reqVelUp
	// 检查 DOWN 是否满足条件
	downQualified := allowDown && mDown.ok && mDown.delta >= reqMoveDown && mDown.velocity >= reqVelDown

	// 8. 价格优先选择逻辑（如果启用）
	// 当 UP/DOWN 都满足速度条件时，优先选择价格更高的一边
	// 因为订单簿是镜像的，速度通常相同，价格更高的胜率更大
	if s.PreferHigherPrice && upQualified && downQualified {
		// 两边都满足条件，优先选择价格更高的
		if upPriceCents > downPriceCents {
			winner = domain.TokenTypeUp
			winMet = mUp
		} else if downPriceCents > upPriceCents {
			winner = domain.TokenTypeDown
			winMet = mDown
		} else {
			// 价格相同，选择速度更快的（虽然通常相同）
			if mUp.velocity >= mDown.velocity {
				winner = domain.TokenTypeUp
				winMet = mUp
			} else {
				winner = domain.TokenTypeDown
				winMet = mDown
			}
		}
		// 如果配置了最小优先价格阈值，检查是否满足
		if s.MinPreferredPriceCents > 0 {
			winnerPrice := upPriceCents
			if winner == domain.TokenTypeDown {
				winnerPrice = downPriceCents
			}
			if winnerPrice < s.MinPreferredPriceCents {
				// 价格低于阈值，不触发
				winner = ""
			}
		}
	} else {
		// 只有一边满足条件，或未启用价格优先选择，使用原逻辑
		if upQualified {
			winner = domain.TokenTypeUp
			winMet = mUp
		}
		if downQualified {
			if winner == "" || mDown.velocity > winMet.velocity {
				winner = domain.TokenTypeDown
				winMet = mDown
			}
		}
		// 如果启用价格优先选择但只有一边满足，也检查价格阈值
		if s.PreferHigherPrice && winner != "" && s.MinPreferredPriceCents > 0 {
			winnerPrice := upPriceCents
			if winner == domain.TokenTypeDown {
				winnerPrice = downPriceCents
			}
			if winnerPrice < s.MinPreferredPriceCents {
				winner = ""
			}
		}
	}
	if winner == "" {
		s.mu.Unlock()
		return nil
	}

	// 方向级别的去重：避免同一方向在短时间内重复触发
	// 这可以显著减少 duplicate in-flight 错误
	if s.lastTriggerSide == winner && !s.lastTriggerSideAt.IsZero() {
		sideCooldown := time.Duration(s.CooldownMs) * time.Millisecond
		if sideCooldown <= 0 {
			sideCooldown = 2 * time.Second // 默认 2 秒
		}
		if now.Sub(s.lastTriggerSideAt) < sideCooldown {
			// 日志限流：避免短时间内重复打印相同的日志
			// 如果距离上次打印相同方向的冷却期日志超过 5 秒，才打印
			shouldLog := false
			if s.lastCooldownLogSide != winner || s.lastCooldownLogAt.IsZero() {
				shouldLog = true
			} else {
				logThrottle := time.Duration(s.cooldownLogThrottleMs) * time.Millisecond
				if logThrottle <= 0 {
					logThrottle = 5 * time.Second // 默认 5 秒
				}
				if now.Sub(s.lastCooldownLogAt) >= logThrottle {
					shouldLog = true
				}
			}
			if shouldLog {
				s.lastCooldownLogSide = winner
				s.lastCooldownLogAt = now
				// 降级为 Debug 级别，减少日志噪音（这是正常的去重行为）
				log.Debugf("🔄 [%s] 跳过：同一方向 %s 在冷却期内（距离上次触发 %.2fs，冷却时间 %.2fs）",
					ID, winner, now.Sub(s.lastTriggerSideAt).Seconds(), sideCooldown.Seconds())
			}
			s.mu.Unlock()
			return nil
		}
	}

	// 提前更新 lastTriggerSideAt（在下单之前），避免后续触发在策略层就跳过
	// 这样可以减少不必要的下单尝试，减少 duplicate in-flight 错误
	s.lastTriggerSide = winner
	s.lastTriggerSideAt = now

	// 5.5 库存偏斜检查：如果净持仓超过阈值，降低该方向的交易频率
	if s.Config.InventoryThreshold > 0 && s.inventoryCalculator != nil && e.Market != nil {
		shouldSkip := s.inventoryCalculator.CheckInventorySkew(e.Market.Slug, s.Config.InventoryThreshold, winner)
		if shouldSkip {
			// 计算净持仓详情（用于日志）
			result := s.inventoryCalculator.CalculateNetPosition(e.Market.Slug)
			s.mu.Unlock()
			log.Infof("🔄 [%s] 跳过：库存偏斜保护触发（方向=%s, 净持仓=%.2f, UP持仓=%.2f, DOWN持仓=%.2f, 阈值=%.2f）",
				ID, winner, result.NetPosition, result.UpInventory, result.DownInventory, s.Config.InventoryThreshold)
			return nil
		}
	}

	// 可选：用 Binance 1s "底层硬动"过滤（借鉴 momentum bot 的 move threshold 思路）
	if s.UseBinanceMoveConfirm {
		if s.BinanceFuturesKlines == nil {
			s.mu.Unlock()
			return nil
		}
		nowMs := now.UnixMilli()
		cur, okCur := s.BinanceFuturesKlines.Latest("1s")
		past, okPast := s.BinanceFuturesKlines.NearestAtOrBefore("1s", nowMs-int64(s.MoveConfirmWindowSeconds)*1000)
		if !okCur || !okPast || past.Close <= 0 {
			s.mu.Unlock()
			return nil
		}
		ret := (cur.Close - past.Close) / past.Close
		retBps := int(math.Abs(ret)*10000 + 0.5)
		dir := domain.TokenTypeDown
		if ret >= 0 {
			dir = domain.TokenTypeUp
		}
		if retBps < s.MinUnderlyingMoveBps || dir != winner {
			s.mu.Unlock()
			return nil
		}
	}

	// 放锁外做 IO（下单/拉盘口）
	// 备注：这里用一个小技巧：先把必要字段拷贝出来
	market := e.Market
	biasTok := s.biasToken
	biasReason := s.biasReason
	hedgeOffset := s.HedgeOffsetCents
	maxSpread := s.MaxSpreadCents
	orderSize := s.OrderSize
	hedgeSize := s.HedgeOrderSize
	minOrderSize := s.minOrderSize
	minShareSize := s.minShareSize
	minEntryPriceCents := s.MinEntryPriceCents
	maxEntryPriceCents := s.MaxEntryPriceCents

	// 调试：确认配置值是否正确加载
	if minEntryPriceCents > 0 || maxEntryPriceCents > 0 {
		log.Debugf("🔍 [%s] 配置值已加载: minEntryPriceCents=%dc maxEntryPriceCents=%dc",
			ID, minEntryPriceCents, maxEntryPriceCents)
	} else {
		log.Warnf("⚠️ [%s] 警告：价格区间配置未设置（minEntryPriceCents=%dc maxEntryPriceCents=%dc），将不进行价格区间检查",
			ID, minEntryPriceCents, maxEntryPriceCents)
	}

	s.mu.Unlock()

	// 在下单前检查本地订单状态（利用 OrderEngine 的本地状态）
	// 防止重复下单和并发问题
	if s.TradingService != nil {
		activeOrders := s.TradingService.GetActiveOrders()
		for _, order := range activeOrders {
			// 只检查当前市场的订单
			if order.MarketSlug != market.Slug {
				continue
			}
			// 检查是否相同方向且状态为 open/pending
			if order.TokenType == winner &&
				(order.Status == domain.OrderStatusOpen || order.Status == domain.OrderStatusPending) {
				log.Debugf("🔄 [%s] 发现已有相同方向的订单，取消旧订单: orderID=%s status=%s",
					ID, order.OrderID, order.Status)
				// 取消旧订单（不等待结果，异步执行）
				go func(orderID string) {
					_ = s.TradingService.CancelOrder(context.Background(), orderID)
				}(order.OrderID)
			}
		}
	}

	if hedgeSize <= 0 {
		hedgeSize = orderSize
	}
	if hedgeOffset <= 0 {
		hedgeOffset = 3
	}

	// 使用更短的超时时间（10秒），快速失败，避免阻塞策略
	// 如果 GetTopOfBook 超时，策略会立即返回，不阻塞后续的价格变化事件处理
	orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// ===== 市场质量 gate（提升胜率）=====
	// 在真正下单前先对盘口做一次质量评估，过滤：stale/partial/价差过大/镜像偏差等情况。
	if s.EnableMarketQualityGate != nil && *s.EnableMarketQualityGate {
		maxSpreadCentsGate := s.MarketQualityMaxSpreadCents
		if maxSpreadCentsGate <= 0 {
			maxSpreadCentsGate = maxSpread
		}
		if maxSpreadCentsGate <= 0 {
			maxSpreadCentsGate = 10
		}
		maxAgeMs := s.MarketQualityMaxBookAgeMs
		if maxAgeMs <= 0 {
			maxAgeMs = 3000
		}
		mq, mqErr := s.TradingService.GetMarketQuality(orderCtx, market, &services.MarketQualityOptions{
			MaxBookAge:     time.Duration(maxAgeMs) * time.Millisecond,
			MaxSpreadPips:  maxSpreadCentsGate * 100, // 1c=100 pips
			PreferWS:       true,
			FallbackToREST: true,
			AllowPartialWS: true,
		})
		if mqErr != nil {
			log.Debugf("⏭️ [%s] 跳过：MarketQuality 获取失败: %v", ID, mqErr)
			return nil
		}
		// 只检查 Score >= marketQualityMinScore，不使用 Tradable()（它硬编码要求 >= 60）
		// Tradable() 的 Complete/Fresh 检查已经在 GetMarketQuality 中处理
		if mq == nil || mq.Score < s.MarketQualityMinScore {
			// 计算每一项的扣分明细（用于分析）
			scoreBreakdown := ""
			if mq != nil && len(mq.Problems) > 0 {
				deductions := make(map[string]int)
				for _, problem := range mq.Problems {
					switch problem {
					case "incomplete_top":
						deductions[problem] = 50
					case "crossed_yes", "crossed_no":
						deductions[problem] = 40
					case "ws_partial":
						deductions[problem] = 35
					case "ws_stale":
						deductions[problem] = 25
					case "wide_spread_yes", "wide_spread_no":
						deductions[problem] = 20
					case "effective_price_failed":
						deductions[problem] = 20
					case "mirror_gap_buy_yes", "mirror_gap_buy_no":
						deductions[problem] = 10
					case "rest_failed":
						deductions[problem] = 15
					}
				}
				// 构建扣分明细字符串
				parts := make([]string, 0, len(deductions))
				for problem, points := range deductions {
					parts = append(parts, fmt.Sprintf("%s(-%d)", problem, points))
				}
				if len(parts) > 0 {
					scoreBreakdown = fmt.Sprintf(" 扣分明细: %s", strings.Join(parts, ", "))
				}
			}
			log.Debugf("⏭️ [%s] 跳过：MarketQuality gate 未通过: score=%d(min=%d) tradable=%v problems=%v source=%s%s",
				ID, func() int {
					if mq != nil {
						return mq.Score
					}
					return -1
				}(),
				s.MarketQualityMinScore,
				func() bool {
					if mq != nil {
						return mq.Tradable()
					}
					return false
				}(),
				func() []string {
					if mq != nil {
						return mq.Problems
					}
					return nil
				}(),
				func() string {
					if mq != nil {
						return mq.Source
					}
					return ""
				}(),
				scoreBreakdown,
			)
			return nil
		}
	}

	entryAsset := market.YesAssetID
	hedgeAsset := market.NoAssetID
	if winner == domain.TokenTypeDown {
		entryAsset = market.NoAssetID
		hedgeAsset = market.YesAssetID
	}

	// ===== 使用有效价格计算（考虑 Polymarket 订单簿的镜像特性）=====
	// 获取 YES 和 NO 的实际市场价格（同时获取，确保一致性）
	yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(orderCtx, market)
	if err != nil {
		log.Warnf("⚠️ [%s] 获取订单簿失败（快速失败，不阻塞策略）: %v", ID, err)
		return nil // 快速返回，不阻塞策略
	}

	// 转换为小数价格（用于计算）
	yesBidDec := yesBid.ToDecimal()
	yesAskDec := yesAsk.ToDecimal()
	noBidDec := noBid.ToDecimal()
	noAskDec := noAsk.ToDecimal()

	// 记录订单簿价格（Info 级别，方便调试）
	log.Infof("📊 [%s] 订单簿价格: YES bid=%.4f ask=%.4f, NO bid=%.4f ask=%.4f (source=%s)",
		ID, yesBidDec, yesAskDec, noBidDec, noAskDec, source)

	// 验证价格有效性
	if yesBidDec <= 0 || yesAskDec <= 0 || noBidDec <= 0 || noAskDec <= 0 {
		log.Warnf("⚠️ [%s] 订单簿价格无效: YES bid=%.4f ask=%.4f, NO bid=%.4f ask=%.4f",
			ID, yesBidDec, yesAskDec, noBidDec, noAskDec)
		return nil
	}

	// 根据 winner 确定 entry 和 hedge 的价格
	var entryAskDec, hedgeAskDec float64
	var entryBidDec, hedgeBidDec float64

	if winner == domain.TokenTypeUp {
		// Entry: 买 YES，Hedge: 买 NO
		entryBidDec = yesBidDec
		entryAskDec = yesAskDec
		hedgeBidDec = noBidDec
		hedgeAskDec = noAskDec
	} else {
		// Entry: 买 NO，Hedge: 买 YES
		entryBidDec = noBidDec
		entryAskDec = noAskDec
		hedgeBidDec = yesBidDec
		hedgeAskDec = yesAskDec
	}

	// ===== 价格选择（关键修复）=====
	// 目标：避免 “Entry 吃单 + Hedge 吃单” 造成双边点差成本，使得总成本 > 100c（结构性必亏）。
	//
	// 约束：
	// - Entry 是 FAK：必须使用订单簿实际 ask（taker）
	// - Hedge 是 GTC：应使用“互补挂单价格”在买一侧做 maker（由 hedgeOffsetCents 提供保护边际）
	entryAskCents := int(entryAskDec*100 + 0.5) // FAK 实际下单 ask（cents）
	entryBidCents := int(entryBidDec*100 + 0.5)
	hedgeBidCents := int(hedgeBidDec*100 + 0.5)
	hedgeAskCentsDirect := int(hedgeAskDec*100 + 0.5) // 对侧当前 ask（仅用于防止挂单穿价）

	// 基础验证
	if entryAskCents <= 0 || entryAskCents >= 100 || hedgeAskCentsDirect <= 0 || hedgeAskCentsDirect >= 100 {
		log.Debugf("⚠️ [%s] 订单簿价格无效: entryAsk=%dc hedgeAsk=%dc", ID, entryAskCents, hedgeAskCentsDirect)
		return nil
	}

	// Entry 价格区间检查：主 leg（entry）必须在此区间内才下单
	// ✅ 修复：双向持仓策略，如果当前选中的方向价格不在区间内，检查另一个方向是否满足条件
	// 记录配置值和当前价格用于调试（Info 级别，确保能看到）
	log.Infof("🔍 [%s] Entry 价格区间检查: winner=%s entryAsk=%dc, 配置范围=[%dc, %dc]",
		ID, winner, entryAskCents, minEntryPriceCents, maxEntryPriceCents)

	priceInRange := true
	if minEntryPriceCents > 0 && entryAskCents < minEntryPriceCents {
		priceInRange = false
		log.Infof("⏭️ [%s] %s 方向价格低于下限 (%dc < %dc)，检查另一个方向",
			ID, winner, entryAskCents, minEntryPriceCents)
	}
	if maxEntryPriceCents > 0 && entryAskCents > maxEntryPriceCents {
		priceInRange = false
		log.Infof("⏭️ [%s] %s 方向价格超过上限 (%dc > %dc)，检查另一个方向",
			ID, winner, entryAskCents, maxEntryPriceCents)
	}

	// 如果当前方向价格不在区间内，尝试切换到另一个方向
	if !priceInRange {
		// 检查另一个方向是否满足速度条件且价格在区间内
		alternativeWinner := domain.TokenType("")
		var alternativeEntryAskCents int
		var alternativeWinMet metrics

		if winner == domain.TokenTypeUp {
			// 当前是 UP，检查 DOWN
			if downQualified {
				alternativeWinner = domain.TokenTypeDown
				alternativeEntryAskCents = int(noAskDec*100 + 0.5)
				alternativeWinMet = mDown
			}
		} else {
			// 当前是 DOWN，检查 UP
			if upQualified {
				alternativeWinner = domain.TokenTypeUp
				alternativeEntryAskCents = int(yesAskDec*100 + 0.5)
				alternativeWinMet = mUp
			}
		}

		// 检查另一个方向的价格是否在区间内
		if alternativeWinner != "" {
			altPriceInRange := true
			if minEntryPriceCents > 0 && alternativeEntryAskCents < minEntryPriceCents {
				altPriceInRange = false
			}
			if maxEntryPriceCents > 0 && alternativeEntryAskCents > maxEntryPriceCents {
				altPriceInRange = false
			}

			if altPriceInRange {
				// 切换到另一个方向
				log.Infof("✅ [%s] 切换到 %s 方向（价格在区间内: %dc 在 [%dc, %dc]）",
					ID, alternativeWinner, alternativeEntryAskCents, minEntryPriceCents, maxEntryPriceCents)
				winner = alternativeWinner
				winMet = alternativeWinMet
				entryAskCents = alternativeEntryAskCents
				// 重新计算 entry 和 hedge 的价格
				if winner == domain.TokenTypeUp {
					entryBidDec = yesBidDec
					entryAskDec = yesAskDec
					hedgeBidDec = noBidDec
					hedgeAskDec = noAskDec
				} else {
					entryBidDec = noBidDec
					entryAskDec = noAskDec
					hedgeBidDec = yesBidDec
					hedgeAskDec = yesAskDec
				}
				entryAskCents = int(entryAskDec*100 + 0.5)
				entryBidCents = int(entryBidDec*100 + 0.5)
				hedgeBidCents = int(hedgeBidDec*100 + 0.5)
				hedgeAskCentsDirect = int(hedgeAskDec*100 + 0.5)
			} else {
				// 另一个方向也不在区间内，跳过
				log.Infof("⏭️ [%s] 两个方向价格都不在区间内: UP=%dc DOWN=%dc, 配置范围=[%dc, %dc]",
					ID, int(yesAskDec*100+0.5), int(noAskDec*100+0.5), minEntryPriceCents, maxEntryPriceCents)
				return nil
			}
		} else {
			// 另一个方向不满足速度条件，跳过
			log.Infof("⏭️ [%s] %s 方向价格不在区间内，且另一个方向不满足速度条件",
				ID, winner)
			return nil
		}
	}

	// 如果价格在区间内，也记录一下（用于确认检查已执行）
	if minEntryPriceCents > 0 || maxEntryPriceCents > 0 {
		log.Debugf("✅ [%s] Entry 价格在允许范围内: %dc 在 [%dc, %dc] 区间内",
			ID, entryAskCents, minEntryPriceCents, maxEntryPriceCents)
	}

	// 价差检查（使用实际价差，而非互补价）
	entrySpread := entryAskCents - entryBidCents
	if entrySpread < 0 {
		entrySpread = -entrySpread
	}
	if maxSpread > 0 && entrySpread > maxSpread {
		log.Debugf("⏭️ [%s] 跳过：价差过大 (%dc > %dc)", ID, entrySpread, maxSpread)
		return nil
	}

	// Hedge 挂单价格：互补挂单 = 100 - entryAsk - hedgeOffset
	// 这确保最坏情况下（hedge 以该限价成交）总成本 = 100 - hedgeOffset（留出 offset 作为边际）。
	hedgeLimitCents := 100 - entryAskCents - hedgeOffset
	if hedgeLimitCents <= 0 || hedgeLimitCents >= 100 {
		log.Debugf("⏭️ [%s] 跳过：Hedge 互补挂单价格无效: entryAsk=%dc hedgeOffset=%dc => hedgeLimit=%dc",
			ID, entryAskCents, hedgeOffset, hedgeLimitCents)
		return nil
	}
	// 防止“挂单穿价”变成 taker：买单价格必须严格小于当前 ask
	if hedgeLimitCents >= hedgeAskCentsDirect {
		hedgeLimitCents = hedgeAskCentsDirect - 1
	}
	if hedgeLimitCents <= 0 {
		log.Debugf("⏭️ [%s] 跳过：Hedge 挂单会穿价且无法降到有效区间: hedgeAsk=%dc", ID, hedgeAskCentsDirect)
		return nil
	}
	// 兼容下游变量命名：hedgeAskCents 在策略内一直代表“对冲腿下单价格（cents）”
	hedgeAskCents := hedgeLimitCents

	totalCostCents := entryAskCents + hedgeLimitCents
	if totalCostCents > 100 {
		// 理论上不会发生（互补价 + offset），但做最后一道保护，避免浮点/取整误差带来结构性必亏
		log.Debugf("⏭️ [%s] 跳过：总成本过高 (%dc > 100c): Entry=%dc + Hedge=%dc (bid=%dc ask=%dc)",
			ID, totalCostCents, entryAskCents, hedgeLimitCents, hedgeBidCents, hedgeAskCentsDirect)
		return nil
	}

	// 只检查 Entry 价格上限（Entry 是 FAK，价格固定）
	// 如果 Entry 价格过高（> 95c），记录警告但仍允许下单（由 maxEntryPriceCents 控制）
	if entryAskCents > 95 {
		log.Debugf("💰 [%s] Entry 价格较高: %dc (hedgeLimit=%dc, 总成本=%dc, source=%s)",
			ID, entryAskCents, hedgeLimitCents, totalCostCents, source)
	}

	// 最终下单价格
	entryPriceForFAK := domain.Price{Pips: entryAskCents * 100} // FAK：使用实际 ask
	hedgePrice := domain.Price{Pips: hedgeLimitCents * 100}     // GTC：互补挂单价（maker）
	entryPriceDec := entryPriceForFAK.ToDecimal()
	hedgeDec := hedgePrice.ToDecimal()

	log.Infof("💰 [%s] 价格选择: Entry FAK ask=%dc, Hedge GTC limit=%dc (hedgeOffset=%dc, hedgeBid=%dc hedgeAsk=%dc, totalCost=%dc, source=%s)",
		ID, entryAskCents, hedgeLimitCents, hedgeOffset, hedgeBidCents, hedgeAskCentsDirect, totalCostCents, source)

	// size：确保满足最小金额/最小 shares（GTC）
	entryShares := ensureMinOrderSize(orderSize, entryPriceDec, minOrderSize)
	hedgeShares := ensureMinOrderSize(hedgeSize, hedgeDec, minOrderSize)

	// 确保两边数量相等：使用较大的数量，避免因价格差异导致数量不一致
	maxShares := entryShares
	if hedgeShares > maxShares {
		maxShares = hedgeShares
	}
	entryShares = maxShares
	hedgeShares = maxShares

	// 确保满足最小 share 数量（GTC 限价单）
	if entryShares < minShareSize {
		entryShares = minShareSize
	}
	if hedgeShares < minShareSize {
		hedgeShares = minShareSize
	}
	// 再次确保相等（如果 minShareSize 导致不一致）
	if entryShares != hedgeShares {
		maxShares = entryShares
		if hedgeShares > maxShares {
			maxShares = hedgeShares
		}
		entryShares = maxShares
		hedgeShares = maxShares
	}

	// 调整 Entry 订单的 size，确保 maker amount = size × price 是 2 位小数
	// Entry 订单是 FAK 买入订单，maker amount 必须 <= 2 位小数
	// ⚠️ 注意：使用实际 ask 价格（entryAskDec），而不是有效价格
	entrySharesAdjusted := adjustSizeForMakerAmountPrecision(entryShares, entryAskDec)
	if entrySharesAdjusted != entryShares {
		log.Debugf("🔧 [%s] Entry size 精度调整: %.4f -> %.4f (maker amount: %.2f -> %.2f)",
			ID, entryShares, entrySharesAdjusted, entryShares*entryAskDec, entrySharesAdjusted*entryAskDec)
		entryShares = entrySharesAdjusted
	}

	// 调整 Hedge 订单的 size，确保 maker amount = size × price 是 2 位小数
	// Hedge 订单是 GTC 买入订单，maker amount 必须 <= 2 位小数
	hedgePriceDec := float64(hedgeAskCents) / 100.0
	hedgeSharesAdjusted := adjustSizeForMakerAmountPrecision(hedgeShares, hedgePriceDec)
	if hedgeSharesAdjusted != hedgeShares {
		log.Debugf("🔧 [%s] Hedge size 精度调整: %.4f -> %.4f (maker amount: %.2f -> %.2f)",
			ID, hedgeShares, hedgeSharesAdjusted, hedgeShares*hedgePriceDec, hedgeSharesAdjusted*hedgePriceDec)
		hedgeShares = hedgeSharesAdjusted
	}

	// 记录订单数量信息（用于验证两边是否相等）
	// ⚠️ 注意：Entry 使用实际 ask 价格计算 maker amount，Hedge 使用有效价格
	log.Debugf("📊 [%s] 订单数量: Entry=%.4f shares @ %dc实际ask (maker=%.2f), Hedge=%.4f shares @ %dc有效价格 (maker=%.2f)",
		ID, entryShares, int(entryAskDec*100+0.5), entryShares*entryAskDec, hedgeShares, hedgeAskCents, hedgeShares*hedgeDec)

	// 9. 订单执行：根据配置选择顺序或并发执行
	// sequential: 先下 Entry，等待成交后再下 Hedge（风险低，速度慢）
	// parallel: 同时提交 Entry 和 Hedge（速度快，风险高）
	biasTokStr := string(biasTok)
	if s.Config.OrderExecutionMode == "parallel" {
		return s.executeParallel(orderCtx, market, winner, entryAsset, hedgeAsset, entryPriceForFAK, hedgePrice, entryShares, hedgeShares, entryAskCents, hedgeAskCents, winMet, biasTokStr, biasReason)
	} else {
		return s.executeSequential(orderCtx, market, winner, entryAsset, hedgeAsset, entryPriceForFAK, hedgePrice, entryShares, hedgeShares, entryAskCents, hedgeAskCents, winMet, biasTokStr, biasReason)
	}
}

// executeSequential moved to entry_sequential.go

// executeParallel / monitorAndReorderHedge moved to entry_parallel.go / hedge_reorder.go

// pruneLocked / computeLocked moved to sampling.go

// exit logic moved to exit.go

func (s *Strategy) shouldHandleMarketEvent(m *domain.Market) bool {
	if s == nil || m == nil {
		if s == nil {
			log.Warnf("⚠️ [%s] Strategy 为 nil", ID)
		}
		if m == nil {
			log.Warnf("⚠️ [%s] Market 为 nil", ID)
		}
		return false
	}

	// ✅ 记录每次市场过滤检查（首次和每5秒一次）
	s.mu.Lock()
	shouldLog := s.lastMarketSkipLogAt.IsZero() || time.Since(s.lastMarketSkipLogAt) >= 5*time.Second
	s.mu.Unlock()
	if shouldLog {
		log.Infof("🔍 [%s] 市场过滤检查: eventMarket=%s 前缀=%s",
			ID, m.Slug, s.marketSlugPrefix)
	}

	// 目标市场过滤：只处理目标 market（通过 prefix 匹配）
	if !strings.HasPrefix(strings.ToLower(m.Slug), s.marketSlugPrefix) {
		if shouldLog {
			log.Infof("🔄 [%s] 市场过滤: eventMarket=%s 不匹配前缀=%s",
				ID, m.Slug, s.marketSlugPrefix)
		}
		return false
	}

	// 【重要】验证事件中的 market 是否与 TradingService 中的当前 market 匹配
	// 周期切换后，价格更新事件中的 Market 可能还是旧周期的数据
	// 如果 market 不匹配，说明这是旧周期的价格更新，应该忽略
	if s.TradingService != nil {
		currentMarketSlug := s.TradingService.GetCurrentMarket()
		if currentMarketSlug == "" {
			// ✅ 当前市场为空，可能是启动阶段，记录日志（不限流，确保能看到）
			log.Warnf("⚠️ [%s] TradingService 当前市场为空，等待市场初始化: eventMarket=%s (这可能导致价格事件被过滤)",
				ID, m.Slug)
			return false
		}
		if currentMarketSlug != m.Slug {
			if shouldLog {
				log.Infof("🔄 [%s] 跳过旧周期价格更新: eventMarket=%s currentMarket=%s",
					ID, m.Slug, currentMarketSlug)
			}
			return false
		}
		if shouldLog {
			log.Infof("✅ [%s] 市场匹配: eventMarket=%s currentMarket=%s",
				ID, m.Slug, currentMarketSlug)
		}
	} else {
		// ✅ TradingService 为 nil，记录警告（不限流，确保能看到）
		log.Warnf("⚠️ [%s] TradingService 为 nil，无法验证市场: eventMarket=%s", ID, m.Slug)
		return false
	}

	return true
}

func (s *Strategy) maybeLogPriceUpdate(now time.Time, tok domain.TokenType, p domain.Price, marketSlug string) {
	if s == nil {
		return
	}

	// 显示 WebSocket 实时价格（用于调试，带限流避免刷屏）
	priceDecimal := p.ToDecimal()
	priceCents := p.ToCents()

	// 价格日志限流：同一 token 的价格更新，如果价格变化不大且时间间隔短，则限流
	shouldLogPrice := false

	s.mu.Lock()
	// 在锁内检查限流条件
	if s.lastPriceLogToken != tok || s.lastPriceLogAt.IsZero() {
		// 不同 token 或首次，直接打印
		shouldLogPrice = true
	} else {
		// 相同 token，检查时间间隔和价格变化
		logThrottle := time.Duration(s.priceLogThrottleMs) * time.Millisecond
		if logThrottle <= 0 {
			logThrottle = 1 * time.Second // 默认 1 秒
		}
		timeSinceLastLog := now.Sub(s.lastPriceLogAt)
		priceChange := priceCents - s.lastPriceLogPriceCents
		if priceChange < 0 {
			priceChange = -priceChange
		}

		// 如果时间间隔超过限流时间，或者价格变化超过 1 分，则打印
		if timeSinceLastLog >= logThrottle || priceChange >= 1 {
			shouldLogPrice = true
		}
	}

	// 如果需要打印，更新限流状态
	if shouldLogPrice {
		s.lastPriceLogToken = tok
		s.lastPriceLogAt = now
		s.lastPriceLogPriceCents = priceCents
	}
	s.mu.Unlock()

	// 在锁外打印日志（避免长时间持锁）
	if shouldLogPrice {
		log.Debugf("📈 [%s] 价格更新: token=%s price=%.4f (%dc) market=%s",
			ID, tok, priceDecimal, priceCents, marketSlug)
	}
}

func (s *Strategy) maybeLogOrderBook(now time.Time, market *domain.Market) {
	if s == nil || s.TradingService == nil || market == nil {
		return
	}

	// 打印 UP/DOWN 的 bid/ask 价格（带限流，避免频繁调用 API）
	s.mu.Lock()
	shouldLogOrderBook := false
	if s.lastOrderBookLogAt.IsZero() {
		shouldLogOrderBook = true
	} else {
		logThrottle := time.Duration(s.orderBookLogThrottleMs) * time.Millisecond
		if logThrottle <= 0 {
			logThrottle = 2 * time.Second // 默认 2 秒
		}
		if now.Sub(s.lastOrderBookLogAt) >= logThrottle {
			shouldLogOrderBook = true
		}
	}
	if shouldLogOrderBook {
		s.lastOrderBookLogAt = now
	}
	s.mu.Unlock()

	// 在锁外获取订单簿价格并打印（避免长时间持锁）
	if !shouldLogOrderBook {
		return
	}

	// 使用背景上下文，避免阻塞策略主流程
	bookCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(bookCtx, market)
	if err != nil {
		// 静默失败，不影响策略运行
		log.Debugf("⚠️ [%s] 获取订单簿价格失败（实时日志）: %v", ID, err)
		return
	}

	// 验证价差：过滤异常数据（价差过大表示订单簿异常，不应打印）
	yesBidDec := yesBid.ToDecimal()
	yesAskDec := yesAsk.ToDecimal()
	noBidDec := noBid.ToDecimal()
	noAskDec := noAsk.ToDecimal()

	// 计算价差（单位：cents）
	yesSpreadCents := int((yesAskDec - yesBidDec) * 100)
	if yesSpreadCents < 0 {
		yesSpreadCents = -yesSpreadCents
	}
	noSpreadCents := int((noAskDec - noBidDec) * 100)
	if noSpreadCents < 0 {
		noSpreadCents = -noSpreadCents
	}

	// 价差阈值：10 cents（与 market_stream.go 中的 marketDataMaxSpreadCents 保持一致）
	maxSpreadCents := 10
	if yesSpreadCents > maxSpreadCents || noSpreadCents > maxSpreadCents {
		// 价差过大，跳过打印（避免误导性日志）
		log.Debugf("⚠️ [%s] 订单簿价差异常，跳过日志: UP spread=%dc (bid=%.4f ask=%.4f), DOWN spread=%dc (bid=%.4f ask=%.4f)",
			ID, yesSpreadCents, yesBidDec, yesAskDec, noSpreadCents, noBidDec, noAskDec)
		return
	}

	log.Infof("💰 [%s] 实时订单簿: UP bid=%.4f ask=%.4f, DOWN bid=%.4f ask=%.4f (source=%s market=%s)",
		ID, yesBidDec, yesAskDec, noBidDec, noAskDec, source, market.Slug)
}

// maybeHandleExit returns true when we should stop processing entry logic for this tick.
// It encapsulates: "if there is any open position in this market, throttle exit checks, and never open new positions".
func (s *Strategy) maybeHandleExit(ctx context.Context, market *domain.Market, now time.Time) bool {
	if s == nil || s.TradingService == nil || market == nil {
		return false
	}
	if !s.exitEnabled() {
		return false
	}

	positions := s.TradingService.GetOpenPositionsForMarket(market.Slug)
	hasPos := false
	for _, p := range positions {
		if p != nil && p.IsOpen() && p.Size > 0 {
			hasPos = true
			break
		}
	}
	if !hasPos {
		return false
	}

	// 节流：避免每条行情都尝试出场（默认 200ms）
	s.mu.Lock()
	lastCheck := s.lastExitCheckAt
	s.mu.Unlock()
	if lastCheck.IsZero() || now.Sub(lastCheck) >= 200*time.Millisecond {
		s.mu.Lock()
		s.lastExitCheckAt = now
		s.mu.Unlock()

		// tryExitPositions() returns true to indicate "positions exist, skip opening logic" even if no exit is triggered.
		_ = s.tryExitPositions(ctx, market, now, positions)
	}

	// 已有持仓时默认不再开新仓，等待出场逻辑处理完毕（避免叠加风险）
	return true
}

func (s *Strategy) updateCycleStartLocked(market *domain.Market) {
	if s == nil || market == nil {
		return
	}
	if market.Timestamp <= 0 {
		return
	}

	st := market.Timestamp * 1000
	if s.cycleStartMs == 0 || s.cycleStartMs != st {
		s.cycleStartMs = st
		s.biasReady = false
		s.biasToken = ""
		s.biasReason = ""
	}
}

// shouldSkipUntilBiasReadyLocked computes open1m bias state (when enabled) and returns true
// when RequireBiasReady is enabled and bias is still not ready.
// Callers must hold s.mu.
func (s *Strategy) shouldSkipUntilBiasReadyLocked(now time.Time) bool {
	if s == nil {
		return false
	}
	if !s.UseBinanceOpen1mBias {
		return false
	}

	// 如果等太久还没有拿到那根 1m，就降级为“无 bias”继续跑
	if !s.biasReady && s.cycleStartMs > 0 && s.Open1mMaxWaitSeconds > 0 {
		if now.UnixMilli()-s.cycleStartMs > int64(s.Open1mMaxWaitSeconds)*1000 {
			s.biasReady = true
			s.biasToken = ""
			s.biasReason = "open1m_timeout"
		}
	}

	if !s.biasReady && s.BinanceFuturesKlines != nil && s.cycleStartMs > 0 {
		if k, ok := s.BinanceFuturesKlines.Get("1m", s.cycleStartMs); ok && k.IsClosed && k.Open > 0 {
			bodyBps, wickBps, dirTok := candleStatsBps(k, domain.TokenTypeUp, domain.TokenTypeDown)
			if bodyBps < s.Open1mMinBodyBps {
				s.biasReady = true
				s.biasToken = ""
				s.biasReason = "open1m_body_too_small"
			} else if wickBps > s.Open1mMaxWickBps {
				s.biasReady = true
				s.biasToken = ""
				s.biasReason = "open1m_wick_too_large"
			} else {
				s.biasReady = true
				s.biasToken = dirTok
				s.biasReason = "open1m_ok"
			}
		}
	}

	return s.RequireBiasReady && !s.biasReady
}

// Shutdown 策略关闭时调用，用于清理资源（包括关闭 UI）
func (s *Strategy) Shutdown(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	s.uiMu.Lock()
	uiCancel := s.uiCancel
	uiRunning := s.uiRunning
	s.uiMu.Unlock()

	if uiCancel != nil {
		// 关闭UI（输出会被重定向到文件）
		log.Infof("🛑 [%s] 正在关闭UI...", ID)
		uiCancel() // 取消 UI context，导致 UI 退出
	}

	// 等待 UI 退出（如果正在运行）
	if uiRunning {
		// 给 UI 一些时间退出（最多等待3秒）
		done := make(chan struct{})
		go func() {
			s.uiMu.Lock()
			for s.uiRunning {
				s.uiMu.Unlock()
				time.Sleep(100 * time.Millisecond)
				s.uiMu.Lock()
			}
			s.uiMu.Unlock()
			close(done)
		}()

		select {
		case <-done:
			log.Infof("✅ [%s] UI已退出", ID)
		case <-time.After(3 * time.Second):
			log.Warnf("⚠️ [%s] UI未在3秒内退出，强制继续关闭", ID)
		case <-ctx.Done():
			log.Warnf("⚠️ [%s] 关闭超时，强制继续关闭", ID)
		}
	}

	// 清理 UI 状态
	s.uiMu.Lock()
	s.uiCancel = nil
	s.uiMu.Unlock()
}
