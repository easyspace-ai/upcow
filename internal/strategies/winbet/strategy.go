package winbet

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/common"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/services"
	vfdash "github.com/betbot/gobet/internal/strategies/velocityfollow/dashboard"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/marketspec"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	mu              sync.Mutex
	orderUpdateOnce sync.Once

	sampler *velocitySampler

	// 周期状态
	cycleStartTime  time.Time
	lastTriggerTime time.Time
	tradesThisCycle int

	// 风险敞口：Entry 已成交但 Hedge 未成交
	exposures map[string]*hedgeExposure // entryOrderID -> exposure

	// 自动合并 complete sets
	autoMerge common.AutoMergeController

	// Dashboard
	dash             *vfdash.Dashboard
	dashboardCtx     context.Context
	dashboardCancel  context.CancelFunc
	dashboardLoopOnce sync.Once

	// Dashboard 中展示的合并状态
	mergeMu       sync.Mutex
	mergeStatus   string
	mergeAmount   float64
	mergeTxHash   string
	lastMergeTime time.Time
	mergeCount    int
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }

func (s *Strategy) Defaults() error { return s.Config.Defaults() }
func (s *Strategy) Validate() error { return s.Config.Validate() }

func (s *Strategy) Initialize() error {
	if s.TradingService == nil {
		return nil
	}
	if s.sampler == nil {
		s.sampler = newVelocitySampler(s.Config.WindowSeconds)
	}
	if s.exposures == nil {
		s.exposures = make(map[string]*hedgeExposure)
	}

	s.orderUpdateOnce.Do(func() {
		s.TradingService.OnOrderUpdate(services.OrderUpdateHandlerFunc(s.OnOrderUpdate))
	})

	// Dashboard
	if s.Config.DashboardEnabled {
		s.dash = vfdash.New(s.TradingService, s.Config.DashboardUseNativeTUI)
		s.dash.SetTitle("WinBet Strategy Dashboard")
		s.dash.SetEnabled(true)
		s.dash.ReapplyLogRedirect()
		s.dashboardCtx, s.dashboardCancel = context.WithCancel(context.Background())
	}

	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	// 兜底：保证只注册一次订单回调
	if s.TradingService != nil {
		s.orderUpdateOnce.Do(func() {
			s.TradingService.OnOrderUpdate(services.OrderUpdateHandlerFunc(s.OnOrderUpdate))
		})
	}
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	// 启动 Dashboard（若启用）
	if s.Config.DashboardEnabled && s.dash != nil {
		// 统一退出链路：UI 退出时，触发一次 SIGINT（Bubble Tea/native 都会走到这里）
		s.dash.SetExitCallback(func() {
			// 让策略 Run 尽快退出；root ctx 的取消由外层信号处理负责
			if s.dashboardCancel != nil {
				s.dashboardCancel()
			}
		})
		_ = s.dash.Start(ctx)
		s.dashboardLoopOnce.Do(func() {
			go s.dashboardLoop(s.dashboardCtx)
		})
	}

	<-ctx.Done()
	return ctx.Err()
}

func (s *Strategy) Shutdown(ctx context.Context, wg *sync.WaitGroup) {
	_ = wg
	if s.dashboardCancel != nil {
		s.dashboardCancel()
	}
	if s.dash != nil {
		s.dash.Stop()
	}
}

func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, newMarket *domain.Market) {
	s.mu.Lock()
	s.cycleStartTime = time.Now()
	s.lastTriggerTime = time.Time{}
	s.tradesThisCycle = 0
	if s.sampler != nil {
		s.sampler.Reset()
	}
	s.exposures = make(map[string]*hedgeExposure)
	s.mu.Unlock()

	// Dashboard：重置 UI 状态，确保周期切换立即同步
	if s.dash != nil && s.Config.DashboardEnabled && newMarket != nil {
		s.dash.ReapplyLogRedirect()
		s.dash.ResetSnapshot(newMarket)
		s.dash.SendUpdate()
	}
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}

	// 更新样本
	if s.sampler != nil {
		s.sampler.Add(e.TokenType, e.NewPrice.ToDecimal(), time.Now())
	}

	// fail-safe：不要在存在未对冲敞口时继续开新仓
	s.mu.Lock()
	hasExposure := len(s.exposures) > 0
	s.mu.Unlock()
	if hasExposure {
		return nil
	}

	// 节律控制
	if !s.passesRhythmGates(e.Market) {
		return nil
	}

	// 生成交易计划
	plan, dc := s.buildPlan(ctx, e.Market)
	if s.dash != nil && s.Config.DashboardEnabled {
		s.pushDashboard(ctx, e.Market, plan, dc)
	}
	if plan == nil || !plan.shouldTrade {
		return nil
	}

	// 执行交易（顺序：Entry FAK -> Hedge GTC）
	if err := s.executePlan(ctx, e.Market, plan); err != nil {
		// fail-safe：市场切换或暂停交易时属于预期拒绝
		estr := strings.ToLower(err.Error())
		if strings.Contains(estr, "trading paused") || strings.Contains(estr, "market mismatch") {
			log.Warnf("⏸️ [%s] 系统拒绝下单（fail-safe，预期行为）: %v", ID, err)
			return nil
		}
		log.Warnf("⚠️ [%s] 下单失败: %v", ID, err)
		return nil
	}

	s.mu.Lock()
	s.lastTriggerTime = time.Now()
	s.tradesThisCycle++
	s.mu.Unlock()

	return nil
}

type tradePlan struct {
	shouldTrade bool
	reason      string
	direction   domain.TokenType

	entryPriceCents int
	hedgePriceCents int
	entrySize       float64
	hedgeSize       float64
}

type hedgeExposure struct {
	marketSlug string

	entryOrderID string
	hedgeOrderID string

	entryToken domain.TokenType
	hedgeToken domain.TokenType

	entryFilledAt   time.Time
	entryFilledSize float64
	entryPriceCents int

	// 风控：状态
	lastAction      string
	lastActionAt    time.Time
	originalHedgeCents int
	lastHedgeCents     int
}

func (s *Strategy) passesRhythmGates(market *domain.Market) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// warmup
	if !s.cycleStartTime.IsZero() {
		if time.Since(s.cycleStartTime) < time.Duration(s.Config.WarmupMs)*time.Millisecond {
			return false
		}
	}
	// cooldown
	if !s.lastTriggerTime.IsZero() {
		if time.Since(s.lastTriggerTime) < time.Duration(s.Config.CooldownMs)*time.Millisecond {
			return false
		}
	}
	// max trades
	if s.Config.MaxTradesPerCycle > 0 && s.tradesThisCycle >= s.Config.MaxTradesPerCycle {
		return false
	}
	// cycle end protection
	if market != nil && market.Timestamp > 0 && s.Config.CycleEndProtectionMinutes > 0 {
		cycleDuration := cycleDurationFromMarket(market)
		elapsed := time.Since(time.Unix(market.Timestamp, 0))
		protect := time.Duration(s.Config.CycleEndProtectionMinutes) * time.Minute
		if elapsed > cycleDuration-protect {
			return false
		}
	}
	return true
}

func (s *Strategy) buildPlan(ctx context.Context, market *domain.Market) (*tradePlan, *vfdash.DecisionConditions) {
	now := time.Now()

	upVel, upDelta, upMove, upOK := s.sampler.Stats(domain.TokenTypeUp, now)
	downVel, downDelta, downMove, downOK := s.sampler.Stats(domain.TokenTypeDown, now)

	// 选择方向：只考虑“正向推高价格”的一侧
	type cand struct {
		token domain.TokenType
		vel   float64
		delta int
		move  int
	}
	var cands []cand
	if upOK && upDelta > 0 {
		cands = append(cands, cand{token: domain.TokenTypeUp, vel: upVel, delta: upDelta, move: upMove})
	}
	if downOK && downDelta > 0 {
		cands = append(cands, cand{token: domain.TokenTypeDown, vel: downVel, delta: downDelta, move: downMove})
	}

	minMove := s.Config.MinMoveCents
	minVel := s.Config.MinVelocityCentsPerSec
	var chosen *cand
	for i := range cands {
		c := cands[i]
		if c.move < minMove || c.vel < minVel {
			continue
		}
		if chosen == nil || c.vel > chosen.vel {
			tmp := c
			chosen = &tmp
		}
	}

	plan := &tradePlan{shouldTrade: false, reason: "未满足信号条件"}
	dc := &vfdash.DecisionConditions{
		MarketValid: true,
		UpVelocityValue:   upVel,
		DownVelocityValue: downVel,
		UpMoveValue:       upMove,
		DownMoveValue:     downMove,
		UpVelocityOK:      upMove >= minMove && upVel >= minVel && upDelta > 0,
		DownVelocityOK:    downMove >= minMove && downVel >= minVel && downDelta > 0,
		UpMoveOK:          upMove >= minMove,
		DownMoveOK:        downMove >= minMove,
	}
	if chosen == nil {
		dc.Direction = ""
		dc.CanTrade = false
		dc.BlockReason = "未满足速度条件"
		return plan, dc
	}
	dc.Direction = strings.ToUpper(string(chosen.token))
	plan.direction = chosen.token

	// 取一档价格
	yesBid, yesAsk, noBid, noAsk, _, err := s.TradingService.GetTopOfBook(ctx, market)
	if err != nil {
		dc.CanTrade = false
		dc.BlockReason = "无法获取盘口"
		plan.reason = dc.BlockReason
		return plan, dc
	}
	var bid, ask domain.Price
	if plan.direction == domain.TokenTypeUp {
		bid, ask = yesBid, yesAsk
	} else {
		bid, ask = noBid, noAsk
	}

	spreadCents := int((ask.ToDecimal()-bid.ToDecimal())*100 + 0.5)
	if s.Config.MaxSpreadCents > 0 && spreadCents > s.Config.MaxSpreadCents {
		dc.CanTrade = false
		dc.BlockReason = "价差过大"
		plan.reason = dc.BlockReason
		return plan, dc
	}

	entryAskCents := ask.ToCents()
	dc.EntryPriceValue = ask.ToDecimal()
	dc.EntryPriceMin = float64(s.Config.MinEntryPriceCents) / 100.0
	dc.EntryPriceMax = float64(s.Config.MaxEntryPriceCents) / 100.0
	dc.EntryPriceOK = (s.Config.MinEntryPriceCents <= 0 || entryAskCents >= s.Config.MinEntryPriceCents) &&
		(s.Config.MaxEntryPriceCents <= 0 || entryAskCents <= s.Config.MaxEntryPriceCents)
	if !dc.EntryPriceOK {
		dc.CanTrade = false
		dc.BlockReason = "Entry价格不在范围内"
		plan.reason = dc.BlockReason
		return plan, dc
	}

	hedgeCents := 100 - entryAskCents - s.Config.HedgeOffsetCents
	dc.HedgePriceValue = float64(hedgeCents) / 100.0
	dc.HedgePriceOK = hedgeCents > 0 && hedgeCents < 100
	if !dc.HedgePriceOK {
		dc.CanTrade = false
		dc.BlockReason = "Hedge价格无效"
		plan.reason = dc.BlockReason
		return plan, dc
	}

	totalCost := entryAskCents + hedgeCents
	dc.TotalCostValue = float64(totalCost) / 100.0
	dc.TotalCostOK = totalCost <= 100
	if !dc.TotalCostOK {
		dc.CanTrade = false
		dc.BlockReason = "总成本超过100c"
		plan.reason = dc.BlockReason
		return plan, dc
	}

	plan.shouldTrade = true
	plan.reason = "满足信号与风险门槛"
	plan.entryPriceCents = entryAskCents
	plan.hedgePriceCents = hedgeCents
	plan.entrySize = s.Config.OrderSize
	if s.Config.HedgeOrderSize > 0 {
		plan.hedgeSize = s.Config.HedgeOrderSize
	} else {
		plan.hedgeSize = s.Config.OrderSize
	}

	dc.CanTrade = true
	dc.BlockReason = ""
	return plan, dc
}

func (s *Strategy) executePlan(ctx context.Context, market *domain.Market, plan *tradePlan) error {
	if market == nil || plan == nil || !plan.shouldTrade {
		return nil
	}

	entryAsset := market.YesAssetID
	hedgeAsset := market.NoAssetID
	hedgeToken := domain.TokenTypeDown
	if plan.direction == domain.TokenTypeDown {
		entryAsset = market.NoAssetID
		hedgeAsset = market.YesAssetID
		hedgeToken = domain.TokenTypeUp
	}

	entryOrder := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      entryAsset,
		Side:         types.SideBuy,
		Price:        domain.Price{Pips: plan.entryPriceCents * 100},
		Size:         plan.entrySize,
		TokenType:    plan.direction,
		IsEntryOrder: true,
		OrderType:    types.OrderTypeFAK,
		CreatedAt:    time.Now(),
	}

	createdEntry, err := s.TradingService.PlaceOrder(ctx, entryOrder)
	if err != nil {
		return err
	}

	// 等待 Entry 成交（FAK：通常立刻成交或取消；这里用短轮询兜底）
	filled, filledOrder := s.waitOrderFilled(ctx, createdEntry.OrderID, time.Duration(s.Config.EntryFillMaxWaitMs)*time.Millisecond)
	if !filled {
		return nil
	}

	entryPriceCents := plan.entryPriceCents
	entryFilledAt := time.Now()
	entrySize := plan.entrySize
	if filledOrder != nil {
		if filledOrder.FilledPrice != nil {
			entryPriceCents = filledOrder.FilledPrice.ToCents()
		} else {
			entryPriceCents = filledOrder.Price.ToCents()
		}
		if filledOrder.FilledAt != nil {
			entryFilledAt = *filledOrder.FilledAt
		}
		if filledOrder.FilledSize > 0 {
			entrySize = filledOrder.FilledSize
		}
	}

	hedgeOrder := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      hedgeAsset,
		Side:         types.SideBuy,
		Price:        domain.Price{Pips: plan.hedgePriceCents * 100},
		Size:         plan.hedgeSize,
		TokenType:    hedgeToken,
		IsEntryOrder: false,
		OrderType:    types.OrderTypeGTC,
		CreatedAt:    time.Now(),
		BypassRiskOff: true, // 风控动作：允许绕过短时 risk-off，避免敞口失控
	}

	createdHedge, err := s.TradingService.PlaceOrder(ctx, hedgeOrder)
	if err != nil {
		return err
	}

	// 记录敞口（等待对冲成交）
	s.mu.Lock()
	s.exposures[createdEntry.OrderID] = &hedgeExposure{
		marketSlug:        market.Slug,
		entryOrderID:      createdEntry.OrderID,
		hedgeOrderID:      createdHedge.OrderID,
		entryToken:        plan.direction,
		hedgeToken:        hedgeToken,
		entryFilledAt:     entryFilledAt,
		entryFilledSize:   entrySize,
		entryPriceCents:   entryPriceCents,
		lastAction:        "idle",
		lastActionAt:      time.Now(),
		originalHedgeCents: plan.hedgePriceCents,
		lastHedgeCents:     plan.hedgePriceCents,
	}
	s.mu.Unlock()

	return nil
}

func (s *Strategy) waitOrderFilled(ctx context.Context, orderID string, maxWait time.Duration) (bool, *domain.Order) {
	deadline := time.Now().Add(maxWait)
	interval := time.Duration(s.Config.EntryFillCheckIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = 20 * time.Millisecond
	}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false, nil
		default:
		}
		if o, ok := s.TradingService.GetOrder(orderID); ok && o != nil {
			if o.IsFilled() {
				return true, o
			}
			// FAK 被取消/失败则直接结束
			if o.Status == domain.OrderStatusCanceled || o.Status == domain.OrderStatusFailed {
				return false, o
			}
		}
		time.Sleep(interval)
	}
	return false, nil
}

func (s *Strategy) dashboardLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(s.Config.DashboardRefreshMs) * time.Millisecond)
	defer ticker.Stop()

	riskTicker := time.NewTicker(time.Duration(s.Config.RiskCheckIntervalMs) * time.Millisecond)
	defer riskTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			market := s.TradingService.GetCurrentMarketInfo()
			if market != nil {
				plan, dc := s.buildPlan(ctx, market)
				s.pushDashboard(ctx, market, plan, dc)
			}
		case <-riskTicker.C:
			// 风控巡检（对冲敞口）
			s.riskTick(ctx)
		}
	}
}

func (s *Strategy) pushDashboard(ctx context.Context, market *domain.Market, plan *tradePlan, dc *vfdash.DecisionConditions) {
	if s.dash == nil || market == nil {
		return
	}

	yesBid, yesAsk, noBid, noAsk, _, err := s.TradingService.GetTopOfBook(ctx, market)
	var yesBidF, yesAskF, noBidF, noAskF float64
	if err == nil {
		yesBidF = yesBid.ToDecimal()
		yesAskF = yesAsk.ToDecimal()
		noBidF = noBid.ToDecimal()
		noAskF = noAsk.ToDecimal()
	}

	upVel, _, upMove, _ := s.sampler.Stats(domain.TokenTypeUp, time.Now())
	downVel, _, downMove, _ := s.sampler.Stats(domain.TokenTypeDown, time.Now())

	s.mu.Lock()
	trades := s.tradesThisCycle
	last := s.lastTriggerTime
	pending := len(s.exposures)
	s.mu.Unlock()

	// merge 状态
	s.mergeMu.Lock()
	mergeStatus := s.mergeStatus
	mergeAmount := s.mergeAmount
	mergeTx := s.mergeTxHash
	lastMerge := s.lastMergeTime
	mergeCount := s.mergeCount
	s.mergeMu.Unlock()

	// 风控状态
	rm := s.dashboardRiskStatus()

	update := &vfdash.UpdateData{
		YesBid:  yesBidF,
		YesAsk:  yesAskF,
		NoBid:   noBidF,
		NoAsk:   noAskF,
		YesPrice: (yesBidF + yesAskF) / 2,
		NoPrice:  (noBidF + noAskF) / 2,

		UpVelocity:   upVel,
		DownVelocity: downVel,
		UpMove:       upMove,
		DownMove:     downMove,
		Direction:    "",

		TradesThisCycle: trades,
		LastTriggerTime: last,
		PendingHedges:   pending,
		OpenOrders:      len(s.TradingService.GetActiveOrders()),

		RiskManagement:     rm,
		DecisionConditions: dc,

		MergeStatus:   mergeStatus,
		MergeAmount:   mergeAmount,
		MergeTxHash:   mergeTx,
		LastMergeTime: lastMerge,
		MergeCount:    mergeCount,

		// 周期信息：让 UI 根据 CycleEndTime 实时计算倒计时
		CycleEndTime: marketCycleEndTime(market),
	}

	if plan != nil {
		if plan.direction == domain.TokenTypeUp {
			update.Direction = "UP"
		} else if plan.direction == domain.TokenTypeDown {
			update.Direction = "DOWN"
		}
	}

	s.dash.UpdateSnapshot(ctx, market, update)
	s.dash.Render()
}

func (s *Strategy) dashboardRiskStatus() *vfdash.RiskManagementStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	rm := &vfdash.RiskManagementStatus{
		CurrentAction: "idle",
	}
	if len(s.exposures) == 0 {
		return rm
	}

	infos := make([]vfdash.RiskExposureInfo, 0, len(s.exposures))
	for _, exp := range s.exposures {
		if exp == nil {
			continue
		}
		secs := time.Since(exp.entryFilledAt).Seconds()
		countdown := float64(s.Config.AggressiveHedgeTimeoutSeconds) - secs
		if countdown < 0 {
			countdown = 0
		}
		infos = append(infos, vfdash.RiskExposureInfo{
			EntryOrderID:            exp.entryOrderID,
			EntryTokenType:          strings.ToUpper(string(exp.entryToken)),
			EntrySize:               exp.entryFilledSize,
			EntryPriceCents:         exp.entryPriceCents,
			HedgeOrderID:            exp.hedgeOrderID,
			HedgeStatus:             "Open",
			ExposureSeconds:         secs,
			MaxLossCents:            s.Config.MaxNegativeProfitCents,
			OriginalHedgePriceCents: exp.originalHedgeCents,
			NewHedgePriceCents:      exp.lastHedgeCents,
			CountdownSeconds:        countdown,
		})
		// 只展示一条主要 action（策略内部也只允许单敞口正常情况下出现）
		rm.CurrentAction = exp.lastAction
		rm.CurrentActionEntry = exp.entryOrderID
		rm.CurrentActionHedge = exp.hedgeOrderID
		rm.CurrentActionTime = exp.lastActionAt
	}
	rm.RiskExposuresCount = len(infos)
	rm.RiskExposures = infos
	return rm
}

func (s *Strategy) riskTick(ctx context.Context) {
	s.mu.Lock()
	// 快路径：无敞口
	if len(s.exposures) == 0 {
		s.mu.Unlock()
		return
	}
	// 复制 keys，避免持锁做 IO
	keys := make([]string, 0, len(s.exposures))
	for k := range s.exposures {
		keys = append(keys, k)
	}
	s.mu.Unlock()

	market := s.TradingService.GetCurrentMarketInfo()
	if market == nil {
		return
	}

	for _, entryID := range keys {
		s.mu.Lock()
		exp := s.exposures[entryID]
		s.mu.Unlock()
		if exp == nil {
			continue
		}

		// hedge 已成交则清理并尝试 autoMerge
		if hedge, ok := s.TradingService.GetOrder(exp.hedgeOrderID); ok && hedge != nil && hedge.IsFilled() {
			s.mu.Lock()
			delete(s.exposures, entryID)
			s.mu.Unlock()

			s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, market, s.AutoMerge, log.Infof, s.onAutoMerge)
			continue
		}

		elapsed := time.Since(exp.entryFilledAt)
		if elapsed >= time.Duration(s.AggressiveHedgeTimeoutSeconds)*time.Second {
			// 激进对冲：取消旧 hedge，按当前 ask 直接买入（FAK）
			s.aggressiveHedge(ctx, market, exp)
			continue
		}
		if elapsed >= time.Duration(s.HedgeReorderTimeoutSeconds)*time.Second {
			// 调价：取消并以更合理的价格重挂
			s.repriceHedge(ctx, market, exp)
			continue
		}
	}
}

func (s *Strategy) repriceHedge(ctx context.Context, market *domain.Market, exp *hedgeExposure) {
	_, yesAsk, _, noAsk, _, err := s.TradingService.GetTopOfBook(ctx, market)
	if err != nil {
		return
	}
	var hedgeAsk domain.Price
	var hedgeAsset string
	if exp.hedgeToken == domain.TokenTypeUp {
		hedgeAsk = yesAsk
		hedgeAsset = market.YesAssetID
	} else {
		hedgeAsk = noAsk
		hedgeAsset = market.NoAssetID
	}

	// 允许的最大总成本（cents）
	maxTotal := 100
	if s.Config.AllowNegativeProfitOnHedge && s.Config.MaxNegativeProfitCents > 0 {
		maxTotal = 100 + s.Config.MaxNegativeProfitCents
	}
	maxHedgeCents := maxTotal - exp.entryPriceCents
	if maxHedgeCents <= 0 {
		return
	}

	newCents := hedgeAsk.ToCents()
	if newCents > maxHedgeCents {
		newCents = maxHedgeCents
	}
	if newCents <= 0 || newCents >= 100 {
		return
	}

	// 取消旧单（风控动作允许）
	_ = s.TradingService.CancelOrder(ctx, exp.hedgeOrderID)

	newOrder := &domain.Order{
		MarketSlug:    market.Slug,
		AssetID:       hedgeAsset,
		Side:          types.SideBuy,
		Price:         domain.Price{Pips: newCents * 100},
		Size:          exp.entryFilledSize,
		TokenType:     exp.hedgeToken,
		IsEntryOrder:  false,
		OrderType:     types.OrderTypeGTC,
		CreatedAt:     time.Now(),
		BypassRiskOff: true,
	}
	created, err := s.TradingService.PlaceOrder(ctx, newOrder)
	if err != nil || created == nil || created.OrderID == "" {
		return
	}

	s.mu.Lock()
	exp.hedgeOrderID = created.OrderID
	exp.lastHedgeCents = newCents
	exp.lastAction = "reordering"
	exp.lastActionAt = time.Now()
	s.mu.Unlock()
}

func (s *Strategy) aggressiveHedge(ctx context.Context, market *domain.Market, exp *hedgeExposure) {
	_, yesAsk, _, noAsk, _, err := s.TradingService.GetTopOfBook(ctx, market)
	if err != nil {
		return
	}
	var hedgeAsk domain.Price
	var hedgeAsset string
	if exp.hedgeToken == domain.TokenTypeUp {
		hedgeAsk = yesAsk
		hedgeAsset = market.YesAssetID
	} else {
		hedgeAsk = noAsk
		hedgeAsset = market.NoAssetID
	}

	// 取消旧单
	_ = s.TradingService.CancelOrder(ctx, exp.hedgeOrderID)

	newCents := hedgeAsk.ToCents()
	maxTotal := 100
	if s.Config.AllowNegativeProfitOnHedge && s.Config.MaxNegativeProfitCents > 0 {
		maxTotal = 100 + s.Config.MaxNegativeProfitCents
	}
	maxHedge := maxTotal - exp.entryPriceCents
	if maxHedge > 0 && newCents > maxHedge {
		newCents = maxHedge
	}
	if newCents <= 0 {
		return
	}

	newOrder := &domain.Order{
		MarketSlug:    market.Slug,
		AssetID:       hedgeAsset,
		Side:          types.SideBuy,
		Price:         domain.Price{Pips: newCents * 100},
		Size:          exp.entryFilledSize,
		TokenType:     exp.hedgeToken,
		IsEntryOrder:  false,
		OrderType:     types.OrderTypeFAK, // 激进：FAK
		CreatedAt:     time.Now(),
		BypassRiskOff: true,
	}
	created, err := s.TradingService.PlaceOrder(ctx, newOrder)
	if err != nil || created == nil || created.OrderID == "" {
		return
	}

	s.mu.Lock()
	exp.hedgeOrderID = created.OrderID
	exp.lastHedgeCents = newCents
	exp.lastAction = "aggressive_hedging"
	exp.lastActionAt = time.Now()
	s.mu.Unlock()
}

func (s *Strategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	_ = ctx
	if order == nil || order.OrderID == "" {
		return nil
	}

	// 清理敞口：hedge 成交
	if order.IsFilled() && !order.IsEntryOrder {
		s.mu.Lock()
		for entryID, exp := range s.exposures {
			if exp != nil && exp.hedgeOrderID == order.OrderID {
				delete(s.exposures, entryID)
				break
			}
		}
		s.mu.Unlock()

		// hedge 成交后尝试 autoMerge
		market := s.TradingService.GetCurrentMarketInfo()
		if market != nil {
			s.autoMerge.MaybeAutoMerge(context.Background(), s.TradingService, market, s.AutoMerge, log.Infof, s.onAutoMerge)
		}
	}
	return nil
}

func (s *Strategy) onAutoMerge(status string, amount float64, txHash string, err error) {
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	s.mergeStatus = status
	s.mergeAmount = amount
	if txHash != "" {
		s.mergeTxHash = txHash
		s.lastMergeTime = time.Now()
		s.mergeCount++
	}
	_ = err
}

func cycleDurationFromMarket(market *domain.Market) time.Duration {
	if market == nil || market.Slug == "" {
		return 15 * time.Minute
	}
	parts := strings.Split(market.Slug, "-")
	if len(parts) >= 3 {
		tf, err := marketspec.ParseTimeframe(parts[2])
		if err == nil {
			return tf.Duration()
		}
	}
	// hourly ET 格式：兜底按 1h
	slugLower := strings.ToLower(market.Slug)
	if strings.HasSuffix(slugLower, "-et") || strings.Contains(slugLower, "-et-") {
		return 1 * time.Hour
	}
	return 15 * time.Minute
}

func marketCycleEndTime(market *domain.Market) time.Time {
	if market == nil || market.Timestamp <= 0 {
		return time.Time{}
	}
	start := time.Unix(market.Timestamp, 0)
	return start.Add(cycleDurationFromMarket(market))
}

