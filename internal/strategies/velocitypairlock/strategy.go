package velocitypairlock

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/orderutil"
	"github.com/betbot/gobet/internal/ports"
	"github.com/betbot/gobet/internal/services"
	gcfg "github.com/betbot/gobet/pkg/config"
	"github.com/betbot/gobet/pkg/bbgo"
)

func init() {
	bbgo.RegisterStrategy("velocitypairlock", &Strategy{})
}

// Strategy：BTC 15m Up/Down 速度触发对冲策略（双向限价 + 自动 merge 释放资金）。
//
// 设计原则：
// - 事件驱动：只在价格事件到来时做轻量计算；下单/合并放入 goroutine，避免阻塞行情分发
// - 单对单：同一时刻最多允许一对（UP+DOWN）在途，资金有限时更安全、更可控
// - 可维护：信号/定价/状态机/合并逻辑独立文件，便于后续扩展（盘口质量、止盈止损、重下/FAK 等）
type Strategy struct {
	// ===== 注入（由 Trader 注入）=====
	TradingService *services.TradingService `json:"-" yaml:"-"`

	// ===== 配置（由 exchangeStrategies 注入到 struct）=====
	Config `json:",inline" yaml:",inline"`

	// ===== 运行期 =====
	orderExecutor bbgo.OrderExecutor
	log          *logrus.Entry

	st state

	// 仅用于 Run 启动确认日志的 once（无锁）
	started atomic.Bool
}

func (s *Strategy) ID() string { return "velocitypairlock" }

// Name 兼容旧接口（如果有旧注册表使用）
func (s *Strategy) Name() string { return s.ID() }

func (s *Strategy) Defaults() error {
	s.Config.Defaults()
	if s.log == nil {
		s.log = logrus.WithField("strategy", s.ID())
	}
	s.st.cfg = s.Config
	if s.st.upVel == nil {
		s.st.upVel = NewVelocityTracker(s.Config.WindowSeconds)
	}
	if s.st.downVel == nil {
		s.st.downVel = NewVelocityTracker(s.Config.WindowSeconds)
	}
	return nil
}

func (s *Strategy) Validate() error {
	s.Config.Defaults()
	return s.Config.Validate()
}

// Subscribe 注册回调（价格事件 + 订单更新）。
func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	if s.log == nil {
		s.log = logrus.WithField("strategy", s.ID())
	}
	if session == nil {
		return
	}
	session.OnPriceChanged(s)

	// BestBook 透传给 TradingService（如果上层尚未注入）
	if s.TradingService != nil && session.BestBook() != nil {
		s.TradingService.SetBestBook(session.BestBook())
	}

	// 订单更新：优先注册到 TradingService（OrderEngine 会统一回调），并兼容注册到 UserWebSocket（如果存在）
	if s.TradingService != nil {
		s.TradingService.OnOrderUpdate(s)
	}
	if session.UserDataStream != nil {
		session.UserDataStream.OnOrderUpdate(s)
	}
}

// OnCycle 在周期切换时重置状态（避免跨周期污染）。
func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, newMarket *domain.Market) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()

	// 停止盯盘协程
	s.stopMonitorLocked()

	if s.st.upVel != nil {
		s.st.upVel.Reset()
	}
	if s.st.downVel != nil {
		s.st.downVel.Reset()
	}
	s.st.rt.market = newMarket
	s.st.rt.tradesThisCycle = 0
	s.resetPairLocked("cycle_switch")
	// 给一点保护：刚切换时盘口/WS 可能还在同步
	s.st.rt.cooldownUntil = time.Now().Add(800 * time.Millisecond)
}

func (s *Strategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	s.orderExecutor = orderExecutor
	if s.log == nil {
		s.log = logrus.WithField("strategy", s.ID())
	}
	if !s.started.Swap(true) {
		s.log.Infof("✅ 策略启动：%s enabled=%v", s.ID(), s.Config.Enabled)
	}

	// 初始 market（若 session 已就绪）
	if session != nil {
		s.st.mu.Lock()
		s.st.rt.market = session.Market()
		s.st.mu.Unlock()
	}

	<-ctx.Done()
	return ctx.Err()
}

// OnOrderUpdate implements ports.OrderUpdateHandler.
func (s *Strategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	_ = ctx
	if order == nil {
		return nil
	}
	if !s.Config.Enabled {
		return nil
	}

	s.st.mu.Lock()
	needStartMonitor := false

	// 只关心当前一对相关的订单
	if s.st.rt.phase != phaseOpen &&
		s.st.rt.phase != phasePrimaryOpen &&
		s.st.rt.phase != phaseHedgeOpen &&
		s.st.rt.phase != phaseFilled &&
		s.st.rt.phase != phaseMerging {
		s.st.mu.Unlock()
		return nil
	}
	if order.OrderID == "" {
		s.st.mu.Unlock()
		return nil
	}

	// 顺序模式：主 leg 成交 -> 下对冲 leg
	if s.st.rt.phase == phasePrimaryOpen && s.st.rt.primaryOrderID != "" && order.OrderID == s.st.rt.primaryOrderID {
		if order.Status == domain.OrderStatusFilled {
			s.st.rt.primaryFilled = true
			s.st.rt.primaryFillCents = order.Price.ToCents()
			s.st.rt.primaryFillSize = order.ExecutedSize()
			s.log.Infof("✅ 主 leg 成交：orderID=%s token=%s price=%dc size=%.2f", order.OrderID, s.st.rt.primaryToken, s.st.rt.primaryFillCents, s.st.rt.primaryFillSize)
			// 下对冲单放到 goroutine（避免阻塞 WS）
			market := s.st.rt.market
			hedgeToken := s.st.rt.hedgeToken
			hedgeCents := s.st.rt.hedgeTargetCents
			size := s.st.rt.primaryFillSize
			s.st.rt.phase = phasePlacing
			s.st.mu.Unlock()
			go s.placeHedgeAfterPrimaryFilled(market, hedgeToken, hedgeCents, size)
			return nil
		}
		if order.Status == domain.OrderStatusCanceled || order.Status == domain.OrderStatusFailed {
			s.log.Warnf("⚠️ 主 leg 进入终态但未成交：orderID=%s status=%s，重置本对", order.OrderID, order.Status)
			s.resetPairLocked("primary_terminal")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
			s.st.mu.Unlock()
			return nil
		}
		s.st.mu.Unlock()
		return nil
	}

	// 顺序模式：对冲 leg 状态
	if s.st.rt.phase == phaseHedgeOpen && s.st.rt.hedgeOrderID != "" && order.OrderID == s.st.rt.hedgeOrderID {
		if order.Status == domain.OrderStatusFilled {
			s.st.rt.hedgeFilled = true
			s.log.Infof("✅ 对冲 leg 成交：orderID=%s token=%s", order.OrderID, s.st.rt.hedgeToken)
			s.stopMonitorLocked()
			s.st.rt.phase = phaseFilled
			s.triggerAutoMergeLocked()
			s.st.mu.Unlock()
			return nil
		}
		if order.Status == domain.OrderStatusCanceled || order.Status == domain.OrderStatusFailed {
			s.log.Warnf("⚠️ 对冲 leg 进入终态但未成交：orderID=%s status=%s，重置本对", order.OrderID, order.Status)
			s.stopMonitorLocked()
			s.resetPairLocked("hedge_terminal")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
			s.st.mu.Unlock()
			return nil
		}
		s.st.mu.Unlock()
		return nil
	}

	// 并发模式：两边订单状态（维持原逻辑）
	updated := false
	if s.st.rt.upOrderID != "" && order.OrderID == s.st.rt.upOrderID {
		if order.Status == domain.OrderStatusFilled {
			s.st.rt.upFilled = true
			updated = true
		} else if order.Status == domain.OrderStatusCanceled || order.Status == domain.OrderStatusFailed {
			s.log.Warnf("⚠️ UP 订单进入终态但未成交：orderID=%s status=%s，重置本对", order.OrderID, order.Status)
			s.resetPairLocked("up_terminal")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
			s.st.mu.Unlock()
			return nil
		}
	}
	if s.st.rt.downOrderID != "" && order.OrderID == s.st.rt.downOrderID {
		if order.Status == domain.OrderStatusFilled {
			s.st.rt.downFilled = true
			updated = true
		} else if order.Status == domain.OrderStatusCanceled || order.Status == domain.OrderStatusFailed {
			s.log.Warnf("⚠️ DOWN 订单进入终态但未成交：orderID=%s status=%s，重置本对", order.OrderID, order.Status)
			s.resetPairLocked("down_terminal")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
			s.st.mu.Unlock()
			return nil
		}
	}

	if updated && s.st.rt.upFilled && s.st.rt.downFilled {
		if s.st.rt.phase != phaseMerging {
			s.st.rt.phase = phaseFilled
			s.stopMonitorLocked()
			s.triggerAutoMergeLocked()
		}
		s.st.mu.Unlock()
		return nil
	}

	// 并发模式：只成交了一边，另一边未成交 -> 进入 hedge_open 并启动盯盘锁损
	if updated && s.st.rt.phase == phaseOpen && s.st.cfg.PriceStopEnabled {
		oneFilled := (s.st.rt.upFilled && !s.st.rt.downFilled) || (!s.st.rt.upFilled && s.st.rt.downFilled)
		if oneFilled && !s.st.rt.monitorRunning {
			// 注意：这里的 order 可能是 UP 或 DOWN 的更新；只在“刚刚收到 filled 的那条”更新 primaryFill*
			if order.Status == domain.OrderStatusFilled {
				s.st.rt.primaryFillCents = order.Price.ToCents()
				s.st.rt.primaryFillSize = order.ExecutedSize()
			}
			// 进入 hedge_open（盯盘目标：未成交的那条订单）
			s.st.rt.phase = phaseHedgeOpen
			s.st.rt.stopLevel = stopNone
			needStartMonitor = true
		}
	}

	s.st.mu.Unlock()
	if needStartMonitor {
		s.startMonitorIfNeeded()
	}
	return nil
}

func clampCents(v int, min int, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func (s *Strategy) makeBuyOrderForToken(market *domain.Market, token domain.TokenType, targetCents int, bestAskCents int, style string, size float64, bypassRiskOff bool) (domain.Order, error) {
	if market == nil {
		return domain.Order{}, fmt.Errorf("market is nil")
	}
	if token != domain.TokenTypeUp && token != domain.TokenTypeDown {
		return domain.Order{}, fmt.Errorf("invalid token type: %s", token)
	}
	assetID := market.NoAssetID
	if token == domain.TokenTypeUp {
		assetID = market.YesAssetID
	}
	orderType := types.OrderTypeGTC
	priceCents := targetCents
	if style == "taker" {
		// FAK 吃单：bestAsk + offset（buy）
		orderType = types.OrderTypeFAK
		priceCents = clampCents(bestAskCents+s.st.cfg.TakerOffsetCents, 1, 99)
	}
	return domain.Order{
		MarketSlug:    market.Slug,
		AssetID:       assetID,
		Side:          types.SideBuy,
		Price:         priceFromCents(priceCents),
		Size:          size,
		TokenType:     token,
		IsEntryOrder:  true,
		Status:        domain.OrderStatusPending,
		CreatedAt:     time.Now(),
		OrderType:     orderType,
		BypassRiskOff: bypassRiskOff,
	}, nil
}

// OnPriceChanged implements stream.PriceChangeHandler.
func (s *Strategy) OnPriceChanged(ctx context.Context, ev *events.PriceChangedEvent) error {
	if !s.Config.Enabled {
		return nil
	}
	if ev == nil {
		return nil
	}
	mkt := ev.Market
	if mkt == nil {
		return nil
	}

	// 热路径：先更新速度 tracker（持锁时间很短）
	var shouldTrigger bool
	var primaryToken domain.TokenType
	now := ev.Timestamp
	if now.IsZero() {
		now = time.Now()
	}
	newCents := ev.NewPrice.ToCents()

	s.st.mu.Lock()
	if s.st.upVel == nil || s.st.downVel == nil {
		s.st.upVel = NewVelocityTracker(s.Config.WindowSeconds)
		s.st.downVel = NewVelocityTracker(s.Config.WindowSeconds)
	}
	s.st.rt.market = mkt

	// 更新对应 token 的速度序列
	switch ev.TokenType {
	case domain.TokenTypeUp:
		s.st.upVel.Add(now, newCents)
		shouldTrigger = s.velocityHitLocked(s.st.upVel)
		primaryToken = domain.TokenTypeUp
	case domain.TokenTypeDown:
		s.st.downVel.Add(now, newCents)
		shouldTrigger = s.velocityHitLocked(s.st.downVel)
		primaryToken = domain.TokenTypeDown
	default:
		s.st.mu.Unlock()
		return nil
	}

	// 状态门禁：同一时刻只允许一对在途
	if s.st.rt.phase != phaseIdle {
		s.st.mu.Unlock()
		return nil
	}
	if !s.st.rt.cooldownUntil.IsZero() && time.Now().Before(s.st.rt.cooldownUntil) {
		s.st.mu.Unlock()
		return nil
	}
	if s.st.cfg.MaxTradesPerCycle > 0 && s.st.rt.tradesThisCycle >= s.st.cfg.MaxTradesPerCycle {
		s.st.mu.Unlock()
		return nil
	}
	if s.isInCycleEndProtectionLocked(time.Now()) {
		s.st.mu.Unlock()
		return nil
	}
	if !shouldTrigger {
		s.st.mu.Unlock()
		return nil
	}

	// 标记为 placing（立刻占位，防止并发触发）
	s.st.rt.phase = phasePlacing
	s.st.mu.Unlock()

	// 下单放到 goroutine（避免阻塞行情线程）
	go s.placePairAsync(primaryToken, mkt)
	return nil
}

func (s *Strategy) velocityHitLocked(t *VelocityTracker) bool {
	if t == nil {
		return false
	}
	vel, move, _, ok := t.VelocityCentsPerSec()
	if !ok {
		return false
	}
	if s.st.cfg.MinMoveCents > 0 && int(math.Abs(float64(move))) < s.st.cfg.MinMoveCents {
		return false
	}
	if math.Abs(vel) < s.st.cfg.MinVelocityCentsPerSec {
		return false
	}
	return true
}

func (s *Strategy) isInCycleEndProtectionLocked(now time.Time) bool {
	if s.st.cfg.CycleEndProtectionMinutes <= 0 {
		return false
	}
	if s.st.rt.market == nil || s.st.rt.market.Timestamp <= 0 {
		return false
	}

	// 尝试从全局 market spec 读取周期时长；失败则默认 15m
	cycleDur := 15 * time.Minute
	if gc := gcfg.Get(); gc != nil {
		if sp, err := gc.Market.Spec(); err == nil {
			if d := sp.Duration(); d > 0 {
				cycleDur = d
			}
		}
	}

	start := time.Unix(s.st.rt.market.Timestamp, 0)
	end := start.Add(cycleDur)
	protect := time.Duration(s.st.cfg.CycleEndProtectionMinutes) * time.Minute
	return end.Sub(now) <= protect
}

func (s *Strategy) placePairAsync(primaryToken domain.TokenType, market *domain.Market) {
	if market == nil {
		s.st.mu.Lock()
		s.resetPairLocked("nil_market")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}
	if s.orderExecutor == nil {
		s.st.mu.Lock()
		s.resetPairLocked("nil_order_executor")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}
	if s.TradingService == nil {
		s.st.mu.Lock()
		s.resetPairLocked("nil_trading_service")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}

	// 取两边当前 bestAsk 作为“挂单参考价”（买单）。
	// 注意：这里取 bestAsk 是为了提高成交率；如果你想更偏 maker，可改为 bestBid。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	upAsk, upErr := orderutil.QuoteBuyPrice(ctx, s.TradingService, market.YesAssetID, s.st.cfg.MaxEntryPriceCents)
	downAsk, downErr := orderutil.QuoteBuyPrice(ctx, s.TradingService, market.NoAssetID, s.st.cfg.MaxEntryPriceCents)

	// 选主边：优先用触发侧；若因边界/缺价失败，则回退另一侧
	type sidePlan struct {
		primaryToken domain.TokenType
		primaryCents int
		hedgeCents   int
	}

	plans := make([]sidePlan, 0, 2)
	if primaryToken == domain.TokenTypeUp && upErr == nil {
		if pp, err := PricePairLock(upAsk.ToCents(), s.st.cfg.ProfitCents, s.st.cfg.MinEntryPriceCents, s.st.cfg.MaxEntryPriceCents); err == nil {
			plans = append(plans, sidePlan{primaryToken: domain.TokenTypeUp, primaryCents: pp.PrimaryCents, hedgeCents: pp.HedgeCents})
		}
	}
	if primaryToken == domain.TokenTypeDown && downErr == nil {
		if pp, err := PricePairLock(downAsk.ToCents(), s.st.cfg.ProfitCents, s.st.cfg.MinEntryPriceCents, s.st.cfg.MaxEntryPriceCents); err == nil {
			plans = append(plans, sidePlan{primaryToken: domain.TokenTypeDown, primaryCents: pp.PrimaryCents, hedgeCents: pp.HedgeCents})
		}
	}
	// fallback：另一边
	if primaryToken != domain.TokenTypeUp && upErr == nil {
		if pp, err := PricePairLock(upAsk.ToCents(), s.st.cfg.ProfitCents, s.st.cfg.MinEntryPriceCents, s.st.cfg.MaxEntryPriceCents); err == nil {
			plans = append(plans, sidePlan{primaryToken: domain.TokenTypeUp, primaryCents: pp.PrimaryCents, hedgeCents: pp.HedgeCents})
		}
	}
	if primaryToken != domain.TokenTypeDown && downErr == nil {
		if pp, err := PricePairLock(downAsk.ToCents(), s.st.cfg.ProfitCents, s.st.cfg.MinEntryPriceCents, s.st.cfg.MaxEntryPriceCents); err == nil {
			plans = append(plans, sidePlan{primaryToken: domain.TokenTypeDown, primaryCents: pp.PrimaryCents, hedgeCents: pp.HedgeCents})
		}
	}

	if len(plans) == 0 {
		s.log.Warnf("⏸️ 触发后无法计算可用挂单价格：upErr=%v downErr=%v", upErr, downErr)
		s.st.mu.Lock()
		s.resetPairLocked("no_valid_plan")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}
	plan := plans[0]

	// 构造订单（两边都是 BUY + GTC）
	upPriceCents := 0
	downPriceCents := 0
	if plan.primaryToken == domain.TokenTypeUp {
		upPriceCents = plan.primaryCents
		downPriceCents = plan.hedgeCents
	} else {
		downPriceCents = plan.primaryCents
		upPriceCents = plan.hedgeCents
	}

	// 顺序模式 gate：只允许主 leg 价格在区间内时走 sequential
	if s.st.cfg.OrderExecutionMode == "sequential" {
		primaryCents := plan.primaryCents
		if primaryCents < s.st.cfg.SequentialPrimaryMinCents || primaryCents > s.st.cfg.SequentialPrimaryMaxCents {
			s.log.Infof("⏸️ sequential gate：主 leg 价格不在区间内，跳过：primary=%dc range=[%d,%d]",
				primaryCents, s.st.cfg.SequentialPrimaryMinCents, s.st.cfg.SequentialPrimaryMaxCents)
			s.st.mu.Lock()
			s.resetPairLocked("sequential_gate")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
			s.st.mu.Unlock()
			return
		}
	}

	// 最小金额检查（不做 size 自动放大，避免破坏“一对一”对冲；用户可自行调大 orderSize）
	if float64(upPriceCents)/100.0*s.st.cfg.OrderSize < s.st.cfg.MinOrderUSDC ||
		float64(downPriceCents)/100.0*s.st.cfg.OrderSize < s.st.cfg.MinOrderUSDC {
		s.log.Warnf("⏸️ 订单金额不足最小要求：orderSize=%.4f up=%dc down=%dc minUSDC=%.2f",
			s.st.cfg.OrderSize, upPriceCents, downPriceCents, s.st.cfg.MinOrderUSDC)
		s.st.mu.Lock()
		s.resetPairLocked("min_order_usdc")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}

	if s.st.cfg.OrderExecutionMode == "sequential" {
		// 顺序：只下主 leg，等待成交后再下对冲
		primaryToken := plan.primaryToken
		primaryCents := plan.primaryCents
		hedgeCents := plan.hedgeCents

		primaryAsset := market.YesAssetID
		hedgeAsset := market.NoAssetID
		hedgeToken := domain.TokenTypeDown
		if primaryToken == domain.TokenTypeDown {
			primaryAsset = market.NoAssetID
			hedgeAsset = market.YesAssetID
			hedgeToken = domain.TokenTypeUp
		}

		bestAskCents := upAsk.ToCents()
		if primaryToken == domain.TokenTypeDown {
			bestAskCents = downAsk.ToCents()
		}
		primaryOrder, err := s.makeBuyOrderForToken(market, primaryToken, primaryCents, bestAskCents, s.st.cfg.PrimaryOrderStyle, s.st.cfg.OrderSize, false)
		if err != nil {
			s.log.Warnf("❌ sequential 主 leg 构造订单失败：err=%v", err)
			s.st.mu.Lock()
			s.resetPairLocked("sequential_primary_build_failed")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
			s.st.mu.Unlock()
			return
		}
		primaryOrder.AssetID = primaryAsset

		submitCtx, submitCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer submitCancel()
		created, err := s.orderExecutor.SubmitOrders(submitCtx, primaryOrder)
		if err != nil || len(created) == 0 || created[0] == nil || created[0].OrderID == "" {
			s.log.Warnf("❌ sequential 主 leg 下单失败：err=%v primary=%dc", err, primaryCents)
			s.st.mu.Lock()
			s.resetPairLocked("sequential_primary_submit_failed")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
			s.st.mu.Unlock()
			return
		}

		primaryID := created[0].OrderID
		s.st.mu.Lock()
		s.st.rt.market = market
		s.st.rt.primaryToken = primaryToken
		s.st.rt.primaryOrderID = primaryID
		s.st.rt.primaryFilled = false
		s.st.rt.primaryFillCents = 0
		s.st.rt.hedgeToken = hedgeToken
		s.st.rt.hedgeOrderID = ""
		s.st.rt.hedgeFilled = false
		s.st.rt.hedgeTargetCents = hedgeCents
		s.st.rt.stopLevel = stopNone
		s.st.rt.phase = phasePrimaryOpen
		s.st.rt.tradesThisCycle++
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()

		s.log.Infof("✅ sequential：主 leg 已下单｜token=%s price=%dc orderID=%s hedgeTarget=%dc profit=%dc size=%.2f",
			primaryToken, primaryCents, primaryID, hedgeCents, s.st.cfg.ProfitCents, s.st.cfg.OrderSize)

		// 超时撤主 leg
		wait := time.Duration(s.st.cfg.SequentialPrimaryMaxWaitMs) * time.Millisecond
		if wait > 0 {
			time.AfterFunc(wait, func() {
				s.st.mu.Lock()
				if s.st.rt.phase != phasePrimaryOpen || s.st.rt.primaryOrderID != primaryID || s.st.rt.primaryFilled {
					s.st.mu.Unlock()
					return
				}
				mkt := s.st.rt.market
				s.st.mu.Unlock()

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = s.TradingService.CancelOrder(ctx, primaryID)
				s.st.mu.Lock()
				// 仍然处于等待态才 reset
				if s.st.rt.phase == phasePrimaryOpen && s.st.rt.primaryOrderID == primaryID && !s.st.rt.primaryFilled {
					s.log.Warnf("⏱️ 主 leg 等待成交超时，已尝试撤单并重置：orderID=%s market=%s", primaryID, func() string {
						if mkt != nil {
							return mkt.Slug
						}
						return ""
					}())
					s.resetPairLocked("sequential_primary_timeout")
					s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
				}
				s.st.mu.Unlock()
			})
		}
		_ = hedgeAsset
		return
	}

	// parallel：并发提交 UP+DOWN（主/对冲可分别配置下单类型）
	upStyle := s.st.cfg.HedgeOrderStyle
	downStyle := s.st.cfg.HedgeOrderStyle
	if plan.primaryToken == domain.TokenTypeUp {
		upStyle = s.st.cfg.PrimaryOrderStyle
	} else {
		downStyle = s.st.cfg.PrimaryOrderStyle
	}
	upOrder, err := s.makeBuyOrderForToken(market, domain.TokenTypeUp, upPriceCents, upAsk.ToCents(), upStyle, s.st.cfg.OrderSize, false)
	if err != nil {
		s.log.Warnf("❌ parallel 构造 UP 订单失败：err=%v", err)
		s.st.mu.Lock()
		s.resetPairLocked("parallel_up_build_failed")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}
	downOrder, err := s.makeBuyOrderForToken(market, domain.TokenTypeDown, downPriceCents, downAsk.ToCents(), downStyle, s.st.cfg.OrderSize, false)
	if err != nil {
		s.log.Warnf("❌ parallel 构造 DOWN 订单失败：err=%v", err)
		s.st.mu.Lock()
		s.resetPairLocked("parallel_down_build_failed")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}

	submitCtx, submitCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer submitCancel()

	created, err := s.orderExecutor.SubmitOrders(submitCtx, upOrder, downOrder)
	if err != nil {
		// 失败回滚：尽量撤掉已创建的订单，避免单边裸奔
		if len(created) > 0 {
			_ = s.orderExecutor.CancelOrders(context.Background(), created...)
		}
		s.log.Warnf("❌ 双边挂单失败：err=%v (up=%dc down=%dc profit=%dc)", err, upPriceCents, downPriceCents, s.st.cfg.ProfitCents)
		s.st.mu.Lock()
		s.resetPairLocked("submit_failed")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}

	var upID, downID string
	for _, o := range created {
		if o == nil {
			continue
		}
		if o.AssetID == market.YesAssetID {
			upID = o.OrderID
		} else if o.AssetID == market.NoAssetID {
			downID = o.OrderID
		}
	}
	if upID == "" || downID == "" {
		// 极端情况：创建成功但回包异常，直接重置并进入冷却
		s.log.Warnf("⚠️ 双边挂单回包缺少 orderID：upID=%q downID=%q，进入冷却", upID, downID)
		s.st.mu.Lock()
		s.resetPairLocked("missing_order_id")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}

	s.st.mu.Lock()
	s.st.rt.market = market
	s.st.rt.upOrderID = upID
	s.st.rt.downOrderID = downID
	s.st.rt.upFilled = false
	s.st.rt.downFilled = false
	// 记录本次 primary/hedge（用于并发模式下一边成交后的盯盘锁损）
	s.st.rt.primaryToken = plan.primaryToken
	if plan.primaryToken == domain.TokenTypeUp {
		s.st.rt.primaryOrderID = upID
		s.st.rt.hedgeToken = domain.TokenTypeDown
		s.st.rt.hedgeOrderID = downID
	} else {
		s.st.rt.primaryOrderID = downID
		s.st.rt.hedgeToken = domain.TokenTypeUp
		s.st.rt.hedgeOrderID = upID
	}
	s.st.rt.phase = phaseOpen
	s.st.rt.tradesThisCycle++
	s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
	s.st.mu.Unlock()

	s.log.Infof("✅ 速度触发：双边挂单已创建｜UP=%dc(%s) DOWN=%dc(%s) profit=%dc size=%.2f",
		upPriceCents, upID, downPriceCents, downID, s.st.cfg.ProfitCents, s.st.cfg.OrderSize)
}

func (s *Strategy) placeHedgeAfterPrimaryFilled(market *domain.Market, hedgeToken domain.TokenType, hedgeCents int, size float64) {
	if market == nil || s.TradingService == nil || s.orderExecutor == nil {
		s.st.mu.Lock()
		s.resetPairLocked("hedge_missing_deps")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}
	assetID := market.NoAssetID
	if hedgeToken == domain.TokenTypeUp {
		assetID = market.YesAssetID
	}

	// 对冲单下单方式（limit/taker）
	bestAskCents := 0
	if s.st.cfg.HedgeOrderStyle == "taker" {
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		p, err := orderutil.QuoteBuyPrice(cctx, s.TradingService, assetID, 0)
		if err != nil {
			s.log.Warnf("❌ 对冲单吃单模式获取 bestAsk 失败：err=%v", err)
			s.st.mu.Lock()
			s.resetPairLocked("hedge_taker_quote_failed")
			s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
			s.st.mu.Unlock()
			return
		}
		bestAskCents = p.ToCents()
	}
	hedgeOrder, err := s.makeBuyOrderForToken(market, hedgeToken, hedgeCents, bestAskCents, s.st.cfg.HedgeOrderStyle, size, true)
	if err != nil {
		s.log.Warnf("❌ 对冲单构造失败：err=%v", err)
		s.st.mu.Lock()
		s.resetPairLocked("hedge_build_failed")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}
	hedgeOrder.AssetID = assetID

	submitCtx, submitCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer submitCancel()
	created, err := s.orderExecutor.SubmitOrders(submitCtx, hedgeOrder)
	if err != nil || len(created) == 0 || created[0] == nil || created[0].OrderID == "" {
		s.log.Warnf("❌ 对冲 leg 下单失败：err=%v hedge=%dc token=%s", err, hedgeCents, hedgeToken)
		s.st.mu.Lock()
		s.resetPairLocked("hedge_submit_failed")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		s.st.mu.Unlock()
		return
	}

	hedgeID := created[0].OrderID
	s.st.mu.Lock()
	s.st.rt.hedgeOrderID = hedgeID
	s.st.rt.hedgeFilled = false
	s.st.rt.phase = phaseHedgeOpen
	s.st.mu.Unlock()

	s.log.Infof("✅ 对冲 leg 已下单｜token=%s price=%dc orderID=%s size=%.2f", hedgeToken, hedgeCents, hedgeID, size)

	// 启动盯盘止损
	if s.st.cfg.PriceStopEnabled {
		s.startMonitorIfNeeded()
	}
}

func (s *Strategy) startMonitorIfNeeded() {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	// 仅在 hedgeOpen 才盯盘
	if s.st.rt.monitorRunning || s.st.rt.phase != phaseHedgeOpen || s.st.rt.hedgeOrderID == "" {
		return
	}
	interval := time.Duration(s.st.cfg.PriceStopCheckIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.st.rt.monitorCancel = cancel
	s.st.rt.monitorRunning = true
	hedgeOrderID := s.st.rt.hedgeOrderID
	s.log.Infof("👀 启动盯盘止损：interval=%s hedgeOrderID=%s", interval, hedgeOrderID)
	go s.monitorLoop(ctx, interval, hedgeOrderID)
}

func (s *Strategy) stopMonitorLocked() {
	if s.st.rt.monitorCancel != nil {
		s.st.rt.monitorCancel()
		s.st.rt.monitorCancel = nil
	}
	s.st.rt.monitorRunning = false
	s.st.rt.stopLevel = stopNone
}

func (s *Strategy) monitorLoop(ctx context.Context, interval time.Duration, hedgeOrderID string) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkStopLossOnce(ctx, hedgeOrderID)
		}
	}
}

func (s *Strategy) checkStopLossOnce(ctx context.Context, hedgeOrderID string) {
	_ = ctx
	if s.TradingService == nil {
		return
	}

	s.st.mu.Lock()
	if s.st.rt.phase != phaseHedgeOpen || s.st.rt.hedgeOrderID != hedgeOrderID || s.st.rt.hedgeFilled {
		s.st.mu.Unlock()
		return
	}
	market := s.st.rt.market
	primaryFill := s.st.rt.primaryFillCents
	hedgeToken := s.st.rt.hedgeToken
	soft := s.st.cfg.PriceStopSoftLossCents
	hard := s.st.cfg.PriceStopHardLossCents
	maxLoss := s.st.cfg.MaxAcceptableLossCents
	currentLevel := s.st.rt.stopLevel
	s.st.mu.Unlock()

	if market == nil || primaryFill <= 0 {
		return
	}
	assetID := market.NoAssetID
	if hedgeToken == domain.TokenTypeUp {
		assetID = market.YesAssetID
	}
	// 取当前对冲侧 bestAsk（买单）
	cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p, err := orderutil.QuoteBuyPrice(cctx, s.TradingService, assetID, 0)
	if err != nil {
		return
	}
	hedgeAsk := p.ToCents()
	// 预计锁定收益（分）：100 - (primary + hedgeAsk)
	pnl := 100 - (primaryFill + hedgeAsk)

	// pnl 为负：亏损
	if pnl >= 0 {
		return
	}

	// 超过最大可接受亏损：不自动锁损（避免“为了对冲”吃得太贵）
	if -pnl > maxLoss {
		s.log.Warnf("🛑 预计锁损亏损过大，拒绝自动锁损：pnl=%dc maxLoss=%dc primary=%dc hedgeAsk=%dc",
			pnl, maxLoss, primaryFill, hedgeAsk)
		// 风控降频：短时间不再开新仓
		s.TradingService.TriggerRiskOff(5*time.Second, "velocitypairlock_stoploss_too_large")
		return
	}

	// 达到 hard：撤旧对冲单 -> FAK 吃单
	if pnl <= hard && currentLevel != stopHard {
		s.log.Warnf("🔻 触发硬锁损：pnl=%dc (<=%dc) 先撤对冲单再 FAK 锁损", pnl, hard)
		go s.executeStopLoss(hedgeOrderID, hedgeToken, market, hedgeAsk+s.st.cfg.TakerOffsetCents, types.OrderTypeFAK)
		s.st.mu.Lock()
		if s.st.rt.hedgeOrderID == hedgeOrderID && s.st.rt.phase == phaseHedgeOpen {
			s.st.rt.stopLevel = stopHard
		}
		s.st.mu.Unlock()
		return
	}

	// 达到 soft：撤旧对冲单 -> GTC@bestAsk（更激进，尽量成交，但不强制）
	if pnl <= soft && currentLevel == stopNone {
		s.log.Warnf("🔸 触发软锁损：pnl=%dc (<=%dc) 先撤对冲单再提价对冲", pnl, soft)
		ot := types.OrderTypeGTC
		price := hedgeAsk
		if s.st.cfg.HedgeOrderStyle == "taker" {
			ot = types.OrderTypeFAK
			price = hedgeAsk + s.st.cfg.TakerOffsetCents
		}
		go s.executeStopLoss(hedgeOrderID, hedgeToken, market, price, ot)
		s.st.mu.Lock()
		if s.st.rt.hedgeOrderID == hedgeOrderID && s.st.rt.phase == phaseHedgeOpen {
			s.st.rt.stopLevel = stopSoft
		}
		s.st.mu.Unlock()
		return
	}
}

func (s *Strategy) executeStopLoss(oldHedgeOrderID string, hedgeToken domain.TokenType, market *domain.Market, newPriceCents int, orderType types.OrderType) {
	if s.TradingService == nil || s.orderExecutor == nil || market == nil {
		return
	}
	// 1) 撤掉旧对冲单
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.TradingService.CancelOrder(cctx, oldHedgeOrderID)

	// 2) 新建锁损对冲单（更激进）
	s.st.mu.Lock()
	// 若状态已变化（比如已经成交/重置），退出
	if s.st.rt.phase != phaseHedgeOpen || s.st.rt.hedgeOrderID != oldHedgeOrderID || s.st.rt.hedgeFilled {
		s.st.mu.Unlock()
		return
	}
	size := s.st.rt.primaryFillSize
	if size <= 0 {
		size = s.st.cfg.OrderSize
	}
	s.st.mu.Unlock()

	assetID := market.NoAssetID
	if hedgeToken == domain.TokenTypeUp {
		assetID = market.YesAssetID
	}
	newOrder := domain.Order{
		MarketSlug:    market.Slug,
		AssetID:       assetID,
		Side:          types.SideBuy,
		Price:         priceFromCents(clampCents(newPriceCents, 1, 99)),
		Size:          size,
		TokenType:     hedgeToken,
		IsEntryOrder:  true,
		Status:        domain.OrderStatusPending,
		CreatedAt:     time.Now(),
		OrderType:     orderType,
		BypassRiskOff: true,
	}
	submitCtx, submitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer submitCancel()
	created, err := s.orderExecutor.SubmitOrders(submitCtx, newOrder)
	if err != nil || len(created) == 0 || created[0] == nil || created[0].OrderID == "" {
		s.log.Warnf("❌ 锁损对冲下单失败：err=%v type=%s price=%dc", err, orderType, newPriceCents)
		return
	}
	newID := created[0].OrderID
	s.st.mu.Lock()
	if s.st.rt.phase == phaseHedgeOpen && s.st.rt.hedgeOrderID == oldHedgeOrderID {
		s.st.rt.hedgeOrderID = newID
		s.st.rt.hedgeFilled = false
		// 盯盘协程继续盯（但需要同步 hedgeOrderID）
		s.log.Warnf("✅ 已替换对冲单：old=%s new=%s type=%s price=%dc", oldHedgeOrderID, newID, orderType, newPriceCents)
	}
	s.st.mu.Unlock()
}

func (s *Strategy) triggerAutoMergeLocked() {
	if !s.st.cfg.AutoMerge.Enabled {
		s.log.Infof("ℹ️ 双边已成交，但 autoMerge 未启用：等待结算（不合并释放资金）")
		s.st.rt.phase = phaseCooldown
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		// 不清空订单（保留用于审计）；但允许继续开单会导致资金不足，所以默认仍走 cooldown
		s.resetPairLocked("filled_no_automerge")
		return
	}
	if s.TradingService == nil || s.st.rt.market == nil {
		s.log.Warnf("⚠️ 无法 autoMerge：TradingService/market 为空")
		s.resetPairLocked("automerge_missing_deps")
		s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
		return
	}

	delay := time.Duration(s.st.cfg.AutoMerge.MergeTriggerDelaySeconds) * time.Second
	if delay < 0 {
		delay = 0
	}
	s.st.rt.phase = phaseMerging

	market := s.st.rt.market
	cfg := s.st.cfg.AutoMerge

	s.log.Infof("🔄 双边已成交：%ds 后触发 merge complete sets（释放资金继续开单）", int(delay.Seconds()))

	time.AfterFunc(delay, func() {
		s.st.rt.autoMergeCtl.MaybeAutoMerge(
			context.Background(),
			s.TradingService,
			market,
			cfg,
			func(format string, args ...any) { s.log.Infof(format, args...) },
			func(status string, amount float64, txHash string, err error) {
				// 回调里只做轻量状态更新，避免阻塞 autoMerge goroutine
				if status == "balance_refreshed" || status == "completed" {
					s.st.mu.Lock()
					defer s.st.mu.Unlock()
					s.log.Infof("✅ merge 完成（资金已刷新）：amount=%.6f tx=%s", amount, txHash)
					s.resetPairLocked("merge_done")
					s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
				}
				if status == "failed" && err != nil {
					s.st.mu.Lock()
					defer s.st.mu.Unlock()
					s.log.Warnf("⚠️ merge 失败：amount=%.6f err=%v", amount, err)
					// 失败也允许继续尝试下一次信号（资金可能仍被占用，取决于实际持仓）
					s.resetPairLocked("merge_failed")
					s.st.rt.cooldownUntil = time.Now().Add(s.st.cfg.CooldownDuration())
				}
			},
		)
	})
}

func (s *Strategy) resetPairLocked(reason string) {
	s.stopMonitorLocked()
	s.st.rt.phase = phaseIdle
	s.st.rt.upOrderID = ""
	s.st.rt.downOrderID = ""
	s.st.rt.upFilled = false
	s.st.rt.downFilled = false
	s.st.rt.primaryToken = ""
	s.st.rt.primaryOrderID = ""
	s.st.rt.primaryFilled = false
	s.st.rt.primaryFillCents = 0
	s.st.rt.primaryFillSize = 0
	s.st.rt.hedgeToken = ""
	s.st.rt.hedgeOrderID = ""
	s.st.rt.hedgeFilled = false
	s.st.rt.hedgeTargetCents = 0
	s.st.rt.stopLevel = stopNone
	_ = reason
}

func priceFromCents(c int) domain.Price {
	// 1 cent = 100 pips
	return domain.Price{Pips: c * 100}
}

// ===== compile-time guard =====
var _ bbgo.SingleExchangeStrategy = (*Strategy)(nil)
var _ bbgo.ExchangeSessionSubscriber = (*Strategy)(nil)
var _ bbgo.StrategyDefaulter = (*Strategy)(nil)
var _ bbgo.StrategyValidator = (*Strategy)(nil)
var _ bbgo.CycleAwareStrategy = (*Strategy)(nil)
var _ ports.OrderUpdateHandler = (*Strategy)(nil)
