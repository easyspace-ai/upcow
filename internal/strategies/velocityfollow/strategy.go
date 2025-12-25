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

	mu sync.Mutex // 保护共享状态

	// 价格样本：用于计算速度
	samples map[domain.TokenType][]sample

	// 周期状态管理
	firstSeenAt        time.Time // 首次看到价格的时间
	lastTriggerAt      time.Time // 上次触发时间（用于冷却）
	tradedThisCycle    bool      // 本周期是否已交易（兼容旧逻辑）
	tradesCountThisCycle int     // 本周期已交易次数（新逻辑）

	// 方向级别的去重：避免同一方向在短时间内重复触发
	lastTriggerSide   domain.TokenType
	lastTriggerSideAt time.Time

	// 订单跟踪：利用本地订单状态管理（新架构特性）
	lastEntryOrderID     string                    // 最后下单的 Entry 订单ID
	lastHedgeOrderID     string                    // 最后下单的 Hedge 订单ID
	lastEntryOrderStatus domain.OrderStatus        // Entry 订单状态
	pendingOrders        map[string]*domain.Order // 待确认的订单（通过订单ID跟踪）

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

	// 6. 注册订单更新回调（新架构特性：利用本地订单状态管理）
	// 当订单状态更新时（通过 WebSocket 或 API 同步），立即更新本地状态
	if s.TradingService != nil {
		handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
		s.TradingService.OnOrderUpdate(handler)
		log.Infof("✅ [%s] 已注册订单更新回调（利用本地订单状态管理）", ID)
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
	} else if order.HedgeOrderID != nil && order.OrderID == *order.HedgeOrderID {
		// Hedge 订单更新
		s.lastHedgeOrderID = order.OrderID
		log.Debugf("📊 [%s] Hedge 订单状态更新: orderID=%s status=%s filledSize=%.4f",
			ID, order.OrderID, order.Status, order.FilledSize)
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
	priceCents := e.NewPrice.ToCents()
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
			s.mu.Unlock()
			log.Debugf("🔄 [%s] 跳过：同一方向 %s 在冷却期内（距离上次触发 %.2fs）", ID, winner, now.Sub(s.lastTriggerSideAt).Seconds())
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

	// 验证价格有效性
	if yesBidDec <= 0 || yesAskDec <= 0 || noBidDec <= 0 || noAskDec <= 0 {
		log.Debugf("⚠️ [%s] 订单簿价格无效: YES bid=%.4f ask=%.4f, NO bid=%.4f ask=%.4f", 
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

	// ===== 计算有效价格（考虑镜像订单簿）=====
	// 买 Entry: 直接买 entryAsk 或 通过卖 hedge (成本 = 1 - hedgeBid)
	effectiveBuyEntry := entryAskDec
	if 1-hedgeBidDec < effectiveBuyEntry {
		effectiveBuyEntry = 1 - hedgeBidDec
	}

	// 买 Hedge: 直接买 hedgeAsk 或 通过卖 entry (成本 = 1 - entryBid)
	effectiveBuyHedge := hedgeAskDec
	if 1-entryBidDec < effectiveBuyHedge {
		effectiveBuyHedge = 1 - entryBidDec
	}

	// 转换为分（cents）
	entryAskCents := int(effectiveBuyEntry*100 + 0.5)
	hedgeAskCents := int(effectiveBuyHedge*100 + 0.5)

	// 价格验证
	if entryAskCents <= 0 || entryAskCents >= 100 || hedgeAskCents <= 0 || hedgeAskCents >= 100 {
		log.Debugf("⚠️ [%s] 有效价格无效: entry=%dc hedge=%dc", ID, entryAskCents, hedgeAskCents)
		return nil
	}

	// Entry 价格上限检查
	if maxEntry > 0 && entryAskCents > maxEntry {
		log.Debugf("⏭️ [%s] 跳过：Entry 价格超过上限 (%dc > %dc)", ID, entryAskCents, maxEntry)
		return nil
	}

	// 价差检查（使用实际价差，而非互补价）
	entrySpread := entryAskCents - int(entryBidDec*100+0.5)
	if entrySpread < 0 {
		entrySpread = -entrySpread
	}
	if maxSpread > 0 && entrySpread > maxSpread {
		log.Debugf("⏭️ [%s] 跳过：价差过大 (%dc > %dc)", ID, entrySpread, maxSpread)
		return nil
	}

	// ===== 价格滑点保护 =====
	// 检查有效价格是否合理（总成本应该接近 $1，允许一定误差）
	totalCostDec := effectiveBuyEntry + effectiveBuyHedge
	totalCostCents := int(totalCostDec*100 + 0.5)
	
	// 如果总成本过高（> $1.05），说明价格可能有问题，拒绝下单
	if totalCostCents > 105 {
		log.Warnf("⚠️ [%s] 价格滑点保护触发: 总成本过高 (%dc > 105c, entry=%dc hedge=%dc, source=%s)", 
			ID, totalCostCents, entryAskCents, hedgeAskCents, source)
		return nil
	}

	// 记录有效价格信息
	log.Debugf("💰 [%s] 有效价格计算: Entry=%dc (直接=%dc, 镜像=%dc), Hedge=%dc (直接=%dc, 镜像=%dc), 总成本=%dc, source=%s",
		ID, entryAskCents, int(entryAskDec*100+0.5), int((1-hedgeBidDec)*100+0.5),
		hedgeAskCents, int(hedgeAskDec*100+0.5), int((1-entryBidDec)*100+0.5),
		totalCostCents, source)

	entryPrice := domain.Price{Pips: entryAskCents * 100}   // 1 cent = 100 pips
	hedgePrice := domain.Price{Pips: hedgeAskCents * 100} // 1 cent = 100 pips

	entryAskDec = effectiveBuyEntry
	hedgeDec := effectiveBuyHedge

	// size：确保满足最小金额/最小 shares（GTC）
	entryShares := ensureMinOrderSize(orderSize, entryAskDec, minOrderSize)
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
	
	// 记录订单数量信息（用于验证两边是否相等）
	log.Debugf("📊 [%s] 订单数量: Entry=%.4f shares @ %dc, Hedge=%.4f shares @ %dc (已确保相等)", 
		ID, entryShares, entryAskCents, hedgeShares, hedgeAskCents)

	// 9. 订单执行：根据配置选择顺序或并发执行
	// sequential: 先下 Entry，等待成交后再下 Hedge（风险低，速度慢）
	// parallel: 同时提交 Entry 和 Hedge（速度快，风险高）
	biasTokStr := string(biasTok)
	if s.Config.OrderExecutionMode == "parallel" {
		return s.executeParallel(orderCtx, market, winner, entryAsset, hedgeAsset, entryPrice, hedgePrice, entryShares, hedgeShares, entryAskCents, hedgeAskCents, winMet, biasTokStr, biasReason)
	} else {
		return s.executeSequential(orderCtx, market, winner, entryAsset, hedgeAsset, entryPrice, hedgePrice, entryShares, hedgeShares, entryAskCents, hedgeAskCents, winMet, biasTokStr, biasReason)
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
	s.mu.Lock()
	defer s.mu.Unlock()

	// 使用更短的超时时间（10秒），快速失败，避免阻塞策略
	orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// ===== 顺序下单：先买主单（Entry），成交后再下对冲单（Hedge）=====
	// 主单：价格 >= minPreferredPriceCents 的订单（FAK，立即成交或取消）
	log.Infof("📤 [%s] 步骤1: 下主单 Entry (side=%s price=%dc size=%.4f FAK)", 
		ID, winner, entryAskCents, entryShares)
	
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
		s.mu.Unlock()
		return nil
	}
	
	if entryOrderResult == nil || entryOrderResult.OrderID == "" {
		log.Warnf("⚠️ [%s] 主单下单失败: 订单ID为空", ID)
		s.mu.Unlock()
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
	if !entryFilled && s.TradingService != nil {
		activeOrders := s.TradingService.GetActiveOrders()
		for _, order := range activeOrders {
			if order.OrderID == entryOrderID {
				if order.Status == domain.OrderStatusFilled {
					entryFilled = true
					log.Infof("✅ [%s] 主单已成交（立即检查）: orderID=%s filledSize=%.4f", 
						ID, order.OrderID, order.FilledSize)
					break
				} else if order.Status == domain.OrderStatusFailed || 
						  order.Status == domain.OrderStatusCanceled {
					log.Warnf("⚠️ [%s] 主单失败/取消（立即检查）: orderID=%s status=%s", 
						ID, order.OrderID, order.Status)
					s.mu.Unlock()
					return nil
				}
			}
		}
	}
	
	// 如果未成交，轮询检查订单状态（使用更短的间隔）
	if !entryFilled {
		deadline := time.Now().Add(maxWaitTime)
		checkCount := 0
		for time.Now().Before(deadline) {
			checkCount++
			// 查询订单状态（使用本地订单状态管理）
			if s.TradingService != nil {
				activeOrders := s.TradingService.GetActiveOrders()
				for _, order := range activeOrders {
					if order.OrderID == entryOrderID {
						if order.Status == domain.OrderStatusFilled {
							entryFilled = true
							log.Infof("✅ [%s] 主单已成交（轮询检查，第%d次）: orderID=%s filledSize=%.4f", 
								ID, checkCount, order.OrderID, order.FilledSize)
							break
						} else if order.Status == domain.OrderStatusFailed || 
								  order.Status == domain.OrderStatusCanceled {
							log.Warnf("⚠️ [%s] 主单失败/取消（轮询检查，第%d次）: orderID=%s status=%s", 
								ID, checkCount, order.OrderID, order.Status)
							s.mu.Unlock()
							return nil
						}
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
	if hedgeErr != nil {
		log.Warnf("⚠️ [%s] 对冲单下单失败: err=%v (主单已成交，需要手动处理)", 
			ID, hedgeErr)
		// 主单已成交，对冲单失败，这是一个风险情况
		execErr = hedgeErr
	} else if hedgeOrderResult != nil && hedgeOrderResult.OrderID != "" {
		log.Infof("✅ [%s] 对冲单已提交: orderID=%s status=%s (关联主单=%s)", 
			ID, hedgeOrderResult.OrderID, hedgeOrderResult.Status, entryOrderID)
	}
	
	// 更新订单关联关系（如果对冲单成功）
	if hedgeOrderResult != nil && hedgeOrderResult.OrderID != "" {
		// 更新主单的对冲订单ID
		if entryOrderResult != nil {
			entryOrderResult.HedgeOrderID = &hedgeOrderResult.OrderID
		}
		s.lastHedgeOrderID = hedgeOrderResult.OrderID
	}
	
	if execErr == nil && entryOrderResult != nil {
		s.lastTriggerAt = time.Now()
		s.lastTriggerSide = winner
		s.lastTriggerSideAt = time.Now()
		s.tradedThisCycle = true
		s.tradesCountThisCycle++ // 增加交易计数
		
		// 更新订单跟踪状态
		s.lastEntryOrderID = entryOrderResult.OrderID
		s.lastEntryOrderStatus = entryOrderResult.Status
		if entryFilled {
			s.lastEntryOrderStatus = domain.OrderStatusFilled
		}
		
		log.Infof("⚡ [%s] 触发(顺序): side=%s ask=%dc hedge=%dc vel=%.3f(c/s) move=%dc/%0.1fs bias=%s(%s) market=%s trades=%d/%d",
			ID, winner, entryAskCents, hedgeAskCents, winMet.velocity, winMet.delta, winMet.seconds, biasTok, biasReason, market.Slug, s.tradesCountThisCycle, s.MaxTradesPerCycle)
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
	s.mu.Lock()
	defer s.mu.Unlock()

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
	if execErr == nil && len(createdOrders) > 0 {
		s.lastTriggerAt = time.Now()
		s.lastTriggerSide = winner
		s.lastTriggerSideAt = time.Now()
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
		
		log.Infof("⚡ [%s] 触发(并发): side=%s ask=%dc hedge=%dc vel=%.3f(c/s) move=%dc/%0.1fs bias=%s(%s) market=%s trades=%d/%d orders=%d",
			ID, winner, entryAskCents, hedgeAskCents, winMet.velocity, winMet.delta, winMet.seconds, biasTok, biasReason, market.Slug, s.tradesCountThisCycle, s.MaxTradesPerCycle, len(createdOrders))
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

