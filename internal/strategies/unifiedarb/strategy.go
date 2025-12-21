package unifiedarb

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/internal/strategies/orderutil"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/sirupsen/logrus"
)

const ID = "unifiedarb"

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

type phase string

const (
	phaseBuild   phase = "build"
	phaseLock    phase = "lock"
	phaseAmplify phase = "amplify"
)

type plan struct {
	id        string
	market    string
	createdAt time.Time

	orderIDs []string
	done     map[string]bool // orderID -> done

	// riskShares: 该计划的“最坏执行未对冲规模”估计（用于并行风险预算）
	riskShares float64
	decision   string
}

// Strategy：统一套利策略（融合 arbitrage / pairedtrading / pairlock 的“锁定型套利”共性）
//
// 运行方式：
// - 订阅 PriceChanged + OrderUpdate
// - 通过 loop 合并事件推进内部状态机（避免在回调里做重活/阻塞）
// - 所有下单统一走 TradingService.ExecuteMultiLeg
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	guard common.MarketSlugGuard

	// event aggregation
	signalC chan struct{}
	priceMu sync.Mutex
	latest  map[domain.TokenType]*events.PriceChangedEvent
	orderC  chan *domain.Order

	// last seen reference price (from PriceChangedEvent.NewPrice)
	lastPxMu sync.Mutex
	lastPx   map[domain.TokenType]domain.Price

	loopOnce   sync.Once
	loopCancel context.CancelFunc

	// cycle state
	stateMu    sync.Mutex
	state      *domain.ArbitragePositionState
	lastFilled map[string]float64 // orderID -> last filledSize snapshot
	lastStatus map[string]domain.OrderStatus
	rounds     int
	lastSubmit time.Time
	paused     bool
	closeout   bool

	// plan tracking (pairlock-like)
	plansMu sync.Mutex
	plans   map[string]*plan

	// observability
	lastPhaseMu sync.Mutex
	lastPhase   phase
	lastLogAt   time.Time
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }

func (s *Strategy) Defaults() error { return nil }

func (s *Strategy) Validate() error { return s.Config.Validate() }

func (s *Strategy) Initialize() error {
	if s.signalC == nil {
		s.signalC = make(chan struct{}, 1)
	}
	if s.latest == nil {
		s.latest = make(map[domain.TokenType]*events.PriceChangedEvent)
	}
	if s.lastPx == nil {
		s.lastPx = make(map[domain.TokenType]domain.Price)
	}
	if s.orderC == nil {
		s.orderC = make(chan *domain.Order, 2048)
	}
	if s.lastFilled == nil {
		s.lastFilled = make(map[string]float64)
	}
	if s.lastStatus == nil {
		s.lastStatus = make(map[string]domain.OrderStatus)
	}
	if s.plans == nil {
		s.plans = make(map[string]*plan)
	}
	// 默认开启对冲（与 arbitrage/pairlock 简化版一致）
	if !s.Config.HedgeEnabled {
		// allow explicit disable; if user left it default false but expects enabled, they can set true
		// 为了不破坏旧配置（没有 hedgeEnabled 字段的场景），这里做一个“缺省启用”的折中：
		// - 当 hedgeEnabled 未显式配置时（bool 默认 false），我们仍然启用对冲，但允许用户显式关掉。
		// 由于无法区分“未配置”与“配置为 false”，这里用“MinExposureToHedge/HedgeDelaySeconds 任一被设置”来推断用户意图。
		if s.Config.MinExposureToHedge > 0 || s.Config.HedgeDelaySeconds > 0 || s.Config.HedgeSellPriceOffsetCents > 0 {
			// user likely configured hedge fields => keep HedgeEnabled=false if they want, do nothing
		} else {
			s.Config.HedgeEnabled = true
		}
	}
	if s.Config.HedgeDelaySeconds == 0 {
		s.Config.HedgeDelaySeconds = 2
	}
	if s.Config.HedgeSellPriceOffsetCents == 0 {
		s.Config.HedgeSellPriceOffsetCents = 2
	}
	if s.Config.MinExposureToHedge == 0 {
		s.Config.MinExposureToHedge = 1.0
	}
	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	session.OnOrderUpdate(s)
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	common.StartLoopOnce(ctx, &s.loopOnce, func(cancel context.CancelFunc) { s.loopCancel = cancel }, 0, s.loop)
	<-ctx.Done()
	return ctx.Err()
}

func (s *Strategy) OnPriceChanged(_ context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil {
		return nil
	}
	s.lastPxMu.Lock()
	s.lastPx[e.TokenType] = e.NewPrice
	s.lastPxMu.Unlock()
	s.priceMu.Lock()
	s.latest[e.TokenType] = e
	s.priceMu.Unlock()
	common.TrySignal(s.signalC)
	return nil
}

func (s *Strategy) OnOrderUpdate(_ context.Context, order *domain.Order) error {
	if order == nil {
		return nil
	}
	select {
	case s.orderC <- order:
	default:
	}
	common.TrySignal(s.signalC)
	return nil
}

func (s *Strategy) loop(loopCtx context.Context, _ <-chan time.Time) {
	for {
		select {
		case <-loopCtx.Done():
			return
		case <-s.signalC:
			s.step(loopCtx)
		}
	}
}

func (s *Strategy) step(loopCtx context.Context) {
	if s.TradingService == nil {
		return
	}

	// 1) 合并价格事件（取最新）
	s.priceMu.Lock()
	evUp := s.latest[domain.TokenTypeUp]
	evDown := s.latest[domain.TokenTypeDown]
	s.latest = make(map[domain.TokenType]*events.PriceChangedEvent)
	s.priceMu.Unlock()

	// 2) 选择市场上下文
	var m *domain.Market
	var now time.Time
	if evUp != nil && evUp.Market != nil {
		m = evUp.Market
		now = evUp.Timestamp
	}
	if m == nil && evDown != nil && evDown.Market != nil {
		m = evDown.Market
		now = evDown.Timestamp
	}
	if m == nil {
		// 仍然要消费订单更新（避免堆积）
		s.drainOrderUpdates()
		return
	}
	if now.IsZero() {
		now = time.Now()
	}

	// 3) 周期切换：重置状态
	if s.guard.Update(m.Slug) {
		s.resetCycle(now, m)
	}

	// 4) 先处理订单更新（更新仓位/成本/plan 状态）
	s.drainOrderUpdates()

	// 4.5) closeout：结算前强制收敛（停止新增交易，必要时撤单/回平）
	if s.maybeCloseout(loopCtx, now, m) {
		return
	}

	// 5) paused 则只继续处理 plan 超时（并不下新单）
	s.checkPlanTimeouts(loopCtx, now, m)
	s.stateMu.Lock()
	paused := s.paused
	s.stateMu.Unlock()
	if paused {
		return
	}

	// 6) 冷却 + 轮数上限
	s.stateMu.Lock()
	if s.rounds >= s.MaxRoundsPerPeriod {
		s.stateMu.Unlock()
		return
	}
	if !s.lastSubmit.IsZero() && now.Sub(s.lastSubmit) < time.Duration(s.CooldownMs)*time.Millisecond {
		s.stateMu.Unlock()
		return
	}
	s.stateMu.Unlock()

	// 7) 并行限制（pairlock 核心风险控制：限制在途轮次）
	if !s.canStartNewPlan() {
		return
	}

	// 8) 计算当前阶段 & 当前锁定状态（pairedtrading 核心：阶段调度）
	ph := s.detectPhase(nowUnix(now), m)
	locked, minProfit := s.isLocked()

	s.maybeLogState(now, m, ph, locked, minProfit)

	// 9) Phase 行为（按 pairedtrading README：Build -> Lock -> Amplify）
	switch ph {
	case phaseBuild:
		s.maybeBuild(loopCtx, m, now)
	case phaseAmplify:
		// Amplify：方向性放大（前提：尽量保持锁定），否则回退到 Lock 修复风险
		s.maybeAmplify(loopCtx, m, now, locked, minProfit)
	default:
		// Lock：风险敞口驱动（优先修复负利润，其次拉升 min(P_up, P_down) 到目标）
		s.maybeLock(loopCtx, m, now, locked, minProfit)
	}
}

func (s *Strategy) maybeLogState(now time.Time, m *domain.Market, ph phase, locked bool, minProfit float64) {
	// 只在阶段变化或每 5 秒输出一次，避免刷屏
	s.lastPhaseMu.Lock()
	defer s.lastPhaseMu.Unlock()

	shouldLog := false
	if s.lastPhase != ph {
		shouldLog = true
		s.lastPhase = ph
	}
	if s.lastLogAt.IsZero() || now.Sub(s.lastLogAt) >= 5*time.Second {
		shouldLog = true
	}
	if !shouldLog {
		return
	}
	s.lastLogAt = now

	qUp, qDown, cUp, cDown, pUp, pDown := s.stateSnapshot()
	upPx, downPx := s.lastSeenPrice()
	log.Infof("📈 [%s] state: market=%s phase=%s locked=%t minP=%.2f upPx=%dc downPx=%dc QUp=%.2f QDown=%.2f CUp=%.2f CDown=%.2f P_up=%.2f P_down=%.2f",
		ID, m.Slug, ph, locked, minProfit, upPx.Cents, downPx.Cents, qUp, qDown, cUp, cDown, pUp, pDown)
}

func (s *Strategy) lastSeenPrice() (up domain.Price, down domain.Price) {
	s.lastPxMu.Lock()
	defer s.lastPxMu.Unlock()
	up = s.lastPx[domain.TokenTypeUp]
	down = s.lastPx[domain.TokenTypeDown]
	return up, down
}

// maybeCloseout 在临近结算时触发“强制收敛”：
// - 目的：避免尾段流动性变差导致的追单/腿不匹配风险
// - 行为：按 CloseoutAction 执行 pause/cancel_pause/flatten_pause
// 返回 true 表示已进入 closeout 并中止本轮 step。
func (s *Strategy) maybeCloseout(ctx context.Context, now time.Time, m *domain.Market) bool {
	if s.CycleDurationSeconds <= 0 || s.CloseoutStartSeconds <= 0 || m == nil || m.Timestamp <= 0 {
		return false
	}
	elapsed := nowUnix(now) - m.Timestamp
	if elapsed < 0 {
		elapsed = 0
	}
	remaining := int64(s.CycleDurationSeconds) - elapsed
	if remaining > int64(s.CloseoutStartSeconds) {
		return false
	}

	s.stateMu.Lock()
	already := s.closeout
	if !already {
		s.closeout = true
	}
	s.stateMu.Unlock()
	if already {
		return true
	}

	log.Warnf("⏳ [%s] closeout window entered: market=%s remaining=%ds action=%s",
		ID, m.Slug, remaining, s.CloseoutAction)

	switch s.CloseoutAction {
	case "pause":
		s.stateMu.Lock()
		s.paused = true
		s.stateMu.Unlock()
		return true
	case "cancel_pause":
		orderCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		s.TradingService.CancelOrdersForMarket(orderCtx, m.Slug)
		cancel()
		s.stateMu.Lock()
		s.paused = true
		s.stateMu.Unlock()
		return true
	case "flatten_pause":
		orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		// 先撤单避免打架
		s.TradingService.CancelOrdersForMarket(orderCtx, m.Slug)
		// 再把净敞口回平（QUp≈QDown）
		s.tryFlatten(orderCtx, m)
		cancel()
		s.stateMu.Lock()
		s.paused = true
		s.stateMu.Unlock()
		return true
	default:
		// 未配置 closeout 时不做任何事
		return false
	}
}

func (s *Strategy) resetCycle(now time.Time, m *domain.Market) {
	s.stateMu.Lock()
	s.rounds = 0
	s.lastSubmit = time.Time{}
	s.paused = false
	s.closeout = false
	s.state = domain.NewArbitragePositionState(m)
	s.lastFilled = make(map[string]float64)
	s.lastStatus = make(map[string]domain.OrderStatus)
	s.stateMu.Unlock()

	s.plansMu.Lock()
	s.plans = make(map[string]*plan)
	s.plansMu.Unlock()

	log.Infof("🔄 [%s] 周期切换，重置状态: market=%s ts=%d", ID, m.Slug, m.Timestamp)
	_ = now
}

func (s *Strategy) drainOrderUpdates() {
	for {
		select {
		case o := <-s.orderC:
			s.onOrder(o)
		default:
			return
		}
	}
}

func (s *Strategy) onOrder(o *domain.Order) {
	if o == nil || o.OrderID == "" {
		return
	}

	// 仅基于 FilledSize 的增量更新 state（避免重复回调导致重复累加）
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	prev := s.lastFilled[o.OrderID]
	cur := o.FilledSize
	if cur < prev {
		// 回放/回退（理论上不应发生），以当前为准重置（避免负增量污染）
		prev = 0
	}
	delta := cur - prev
	if delta > 0 && s.state != nil {
		amount := delta * o.Price.ToDecimal()
		switch o.TokenType {
		case domain.TokenTypeUp:
			if o.Side == types.SideBuy {
				s.state.QUp += delta
				s.state.CUp += amount
			} else {
				s.state.QUp -= delta
				if s.state.QUp < 0 {
					s.state.QUp = 0
				}
				s.state.CUp -= amount
			}
		case domain.TokenTypeDown:
			if o.Side == types.SideBuy {
				s.state.QDown += delta
				s.state.CDown += amount
			} else {
				s.state.QDown -= delta
				if s.state.QDown < 0 {
					s.state.QDown = 0
				}
				s.state.CDown -= amount
			}
		}
	}
	s.lastFilled[o.OrderID] = cur
	s.lastStatus[o.OrderID] = o.Status

	// plan tracking：标记腿完成
	s.plansMu.Lock()
	for _, p := range s.plans {
		if p == nil {
			continue
		}
		if p.done == nil {
			p.done = make(map[string]bool)
		}
		if isTerminal(o.Status) {
			p.done[o.OrderID] = true
		}
	}
	s.plansMu.Unlock()
}

func isTerminal(st domain.OrderStatus) bool {
	switch st {
	case domain.OrderStatusFilled, domain.OrderStatusCanceled, domain.OrderStatusFailed:
		return true
	default:
		return false
	}
}

func (s *Strategy) canStartNewPlan() bool {
	s.plansMu.Lock()
	defer s.plansMu.Unlock()
	active := 0
	for _, p := range s.plans {
		if p == nil {
			continue
		}
		if !planDone(p) {
			active++
		}
	}
	return active < s.MaxConcurrentPlans
}

func (s *Strategy) checkPlanTimeouts(ctx context.Context, now time.Time, m *domain.Market) {
	s.plansMu.Lock()
	defer s.plansMu.Unlock()
	for id, p := range s.plans {
		if p == nil {
			delete(s.plans, id)
			continue
		}
		if planDone(p) {
			delete(s.plans, id)
			continue
		}
		if now.Sub(p.createdAt) < time.Duration(s.MaxPlanAgeSeconds)*time.Second {
			continue
		}
		// 超时：按配置执行失败动作，并暂停本周期
		log.Warnf("⚠️ [%s] plan 超时触发失败动作: plan=%s market=%s age=%s action=%s risk=%.4f decision=%s",
			ID, p.id, m.Slug, now.Sub(p.createdAt).Truncate(time.Millisecond), s.OnFailAction, p.riskShares, p.decision)
		s.failAction(ctx, now, m)
		delete(s.plans, id)
	}
}

func planDone(p *plan) bool {
	if p == nil {
		return true
	}
	if len(p.orderIDs) == 0 {
		return true
	}
	if p.done == nil {
		return false
	}
	for _, id := range p.orderIDs {
		if id == "" {
			continue
		}
		if !p.done[id] {
			return false
		}
	}
	return true
}

func (s *Strategy) failAction(ctx context.Context, now time.Time, m *domain.Market) {
	s.stateMu.Lock()
	s.paused = true
	s.stateMu.Unlock()

	switch s.OnFailAction {
	case "pause":
		return
	case "cancel_pause":
		orderCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		s.TradingService.CancelOrdersForMarket(orderCtx, m.Slug)
		return
	case "flatten_pause":
		orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		// 先取消所有活跃订单，避免与回平互相打架
		s.TradingService.CancelOrdersForMarket(orderCtx, m.Slug)
		// 再尝试把净敞口回平到“成对”（QUp≈QDown）
		s.tryFlatten(orderCtx, m)
		return
	default:
		_ = now
		return
	}
}

func (s *Strategy) tryFlatten(ctx context.Context, m *domain.Market) {
	s.stateMu.Lock()
	st := s.state
	s.stateMu.Unlock()
	if st == nil || m == nil {
		return
	}
	diff := st.QUp - st.QDown
	if math.Abs(diff) < s.FailFlattenMinShares {
		return
	}
	var assetID string
	var token domain.TokenType
	var size float64
	if diff > 0 {
		assetID = m.YesAssetID
		token = domain.TokenTypeUp
		size = diff
	} else {
		assetID = m.NoAssetID
		token = domain.TokenTypeDown
		size = -diff
	}

	// 以 bestBid 为基准，做一个“偏移但不超过 slippage 下限”的卖出价
	bestBid, _, err := s.TradingService.GetBestPrice(ctx, assetID)
	if err != nil || bestBid <= 0 {
		return
	}
	bestBidCents := int(bestBid*100 + 0.5)
	priceCents := bestBidCents - 2
	if priceCents < 1 {
		priceCents = 1
	}
	if s.FailMaxSellSlippageCents > 0 {
		minAllowed := bestBidCents - s.FailMaxSellSlippageCents
		if priceCents < minAllowed {
			priceCents = minAllowed
			if priceCents < 1 {
				priceCents = 1
			}
		}
	}

	req := execution.MultiLegRequest{
		Name:       "unifiedarb_flatten",
		MarketSlug: m.Slug,
		Legs: []execution.LegIntent{
			{
				Name:      "sell_excess",
				AssetID:   assetID,
				TokenType: token,
				Side:      types.SideSell,
				Price:     domain.Price{Cents: priceCents},
				Size:      size,
				OrderType: types.OrderTypeFAK,
			},
		},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}
	_, _ = s.TradingService.ExecuteMultiLeg(ctx, req)
}

func (s *Strategy) detectPhase(nowUnix int64, m *domain.Market) phase {
	// 若未启用分阶段，则默认 lock
	if s.CycleDurationSeconds <= 0 {
		return phaseLock
	}
	elapsed := int64(0)
	if m != nil && m.Timestamp > 0 {
		elapsed = nowUnix - m.Timestamp
		if elapsed < 0 {
			elapsed = 0
		}
	}
	ph := phaseLock
	if int(elapsed) < s.BuildDurationSeconds {
		ph = phaseBuild
	} else if int(elapsed) >= s.AmplifyStartSeconds {
		ph = phaseAmplify
	}

	// early switch：优先使用 PriceChangedEvent.NewPrice 作为 reference（无需每步查 bestbook）
	upPx, downPx := s.lastSeenPrice()
	upDec := upPx.ToDecimal()
	downDec := downPx.ToDecimal()
	maxPx := math.Max(upDec, downDec)
	if maxPx <= 0 {
		// fallback：用 bestAsk
		askUp, askDown := s.latestAskSnapshot()
		maxPx = math.Max(askUp, askDown)
	}
	if s.EarlyLockPrice > 0 && maxPx >= s.EarlyLockPrice {
		if ph == phaseBuild {
			ph = phaseLock
		}
	}
	if s.EarlyAmplifyPrice > 0 && maxPx >= s.EarlyAmplifyPrice {
		locked, _ := s.isLocked()
		if locked {
			ph = phaseAmplify
		}
	}
	return ph
}

func (s *Strategy) latestAskSnapshot() (upAsk float64, downAsk float64) {
	// 这里不走 orderbook API，直接用 BestPrice（会命中 TradingService 的 bestBook 缓存）
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	s.stateMu.Lock()
	m := (*domain.Market)(nil)
	if s.state != nil {
		m = s.state.Market
	}
	s.stateMu.Unlock()
	if m == nil {
		return 0, 0
	}
	_, up, _ := s.TradingService.GetBestPrice(ctx, m.YesAssetID)
	_, down, _ := s.TradingService.GetBestPrice(ctx, m.NoAssetID)
	return up, down
}

func (s *Strategy) isLocked() (locked bool, minProfit float64) {
	s.stateMu.Lock()
	st := s.state
	s.stateMu.Unlock()
	if st == nil {
		return false, 0
	}
	pu := st.ProfitIfUpWin()
	pd := st.ProfitIfDownWin()
	minProfit = math.Min(pu, pd)
	locked = pu > 0 && pd > 0
	return locked, minProfit
}

func (s *Strategy) maybeBuild(ctx context.Context, m *domain.Market, now time.Time) {
	if s.BaseTarget <= 0 || s.BuildLotSize <= 0 || s.BuildThreshold <= 0 {
		return
	}
	qUp, qDown, _, _, _, _ := s.stateSnapshot()
	if qUp >= s.BaseTarget && qDown >= s.BaseTarget {
		return
	}

	orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	_, upAskDec, err1 := s.TradingService.GetBestPrice(orderCtx, m.YesAssetID)
	_, downAskDec, err2 := s.TradingService.GetBestPrice(orderCtx, m.NoAssetID)
	if err1 != nil || err2 != nil || upAskDec <= 0 || downAskDec <= 0 {
		return
	}
	if upAskDec > s.BuildThreshold && downAskDec > s.BuildThreshold {
		return
	}

	total := qUp + qDown
	ratioUp := 0.5
	if total > 0 {
		ratioUp = qUp / total
	}

	// 维持双边比例，避免单边过重（pairedtrading README：40%-60%）
	target := domain.TokenTypeUp
	if ratioUp < s.MinRatio {
		target = domain.TokenTypeUp
	} else if ratioUp > s.MaxRatio {
		target = domain.TokenTypeDown
	} else {
		// 在比例允许区间内：优先补齐低于 baseTarget 的方向；若两边都低，则买更便宜的一边
		upNeed := qUp < s.BaseTarget && upAskDec <= s.BuildThreshold
		downNeed := qDown < s.BaseTarget && downAskDec <= s.BuildThreshold
		if upNeed && downNeed {
			if upAskDec <= downAskDec {
				target = domain.TokenTypeUp
			} else {
				target = domain.TokenTypeDown
			}
		} else if upNeed {
			target = domain.TokenTypeUp
		} else if downNeed {
			target = domain.TokenTypeDown
		} else {
			return
		}
	}

	if target == domain.TokenTypeUp && upAskDec > s.BuildThreshold {
		return
	}
	if target == domain.TokenTypeDown && downAskDec > s.BuildThreshold {
		return
	}

	req := s.buildSingleBuyReq(m, target, s.BuildLotSize, "build", map[domain.TokenType]domain.Price{
		domain.TokenTypeUp:   domain.PriceFromDecimal(upAskDec),
		domain.TokenTypeDown: domain.PriceFromDecimal(downAskDec),
	})
	if req == nil {
		return
	}
	_ = s.submitPlan(orderCtx, now, req, fmt.Sprintf("build target=%s ratioUp=%.2f", target, ratioUp))
}

func (s *Strategy) maybeLock(ctx context.Context, m *domain.Market, now time.Time, locked bool, minProfit float64) {
	// 1) 优先吃掉“无方向的确定性套利”（complete-set）
	if s.maybeCompleteSet(ctx, m, now, "lock_complete_set") {
		return
	}

	orderCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	yesAsk, err1 := s.quoteBuy(orderCtx, m, domain.TokenTypeUp, s.LockPriceMax, "lock")
	noAsk, err2 := s.quoteBuy(orderCtx, m, domain.TokenTypeDown, s.LockPriceMax, "lock")
	if err1 != nil || err2 != nil {
		return
	}
	upAskDec := yesAsk.ToDecimal()
	downAskDec := noAsk.ToDecimal()
	upAsk := yesAsk
	downAsk := noAsk

	// 2) 极端价格：买入反向保险（pairedtrading README）
	if s.ExtremeHigh > 0 {
		if upAskDec >= s.ExtremeHigh && downAskDec <= s.LockPriceMax && s.InsuranceSize > 0 {
			req := s.buildSingleBuyReq(m, domain.TokenTypeDown, s.InsuranceSize, "lock_extreme_insurance", map[domain.TokenType]domain.Price{
				domain.TokenTypeDown: downAsk,
			})
			if req != nil {
				_ = s.submitPlan(orderCtx, now, req, "extreme_high_buy_insurance_down")
			}
			return
		}
		if downAskDec >= s.ExtremeHigh && upAskDec <= s.LockPriceMax && s.InsuranceSize > 0 {
			req := s.buildSingleBuyReq(m, domain.TokenTypeUp, s.InsuranceSize, "lock_extreme_insurance", map[domain.TokenType]domain.Price{
				domain.TokenTypeUp: upAsk,
			})
			if req != nil {
				_ = s.submitPlan(orderCtx, now, req, "extreme_high_buy_insurance_up")
			}
			return
		}
	}

	_, _, _, _, pu, pd := s.stateSnapshot()

	// 3) 风险优先：先修复明显负利润（达到 lockThreshold 才触发，避免噪声频繁交易）
	if s.LockThreshold > 0 {
		if pu < 0 && -pu >= s.LockThreshold && upAskDec <= s.LockPriceMax {
			req := s.buildSingleBuyReq(m, domain.TokenTypeUp, s.OrderSize, "lock_fix_negative", map[domain.TokenType]domain.Price{
				domain.TokenTypeUp: upAsk,
			})
			if req != nil {
				_ = s.submitPlan(orderCtx, now, req, "fix_negative_profit_up")
			}
			return
		}
		if pd < 0 && -pd >= s.LockThreshold && downAskDec <= s.LockPriceMax {
			req := s.buildSingleBuyReq(m, domain.TokenTypeDown, s.OrderSize, "lock_fix_negative", map[domain.TokenType]domain.Price{
				domain.TokenTypeDown: downAsk,
			})
			if req != nil {
				_ = s.submitPlan(orderCtx, now, req, "fix_negative_profit_down")
			}
			return
		}
	}

	// 4) 均衡与冲目标：选择能提升 min(P_up, P_down) 的买入
	targetMin := 0.0
	if s.TargetProfitBase > 0 {
		targetMin = s.TargetProfitBase
	}
	if (!locked) || (targetMin > 0 && minProfit < targetMin) {
		bestTok := domain.TokenType("")
		bestMin := minProfit

		lot := s.OrderSize
		if s.BuildLotSize > 0 {
			lot = math.Min(lot, s.BuildLotSize)
		}
		if lot <= 0 {
			lot = s.OrderSize
		}

		if upAskDec > 0 && upAskDec <= s.LockPriceMax {
			pu2, pd2 := simulateBuy(pu, pd, lot, upAskDec, domain.TokenTypeUp)
			min2 := math.Min(pu2, pd2)
			if min2 > bestMin {
				bestMin = min2
				bestTok = domain.TokenTypeUp
			}
		}
		if downAskDec > 0 && downAskDec <= s.LockPriceMax {
			pu2, pd2 := simulateBuy(pu, pd, lot, downAskDec, domain.TokenTypeDown)
			min2 := math.Min(pu2, pd2)
			if min2 > bestMin {
				bestMin = min2
				bestTok = domain.TokenTypeDown
			}
		}
		if bestTok != "" {
			req := s.buildSingleBuyReq(m, bestTok, lot, "lock_balance", map[domain.TokenType]domain.Price{
				domain.TokenTypeUp:   upAsk,
				domain.TokenTypeDown: downAsk,
			})
			if req != nil {
				_ = s.submitPlan(orderCtx, now, req, fmt.Sprintf("balance_min_profit tok=%s minP=%.2f->%.2f", bestTok, minProfit, bestMin))
			}
		}
	}
}

func (s *Strategy) maybeAmplify(ctx context.Context, m *domain.Market, now time.Time, locked bool, minProfit float64) {
	// 未锁定时，先回到 lock 修复风险敞口
	if !locked {
		s.maybeLock(ctx, m, now, locked, minProfit)
		return
	}

	// 仍优先吃“确定性套利”
	if s.maybeCompleteSet(ctx, m, now, "amplify_complete_set") {
		return
	}

	if s.AmplifyTarget > 0 && minProfit >= s.AmplifyTarget {
		return
	}

	orderCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	upAsk, err1 := s.quoteBuy(orderCtx, m, domain.TokenTypeUp, s.AmplifyPriceMax, "amplify")
	downAsk, err2 := s.quoteBuy(orderCtx, m, domain.TokenTypeDown, s.AmplifyPriceMax, "amplify")
	if err1 != nil || err2 != nil {
		return
	}
	upAskDec := upAsk.ToDecimal()
	downAskDec := downAsk.ToDecimal()

	main := domain.TokenType("")
	if upAskDec >= s.DirectionThreshold && upAskDec >= downAskDec {
		main = domain.TokenTypeUp
	} else if downAskDec >= s.DirectionThreshold && downAskDec >= upAskDec {
		main = domain.TokenTypeDown
	} else {
		// 没有明确主方向：回退到 lock（用 minProfit 均衡方式小步推进）
		s.maybeLock(ctx, m, now, locked, minProfit)
		return
	}

	mainAskDec := upAskDec
	oppAskDec := downAskDec
	mainAsset := m.YesAssetID
	oppAsset := m.NoAssetID
	if main == domain.TokenTypeDown {
		mainAskDec = downAskDec
		oppAskDec = upAskDec
		mainAsset = m.NoAssetID
		oppAsset = m.YesAssetID
	}
	if s.AmplifyPriceMax > 0 && mainAskDec > s.AmplifyPriceMax {
		return
	}

	// 反向保险：只在“极低价”时买一点
	insTok := opposite(main)
	insSize := 0.0
	if s.InsuranceSize > 0 && s.InsurancePriceMax > 0 && oppAskDec > 0 && oppAskDec <= s.InsurancePriceMax {
		insSize = s.InsuranceSize
	}

	_, _, _, _, pu, pd := s.stateSnapshot()
	// 预检：放大后仍需保持锁定（两边利润 > 0）
	mainSize := s.OrderSize
	if mainSize <= 0 {
		return
	}
	pu2, pd2 := simulateAmplify(pu, pd, main, mainSize, mainAskDec, insTok, insSize, oppAskDec)
	if pu2 <= 0 || pd2 <= 0 {
		return
	}

	mainPrice := domain.PriceFromDecimal(mainAskDec)
	oppPrice := domain.PriceFromDecimal(oppAskDec)
	legs := []execution.LegIntent{
		{
			Name:      "buy_main",
			AssetID:   mainAsset,
			TokenType: main,
			Side:      types.SideBuy,
			Price:     mainPrice,
			Size:      ensureMinOrderSize(mainSize, mainAskDec, s.MinOrderSize),
			OrderType: types.OrderTypeFAK,
		},
	}
	if insSize > 0 {
		legs = append(legs, execution.LegIntent{
			Name:      "buy_insurance",
			AssetID:   oppAsset,
			TokenType: insTok,
			Side:      types.SideBuy,
			Price:     oppPrice,
			Size:      ensureMinOrderSize(insSize, oppAskDec, s.MinOrderSize),
			OrderType: types.OrderTypeFAK,
		})
	}
	req := &execution.MultiLegRequest{
		Name:       "unifiedarb_amplify",
		MarketSlug: m.Slug,
		Legs:       legs,
		Hedge:      s.hedgeConfig(),
	}
	_ = s.submitPlan(orderCtx, now, req, fmt.Sprintf("amplify main=%s mainAsk=%.4f insurance=%t", main, mainAskDec, insSize > 0))
}

func (s *Strategy) maybeCompleteSet(ctx context.Context, m *domain.Market, now time.Time, reason string) bool {
	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	yesAsk, err := s.quoteBuy(orderCtx, m, domain.TokenTypeUp, 1.0, "complete_set")
	if err != nil {
		return false
	}
	noAsk, err := s.quoteBuy(orderCtx, m, domain.TokenTypeDown, 1.0, "complete_set")
	if err != nil {
		return false
	}

	total := yesAsk.Cents + noAsk.Cents
	maxTotal := 100 - s.ProfitTargetCents
	if total > maxTotal {
		return false
	}

	req := s.buildCompleteSetReq(m, yesAsk, noAsk, s.OrderSize, reason)
	if req == nil {
		return false
	}
	return s.submitPlan(orderCtx, now, req, fmt.Sprintf("complete_set profitTargetCents=%d", s.ProfitTargetCents))
}

// quoteBuy 统一买入报价入口：
// - priceMaxDecimal：用于阶段价格上限（如 lockPriceMax/amplifyPriceMax），<=0 表示不限制
// - entryMaxBuySlippageCents：相对滑点保护（基于 PriceChangedEvent.NewPrice 作为 reference）
func (s *Strategy) quoteBuy(ctx context.Context, m *domain.Market, tok domain.TokenType, priceMaxDecimal float64, reason string) (domain.Price, error) {
	if m == nil {
		return domain.Price{}, fmt.Errorf("market nil")
	}
	assetID := m.NoAssetID
	if tok == domain.TokenTypeUp {
		assetID = m.YesAssetID
	}
	// 阶段上限
	maxCents := 0
	if priceMaxDecimal > 0 {
		maxCents = int(priceMaxDecimal*100 + 0.5)
	}
	// 相对滑点：reference 来自最新 price event
	if s.EntryMaxBuySlippageCents > 0 {
		refUp, refDown := s.lastSeenPrice()
		ref := refDown
		if tok == domain.TokenTypeUp {
			ref = refUp
		}
		if ref.Cents > 0 {
			refMax := ref.Cents + s.EntryMaxBuySlippageCents
			if maxCents == 0 || refMax < maxCents {
				maxCents = refMax
			}
		}
	}
	p, err := orderutil.QuoteBuyPrice(ctx, s.TradingService, assetID, maxCents)
	if err != nil {
		return domain.Price{}, err
	}
	// 额外防护：用于调试时快速定位是哪个阶段触发的保护
	if maxCents > 0 && p.Cents > maxCents {
		return domain.Price{}, fmt.Errorf("buy blocked(%s): tok=%s bestAsk=%dc max=%dc", reason, tok, p.Cents, maxCents)
	}
	return p, nil
}

func (s *Strategy) buildSingleBuyReq(m *domain.Market, tok domain.TokenType, desiredSize float64, reason string, price map[domain.TokenType]domain.Price) *execution.MultiLegRequest {
	if m == nil || desiredSize <= 0 {
		return nil
	}
	p, ok := price[tok]
	if !ok {
		return nil
	}
	if p.Cents <= 0 || p.ToDecimal() <= 0 {
		return nil
	}
	size := ensureMinOrderSize(desiredSize, p.ToDecimal(), s.MinOrderSize)
	assetID := m.NoAssetID
	if tok == domain.TokenTypeUp {
		assetID = m.YesAssetID
	}
	return &execution.MultiLegRequest{
		Name:       fmt.Sprintf("unifiedarb_%s_%s", reason, tok),
		MarketSlug: m.Slug,
		Legs: []execution.LegIntent{
			{
				Name:      "buy_one",
				AssetID:   assetID,
				TokenType: tok,
				Side:      types.SideBuy,
				Price:     p,
				Size:      size,
				OrderType: types.OrderTypeFAK,
			},
		},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}
}

func (s *Strategy) buildCompleteSetReq(m *domain.Market, yesAsk, noAsk domain.Price, desiredSize float64, reason string) *execution.MultiLegRequest {
	if m == nil {
		return nil
	}
	if desiredSize <= 0 {
		return nil
	}
	size := desiredSize
	// 确保两腿单笔金额都 >= MinOrderSize
	if yesAsk.ToDecimal() > 0 {
		size = math.Max(size, s.MinOrderSize/yesAsk.ToDecimal())
	}
	if noAsk.ToDecimal() > 0 {
		size = math.Max(size, s.MinOrderSize/noAsk.ToDecimal())
	}
	if size <= 0 || math.IsInf(size, 0) || math.IsNaN(size) {
		return nil
	}

	hedge := execution.AutoHedgeConfig{Enabled: false}
	if s.HedgeEnabled {
		hedge.Enabled = true
		hedge.Delay = time.Duration(s.HedgeDelaySeconds) * time.Second
		hedge.SellPriceOffsetCents = s.HedgeSellPriceOffsetCents
		hedge.MinExposureToHedge = s.MinExposureToHedge
	}
	req := &execution.MultiLegRequest{
		Name:       fmt.Sprintf("unifiedarb_complete_set_%s", reason),
		MarketSlug: m.Slug,
		Legs: []execution.LegIntent{
			{Name: "buy_yes", AssetID: m.YesAssetID, TokenType: domain.TokenTypeUp, Side: types.SideBuy, Price: yesAsk, Size: size, OrderType: types.OrderTypeFAK},
			{Name: "buy_no", AssetID: m.NoAssetID, TokenType: domain.TokenTypeDown, Side: types.SideBuy, Price: noAsk, Size: size, OrderType: types.OrderTypeFAK},
		},
		Hedge: hedge,
	}
	return req
}

func (s *Strategy) submitPlan(ctx context.Context, now time.Time, req *execution.MultiLegRequest, decision string) bool {
	if req == nil {
		return false
	}

	// 并行风险预算：限制“最坏执行未对冲规模”
	// 说明：该预算主要针对“多腿并发执行时的成交不匹配风险”，而非策略的方向性风险。
	if s.MaxTotalUnhedgedShares > 0 {
		newRisk := estimatePlanRiskShares(req)
		curRisk := s.currentActiveRiskShares()
		if curRisk+newRisk > s.MaxTotalUnhedgedShares {
			log.Warnf("⛔ [%s] risk budget exceeded: market=%s cur=%.4f new=%.4f budget=%.4f decision=%s",
				ID, req.MarketSlug, curRisk, newRisk, s.MaxTotalUnhedgedShares, decision)
			return false
		}
	}

	created, err := s.TradingService.ExecuteMultiLeg(ctx, *req)
	if err != nil {
		return false
	}

	// 记录 plan
	p := &plan{
		id:         fmt.Sprintf("plan_%d", time.Now().UnixNano()),
		market:     req.MarketSlug,
		createdAt:  now,
		decision:   decision,
		riskShares: estimatePlanRiskShares(req),
	}
	for _, o := range created {
		if o == nil || o.OrderID == "" {
			continue
		}
		p.orderIDs = append(p.orderIDs, o.OrderID)
	}
	if len(p.orderIDs) == 0 {
		return false
	}
	p.done = make(map[string]bool, len(p.orderIDs))
	s.plansMu.Lock()
	s.plans[p.id] = p
	s.plansMu.Unlock()

	s.stateMu.Lock()
	s.rounds++
	s.lastSubmit = now
	st := s.state
	s.stateMu.Unlock()

	if st != nil {
		log.Infof("🎯 [%s] submit: rounds=%d/%d market=%s action=%s risk=%.4f decision=%s QUp=%.2f QDown=%.2f P_up=%.2f P_down=%.2f",
			ID, s.rounds, s.MaxRoundsPerPeriod, req.MarketSlug, req.Name, p.riskShares, decision, st.QUp, st.QDown, st.ProfitIfUpWin(), st.ProfitIfDownWin())
	}
	return true
}

func (s *Strategy) currentActiveRiskShares() float64 {
	s.plansMu.Lock()
	defer s.plansMu.Unlock()
	total := 0.0
	for _, p := range s.plans {
		if p == nil {
			continue
		}
		if planDone(p) {
			continue
		}
		total += p.riskShares
	}
	return total
}

func estimatePlanRiskShares(req *execution.MultiLegRequest) float64 {
	if req == nil || len(req.Legs) == 0 {
		return 0
	}

	// complete-set 场景：Up+Down 双腿同时买入，执行不匹配的最坏风险近似为 max(size)。
	if len(req.Legs) == 2 &&
		req.Legs[0].Side == types.SideBuy && req.Legs[1].Side == types.SideBuy &&
		((req.Legs[0].TokenType == domain.TokenTypeUp && req.Legs[1].TokenType == domain.TokenTypeDown) ||
			(req.Legs[0].TokenType == domain.TokenTypeDown && req.Legs[1].TokenType == domain.TokenTypeUp)) {
		return math.Max(req.Legs[0].Size, req.Legs[1].Size)
	}

	// 其他多腿（如 amplify 主方向 + 保险）：保守取 sum(size)
	sum := 0.0
	for _, l := range req.Legs {
		if l.Size > 0 {
			sum += l.Size
		}
	}
	return sum
}

func (s *Strategy) stateSnapshot() (qUp, qDown, cUp, cDown, pUp, pDown float64) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.state == nil {
		return 0, 0, 0, 0, 0, 0
	}
	qUp = s.state.QUp
	qDown = s.state.QDown
	cUp = s.state.CUp
	cDown = s.state.CDown
	pUp = s.state.ProfitIfUpWin()
	pDown = s.state.ProfitIfDownWin()
	return
}

func simulateBuy(pu, pd float64, size float64, ask float64, tok domain.TokenType) (pu2, pd2 float64) {
	if size <= 0 || ask <= 0 || ask >= 1.0 {
		return pu, pd
	}
	switch tok {
	case domain.TokenTypeUp:
		pu2 = pu + size*(1.0-ask)
		pd2 = pd - size*ask
	case domain.TokenTypeDown:
		pd2 = pd + size*(1.0-ask)
		pu2 = pu - size*ask
	default:
		return pu, pd
	}
	return pu2, pd2
}

func simulateAmplify(pu, pd float64, main domain.TokenType, mainSize float64, mainAsk float64, ins domain.TokenType, insSize float64, insAsk float64) (pu2, pd2 float64) {
	pu2, pd2 = simulateBuy(pu, pd, mainSize, mainAsk, main)
	if insSize > 0 && insAsk > 0 {
		pu2, pd2 = simulateBuy(pu2, pd2, insSize, insAsk, ins)
	}
	return pu2, pd2
}

func opposite(t domain.TokenType) domain.TokenType {
	if t == domain.TokenTypeUp {
		return domain.TokenTypeDown
	}
	return domain.TokenTypeUp
}

func ensureMinOrderSize(desiredShares float64, ask float64, minUSDC float64) float64 {
	if desiredShares <= 0 || ask <= 0 {
		return desiredShares
	}
	minShares := minUSDC / ask
	if minShares > desiredShares {
		return minShares
	}
	return desiredShares
}

func (s *Strategy) hedgeConfig() execution.AutoHedgeConfig {
	if !s.HedgeEnabled {
		return execution.AutoHedgeConfig{Enabled: false}
	}
	return execution.AutoHedgeConfig{
		Enabled:              true,
		Delay:                time.Duration(s.HedgeDelaySeconds) * time.Second,
		SellPriceOffsetCents: s.HedgeSellPriceOffsetCents,
		MinExposureToHedge:   s.MinExposureToHedge,
	}
}

func nowUnix(t time.Time) int64 {
	if t.IsZero() {
		return time.Now().Unix()
	}
	return t.Unix()
}
