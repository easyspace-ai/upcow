package velocityfollow

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

// Strategy: 速度跟随策略（Velocity Follow）
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

	// 风险管理系统：实时监控持仓和风险敞口，智能对冲
	riskManager *RiskManager

	// 套利分析大脑：实时分析持仓和利润，判断是否完全锁定
	arbitrageBrain *ArbitrageBrain

	// 未对冲的 Entry 订单（当 Hedge 订单失败时记录）
	unhedgedEntries map[string]*domain.Order

	// 未完成的对冲单跟踪：Entry已成交但Hedge未成交的订单对
	// key=entryOrderID, value=hedgeOrderID
	pendingHedges map[string]string // 跟踪未完成的对冲单，确保对冲单成交后才能开启下一轮交易

	mu sync.Mutex // 保护共享状态
	// 避免在周期切换/重复 Subscribe 时重复注册 handler（OrderEngine handler 列表不去重）
	orderUpdateOnce sync.Once

	// 价格样本：用于计算速度
	samples map[domain.TokenType][]sample

	// 周期状态管理
	firstSeenAt          time.Time // 首次看到价格的时间
	lastTriggerAt        time.Time // 上次触发时间（用于冷却）
	tradedThisCycle      bool      // 本周期是否已交易（兼容旧逻辑）
	tradesCountThisCycle int       // 本周期已交易次数（新逻辑）

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

	// 订单簿价格日志：实时打印 UP/DOWN 的 bid/ask
	lastOrderBookLogAt     time.Time
	orderBookLogThrottleMs int64 // 订单簿价格日志限流时间（毫秒），默认 2 秒

	// 诊断日志限流：避免诊断日志太频繁
	lastDiagnosticLogAt time.Time
	lastNoWinnerLogAt   time.Time

	// 订单跟踪：利用本地订单状态管理（新架构特性）
	lastEntryOrderID     string                   // 最后下单的 Entry 订单ID
	lastHedgeOrderID     string                   // 最后下单的 Hedge 订单ID
	lastEntryOrderStatus domain.OrderStatus       // Entry 订单状态
	pendingOrders        map[string]*domain.Order // 待确认的订单（通过订单ID跟踪）

	// 出场（平仓）节流：避免短时间重复下 SELL
	lastExitAt      time.Time
	lastExitCheckAt time.Time

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
	// 输出配置信息（用于调试）
	log.Infof("🔧 [%s] 策略配置: OrderSize=%.4f WindowSeconds=%d MinMoveCents=%d MinVelocityCentsPerSec=%.3f CooldownMs=%d WarmupMs=%d MaxTradesPerCycle=%d",
		ID, s.Config.OrderSize, s.Config.WindowSeconds, s.Config.MinMoveCents, s.Config.MinVelocityCentsPerSec,
		s.Config.CooldownMs, s.Config.WarmupMs, s.Config.MaxTradesPerCycle)

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
	if s.pendingHedges == nil {
		s.pendingHedges = make(map[string]string)
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

		// 初始化风险管理系统
		s.riskManager = NewRiskManager(s.TradingService, s.Config)
		s.riskManager.Start()
		log.Infof("✅ [%s] 风险管理系统已初始化并启动", ID)

		// 初始化套利分析大脑
		updateInterval := s.Config.ArbitrageBrainUpdateIntervalSeconds
		if updateInterval <= 0 {
			updateInterval = 10 // 默认 10 秒
		}
		s.arbitrageBrain = NewArbitrageBrain(s.TradingService, updateInterval)
		if s.Config.ArbitrageBrainEnabled {
			s.arbitrageBrain.Start()
			log.Infof("✅ [%s] 套利分析大脑已初始化并启动: updateInterval=%ds", ID, updateInterval)
		}

		// 注意：Dashboard在Run()方法中启动，不在Initialize中启动
		// 这样可以确保所有服务都已初始化完成
	}

	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	log.Infof("✅ [%s] 策略已订阅价格变化事件 (session=%s)", ID, session.Name)

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
	// 启动Dashboard UI（默认启用）
	// 注意：bool类型默认为false，所以如果配置文件中没有设置dashboardEnabled，这里默认为true
	dashboardEnabled := s.Config.DashboardEnabled
	// 如果未显式设置（默认为false），则启用Dashboard
	// 这里我们总是启用，除非用户显式设置为false
	if !dashboardEnabled {
		dashboardEnabled = true
	}

	if dashboardEnabled && s.TradingService != nil {
		// 修改日志级别为Debug，避免info日志干扰看板
		// 注意：这会影响全局日志级别，但这是用户要求的
		originalLevel := logrus.GetLevel()
		if originalLevel == logrus.InfoLevel {
			logrus.SetLevel(logrus.DebugLevel)
			log.Debugf("🔧 [%s] 日志级别已调整为Debug（避免干扰Dashboard）", ID)
		}

		// 在goroutine中启动dashboard，避免阻塞主流程
		log.Debugf("✅ [%s] 正在启动Dashboard UI...", ID)
		go func() {
			// 等待一小段时间，确保其他初始化完成
			time.Sleep(500 * time.Millisecond)
			s.RunDashboard()
		}()
	} else {
		log.Infof("⚠️ [%s] Dashboard UI未启动: enabled=%v tradingService=%v", ID, dashboardEnabled, s.TradingService != nil)
	}

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

	// 重置价格样本
	s.samples = make(map[domain.TokenType][]sample)

	// 重置周期状态
	s.firstSeenAt = time.Now()
	s.tradedThisCycle = false
	s.tradesCountThisCycle = 0 // 重置交易计数

	// 重置方向去重状态
	s.lastTriggerSide = ""
	s.lastTriggerSideAt = time.Time{}

	// 重置日志限流状态
	s.lastCooldownLogSide = ""
	s.lastCooldownLogAt = time.Time{}

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
	s.pendingHedges = make(map[string]string) // 重置未完成的对冲单跟踪
	s.lastExitAt = time.Time{}
	s.lastExitCheckAt = time.Time{}
	s.partialTPDone = make(map[string]map[int]bool)
	s.trailing = make(map[string]*trailState)

	// 市场精度信息从配置文件加载，无需在运行时获取

	// 注意：不清 lastTriggerAt，避免周期切换瞬间重复触发

	s.mu.Unlock()

	// 检查并合并上一周期的持仓（在锁外执行，避免长时间持锁）
	if oldMarket != nil && s.TradingService != nil && oldMarket.ConditionID != "" {
		s.checkAndMergePreviousCycle(ctx, oldMarket)
	}
}

// checkAndMergePreviousCycle 检查并合并上一周期的持仓
func (s *Strategy) checkAndMergePreviousCycle(ctx context.Context, oldMarket *domain.Market) {
	if oldMarket == nil || s.TradingService == nil || oldMarket.ConditionID == "" {
		return
	}

	// 获取上一周期的持仓
	positions := s.TradingService.GetOpenPositionsForMarket(oldMarket.Slug)
	if len(positions) == 0 {
		log.Debugf("🔄 [%s] 上一周期无持仓，跳过合并: market=%s", ID, oldMarket.Slug)
		return
	}

	// 计算 UP 和 DOWN 的数量
	var upSize, downSize float64
	for _, pos := range positions {
		if pos == nil || !pos.IsOpen() || pos.Size <= 0 {
			continue
		}
		if pos.TokenType == domain.TokenTypeUp {
			upSize += pos.Size
		} else if pos.TokenType == domain.TokenTypeDown {
			downSize += pos.Size
		}
	}

	// 如果 UP 和 DOWN 都有持仓，计算可合并数量（取最小值）
	if upSize > 0 && downSize > 0 {
		mergeAmount := math.Min(upSize, downSize)
		if mergeAmount > 0 {
			log.Infof("🔄 [%s] 检测到上一周期需要合并: market=%s UP=%.6f DOWN=%.6f mergeAmount=%.6f",
				ID, oldMarket.Slug, upSize, downSize, mergeAmount)

			// 异步执行合并操作，避免阻塞周期切换
			go func() {
				mergeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				metadata := fmt.Sprintf("AutoMerge previous cycle %.6f USDC for %s", mergeAmount, oldMarket.Slug)
				txHash, err := s.TradingService.MergeCompleteSetsViaRelayer(mergeCtx, oldMarket.ConditionID, mergeAmount, metadata)
				if err != nil {
					log.Warnf("⚠️ [%s] 上一周期合并失败: market=%s amount=%.6f err=%v", ID, oldMarket.Slug, mergeAmount, err)
				} else {
					log.Infof("✅ [%s] 上一周期合并已提交: market=%s amount=%.6f tx=%s", ID, oldMarket.Slug, mergeAmount, txHash)
					// 等待一小段时间，让 merge 交易有时间提交
					time.Sleep(2 * time.Second)
				}
			}()
		}
	} else {
		log.Debugf("🔄 [%s] 上一周期持仓不完整，跳过合并: market=%s UP=%.6f DOWN=%.6f",
			ID, oldMarket.Slug, upSize, downSize)
	}
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

			// 注册风险敞口到风险管理系统
			if s.riskManager != nil {
				hedgeOrderID := ""
				if order.HedgeOrderID != nil {
					hedgeOrderID = *order.HedgeOrderID
				}
				s.riskManager.RegisterEntry(order, hedgeOrderID)
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

			// 更新风险管理系统：Hedge已成交
			if s.riskManager != nil {
				s.riskManager.UpdateHedgeStatus(order.OrderID, order.Status)
			}

			// 清除未完成的对冲单跟踪，允许开启下一轮交易
			if s.pendingHedges != nil {
				for entryID, hedgeID := range s.pendingHedges {
					if hedgeID == order.OrderID {
						log.Infof("✅ [%s] 对冲单已成交，清除未完成跟踪，允许开启下一轮交易: entryOrderID=%s hedgeOrderID=%s",
							ID, entryID, hedgeID)
						delete(s.pendingHedges, entryID)
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

			// ✅ 对冲leg完成时自动触发merge（在后台goroutine中执行，不影响主交易流程）
			// 从持仓中获取market对象，然后触发merge以释放资金
			if s.AutoMerge.Enabled && order.MarketSlug != "" {
				marketSlug := order.MarketSlug
				autoMergeCfg := s.AutoMerge
				tradingService := s.TradingService
				autoMergeController := s.autoMerge // 复制引用，避免在goroutine中访问锁保护的字段

				// 在goroutine中执行，避免阻塞订单更新回调
				go func() {
					// 等待一小段时间，让持仓状态更新
					time.Sleep(500 * time.Millisecond)

					// 从持仓中获取market对象
					positions := tradingService.GetOpenPositionsForMarket(marketSlug)
					var market *domain.Market
					for _, p := range positions {
						if p != nil && p.Market != nil && p.Market.IsValid() {
							market = p.Market
							break
						}
					}

					// 如果无法从持仓获取market，跳过merge（需要market对象才能执行merge）
					if market == nil {
						log.Debugf("⚠️ [%s] 无法从持仓获取market对象，跳过自动merge: marketSlug=%s", ID, marketSlug)
						return
					}

					if market != nil && market.IsValid() {
						// 使用独立的context，避免使用已取消的ctx
						mergeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()

						log.Infof("🔄 [%s] 对冲leg完成，触发自动merge: market=%s hedgeOrderID=%s", ID, marketSlug, order.OrderID)
						autoMergeController.MaybeAutoMerge(mergeCtx, tradingService, market, autoMergeCfg, log.Infof)
					}
				}()
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

			// 更新风险管理系统：Hedge失败/取消
			if s.riskManager != nil {
				s.riskManager.UpdateHedgeStatus(order.OrderID, order.Status)
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
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}
	s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)

	// 1. 市场过滤：只处理目标 market + 当前周期 market
	if !s.shouldHandleMarketEvent(e.Market) {
		return nil
	}

	now := e.Timestamp
	if now.IsZero() {
		now = time.Now()
	}

	priceCents := e.NewPrice.ToCents()

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
		s.mu.Unlock()
		return nil
	}

	// 4.5 周期结束前保护：在周期结束前 N 分钟不开新单（降低风险）
	if s.CycleEndProtectionMinutes > 0 && e.Market != nil && e.Market.Timestamp > 0 {
		// 获取周期时长（从全局配置或市场规格获取）
		cycleDuration := 15 * time.Minute // 默认 15 分钟
		if cfg := config.Get(); cfg != nil {
			if spec, err := cfg.Market.Spec(); err == nil {
				cycleDuration = spec.Duration()
			}
		}

		cycleStartTime := time.Unix(e.Market.Timestamp, 0)
		cycleEndTime := cycleStartTime.Add(cycleDuration)
		protectionTime := time.Duration(s.CycleEndProtectionMinutes) * time.Minute

		if now.After(cycleEndTime.Add(-protectionTime)) {
			s.mu.Unlock()
			log.Debugf("⏸️ [%s] 跳过：周期结束前保护（距离周期结束 %.1f 分钟）",
				ID, time.Until(cycleEndTime).Minutes())
			return nil
		}
	}

	// 5. 交易限制检查：MaxTradesPerCycle 控制（0=不设限）
	if s.MaxTradesPerCycle > 0 && s.tradesCountThisCycle >= s.MaxTradesPerCycle {
		s.mu.Unlock()
		log.Debugf("🔄 [%s] 跳过：本周期交易次数已达上限 (%d/%d)", ID, s.tradesCountThisCycle, s.MaxTradesPerCycle)
		return nil
	}

	// 5.2 检查是否有未完成的对冲单：确保对冲单成交后才能开启下一轮交易
	if s.pendingHedges != nil && len(s.pendingHedges) > 0 {
		// 检查是否有未完成的对冲单（Entry已成交但Hedge未成交）
		hasPendingHedge := false
		for entryID, hedgeID := range s.pendingHedges {
			// 检查Entry订单是否已成交
			if entryOrder, ok := s.TradingService.GetOrder(entryID); ok && entryOrder != nil {
				if entryOrder.Status == domain.OrderStatusFilled {
					// Entry已成交，检查Hedge是否已成交
					if hedgeOrder, ok := s.TradingService.GetOrder(hedgeID); ok && hedgeOrder != nil {
						if hedgeOrder.Status != domain.OrderStatusFilled {
							hasPendingHedge = true
							log.Debugf("🔄 [%s] 跳过：存在未完成的对冲单，等待对冲单成交后再开启下一轮交易: entryOrderID=%s hedgeOrderID=%s hedgeStatus=%s",
								ID, entryID, hedgeID, hedgeOrder.Status)
							break
						} else {
							// Hedge已成交，清除跟踪
							delete(s.pendingHedges, entryID)
						}
					} else {
						// Hedge订单不存在，可能已成交或失败，清除跟踪
						delete(s.pendingHedges, entryID)
					}
				} else {
					// Entry未成交，清除跟踪
					delete(s.pendingHedges, entryID)
				}
			} else {
				// Entry订单不存在，清除跟踪
				delete(s.pendingHedges, entryID)
			}
		}
		if hasPendingHedge {
			s.mu.Unlock()
			return nil
		}
	}

	// 5.3 冷却时间检查
	if !s.lastTriggerAt.IsZero() && now.Sub(s.lastTriggerAt) < time.Duration(s.CooldownMs)*time.Millisecond {
		s.mu.Unlock()
		return nil
	}

	// 6. 速度计算：更新样本并计算 UP/DOWN 价格变化速度
	// priceCents 已在前面定义，这里直接使用
	if priceCents <= 0 || priceCents >= 100 {
		s.mu.Unlock()
		return nil
	}
	s.samples[e.TokenType] = append(s.samples[e.TokenType], sample{ts: now, priceCents: priceCents})
	s.pruneLocked(now)

	// 计算当前速度（用于日志显示）
	currentVelocity := 0.0
	if m := s.computeLocked(e.TokenType); m.ok {
		currentVelocity = m.velocity
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
	s.mu.Unlock()

	// 显示 WebSocket 实时价格（用于调试，带限流避免刷屏）
	// 在 samples 更新后调用，以便显示当前速度
	s.maybeLogPriceUpdate(now, e.TokenType, e.NewPrice, e.Market.Slug, currentVelocity)

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

	// 诊断日志：如果速度不满足条件，输出详细信息（限流：每10秒最多一次）
	s.mu.Lock()
	shouldLogDiagnostic := false
	if s.lastDiagnosticLogAt.IsZero() {
		shouldLogDiagnostic = true
	} else if now.Sub(s.lastDiagnosticLogAt) >= 10*time.Second {
		shouldLogDiagnostic = true
	}
	if shouldLogDiagnostic {
		s.lastDiagnosticLogAt = now
	}
	s.mu.Unlock()

	if shouldLogDiagnostic && (!upQualified || !downQualified) {
		var reasons []string
		if !allowUp {
			reasons = append(reasons, fmt.Sprintf("UP被bias禁止(bias=%s mode=%s)", s.biasToken, s.BiasMode))
		}
		if !allowDown {
			reasons = append(reasons, fmt.Sprintf("DOWN被bias禁止(bias=%s mode=%s)", s.biasToken, s.BiasMode))
		}
		if allowUp && (!mUp.ok || mUp.delta < reqMoveUp || mUp.velocity < reqVelUp) {
			if !mUp.ok {
				reasons = append(reasons, fmt.Sprintf("UP速度计算失败(样本不足)"))
			} else {
				if mUp.delta < reqMoveUp {
					reasons = append(reasons, fmt.Sprintf("UP位移不足(%.1fc < %dc)", mUp.delta, reqMoveUp))
				}
				if mUp.velocity < reqVelUp {
					reasons = append(reasons, fmt.Sprintf("UP速度不足(%.3f c/s < %.3f c/s)", mUp.velocity, reqVelUp))
				}
			}
		}
		if allowDown && (!mDown.ok || mDown.delta < reqMoveDown || mDown.velocity < reqVelDown) {
			if !mDown.ok {
				reasons = append(reasons, fmt.Sprintf("DOWN速度计算失败(样本不足)"))
			} else {
				if mDown.delta < reqMoveDown {
					reasons = append(reasons, fmt.Sprintf("DOWN位移不足(%.1fc < %dc)", mDown.delta, reqMoveDown))
				}
				if mDown.velocity < reqVelDown {
					reasons = append(reasons, fmt.Sprintf("DOWN速度不足(%.3f c/s < %.3f c/s)", mDown.velocity, reqVelDown))
				}
			}
		}
		if len(reasons) > 0 {
			log.Infof("🔍 [%s] 未触发交易原因: %s | UP: ok=%v delta=%.1fc velocity=%.3f c/s (需要: delta>=%dc velocity>=%.3f c/s) | DOWN: ok=%v delta=%.1fc velocity=%.3f c/s (需要: delta>=%dc velocity>=%.3f c/s)",
				ID, strings.Join(reasons, ", "),
				mUp.ok, mUp.delta, mUp.velocity, reqMoveUp, reqVelUp,
				mDown.ok, mDown.delta, mDown.velocity, reqMoveDown, reqVelDown)
		}
	}

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
		// 如果没有满足条件的winner，输出诊断信息（限流：每10秒最多一次）
		s.mu.Lock()
		shouldLogNoWinner := false
		if s.lastNoWinnerLogAt.IsZero() {
			shouldLogNoWinner = true
		} else if now.Sub(s.lastNoWinnerLogAt) >= 10*time.Second {
			shouldLogNoWinner = true
		}
		if shouldLogNoWinner {
			s.lastNoWinnerLogAt = now
		}
		s.mu.Unlock()

		if shouldLogNoWinner {
			log.Infof("🔍 [%s] 未选择交易方向: UP满足=%v (ok=%v delta=%.1fc velocity=%.3f c/s 需要: delta>=%dc velocity>=%.3f c/s) | DOWN满足=%v (ok=%v delta=%.1fc velocity=%.3f c/s 需要: delta>=%dc velocity>=%.3f c/s)",
				ID,
				upQualified, mUp.ok, mUp.delta, mUp.velocity, reqMoveUp, reqVelUp,
				downQualified, mDown.ok, mDown.delta, mDown.velocity, reqMoveDown, reqVelDown)
		}
		return nil
	}

	// 方向级别的去重：避免同一方向在短时间内重复触发
	// 这可以显著减少 duplicate in-flight 错误
	s.mu.Lock()
	lastTriggerSide := s.lastTriggerSide
	lastTriggerSideAt := s.lastTriggerSideAt
	s.mu.Unlock()

	if lastTriggerSide == winner && !lastTriggerSideAt.IsZero() {
		sideCooldown := time.Duration(s.CooldownMs) * time.Millisecond
		if sideCooldown <= 0 {
			sideCooldown = 2 * time.Second // 默认 2 秒
		}
		if now.Sub(lastTriggerSideAt) < sideCooldown {
			// 日志限流：避免短时间内重复打印相同的日志
			// 如果距离上次打印相同方向的冷却期日志超过 5 秒，才打印
			s.mu.Lock()
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
			}
			s.mu.Unlock()
			if shouldLog {
				// 降级为 Debug 级别，减少日志噪音（这是正常的去重行为）
				log.Debugf("🔄 [%s] 跳过：同一方向 %s 在冷却期内（距离上次触发 %.2fs，冷却时间 %.2fs）",
					ID, winner, now.Sub(lastTriggerSideAt).Seconds(), sideCooldown.Seconds())
			}
			return nil
		}
	}

	// 提前更新 lastTriggerSideAt（在下单之前），避免后续触发在策略层就跳过
	// 这样可以减少不必要的下单尝试，减少 duplicate in-flight 错误
	s.mu.Lock()
	s.lastTriggerSide = winner
	s.lastTriggerSideAt = now
	s.mu.Unlock()

	// 5.5 库存偏斜检查：如果净持仓超过阈值，降低该方向的交易频率
	if s.Config.InventoryThreshold > 0 && s.inventoryCalculator != nil && e.Market != nil {
		shouldSkip := s.inventoryCalculator.CheckInventorySkew(e.Market.Slug, s.Config.InventoryThreshold, winner)
		if shouldSkip {
			// 计算净持仓详情（用于日志）
			result := s.inventoryCalculator.CalculateNetPosition(e.Market.Slug)
			log.Infof("🔄 [%s] 跳过：库存偏斜保护触发（方向=%s, 净持仓=%.2f, UP持仓=%.2f, DOWN持仓=%.2f, 阈值=%.2f）",
				ID, winner, result.NetPosition, result.UpInventory, result.DownInventory, s.Config.InventoryThreshold)
			return nil
		}
	}

	// 可选：用 Binance 1s "底层硬动"过滤（借鉴 momentum bot 的 move threshold 思路）
	if s.UseBinanceMoveConfirm {
		if s.BinanceFuturesKlines == nil {
			return nil
		}
		nowMs := now.UnixMilli()
		cur, okCur := s.BinanceFuturesKlines.Latest("1s")
		past, okPast := s.BinanceFuturesKlines.NearestAtOrBefore("1s", nowMs-int64(s.MoveConfirmWindowSeconds)*1000)
		if !okCur || !okPast || past.Close <= 0 {
			return nil
		}
		ret := (cur.Close - past.Close) / past.Close
		retBps := int(math.Abs(ret)*10000 + 0.5)
		dir := domain.TokenTypeDown
		if ret >= 0 {
			dir = domain.TokenTypeUp
		}
		if retBps < s.MinUnderlyingMoveBps || dir != winner {
			return nil
		}
	}

	// 放锁外做 IO（下单/拉盘口）
	// 备注：这里用一个小技巧：先把必要字段拷贝出来
	s.mu.Lock()
	market := e.Market
	biasTok := s.biasToken
	biasReason := s.biasReason
	hedgeOffset := s.HedgeOffsetCents
	maxSpread := s.MaxSpreadCents
	orderSize := s.OrderSize
	hedgeSize := s.HedgeOrderSize
	minOrderSize := s.minOrderSize
	minShareSize := s.minShareSize
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
			log.Infof("⏭️ [%s] 跳过：MarketQuality 获取失败: %v", ID, mqErr)
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
			log.Infof("⏭️ [%s] 跳过：MarketQuality gate 未通过: score=%d(min=%d) tradable=%v problems=%v source=%s%s",
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

	// Entry 价格区间检查：主leg下单必须在价格区间内
	minEntry := s.MinEntryPriceCents
	maxEntry := s.MaxEntryPriceCents
	if minEntry > 0 && entryAskCents < minEntry {
		log.Infof("⏭️ [%s] 跳过：Entry 价格低于下限 (%dc < %dc)", ID, entryAskCents, minEntry)
		return nil
	}
	if maxEntry > 0 && entryAskCents > maxEntry {
		log.Infof("⏭️ [%s] 跳过：Entry 价格超过上限 (%dc > %dc)", ID, entryAskCents, maxEntry)
		return nil
	}

	// 价差检查（使用实际价差，而非互补价）
	entrySpread := entryAskCents - entryBidCents
	if entrySpread < 0 {
		entrySpread = -entrySpread
	}
	if maxSpread > 0 && entrySpread > maxSpread {
		log.Infof("⏭️ [%s] 跳过：价差过大 (%dc > %dc)", ID, entrySpread, maxSpread)
		return nil
	}

	// Hedge 挂单价格：互补挂单 = 100 - entryAsk - hedgeOffset
	// 这确保最坏情况下（hedge 以该限价成交）总成本 = 100 - hedgeOffset（留出 offset 作为边际）。
	hedgeLimitCents := 100 - entryAskCents - hedgeOffset
	if hedgeLimitCents <= 0 || hedgeLimitCents >= 100 {
		log.Infof("⏭️ [%s] 跳过：Hedge 互补挂单价格无效: entryAsk=%dc hedgeOffset=%dc => hedgeLimit=%dc",
			ID, entryAskCents, hedgeOffset, hedgeLimitCents)
		return nil
	}
	// 防止“挂单穿价”变成 taker：买单价格必须严格小于当前 ask
	if hedgeLimitCents >= hedgeAskCentsDirect {
		hedgeLimitCents = hedgeAskCentsDirect - 1
	}
	if hedgeLimitCents <= 0 {
		log.Infof("⏭️ [%s] 跳过：Hedge 挂单会穿价且无法降到有效区间: hedgeAsk=%dc", ID, hedgeAskCentsDirect)
		return nil
	}
	// 兼容下游变量命名：hedgeAskCents 在策略内一直代表“对冲腿下单价格（cents）”
	hedgeAskCents := hedgeLimitCents

	totalCostCents := entryAskCents + hedgeLimitCents
	if totalCostCents > 100 {
		// 理论上不会发生（互补价 + offset），但做最后一道保护，避免浮点/取整误差带来结构性必亏
		log.Infof("⏭️ [%s] 跳过：总成本过高 (%dc > 100c): Entry=%dc + Hedge=%dc (bid=%dc ask=%dc)",
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
		return false
	}

	// 目标市场过滤：只处理目标 market（通过 prefix 匹配）
	if !strings.HasPrefix(strings.ToLower(m.Slug), s.marketSlugPrefix) {
		return false
	}

	// 【重要】验证事件中的 market 是否与 TradingService 中的当前 market 匹配
	// 周期切换后，价格更新事件中的 Market 可能还是旧周期的数据
	// 如果 market 不匹配，说明这是旧周期的价格更新，应该忽略
	if s.TradingService != nil {
		currentMarketSlug := s.TradingService.GetCurrentMarket()
		if currentMarketSlug != "" && currentMarketSlug != m.Slug {
			log.Debugf("🔄 [%s] 跳过旧周期价格更新: eventMarket=%s currentMarket=%s",
				ID, m.Slug, currentMarketSlug)
			return false
		}
	}

	return true
}

func (s *Strategy) maybeLogPriceUpdate(now time.Time, tok domain.TokenType, p domain.Price, marketSlug string, currentVelocity float64) {
	if s == nil {
		return
	}

	// 显示 WebSocket 实时价格（用于调试，带限流避免刷屏）
	priceDecimal := p.ToDecimal()
	priceCents := p.ToCents()

	// 价格日志限流：同一 token 的价格更新，如果价格变化不大且时间间隔短，则限流
	shouldLogPrice := false
	var minVelocityThreshold float64

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

	// 获取最小速度阈值和交易次数信息
	var tradesCount int
	var maxTradesPerCycle int
	if shouldLogPrice {
		minVelocityThreshold = s.MinVelocityCentsPerSec
		if minVelocityThreshold <= 0 {
			// 如果未设置，使用默认值 = minMoveCents / windowSeconds
			if s.WindowSeconds > 0 {
				minVelocityThreshold = float64(s.MinMoveCents) / float64(s.WindowSeconds)
			} else {
				minVelocityThreshold = float64(s.MinMoveCents) / 10.0 // 默认10秒窗口
			}
		}

		// 获取交易次数信息
		tradesCount = s.tradesCountThisCycle
		maxTradesPerCycle = s.MaxTradesPerCycle

		// 更新限流状态
		s.lastPriceLogToken = tok
		s.lastPriceLogAt = now
		s.lastPriceLogPriceCents = priceCents
	}
	s.mu.Unlock()

	// 在锁外打印日志（避免长时间持锁）
	if shouldLogPrice {
		// 构建交易次数信息字符串
		tradesInfo := ""
		if maxTradesPerCycle > 0 {
			tradesInfo = fmt.Sprintf(" trades=%d/%d", tradesCount, maxTradesPerCycle)
		}

		if currentVelocity > 0 {
			log.Debugf("📈 [%s] 价格更新: token=%s price=%.4f (%dc) velocity=%.3f c/s (min=%.3f c/s)%s market=%s",
				ID, tok, priceDecimal, priceCents, currentVelocity, minVelocityThreshold, tradesInfo, marketSlug)
		} else {
			log.Debugf("📈 [%s] 价格更新: token=%s price=%.4f (%dc) velocity=N/A (min=%.3f c/s)%s market=%s",
				ID, tok, priceDecimal, priceCents, minVelocityThreshold, tradesInfo, marketSlug)
		}
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

	// 计算 UP/DOWN 的价格变化速度（在锁内计算，避免数据竞争）
	// 注意：这里计算的是显示用的速度（包括下跌），与交易逻辑中的 computeLocked 不同
	var upVelocityStr, downVelocityStr string
	if shouldLogOrderBook {
		s.lastOrderBookLogAt = now

		// 计算 UP 速度（显示用：包括上涨和下跌）
		upVel := s.computeVelocityForDisplayLocked(domain.TokenTypeUp)
		if upVel != nil {
			if *upVel >= 0 {
				upVelocityStr = fmt.Sprintf("velocity=+%.3f c/s", *upVel)
			} else {
				upVelocityStr = fmt.Sprintf("velocity=%.3f c/s", *upVel)
			}
		} else {
			upVelocityStr = "velocity=N/A"
		}

		// 计算 DOWN 速度（显示用：包括上涨和下跌）
		downVel := s.computeVelocityForDisplayLocked(domain.TokenTypeDown)
		if downVel != nil {
			if *downVel >= 0 {
				downVelocityStr = fmt.Sprintf("velocity=+%.3f c/s", *downVel)
			} else {
				downVelocityStr = fmt.Sprintf("velocity=%.3f c/s", *downVel)
			}
		} else {
			downVelocityStr = "velocity=N/A"
		}
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

	log.Infof("💰 [%s] 实时订单簿: UP bid=%.4f ask=%.4f (%s), DOWN bid=%.4f ask=%.4f (%s) (source=%s market=%s)",
		ID, yesBidDec, yesAskDec, upVelocityStr, noBidDec, noAskDec, downVelocityStr, source, market.Slug)
}

// computeVelocityForDisplayLocked 计算用于显示的速度（包括上涨和下跌）
// 与 computeLocked 不同，这个函数会返回双向速度，用于日志显示
// 返回值：nil 表示无法计算（样本不足等），否则返回速度值（可能为负数表示下跌）
func (s *Strategy) computeVelocityForDisplayLocked(tok domain.TokenType) *float64 {
	arr := s.samples[tok]
	if len(arr) < 2 {
		return nil
	}
	first := arr[0]
	last := arr[len(arr)-1]
	dt := last.ts.Sub(first.ts).Seconds()
	if dt <= 0.001 {
		return nil
	}
	delta := last.priceCents - first.priceCents
	vel := float64(delta) / dt
	if math.IsNaN(vel) || math.IsInf(vel, 0) {
		return nil
	}
	return &vel
}

// GetVelocityForDisplay 获取用于显示的速度（公开方法，供Dashboard使用）
func (s *Strategy) GetVelocityForDisplay(tok domain.TokenType) float64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	vel := s.computeVelocityForDisplayLocked(tok)
	if vel == nil {
		return 0
	}
	return *vel
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

// StateMachineStatus 状态机状态（用于Dashboard显示）
type StateMachineStatus struct {
	// 基础条件
	MarketValid       bool `json:"market_valid"`
	BiasReady         bool `json:"bias_ready"`
	WarmupPassed      bool `json:"warmup_passed"`
	CycleEndProtected bool `json:"cycle_end_protected"`
	TradesLimitOK     bool `json:"trades_limit_ok"`
	NoPendingHedge    bool `json:"no_pending_hedge"`
	CooldownPassed    bool `json:"cooldown_passed"`

	// UP方向条件
	UpAllowed          bool    `json:"up_allowed"`
	UpVelocityOK       bool    `json:"up_velocity_ok"`
	UpDeltaOK          bool    `json:"up_delta_ok"`
	UpVelocityValue    float64 `json:"up_velocity_value"`
	UpDeltaValue       float64 `json:"up_delta_value"`
	UpVelocityRequired float64 `json:"up_velocity_required"`
	UpDeltaRequired    int     `json:"up_delta_required"`

	// DOWN方向条件
	DownAllowed          bool    `json:"down_allowed"`
	DownVelocityOK       bool    `json:"down_velocity_ok"`
	DownDeltaOK          bool    `json:"down_delta_ok"`
	DownVelocityValue    float64 `json:"down_velocity_value"`
	DownDeltaValue       float64 `json:"down_delta_value"`
	DownVelocityRequired float64 `json:"down_velocity_required"`
	DownDeltaRequired    int     `json:"down_delta_required"`

	// 最终选择
	Winner       string `json:"winner"`
	WinnerReason string `json:"winner_reason"`

	// 其他检查（在下单前）
	MarketQualityOK bool `json:"market_quality_ok"`
	PriceRangeOK    bool `json:"price_range_ok"`
	SpreadOK        bool `json:"spread_ok"`
	SideCooldownOK  bool `json:"side_cooldown_ok"`
	InventoryOK     bool `json:"inventory_ok"`
}

// GetStateMachineStatus 获取状态机的所有条件状态（用于Dashboard显示）
func (s *Strategy) GetStateMachineStatus() *StateMachineStatus {
	now := time.Now()
	status := &StateMachineStatus{}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 基础条件检查
	status.MarketValid = true // 假设市场有效（实际检查在OnPriceChanged中）
	status.BiasReady = s.biasReady || !s.UseBinanceOpen1mBias
	status.WarmupPassed = s.firstSeenAt.IsZero() || now.Sub(s.firstSeenAt) >= time.Duration(s.WarmupMs)*time.Millisecond
	status.TradesLimitOK = s.MaxTradesPerCycle <= 0 || s.tradesCountThisCycle < s.MaxTradesPerCycle
	status.NoPendingHedge = s.pendingHedges == nil || len(s.pendingHedges) == 0
	status.CooldownPassed = s.lastTriggerAt.IsZero() || now.Sub(s.lastTriggerAt) >= time.Duration(s.CooldownMs)*time.Millisecond

	// 周期结束保护检查（需要market信息，这里简化处理）
	status.CycleEndProtected = false // 默认不在保护期内

	// UP/DOWN方向条件
	mUp := s.computeLocked(domain.TokenTypeUp)
	mDown := s.computeLocked(domain.TokenTypeDown)

	reqMoveUp := s.MinMoveCents
	reqMoveDown := s.MinMoveCents
	reqVelUp := s.MinVelocityCentsPerSec
	reqVelDown := s.MinVelocityCentsPerSec

	// Bias调整
	allowUp := true
	allowDown := true
	if s.UseBinanceOpen1mBias && s.biasToken != "" && s.BiasMode == "hard" {
		allowUp = s.biasToken == domain.TokenTypeUp
		allowDown = s.biasToken == domain.TokenTypeDown
	}
	if s.UseBinanceOpen1mBias && s.biasToken != "" && s.BiasMode == "soft" {
		if s.biasToken == domain.TokenTypeUp {
			reqMoveDown += s.OppositeBiasMinMoveExtraCents
			reqVelDown *= s.OppositeBiasVelocityMultiplier
		} else if s.biasToken == domain.TokenTypeDown {
			reqMoveUp += s.OppositeBiasMinMoveExtraCents
			reqVelUp *= s.OppositeBiasVelocityMultiplier
		}
	}

	status.UpAllowed = allowUp
	status.UpVelocityOK = mUp.ok
	status.UpDeltaOK = mUp.ok && mUp.delta >= reqMoveUp
	status.UpVelocityValue = mUp.velocity
	status.UpDeltaValue = float64(mUp.delta)
	status.UpVelocityRequired = reqVelUp
	status.UpDeltaRequired = reqMoveUp

	status.DownAllowed = allowDown
	status.DownVelocityOK = mDown.ok
	status.DownDeltaOK = mDown.ok && mDown.delta >= reqMoveDown
	status.DownVelocityValue = mDown.velocity
	status.DownDeltaValue = float64(mDown.delta)
	status.DownVelocityRequired = reqVelDown
	status.DownDeltaRequired = reqMoveDown

	// 计算winner
	upQualified := allowUp && mUp.ok && mUp.delta >= reqMoveUp && mUp.velocity >= reqVelUp
	downQualified := allowDown && mDown.ok && mDown.delta >= reqMoveDown && mDown.velocity >= reqVelDown

	if upQualified && downQualified {
		if s.PreferHigherPrice {
			// 获取价格（简化处理）
			status.Winner = "UP/DOWN(价格优先)"
			status.WinnerReason = "两边都满足，等待价格优先选择"
		} else {
			if mUp.velocity >= mDown.velocity {
				status.Winner = "UP"
				status.WinnerReason = "速度更快"
			} else {
				status.Winner = "DOWN"
				status.WinnerReason = "速度更快"
			}
		}
	} else if upQualified {
		status.Winner = "UP"
		status.WinnerReason = "UP满足条件"
	} else if downQualified {
		status.Winner = "DOWN"
		status.WinnerReason = "DOWN满足条件"
	} else {
		status.Winner = ""
		status.WinnerReason = "无满足条件的方向"
	}

	// 方向冷却期检查
	status.SideCooldownOK = true
	if status.Winner != "" {
		lastTriggerSide := s.lastTriggerSide
		lastTriggerSideAt := s.lastTriggerSideAt
		if lastTriggerSide == domain.TokenType(status.Winner) && !lastTriggerSideAt.IsZero() {
			sideCooldown := time.Duration(s.CooldownMs) * time.Millisecond
			if sideCooldown <= 0 {
				sideCooldown = 2 * time.Second
			}
			status.SideCooldownOK = now.Sub(lastTriggerSideAt) >= sideCooldown
		}
	}

	// 其他检查（需要实际获取订单簿价格和市场质量，但为了性能，这里简化处理）
	// 注意：这些检查在实际下单时会再次验证，这里只是状态机条件的初步检查
	status.MarketQualityOK = true // 实际检查在下单时进行
	status.PriceRangeOK = true    // 实际检查在下单时进行
	status.SpreadOK = true        // 实际检查在下单时进行
	status.InventoryOK = true     // 实际检查在下单时进行

	// 如果winner不为空，尝试获取订单簿价格进行初步检查（可选，避免阻塞）
	// 这里暂时跳过，因为GetTopOfBook可能需要网络请求，会阻塞状态机检查
	// 实际的价格和价差检查会在下单时进行

	return status
}
