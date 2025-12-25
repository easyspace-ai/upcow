package velocityfollow

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

type sample struct {
	ts         time.Time
	priceCents int
}

type metrics struct {
	ok       bool
	delta    int
	seconds  float64
	velocity float64 // cents/sec
}

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

	// 未对冲的 Entry 订单（当 Hedge 订单失败时记录）
	unhedgedEntries map[string]*domain.Order

	mu sync.Mutex // 保护共享状态

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
	lastPriceLogToken     domain.TokenType
	lastPriceLogAt         time.Time
	lastPriceLogPriceCents int
	priceLogThrottleMs     int64 // 价格日志限流时间（毫秒），默认 1 秒

	// 订单跟踪：利用本地订单状态管理（新架构特性）
	lastEntryOrderID     string                   // 最后下单的 Entry 订单ID
	lastHedgeOrderID     string                   // 最后下单的 Hedge 订单ID
	lastEntryOrderStatus domain.OrderStatus       // Entry 订单状态
	pendingOrders        map[string]*domain.Order // 待确认的订单（通过订单ID跟踪）

	// 出场（平仓）节流：避免短时间重复下 SELL
	lastExitAt       time.Time
	lastExitCheckAt  time.Time

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

	// 6. 注册订单更新回调（新架构特性：利用本地订单状态管理）
	// 当订单状态更新时（通过 WebSocket 或 API 同步），立即更新本地状态
	if s.TradingService != nil {
		handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
		s.TradingService.OnOrderUpdate(handler)
		log.Infof("✅ [%s] 已注册订单更新回调（利用本地订单状态管理）", ID)

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

	// 在 Subscribe 时也注册订单更新回调（兜底方案，确保回调已注册）
	// 因为此时 TradingService 肯定已经注入，且周期切换时会重新调用 Subscribe
	if s.TradingService != nil {
		handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
		s.TradingService.OnOrderUpdate(handler)
		log.Infof("✅ [%s] 已注册订单更新回调（在 Subscribe 中注册，利用本地订单状态管理）", ID)
	} else {
		log.Warnf("⚠️ [%s] TradingService 为 nil，无法注册订单更新回调", ID)
	}
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
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
func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, _ *domain.Market) {
	s.mu.Lock()
	defer s.mu.Unlock()

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
	s.lastExitAt = time.Time{}
	s.lastExitCheckAt = time.Time{}

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
		}
		
		// Hedge 订单失败时，检查对应的 Entry 订单是否已成交
		if order.Status == domain.OrderStatusFailed || order.Status == domain.OrderStatusCanceled {
			log.Warnf("⚠️ [%s] Hedge 订单失败/取消: orderID=%s status=%s",
				ID, order.OrderID, order.Status)

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

	// 1. 市场过滤：只处理目标市场（通过 prefix 匹配）
	if !strings.HasPrefix(strings.ToLower(e.Market.Slug), s.marketSlugPrefix) {
		return nil
	}

	now := e.Timestamp
	if now.IsZero() {
		now = time.Now()
	}

	// 显示 WebSocket 实时价格（用于调试，带限流避免刷屏）
	priceDecimal := e.NewPrice.ToDecimal()
	priceCents := e.NewPrice.ToCents()
	
	// 价格日志限流：同一 token 的价格更新，如果价格变化不大且时间间隔短，则限流
	// 注意：这里需要在加锁前检查，避免死锁
	shouldLogPrice := false
	var timeSinceLastLog time.Duration
	var priceChange int
	
	s.mu.Lock()
	// 在锁内检查限流条件
	if s.lastPriceLogToken != e.TokenType || s.lastPriceLogAt.IsZero() {
		// 不同 token 或首次，直接打印
		shouldLogPrice = true
	} else {
		// 相同 token，检查时间间隔和价格变化
		logThrottle := time.Duration(s.priceLogThrottleMs) * time.Millisecond
		if logThrottle <= 0 {
			logThrottle = 1 * time.Second // 默认 1 秒
		}
		timeSinceLastLog = now.Sub(s.lastPriceLogAt)
		priceChange = priceCents - s.lastPriceLogPriceCents
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
		s.lastPriceLogToken = e.TokenType
		s.lastPriceLogAt = now
		s.lastPriceLogPriceCents = priceCents
	}
	s.mu.Unlock()
	
	// 在锁外打印日志（避免长时间持锁）
	if shouldLogPrice {
		log.Debugf("📈 [%s] 价格更新: token=%s price=%.4f (%dc) market=%s",
			ID, e.TokenType, priceDecimal, priceCents, e.Market.Slug)
	}

	// ===== 出场（平仓）逻辑：优先于开仓 =====
	// 仅当启用 TP/SL/超时退出 且 当前 market 存在持仓时才触发（避免每个 tick 都打 orderbook）
	if s.exitEnabled() && e.Market != nil {
		positions := s.TradingService.GetOpenPositionsForMarket(e.Market.Slug)
		hasPos := false
		for _, p := range positions {
			if p != nil && p.IsOpen() && p.Size > 0 {
				hasPos = true
				break
			}
		}
		if hasPos {
			// 节流：避免每条行情都尝试出场（默认 200ms）
			nowCheck := now
			s.mu.Lock()
			lastCheck := s.lastExitCheckAt
			s.mu.Unlock()
			if lastCheck.IsZero() || nowCheck.Sub(lastCheck) >= 200*time.Millisecond {
				s.mu.Lock()
				s.lastExitCheckAt = nowCheck
				s.mu.Unlock()
				if exited := s.tryExitPositions(ctx, e.Market, nowCheck, positions); exited {
					return nil
				}
			}
			// 已有持仓时默认不再开新仓，等待出场逻辑处理完毕（避免叠加风险）
			return nil
		}
	}

	s.mu.Lock()

	if s.firstSeenAt.IsZero() {
		s.firstSeenAt = now
	}

	// 2. 周期检测：检测周期切换，更新 cycleStartMs
	// 尽量用 market.Timestamp 作为本周期起点（框架会从 slug 解析）
	if e.Market.Timestamp > 0 {
		st := e.Market.Timestamp * 1000
		if s.cycleStartMs == 0 || s.cycleStartMs != st {
			s.cycleStartMs = st
			s.biasReady = false
			s.biasToken = ""
			s.biasReason = ""
		}
	}

	// 3. Binance bias：检查开盘 1m K 线 bias（如果启用）
	// 可选：用"开盘第 1 根 1m K线阴阳"做 bias（hard/soft）
	if s.UseBinanceOpen1mBias {
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

		if s.RequireBiasReady && !s.biasReady {
			s.mu.Unlock()
			return nil
		}
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

	// 5. 交易限制检查
	// 5.1 兼容旧逻辑：OncePerCycle
	if s.OncePerCycle && s.tradedThisCycle {
		s.mu.Unlock()
		return nil
	}
	// 5.2 新逻辑：MaxTradesPerCycle 控制（0=不设限）
	if s.MaxTradesPerCycle > 0 && s.tradesCountThisCycle >= s.MaxTradesPerCycle {
		s.mu.Unlock()
		log.Debugf("🔄 [%s] 跳过：本周期交易次数已达上限 (%d/%d)", ID, s.tradesCountThisCycle, s.MaxTradesPerCycle)
		return nil
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

	// 计算 UP/DOWN 指标，选择"上行更快"的一侧触发
	mUp := s.computeLocked(domain.TokenTypeUp)
	mDown := s.computeLocked(domain.TokenTypeDown)

	// 获取当前价格（用于价格优先选择）
	var upPriceCents, downPriceCents int
	if s.PreferHigherPrice {
		upSamples := s.samples[domain.TokenTypeUp]
		downSamples := s.samples[domain.TokenTypeDown]
		if len(upSamples) > 0 {
			upPriceCents = upSamples[len(upSamples)-1].priceCents
		}
		if len(downSamples) > 0 {
			downPriceCents = downSamples[len(downSamples)-1].priceCents
		}
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
	maxEntry := s.MaxEntryPriceCents
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
	entryAskCents := int(entryAskDec*100 + 0.5)       // FAK 实际下单 ask（cents）
	entryBidCents := int(entryBidDec*100 + 0.5)
	hedgeBidCents := int(hedgeBidDec*100 + 0.5)
	hedgeAskCentsDirect := int(hedgeAskDec*100 + 0.5) // 对侧当前 ask（仅用于防止挂单穿价）

	// 基础验证
	if entryAskCents <= 0 || entryAskCents >= 100 || hedgeAskCentsDirect <= 0 || hedgeAskCentsDirect >= 100 {
		log.Debugf("⚠️ [%s] 订单簿价格无效: entryAsk=%dc hedgeAsk=%dc", ID, entryAskCents, hedgeAskCentsDirect)
		return nil
	}

	// Entry 价格上限检查
	if maxEntry > 0 && entryAskCents > maxEntry {
		log.Debugf("⏭️ [%s] 跳过：Entry 价格超过上限 (%dc > %dc)", ID, entryAskCents, maxEntry)
		return nil
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
	entryPriceForFAK := domain.Price{Pips: entryAskCents * 100}   // FAK：使用实际 ask
	hedgePrice := domain.Price{Pips: hedgeLimitCents * 100}       // GTC：互补挂单价（maker）
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

// executeSequential 顺序下单模式（新架构特性）
//
// 执行流程：
// 1. 下 Entry 订单（FAK，立即成交或取消）
// 2. 等待 Entry 订单成交（轮询检查订单状态）
// 3. Entry 成交后，下 Hedge 订单（GTC 限价单）
//
// 优势：
// - 风险低：确保 Entry 成交后再下 Hedge
// - 适合 FAK 订单：FAK 订单通常立即成交
//
// 参数：
// - SequentialCheckIntervalMs: 检查订单状态的间隔（默认 50ms）
// - SequentialMaxWaitMs: 最大等待时间（默认 1000ms）
func (s *Strategy) executeSequential(ctx context.Context, market *domain.Market, winner domain.TokenType,
	entryAsset, hedgeAsset string, entryPrice, hedgePrice domain.Price, entryShares, hedgeShares float64,
	entryAskCents, hedgeAskCents int, winMet metrics, biasTok, biasReason string) error {
	// 使用更短的超时时间（10秒），快速失败，避免阻塞策略
	orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// ===== 顺序下单：先买主单（Entry），成交后再下对冲单（Hedge）=====
	// ⚠️ 重要：FAK 买入订单必须在下单前再次验证订单簿价格和流动性
	// 因为价格可能在获取订单簿和下单之间发生变化
	// 策略：使用卖二价作为缓冲，提高下单成功率
	// - 卖一价（asks[0]）是最优价格，但可能很快被吃掉
	// - 卖二价（asks[1]）是次优价格，更稳定，有更大的价格缓冲空间
	// - 使用卖二价下单，即使卖一价被吃掉，仍然可以匹配到卖二价
	secondLevelPrice, hasSecondLevel := s.TradingService.GetSecondLevelPrice(orderCtx, entryAsset, types.SideBuy)
	_, actualAsk, err := s.TradingService.GetBestPrice(orderCtx, entryAsset)
	
	if err != nil {
		log.Warnf("⚠️ [%s] 下单前获取订单簿价格失败，使用原价格: err=%v", ID, err)
	} else if actualAsk > 0 {
		// 优先使用卖二价（如果存在且合理）
		targetPrice := actualAsk
		targetPriceName := "卖一价"
		
		if hasSecondLevel && secondLevelPrice > 0 && secondLevelPrice <= actualAsk*1.02 {
			// 卖二价存在且不超过卖一价的 2%，使用卖二价
			targetPrice = secondLevelPrice
			targetPriceName = "卖二价"
			log.Infof("💰 [%s] 使用卖二价作为缓冲: 卖一价=%.4f, 卖二价=%.4f (价格缓冲=%.2f%%)",
				ID, actualAsk, secondLevelPrice, (secondLevelPrice-actualAsk)/actualAsk*100)
		}
		
		// 对于买入订单，需要检查 ask 价格
		targetPriceCents := int(targetPrice*100 + 0.5)
		entryPriceCents := int(entryPrice.ToDecimal()*100 + 0.5)
		priceDiffCents := targetPriceCents - entryPriceCents
		
		if priceDiffCents > 0 {
			// 订单簿的 ask 价格高于我们的价格
			// 如果价格偏差 <= 5c，调整价格为订单簿的 ask 价格
			// 如果价格偏差 > 5c，跳过这次下单（市场波动太大）
			if priceDiffCents <= 5 {
				log.Warnf("⚠️ [%s] 订单簿价格变化：原价格=%dc, %s=%dc (偏差=%dc)，调整为订单簿价格",
					ID, entryPriceCents, targetPriceName, targetPriceCents, priceDiffCents)
				entryPrice = domain.PriceFromDecimal(targetPrice)
			} else {
				log.Warnf("⚠️ [%s] 订单簿价格变化过大：原价格=%dc, %s=%dc (偏差=%dc > 5c)，跳过下单",
					ID, entryPriceCents, targetPriceName, targetPriceCents, priceDiffCents)
				return nil // 跳过这次下单
			}
		} else if priceDiffCents < 0 {
			// 订单簿的 ask 价格低于我们的价格，这是正常的，可以使用我们的价格
			log.Debugf("💰 [%s] 订单簿价格更好：我们的价格=%dc, %s=%dc，使用我们的价格",
				ID, entryPriceCents, targetPriceName, targetPriceCents)
		} else {
			// 价格一致
			log.Debugf("💰 [%s] 订单簿价格一致：价格=%dc (%s)", ID, entryPriceCents, targetPriceName)
		}
	}

	// ⚠️ 重要：价格调整后，需要重新进行精度调整
	// 因为价格可能从有效价格调整为实际订单簿价格（卖一价或卖二价）
	// 精度调整必须使用实际下单价格，确保 maker amount = size × price 是 2 位小数
	entryPriceDec := entryPrice.ToDecimal()
	entrySharesAdjusted := adjustSizeForMakerAmountPrecision(entryShares, entryPriceDec)
	if entrySharesAdjusted != entryShares {
		log.Infof("🔧 [%s] Entry size 精度调整（价格调整后）: %.4f -> %.4f (maker amount: %.2f -> %.2f, price=%.4f)",
			ID, entryShares, entrySharesAdjusted, entryShares*entryPriceDec, entrySharesAdjusted*entryPriceDec, entryPriceDec)
		entryShares = entrySharesAdjusted
	}

	// 检查订单簿流动性（使用 REST API 获取完整订单簿）
	hasLiquidity, actualPrice, availableSize := s.TradingService.CheckOrderBookLiquidity(
		orderCtx, entryAsset, types.SideBuy, entryPrice.ToDecimal(), entryShares)
	if !hasLiquidity {
		log.Warnf("⚠️ [%s] 订单簿无流动性：价格=%dc, size=%.4f，跳过下单",
			ID, int(entryPrice.ToDecimal()*100+0.5), entryShares)
		return nil // 跳过这次下单
	}
	
	// 如果可用数量不足，记录警告但仍尝试下单（FAK 允许部分成交）
	if availableSize < entryShares {
		log.Warnf("⚠️ [%s] 订单簿流动性不足：需要=%.4f, 可用=%.4f, 实际价格=%.4f，FAK订单将尝试部分成交",
			ID, entryShares, availableSize, actualPrice)
		// FAK 订单允许部分成交，所以继续下单
	} else {
		log.Infof("✅ [%s] 订单簿流动性充足：需要=%.4f, 可用=%.4f, 实际价格=%.4f",
			ID, entryShares, availableSize, actualPrice)
	}

	// 主单：价格 >= minPreferredPriceCents 的订单（FAK，立即成交或取消）
	log.Infof("📤 [%s] 步骤1: 下主单 Entry (side=%s price=%dc size=%.4f FAK)",
		ID, winner, int(entryPrice.ToDecimal()*100+0.5), entryShares)

	entryOrder := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      entryAsset,
		TokenType:    winner,
		Side:         types.SideBuy,
		Price:        entryPrice,
		Size:         entryShares,
		OrderType:    types.OrderTypeFAK,
		IsEntryOrder: true,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
	}

	entryOrderResult, execErr := s.TradingService.PlaceOrder(orderCtx, entryOrder)
	if execErr != nil {
		log.Warnf("⚠️ [%s] 主单下单失败: err=%v side=%s market=%s", ID, execErr, winner, market.Slug)
		return nil
	}

	if entryOrderResult == nil || entryOrderResult.OrderID == "" {
		log.Warnf("⚠️ [%s] 主单下单失败: 订单ID为空", ID)
		return nil
	}

	log.Infof("✅ [%s] 主单已提交: orderID=%s status=%s",
		ID, entryOrderResult.OrderID, entryOrderResult.Status)

	// 等待主单成交（FAK 订单要么立即成交，要么立即取消）
	// 优化：使用更短的检查间隔和更长的等待时间，同时使用订单更新回调来检测成交
	maxWaitTime := time.Duration(s.Config.SequentialMaxWaitMs) * time.Millisecond
	if maxWaitTime <= 0 {
		maxWaitTime = 2000 * time.Millisecond // 默认 2 秒
	}
	checkInterval := time.Duration(s.Config.SequentialCheckIntervalMs) * time.Millisecond
	if checkInterval <= 0 {
		checkInterval = 20 * time.Millisecond // 默认 20ms（更频繁）
	}
	entryFilled := false
	entryOrderID := entryOrderResult.OrderID

	// ✅ 修复：在纸交易模式下，FAK 订单应该立即成交
	// 因为 io_executor 在纸交易模式下会将 FAK 订单状态设置为 filled
	if s.TradingService != nil && s.TradingService.IsDryRun() && entryOrderResult.OrderType == types.OrderTypeFAK {
		// 纸交易模式：FAK 订单立即成交
		entryFilled = true
		log.Infof("✅ [%s] 主单已成交（纸交易模式，FAK 订单立即成交）: orderID=%s",
			ID, entryOrderID)
	}

	// 先检查一次订单状态（可能已经成交）
	// ⚠️ 重要：优先检查 entryOrderResult 的状态，因为它可能已经通过 WebSocket 更新
	if !entryFilled && entryOrderResult != nil {
		if entryOrderResult.Status == domain.OrderStatusFilled {
			entryFilled = true
			log.Infof("✅ [%s] 主单已成交（通过订单结果）: orderID=%s filledSize=%.4f",
				ID, entryOrderID, entryOrderResult.FilledSize)
		} else if entryOrderResult.Status == domain.OrderStatusFailed ||
			entryOrderResult.Status == domain.OrderStatusCanceled {
			log.Warnf("⚠️ [%s] 主单失败/取消（通过订单结果）: orderID=%s status=%s",
				ID, entryOrderID, entryOrderResult.Status)
			return nil
		}
	}

	// 如果订单结果中没有成交信息，再检查本地订单状态（包含已成交订单）
	// ⚠️ 修复：GetActiveOrders 只包含 openOrders，订单一旦 filled 会从列表移除，导致“误判未成交”。
	if !entryFilled && s.TradingService != nil {
		if ord, ok := s.TradingService.GetOrder(entryOrderID); ok && ord != nil {
			if ord.Status == domain.OrderStatusFilled {
				entryFilled = true
				log.Infof("✅ [%s] 主单已成交（立即检查）: orderID=%s filledSize=%.4f",
					ID, ord.OrderID, ord.FilledSize)
			} else if ord.Status == domain.OrderStatusFailed || ord.Status == domain.OrderStatusCanceled {
				log.Warnf("⚠️ [%s] 主单失败/取消（立即检查）: orderID=%s status=%s",
					ID, ord.OrderID, ord.Status)
				return nil
			}
		}
	}

	// 如果未成交，轮询检查订单状态（使用更短的间隔）
	if !entryFilled {
		deadline := time.Now().Add(maxWaitTime)
		checkCount := 0
		for time.Now().Before(deadline) {
			checkCount++
			// 查询订单状态（包含已成交/已取消）
			if s.TradingService != nil {
				if ord, ok := s.TradingService.GetOrder(entryOrderID); ok && ord != nil {
					if ord.Status == domain.OrderStatusFilled {
						entryFilled = true
						log.Infof("✅ [%s] 主单已成交（轮询检查，第%d次）: orderID=%s filledSize=%.4f",
							ID, checkCount, ord.OrderID, ord.FilledSize)
					} else if ord.Status == domain.OrderStatusFailed || ord.Status == domain.OrderStatusCanceled {
						log.Warnf("⚠️ [%s] 主单失败/取消（轮询检查，第%d次）: orderID=%s status=%s",
							ID, checkCount, ord.OrderID, ord.Status)
						return nil
					}
				}
			}

			if entryFilled {
				break
			}

			// 等待一小段时间后再次检查（使用更短的间隔）
			time.Sleep(checkInterval)
		}

		if !entryFilled {
			log.Debugf("🔄 [%s] 主单轮询检查完成（共检查%d次）: orderID=%s 未在预期时间内成交",
				ID, checkCount, entryOrderID)
		}
	}

	if !entryFilled {
		log.Warnf("⚠️ [%s] 主单未在预期时间内成交: orderID=%s (可能部分成交或仍在处理中)",
			ID, entryOrderID)
		// 即使主单未完全成交，也继续下对冲单（使用实际成交数量）
		// 但为了安全，我们仍然继续执行
	}

	// ✅ 修复：若 Entry 下单前发生了价格上调（例如使用卖二价缓冲），必须同步重算 Hedge 互补挂单价，
	// 否则可能出现 entryPrice 上调后 totalCost > 100c 的结构性必亏。
	{
		entryCentsNow := int(entryPrice.ToDecimal()*100 + 0.5)
		if entryCentsNow > 0 && entryCentsNow < 100 && s.HedgeOffsetCents > 0 {
			newHedgeLimit := 100 - entryCentsNow - s.HedgeOffsetCents
			if newHedgeLimit > 0 && newHedgeLimit < 100 {
				// 防止穿价：确保买单价格 < 当前 ask
				if s.TradingService != nil {
					_, bestAsk, err := s.TradingService.GetBestPrice(orderCtx, hedgeAsset)
					if err == nil && bestAsk > 0 {
						askCents := int(bestAsk*100 + 0.5)
						if newHedgeLimit >= askCents {
							newHedgeLimit = askCents - 1
						}
					}
				}
				if newHedgeLimit > 0 && newHedgeLimit < 100 && newHedgeLimit != hedgeAskCents {
					log.Infof("💰 [%s] Hedge 价格随 Entry 调整而重算: entry=%dc hedge(old)=%dc -> hedge(new)=%dc (offset=%dc)",
						ID, entryCentsNow, hedgeAskCents, newHedgeLimit, s.HedgeOffsetCents)
					hedgeAskCents = newHedgeLimit
					hedgePrice = domain.Price{Pips: hedgeAskCents * 100}
				}
			}
		}
	}

	// ===== 步骤2: 主单成交后，下对冲单（Hedge）=====
	log.Infof("📤 [%s] 步骤2: 下对冲单 Hedge (side=%s price=%dc size=%.4f GTC)",
		ID, opposite(winner), hedgeAskCents, hedgeShares)

	hedgeOrder := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      hedgeAsset,
		TokenType:    opposite(winner),
		Side:         types.SideBuy,
		Price:        hedgePrice,
		Size:         hedgeShares,
		OrderType:    types.OrderTypeGTC,
		IsEntryOrder: false,
		HedgeOrderID: &entryOrderID, // 关联主单ID
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
	}

	hedgeOrderResult, hedgeErr := s.TradingService.PlaceOrder(orderCtx, hedgeOrder)
	hedgeOrderID := ""
	if hedgeErr != nil {
		log.Errorf("❌ [%s] 对冲单下单失败: err=%v (主单已成交，需要处理)",
			ID, hedgeErr)
		
		// ⚠️ 重要：如果 Entry 订单已成交，但 Hedge 订单失败，这是一个高风险情况
		// 选项1：如果 Entry 订单还未完全成交，尝试取消 Entry 订单
		// 选项2：记录未对冲的 Entry 订单，提醒手动处理
		if entryFilled {
			// Entry 订单已成交，无法取消，记录未对冲风险
			log.Errorf("🚨 [%s] 【风险警告】Entry 订单已成交但 Hedge 订单失败！Entry orderID=%s, 需要手动对冲！",
				ID, entryOrderID)
			log.Errorf("🚨 [%s] Entry 订单详情: side=%s, price=%dc, size=%.4f, filledSize=%.4f",
				ID, winner, entryAskCents, entryShares, entryShares)
			log.Errorf("🚨 [%s] 建议：立即手动下 Hedge 订单对冲风险，或取消 Entry 订单（如果可能）",
				ID)
			
			// 记录未对冲的 Entry 订单到策略状态中，方便后续查询
			s.mu.Lock()
			if s.unhedgedEntries == nil {
				s.unhedgedEntries = make(map[string]*domain.Order)
			}
			if entryOrderResult != nil {
				s.unhedgedEntries[entryOrderID] = entryOrderResult
				log.Errorf("🚨 [%s] 已记录未对冲的 Entry 订单到策略状态: orderID=%s",
					ID, entryOrderID)
			}
			s.mu.Unlock()
		} else {
			// Entry 订单未成交或部分成交，尝试取消 Entry 订单
			log.Warnf("⚠️ [%s] Entry 订单未完全成交，尝试取消 Entry 订单以避免未对冲风险: orderID=%s",
				ID, entryOrderID)
			go func(orderID string) {
				if err := s.TradingService.CancelOrder(context.Background(), orderID); err != nil {
					log.Warnf("⚠️ [%s] 取消 Entry 订单失败: orderID=%s err=%v", ID, orderID, err)
				} else {
					log.Infof("✅ [%s] 已取消 Entry 订单（Hedge 订单失败）: orderID=%s", ID, orderID)
				}
			}(entryOrderID)
		}
		
		// 主单已成交，对冲单失败，这是一个风险情况
		execErr = hedgeErr
		return nil // 返回错误，不再继续执行
	} else if hedgeOrderResult != nil && hedgeOrderResult.OrderID != "" {
		hedgeOrderID = hedgeOrderResult.OrderID
		log.Infof("✅ [%s] 对冲单已提交: orderID=%s status=%s (关联主单=%s)",
			ID, hedgeOrderResult.OrderID, hedgeOrderResult.Status, entryOrderID)
	} else {
		log.Errorf("❌ [%s] 对冲单下单失败: 订单ID为空 (主单已成交，需要手动处理)",
			ID)
		// 同样处理：记录未对冲风险或取消 Entry 订单
		if entryFilled {
			log.Errorf("🚨 [%s] 【风险警告】Entry 订单已成交但 Hedge 订单ID为空！Entry orderID=%s",
				ID, entryOrderID)
			s.mu.Lock()
			if s.unhedgedEntries == nil {
				s.unhedgedEntries = make(map[string]*domain.Order)
			}
			if entryOrderResult != nil {
				s.unhedgedEntries[entryOrderID] = entryOrderResult
			}
			s.mu.Unlock()
		} else {
			go func(orderID string) {
				_ = s.TradingService.CancelOrder(context.Background(), orderID)
			}(entryOrderID)
		}
		return nil
	}

	// 更新订单关联关系（如果对冲单成功）
	// entryOrderResult 一定不为 nil（因为如果为 nil，execErr 不为 nil，函数会提前返回）
	if hedgeOrderID != "" {
		entryOrderResult.HedgeOrderID = &hedgeOrderID
	}

	// ===== 主单成交后：实时计算盈亏并监控对冲单 =====
	if entryFilled {
		entryFilledTime := time.Now()
		entryFilledSize := entryShares
		if entryOrderResult.FilledSize > 0 {
			entryFilledSize = entryOrderResult.FilledSize
		}

		// 实时计算盈亏：如果 UP/DOWN 各自 win 时的收益与亏损
		// 使用实际成交价格（从 Trade 消息获取），而不是下单时的价格

		// Entry 成本：优先使用实际成交价格，如果没有则使用实际下单价格（不是有效价格）
		// ⚠️ 重要：entryPrice 是实际下单价格（可能已被调整为订单簿价格），entryAskCents 是有效价格（用于成本估算）
		// 如果 FilledPrice 为空，应该使用实际下单价格 entryPrice，而不是有效价格 entryAskCents
		var entryActualPriceCents int
		entryOrderPriceCents := int(entryPrice.ToDecimal()*100 + 0.5) // 实际下单价格
		if entryOrderResult.FilledPrice != nil {
			entryActualPriceCents = entryOrderResult.FilledPrice.ToCents()
			log.Debugf("💰 [%s] Entry 使用实际成交价格: %dc (下单价格: %dc, 有效价格: %dc)", ID, entryActualPriceCents, entryOrderPriceCents, entryAskCents)
		} else {
			entryActualPriceCents = entryOrderPriceCents // 使用实际下单价格，而不是有效价格
			log.Debugf("💰 [%s] Entry 使用实际下单价格: %dc (有效价格: %dc, 实际成交价格未获取)", ID, entryOrderPriceCents, entryAskCents)
		}
		entryCost := float64(entryActualPriceCents) / 100.0 * entryFilledSize

		// 计算如果 UP win 时的盈亏
		var upWinProfit, downWinProfit float64
		if winner == domain.TokenTypeUp {
			// Entry 是 UP，如果 UP win：收益 = entryFilledSize * $1 - entryCost
			upWinProfit = entryFilledSize*1.0 - entryCost
			// 如果 DOWN win：亏损 = -entryCost（对冲单未成交时）
			downWinProfit = -entryCost
		} else {
			// Entry 是 DOWN，如果 DOWN win：收益 = entryFilledSize * $1 - entryCost
			downWinProfit = entryFilledSize*1.0 - entryCost
			// 如果 UP win：亏损 = -entryCost（对冲单未成交时）
			upWinProfit = -entryCost
		}

		// 计算 Hedge 订单成本（无论是否已成交）
		// 如果对冲单已成交，使用实际成交价格；如果未成交，使用下单价格
		if hedgeOrderID != "" && s.TradingService != nil {
			var hedgeOrder *domain.Order
			if ord, ok := s.TradingService.GetOrder(hedgeOrderID); ok {
				hedgeOrder = ord
			}

			if hedgeOrder != nil {
				// 获取 Hedge 订单的实际成交数量
				hedgeFilledSize := hedgeOrder.FilledSize
				if hedgeFilledSize <= 0 {
					// 如果未成交，使用下单时的 size（因为我们需要承担这个成本）
					hedgeFilledSize = hedgeShares
				}

				// 优先使用实际成交价格，如果没有则使用实际下单价格（不是有效价格）
				// ⚠️ 重要：hedgePrice 是实际下单价格（有效价格），hedgeAskCents 也是有效价格
				// 对于 GTC 订单，下单价格就是有效价格，所以可以直接使用 hedgeAskCents
				// 但如果 FilledPrice 存在，应该优先使用实际成交价格
				var hedgeActualPriceCents int
				hedgeOrderPriceCents := int(hedgePrice.ToDecimal()*100 + 0.5) // 实际下单价格（对于GTC订单，这就是有效价格）
				if hedgeOrder.FilledPrice != nil {
					hedgeActualPriceCents = hedgeOrder.FilledPrice.ToCents()
					log.Debugf("💰 [%s] Hedge 使用实际成交价格: %dc (下单价格: %dc, 有效价格: %dc)", ID, hedgeActualPriceCents, hedgeOrderPriceCents, hedgeAskCents)
				} else {
					hedgeActualPriceCents = hedgeOrderPriceCents // 使用实际下单价格（对于GTC订单，这就是有效价格）
					if hedgeOrder.Status == domain.OrderStatusFilled {
						log.Debugf("💰 [%s] Hedge 使用下单价格: %dc (实际成交价格未获取，但订单已成交)", ID, hedgeOrderPriceCents)
					} else {
						log.Debugf("💰 [%s] Hedge 使用下单价格: %dc (订单未成交，使用下单价格计算成本)", ID, hedgeOrderPriceCents)
					}
				}

				hedgeCost := float64(hedgeActualPriceCents) / 100.0 * hedgeFilledSize
				totalCost := entryCost + hedgeCost

				// 记录价格对比（如果实际价格与下单价格不同）
				if hedgeOrder.Status == domain.OrderStatusFilled && hedgeActualPriceCents != hedgeAskCents {
					log.Infof("💰 [%s] 对冲单价格差异: 下单价格=%dc, 实际成交价格=%dc, 差异=%dc",
						ID, hedgeAskCents, hedgeActualPriceCents, hedgeActualPriceCents-hedgeAskCents)
				}

				// 重新计算盈亏（考虑 Hedge 成本）
				if winner == domain.TokenTypeUp {
					// Entry UP + Hedge DOWN，无论哪边 win，总成本 = entryCost + hedgeCost
					// UP win: 收益 = entryFilledSize * $1 - totalCost
					// DOWN win: 收益 = hedgeFilledSize * $1 - totalCost
					upWinProfit = entryFilledSize*1.0 - totalCost
					downWinProfit = hedgeFilledSize*1.0 - totalCost
				} else {
					// Entry DOWN + Hedge UP
					downWinProfit = entryFilledSize*1.0 - totalCost
					upWinProfit = hedgeFilledSize*1.0 - totalCost
				}

				// 记录 Hedge 订单状态
				if hedgeOrder.Status == domain.OrderStatusFilled {
					log.Debugf("💰 [%s] Hedge 订单已成交，使用实际成交价格计算成本", ID)
				} else {
					log.Debugf("💰 [%s] Hedge 订单未成交（status=%s），使用下单价格计算成本", ID, hedgeOrder.Status)
				}
			} else {
				// Hedge 订单未找到，使用下单价格计算成本（保守估计）
				log.Debugf("💰 [%s] Hedge 订单未找到，使用下单价格计算成本: price=%dc size=%.4f", ID, hedgeAskCents, hedgeShares)
				hedgeCost := float64(hedgeAskCents) / 100.0 * hedgeShares
				totalCost := entryCost + hedgeCost

				// 重新计算盈亏（考虑 Hedge 成本）
				if winner == domain.TokenTypeUp {
					upWinProfit = entryFilledSize*1.0 - totalCost
					downWinProfit = hedgeShares*1.0 - totalCost
				} else {
					downWinProfit = entryFilledSize*1.0 - totalCost
					upWinProfit = hedgeShares*1.0 - totalCost
				}
			}
		}

		// 计算 Hedge 成本（用于日志显示）
		hedgeCostDisplay := 0.0
		if hedgeOrderID != "" && s.TradingService != nil {
			if ord, ok := s.TradingService.GetOrder(hedgeOrderID); ok && ord != nil {
				hedgeFilledSize := ord.FilledSize
				if hedgeFilledSize <= 0 {
					hedgeFilledSize = hedgeShares
				}
				var hedgeActualPriceCents int
				if ord.FilledPrice != nil {
					hedgeActualPriceCents = ord.FilledPrice.ToCents()
				} else {
					hedgeActualPriceCents = hedgeAskCents
				}
				hedgeCostDisplay = float64(hedgeActualPriceCents) / 100.0 * hedgeFilledSize
			}
		}
		totalCostDisplay := entryCost + hedgeCostDisplay

		log.Infof("💰 [%s] 主单成交后实时盈亏计算: Entry=%s @ %dc(有效)/%dc(下单)/%dc(实际) size=%.4f cost=$%.2f | Hedge cost=$%.2f | Total cost=$%.2f | UP win: $%.2f | DOWN win: $%.2f",
			ID, winner, entryAskCents, entryOrderPriceCents, entryActualPriceCents, entryFilledSize, entryCost, hedgeCostDisplay, totalCostDisplay, upWinProfit, downWinProfit)

		// 启动对冲单重下监控（如果对冲单未成交）
		if hedgeOrderID != "" && s.HedgeReorderTimeoutSeconds > 0 {
			// 使用 Entry 实际下单价格（不是“信号时刻的 ask”）作为对冲成本约束基准
			go s.monitorAndReorderHedge(ctx, market, entryOrderID, hedgeOrderID, hedgeAsset, hedgePrice, hedgeShares, entryFilledTime, entryFilledSize, entryOrderPriceCents, winner)
		}
	}

	var tradesCount int
	// entryOrderResult 一定不为 nil（因为如果为 nil，execErr 不为 nil，函数会提前返回）
	if execErr == nil {
		now := time.Now()
		// 只在更新共享状态时持锁，避免阻塞订单更新回调/行情分发（性能关键）
		s.mu.Lock()
		s.lastTriggerAt = now
		// 注意：lastTriggerSide 和 lastTriggerSideAt 已经在上面提前更新了
		// 这里只需要更新交易计数和订单跟踪状态
		s.tradedThisCycle = true
		s.tradesCountThisCycle++ // 增加交易计数

		// 更新订单跟踪状态
		s.lastEntryOrderID = entryOrderResult.OrderID
		s.lastEntryOrderStatus = entryOrderResult.Status
		if entryFilled {
			s.lastEntryOrderStatus = domain.OrderStatusFilled
		}
		if hedgeOrderID != "" {
			s.lastHedgeOrderID = hedgeOrderID
		}
		tradesCount = s.tradesCountThisCycle
		s.mu.Unlock()

		log.Infof("⚡ [%s] 触发(顺序): side=%s ask=%dc hedge=%dc vel=%.3f(c/s) move=%dc/%0.1fs bias=%s(%s) market=%s trades=%d/%d",
			ID, winner, entryAskCents, hedgeAskCents, winMet.velocity, winMet.delta, winMet.seconds, biasTok, biasReason, market.Slug, tradesCount, s.MaxTradesPerCycle)
		if biasTok != "" || biasReason != "" {
			log.Infof("🧭 [%s] bias: token=%s reason=%s cycleStartMs=%d", ID, biasTok, biasReason, s.cycleStartMs)
		}

		// 额外：打印 Binance 1s/1m 最新 K 线（用于你观察“开盘 1 分钟”关系）
		if s.BinanceFuturesKlines != nil {
			if k1m, ok := s.BinanceFuturesKlines.Latest("1m"); ok {
				log.Infof("📊 [%s] Binance 1m kline: sym=%s o=%.2f c=%.2f h=%.2f l=%.2f closed=%v startMs=%d",
					ID, k1m.Symbol, k1m.Open, k1m.Close, k1m.High, k1m.Low, k1m.IsClosed, k1m.StartTimeMs)
			}
			if k1s, ok := s.BinanceFuturesKlines.Latest("1s"); ok {
				log.Infof("📊 [%s] Binance 1s kline: sym=%s o=%.2f c=%.2f closed=%v startMs=%d",
					ID, k1s.Symbol, k1s.Open, k1s.Close, k1s.IsClosed, k1s.StartTimeMs)
			}
		}
	} else {
		log.Warnf("⚠️ [%s] 下单失败: err=%v side=%s market=%s", ID, execErr, winner, market.Slug)
	}
	return nil
}

// executeParallel 并发下单模式（新架构特性）
//
// 执行流程：
// 1. 同时提交 Entry 和 Hedge 订单（使用 ExecuteMultiLeg）
// 2. 等待两个订单都返回结果
//
// 优势：
// - 速度快：减少下单延迟（~100-200ms）
// - 适合高频交易：减少跨腿时差
//
// 风险：
// - Entry 订单失败时，Hedge 订单可能已提交（通过 OnOrderUpdate 自动取消）
func (s *Strategy) executeParallel(ctx context.Context, market *domain.Market, winner domain.TokenType,
	entryAsset, hedgeAsset string, entryPrice, hedgePrice domain.Price, entryShares, hedgeShares float64,
	entryAskCents, hedgeAskCents int, winMet metrics, biasTok, biasReason string) error {
	// 使用更短的超时时间（10秒），快速失败，避免阻塞策略
	orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// ===== 并发下单：使用 ExecuteMultiLeg 同时提交 Entry 和 Hedge 订单 =====
	req := execution.MultiLegRequest{
		Name:       "velocityfollow",
		MarketSlug: market.Slug,
		Legs: []execution.LegIntent{
			{
				Name:      "taker_buy_winner",
				AssetID:   entryAsset,
				TokenType: winner,
				Side:      types.SideBuy,
				Price:     entryPrice,
				Size:      entryShares,
				OrderType: types.OrderTypeFAK,
			},
			{
				Name:      "maker_buy_hedge",
				AssetID:   hedgeAsset,
				TokenType: opposite(winner),
				Side:      types.SideBuy,
				Price:     hedgePrice,
				Size:      hedgeShares,
				OrderType: types.OrderTypeGTC,
			},
		},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}

	createdOrders, execErr := s.TradingService.ExecuteMultiLeg(orderCtx, req)
	var tradesCount int
	if execErr == nil && len(createdOrders) > 0 {
		now := time.Now()
		// 只在更新共享状态时持锁（性能关键）
		s.mu.Lock()
		s.lastTriggerAt = now
		s.lastTriggerSide = winner
		s.lastTriggerSideAt = now
		s.tradedThisCycle = true
		s.tradesCountThisCycle++ // 增加交易计数

		// 更新订单跟踪状态
		for _, order := range createdOrders {
			if order == nil || order.OrderID == "" {
				continue
			}
			if order.TokenType == winner {
				s.lastEntryOrderID = order.OrderID
				s.lastEntryOrderStatus = order.Status
			} else if order.TokenType == opposite(winner) {
				s.lastHedgeOrderID = order.OrderID
			}
		}
		tradesCount = s.tradesCountThisCycle
		s.mu.Unlock()

		log.Infof("⚡ [%s] 触发(并发): side=%s ask=%dc hedge=%dc vel=%.3f(c/s) move=%dc/%0.1fs bias=%s(%s) market=%s trades=%d/%d orders=%d",
			ID, winner, entryAskCents, hedgeAskCents, winMet.velocity, winMet.delta, winMet.seconds, biasTok, biasReason, market.Slug, tradesCount, s.MaxTradesPerCycle, len(createdOrders))
		if biasTok != "" || biasReason != "" {
			log.Infof("🧭 [%s] bias: token=%s reason=%s cycleStartMs=%d", ID, biasTok, biasReason, s.cycleStartMs)
		}

		// 额外：打印 Binance 1s/1m 最新 K 线（用于你观察"开盘 1 分钟"关系）
		if s.BinanceFuturesKlines != nil {
			if k1m, ok := s.BinanceFuturesKlines.Latest("1m"); ok {
				log.Infof("📊 [%s] Binance 1m kline: sym=%s o=%.2f c=%.2f h=%.2f l=%.2f closed=%v startMs=%d",
					ID, k1m.Symbol, k1m.Open, k1m.Close, k1m.High, k1m.Low, k1m.IsClosed, k1m.StartTimeMs)
			}
			if k1s, ok := s.BinanceFuturesKlines.Latest("1s"); ok {
				log.Infof("📊 [%s] Binance 1s kline: sym=%s o=%.2f c=%.2f closed=%v startMs=%d",
					ID, k1s.Symbol, k1s.Open, k1s.Close, k1s.IsClosed, k1s.StartTimeMs)
			}
		}
	} else {
		log.Warnf("⚠️ [%s] 下单失败: err=%v side=%s market=%s", ID, execErr, winner, market.Slug)
	}
	return nil
}

// monitorAndReorderHedge 监控对冲单成交状态，如果超时未成交则重新下单
func (s *Strategy) monitorAndReorderHedge(ctx context.Context, market *domain.Market,
	entryOrderID, hedgeOrderID, hedgeAsset string, hedgePrice domain.Price, hedgeShares float64,
	entryFilledTime time.Time, entryFilledSize float64, entryAskCents int, winner domain.TokenType) {

	timeout := time.Duration(s.HedgeReorderTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second // 默认 30 秒
	}

	deadline := entryFilledTime.Add(timeout)
	checkInterval := 1 * time.Second // 每秒检查一次

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()

			// 检查是否超时
			if now.After(deadline) {
				// 超时：检查对冲单状态
				if s.TradingService == nil {
					return
				}

				hedgeFilled := false
				if ord, ok := s.TradingService.GetOrder(hedgeOrderID); ok && ord != nil {
					hedgeFilled = ord.Status == domain.OrderStatusFilled
				}

				if hedgeFilled {
					// 对冲单已成交，停止监控
					log.Infof("✅ [%s] 对冲单监控结束：对冲单已成交 orderID=%s", ID, hedgeOrderID)
					return
				}

				// 对冲单未成交，取消旧单并重新下单
				log.Warnf("⏰ [%s] 对冲单超时未成交（%d秒），取消旧单并重新下单: orderID=%s",
					ID, s.HedgeReorderTimeoutSeconds, hedgeOrderID)

				// 取消旧对冲单
				if err := s.TradingService.CancelOrder(ctx, hedgeOrderID); err != nil {
					log.Warnf("⚠️ [%s] 取消旧对冲单失败: orderID=%s err=%v", ID, hedgeOrderID, err)
				} else {
					log.Infof("✅ [%s] 已取消旧对冲单: orderID=%s", ID, hedgeOrderID)
				}

				// 重新获取订单簿价格（确保价格是最新的）
				reorderCtx, reorderCancel := context.WithTimeout(ctx, 5*time.Second)
				defer reorderCancel()

				_, yesAsk, _, noAsk, source, err := s.TradingService.GetTopOfBook(reorderCtx, market)
				if err != nil {
					log.Warnf("⚠️ [%s] 重新获取订单簿价格失败，使用原价格: err=%v", ID, err)
					// 使用原价格继续
				} else {
					// ✅ 修复：对冲单重下也必须遵守“互补挂单”原则，避免追价买到 ask 导致结构性必亏
					oldPriceCents := int(hedgePrice.ToDecimal()*100 + 0.5)
					hedgeAskCentsDirect := int(yesAsk.ToCents())
					if winner == domain.TokenTypeUp {
						// Hedge 是 DOWN
						hedgeAskCentsDirect = noAsk.ToCents()
					}

					// 基于 Entry 成本约束的最大对冲价格（cents）
					// 注：entryAskCents 是 Entry 下单时的实际 ask（FAK）；用它来约束 hedge 的最坏成本。
					maxHedgeCents := 100 - entryAskCents - s.HedgeOffsetCents
					newLimitCents := maxHedgeCents
					if hedgeAskCentsDirect > 0 && newLimitCents >= hedgeAskCentsDirect {
						newLimitCents = hedgeAskCentsDirect - 1
					}
					if newLimitCents <= 0 || newLimitCents >= 100 {
						log.Errorf("🚨 [%s] 对冲重下失败：互补挂单价格无效: entryAsk=%dc hedgeOffset=%dc => maxHedge=%dc (hedgeAsk=%dc)",
							ID, entryAskCents, s.HedgeOffsetCents, maxHedgeCents, hedgeAskCentsDirect)
						// 保守处理：停止重下，维持未对冲风险提示
						return
					}

					hedgePrice = domain.Price{Pips: newLimitCents * 100}
					log.Infof("💰 [%s] 重新计算对冲单价格: 原=%dc 新=%dc (max=%dc hedgeAsk=%dc source=%s)",
						ID, oldPriceCents, newLimitCents, maxHedgeCents, hedgeAskCentsDirect, source)
				}

				// 重新下单
				newHedgeOrder := &domain.Order{
					MarketSlug:   market.Slug,
					AssetID:      hedgeAsset,
					TokenType:    opposite(winner),
					Side:         types.SideBuy,
					Price:        hedgePrice,
					Size:         hedgeShares,
					OrderType:    types.OrderTypeGTC,
					IsEntryOrder: false,
					HedgeOrderID: &entryOrderID,
					Status:       domain.OrderStatusPending,
					CreatedAt:    time.Now(),
				}

				newHedgeResult, err := s.TradingService.PlaceOrder(reorderCtx, newHedgeOrder)
				if err != nil {
					log.Errorf("❌ [%s] 重新下对冲单失败: err=%v (主单已成交，存在风险敞口)", ID, err)
				} else if newHedgeResult != nil && newHedgeResult.OrderID != "" {
					log.Infof("✅ [%s] 对冲单已重新提交: orderID=%s (原订单=%s)",
						ID, newHedgeResult.OrderID, hedgeOrderID)

					// 更新跟踪状态
					s.mu.Lock()
					s.lastHedgeOrderID = newHedgeResult.OrderID
					s.mu.Unlock()
				}

				// 重新下单后，继续监控新订单（最多再等一次超时时间）
				hedgeOrderID = ""
				if newHedgeResult != nil && newHedgeResult.OrderID != "" {
					hedgeOrderID = newHedgeResult.OrderID
					deadline = time.Now().Add(timeout) // 重置超时时间
				} else {
					// 重新下单失败，停止监控
					return
				}
			} else {
				// 未超时，检查对冲单是否已成交
				if s.TradingService == nil {
					continue
				}

				if ord, ok := s.TradingService.GetOrder(hedgeOrderID); ok && ord != nil {
					if ord.Status == domain.OrderStatusFilled {
						// 对冲单已成交，停止监控
						log.Infof("✅ [%s] 对冲单监控结束：对冲单已成交 orderID=%s (耗时 %.1f秒)",
							ID, hedgeOrderID, time.Since(entryFilledTime).Seconds())
						return
					}
				}
			}
		}
	}
}

func (s *Strategy) pruneLocked(now time.Time) {
	window := time.Duration(s.WindowSeconds) * time.Second
	if window <= 0 {
		window = 10 * time.Second
	}
	cut := now.Add(-window)
	for tok, arr := range s.samples {
		// 找到第一个 >= cut 的索引
		i := 0
		for i < len(arr) && arr[i].ts.Before(cut) {
			i++
		}
		if i > 0 {
			arr = arr[i:]
		}
		// 防止极端情况下 slice 无限增长（保守上限）
		if len(arr) > 512 {
			arr = arr[len(arr)-512:]
		}
		s.samples[tok] = arr
	}
}

func (s *Strategy) computeLocked(tok domain.TokenType) metrics {
	arr := s.samples[tok]
	if len(arr) < 2 {
		return metrics{}
	}
	first := arr[0]
	last := arr[len(arr)-1]
	dt := last.ts.Sub(first.ts).Seconds()
	if dt <= 0.001 {
		return metrics{}
	}
	delta := last.priceCents - first.priceCents
	// 只做“上行”触发（你的描述是追涨买上涨的一方）
	if delta <= 0 {
		return metrics{}
	}
	vel := float64(delta) / dt
	if math.IsNaN(vel) || math.IsInf(vel, 0) {
		return metrics{}
	}
	return metrics{ok: true, delta: delta, seconds: dt, velocity: vel}
}

func opposite(t domain.TokenType) domain.TokenType {
	if t == domain.TokenTypeUp {
		return domain.TokenTypeDown
	}
	return domain.TokenTypeUp
}

func ensureMinOrderSize(desiredShares float64, price float64, minUSDC float64) float64 {
	if desiredShares <= 0 || price <= 0 {
		return desiredShares
	}
	if minUSDC <= 0 {
		minUSDC = 1.0
	}
	minShares := minUSDC / price
	if minShares > desiredShares {
		return minShares
	}
	return desiredShares
}

// adjustSizeForMakerAmountPrecision 调整 size 使得 maker amount = size × price 是 2 位小数
// 对于买入订单（FAK），maker amount 是 USDC 金额，必须 <= 2 位小数
// taker amount (size) 必须 <= 4 位小数
// 策略：先调整 maker amount 到 2 位小数，再重新计算 size 到 4 位小数
func adjustSizeForMakerAmountPrecision(size float64, price float64) float64 {
	if size <= 0 || price <= 0 {
		return size
	}
	
	// 计算 maker amount = size × price
	makerAmount := size * price
	
	// 将 maker amount 向下舍入到 2 位小数
	makerAmountRounded := math.Floor(makerAmount*100) / 100
	
	// 如果舍入后为 0，使用最小有效值（0.01）
	if makerAmountRounded <= 0 {
		makerAmountRounded = 0.01
	}
	
	// 重新计算 size = maker amount / price
	newSize := makerAmountRounded / price
	
	// 将 size 向下舍入到 4 位小数（taker amount 要求）
	newSize = math.Floor(newSize*10000) / 10000
	
	// 确保 size 不为 0
	if newSize <= 0 {
		return size // 如果调整后为 0，返回原始值
	}
	
	return newSize
}

func candleStatsBps(k services.Kline, upTok domain.TokenType, downTok domain.TokenType) (bodyBps int, wickBps int, dirTok domain.TokenType) {
	// body: |c-o|/o
	body := math.Abs(k.Close-k.Open) / k.Open * 10000
	bodyBps = int(body + 0.5)

	hi := k.High
	lo := k.Low
	o := k.Open
	c := k.Close
	maxOC := math.Max(o, c)
	minOC := math.Min(o, c)
	upperWick := (hi - maxOC) / o * 10000
	lowerWick := (minOC - lo) / o * 10000
	w := math.Max(upperWick, lowerWick)
	if w < 0 {
		w = 0
	}
	wickBps = int(w + 0.5)

	dirTok = downTok
	if c >= o {
		dirTok = upTok
	}
	return
}

func (s *Strategy) exitEnabled() bool {
	if s == nil {
		return false
	}
	return s.TakeProfitCents > 0 || s.StopLossCents > 0 || s.MaxHoldSeconds > 0
}

// tryExitPositions 在满足止盈/止损/超时条件时下 SELL FAK 出场。
// 返回 true 表示本次“已有持仓，因此策略将跳过后续开仓逻辑”（无论是否真的触发了出场）。
func (s *Strategy) tryExitPositions(ctx context.Context, market *domain.Market, now time.Time, positions []*domain.Position) bool {
	if s == nil || s.TradingService == nil || market == nil {
		return false
	}

	// 出场冷却：避免短时间重复下 SELL
	exitCooldown := time.Duration(s.ExitCooldownMs) * time.Millisecond
	if exitCooldown <= 0 {
		exitCooldown = 1500 * time.Millisecond
	}
	s.mu.Lock()
	lastExit := s.lastExitAt
	s.mu.Unlock()
	if !lastExit.IsZero() && now.Sub(lastExit) < exitCooldown {
		return true
	}

	// 只在确实需要评估时才拉 top-of-book（优先 WS，必要时回退 REST）
	orderCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	yesBid, _, noBid, _, _, err := s.TradingService.GetTopOfBook(orderCtx, market)
	if err != nil {
		log.Warnf("⚠️ [%s] 出场检查获取盘口失败: %v", ID, err)
		return true // 有持仓但无法评估：保守起见先不新开仓
	}

	type leg struct {
		name    string
		assetID string
		token   domain.TokenType
		price   domain.Price
		size    float64
		reason  string
	}
	legs := make([]leg, 0, 2)

	// 找到是否双边持仓（用于可选“一次性全平”）
	var upPos, downPos *domain.Position
	for _, p := range positions {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}
		if p.TokenType == domain.TokenTypeUp {
			upPos = p
		} else if p.TokenType == domain.TokenTypeDown {
			downPos = p
		}
	}

	shouldExitBoth := false
	if s.ExitBothSidesIfHedged != nil && *s.ExitBothSidesIfHedged {
		shouldExitBoth = upPos != nil && downPos != nil
	}

	evalPos := func(p *domain.Position) (doExit bool, bid domain.Price, reason string) {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			return false, domain.Price{}, ""
		}
		if p.TokenType == domain.TokenTypeUp {
			bid = yesBid
		} else {
			bid = noBid
		}
		if bid.Pips <= 0 {
			return false, domain.Price{}, ""
		}
		curC := bid.ToCents()
		avgC := p.EntryPrice.ToCents()
		if p.AvgPrice > 0 {
			avgC = int(p.AvgPrice*100 + 0.5)
		}
		diff := curC - avgC

		if s.TakeProfitCents > 0 && diff >= s.TakeProfitCents {
			return true, bid, "take_profit"
		}
		if s.StopLossCents > 0 && diff <= -s.StopLossCents {
			return true, bid, "stop_loss"
		}
		if s.MaxHoldSeconds > 0 && !p.EntryTime.IsZero() {
			if now.Sub(p.EntryTime) >= time.Duration(s.MaxHoldSeconds)*time.Second {
				return true, bid, "max_hold"
			}
		}
		return false, domain.Price{}, ""
	}

	// 先判断是否触发出场
	if shouldExitBoth {
		// 任意一侧触发，则两侧都平（降低持仓复杂度）
		doUp, upBid, upReason := evalPos(upPos)
		doDown, downBid, downReason := evalPos(downPos)
		if doUp || doDown {
			reason := upReason
			if reason == "" {
				reason = downReason
			}
			legs = append(legs, leg{name: "exit_sell_up", assetID: market.YesAssetID, token: domain.TokenTypeUp, price: upBid, size: upPos.Size, reason: reason})
			legs = append(legs, leg{name: "exit_sell_down", assetID: market.NoAssetID, token: domain.TokenTypeDown, price: downBid, size: downPos.Size, reason: reason})
		}
	} else {
		// 单边：分别评估
		if do, bid, reason := evalPos(upPos); do {
			legs = append(legs, leg{name: "exit_sell_up", assetID: market.YesAssetID, token: domain.TokenTypeUp, price: bid, size: upPos.Size, reason: reason})
		}
		if do, bid, reason := evalPos(downPos); do {
			legs = append(legs, leg{name: "exit_sell_down", assetID: market.NoAssetID, token: domain.TokenTypeDown, price: bid, size: downPos.Size, reason: reason})
		}
	}

	if len(legs) == 0 {
		return true // 有持仓但未触发：默认不再叠加开仓
	}

	// 出场前先清理本周期挂单（尤其是未成交的 hedge GTC），避免出场后反向被动成交
	s.TradingService.CancelOrdersForMarket(orderCtx, market.Slug)

	req := execution.MultiLegRequest{
		Name:       "velocityfollow_exit",
		MarketSlug: market.Slug,
		Legs:       make([]execution.LegIntent, 0, len(legs)),
		Hedge:      execution.AutoHedgeConfig{Enabled: false},
	}
	for _, l := range legs {
		if l.size <= 0 || l.price.Pips <= 0 {
			continue
		}
		req.Legs = append(req.Legs, execution.LegIntent{
			Name:      l.name,
			AssetID:   l.assetID,
			TokenType: l.token,
			Side:      types.SideSell,
			Price:     l.price,
			Size:      l.size,
			OrderType: types.OrderTypeFAK,
		})
		log.Infof("📤 [%s] 出场: reason=%s token=%s bid=%dc size=%.4f market=%s",
			ID, l.reason, l.token, l.price.ToCents(), l.size, market.Slug)
	}
	if len(req.Legs) == 0 {
		return true
	}

	_, _ = s.TradingService.ExecuteMultiLeg(orderCtx, req)
	s.mu.Lock()
	s.lastExitAt = now
	s.mu.Unlock()
	return true
}
