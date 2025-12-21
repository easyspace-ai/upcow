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

	yesOrderID string
	noOrderID  string
	yesDone    bool
	noDone     bool
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

	// plan tracking (pairlock-like)
	plansMu sync.Mutex
	plans   map[string]*plan
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

	// 9) Phase 行为（保守版：不做方向性加仓，只做“锁定型放大利润”）
	switch ph {
	case phaseBuild:
		// Build 仅在低价区尝试建立基础仓位（可选）
		s.maybeBuild(loopCtx, m, now)
	case phaseAmplify:
		// Amplify：若已锁定但最差利润 < amplifyTarget，则继续做 complete-set
		if locked && s.AmplifyTarget > 0 && minProfit < s.AmplifyTarget {
			s.maybeCompleteSet(loopCtx, m, now, "amplify")
		} else {
			// 即使未锁定/未达放大条件，也允许继续锁定型套利（更稳）
			s.maybeCompleteSet(loopCtx, m, now, "amplify_lock")
		}
	default:
		// Lock：以 complete-set 为主
		_ = locked // kept for future extension
		if s.TargetProfitBase > 0 && minProfit >= s.TargetProfitBase {
			// 已达到基础目标，仍可继续套利（由 ProfitTargetCents 控制门槛）
			s.maybeCompleteSet(loopCtx, m, now, "lock_target_met")
		} else {
			s.maybeCompleteSet(loopCtx, m, now, "lock")
		}
	}
}

func (s *Strategy) resetCycle(now time.Time, m *domain.Market) {
	s.stateMu.Lock()
	s.rounds = 0
	s.lastSubmit = time.Time{}
	s.paused = false
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
		if o.OrderID == p.yesOrderID && isTerminal(o.Status) {
			p.yesDone = true
		}
		if o.OrderID == p.noOrderID && isTerminal(o.Status) {
			p.noDone = true
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
		if !(p.yesDone && p.noDone) {
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
		if p.yesDone && p.noDone {
			delete(s.plans, id)
			continue
		}
		if now.Sub(p.createdAt) < time.Duration(s.MaxPlanAgeSeconds)*time.Second {
			continue
		}
		// 超时：按配置执行失败动作，并暂停本周期
		log.Warnf("⚠️ [%s] plan 超时触发失败动作: plan=%s market=%s age=%s action=%s",
			ID, p.id, m.Slug, now.Sub(p.createdAt).Truncate(time.Millisecond), s.OnFailAction)
		s.failAction(ctx, now, m)
		delete(s.plans, id)
	}
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

	// early switch：基于价格快速切换（保守实现：只用“任意腿 ask”）
	askUp, askDown := s.latestAskSnapshot()
	maxAsk := math.Max(askUp, askDown)
	if s.EarlyLockPrice > 0 && maxAsk >= s.EarlyLockPrice {
		if ph == phaseBuild {
			ph = phaseLock
		}
	}
	if s.EarlyAmplifyPrice > 0 && maxAsk >= s.EarlyAmplifyPrice {
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
	// 若已接近基础目标，则不再建仓
	s.stateMu.Lock()
	st := s.state
	s.stateMu.Unlock()
	if st == nil {
		return
	}
	if st.QUp >= s.BaseTarget && st.QDown >= s.BaseTarget {
		return
	}

	// 低价区才建仓
	orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	maxCents := int(s.BuildThreshold*100 + 0.5)
	if maxCents <= 0 {
		return
	}
	upAsk, err1 := orderutil.QuoteBuyPrice(orderCtx, s.TradingService, m.YesAssetID, maxCents)
	downAsk, err2 := orderutil.QuoteBuyPrice(orderCtx, s.TradingService, m.NoAssetID, maxCents)
	if err1 != nil || err2 != nil {
		return
	}
	// build：以小额 complete-set 为主（保持尽量中性）
	req := s.buildCompleteSetReq(m, upAsk, downAsk, s.BuildLotSize, "build")
	if req == nil {
		return
	}
	s.submitPlan(orderCtx, now, req)
}

func (s *Strategy) maybeCompleteSet(ctx context.Context, m *domain.Market, now time.Time, reason string) {
	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	// 说明：EntryMaxBuySlippageCents 在旧 pairlock 设计里是“相对滑点保护”；
	// 这里缺少可靠的 reference price（且 bestBook 已在 TradingService 内部做了缓存），
	// 因此先不做相对滑点校验，只使用 bestAsk 作为下单价。
	yesAsk, err := orderutil.QuoteBuyPrice(orderCtx, s.TradingService, m.YesAssetID, 0)
	if err != nil {
		return
	}
	noAsk, err := orderutil.QuoteBuyPrice(orderCtx, s.TradingService, m.NoAssetID, 0)
	if err != nil {
		return
	}

	total := yesAsk.Cents + noAsk.Cents
	maxTotal := 100 - s.ProfitTargetCents
	if total > maxTotal {
		return
	}

	req := s.buildCompleteSetReq(m, yesAsk, noAsk, s.OrderSize, reason)
	if req == nil {
		return
	}
	s.submitPlan(orderCtx, now, req)
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

func (s *Strategy) submitPlan(ctx context.Context, now time.Time, req *execution.MultiLegRequest) {
	if req == nil {
		return
	}
	created, err := s.TradingService.ExecuteMultiLeg(ctx, *req)
	if err != nil {
		return
	}

	// 记录 plan
	p := &plan{
		id:        fmt.Sprintf("plan_%d", time.Now().UnixNano()),
		market:    req.MarketSlug,
		createdAt: now,
	}
	if len(created) >= 2 {
		if created[0] != nil {
			p.yesOrderID = created[0].OrderID
		}
		if created[1] != nil {
			p.noOrderID = created[1].OrderID
		}
	}
	s.plansMu.Lock()
	s.plans[p.id] = p
	s.plansMu.Unlock()

	s.stateMu.Lock()
	s.rounds++
	s.lastSubmit = now
	st := s.state
	s.stateMu.Unlock()

	if st != nil {
		log.Infof("🎯 [%s] submit: rounds=%d/%d market=%s QUp=%.2f QDown=%.2f P_up=%.2f P_down=%.2f",
			ID, s.rounds, s.MaxRoundsPerPeriod, req.MarketSlug, st.QUp, st.QDown, st.ProfitIfUpWin(), st.ProfitIfDownWin())
	}
}

func nowUnix(t time.Time) int64 {
	if t.IsZero() {
		return time.Now().Unix()
	}
	return t.Unix()
}
