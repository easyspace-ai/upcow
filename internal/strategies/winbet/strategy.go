package winbet

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/services"
	wbbrain "github.com/betbot/gobet/internal/strategies/winbet/brain"
	wbcapital "github.com/betbot/gobet/internal/strategies/winbet/capital"
	wbdash "github.com/betbot/gobet/internal/strategies/winbet/dashboard"
	"github.com/betbot/gobet/internal/strategies/winbet/gates"
	wboms "github.com/betbot/gobet/internal/strategies/winbet/oms"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/marketspec"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy WinBet（对齐 velocityfollow，去掉“卖/出场”）
// - brain: 速度采样、快慢速策略、套利分析
// - oms: 下单执行、风险管理、对冲重下
// - capital: merge/redeem（非卖出）
// - dashboard: 复用 velocityfollow dashboard（已修复 UI 同步/退出/闪烁核心问题）
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	mu sync.RWMutex
	// 避免在周期切换/重复 Subscribe 时重复注册 handler
	orderUpdateOnce sync.Once

	brain   *wbbrain.Brain
	oms     *wboms.OMS
	capital *wbcapital.Capital
	dash    *wbdash.Dashboard

	gates *gates.Gates

	// dashboard loop（独立 ctx，不受周期切换影响）
	dashboardCtx      context.Context
	dashboardCancel   context.CancelFunc
	dashboardLoopOnce sync.Once

	// Dashboard 退出信号（UI 主动退出）
	dashboardExitCtx    context.Context
	dashboardExitCancel context.CancelFunc

	// 周期状态（用于 dashboard 的 cooldown/warmup 计算展示）
	cycleStartTime  time.Time
	lastTriggerTime time.Time
	tradesThisCycle int
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }

func (s *Strategy) Defaults() error { return s.Config.Defaults() }
func (s *Strategy) Validate() error { return s.Config.Validate() }

func (s *Strategy) Initialize() error {
	if s.TradingService == nil {
		return nil
	}

	var err error
	s.brain, err = wbbrain.New(s.TradingService, &s.Config)
	if err != nil {
		return err
	}
	s.oms, err = wboms.New(s.TradingService, &s.Config)
	if err != nil {
		return err
	}
	s.capital, err = wbcapital.New(s.TradingService, &s.Config)
	if err != nil {
		return err
	}
	if s.oms != nil && s.capital != nil {
		s.oms.SetCapital(s.capital)
	}

	// Dashboard
	if s.Config.DashboardEnabled {
		s.dash = wbdash.New(s.TradingService, s.Config.DashboardUseNativeTUI)
		s.dash.SetTitle("WinBet Strategy Dashboard")
		s.dash.SetEnabled(true)
		s.dash.ReapplyLogRedirect()
		s.dashboardCtx, s.dashboardCancel = context.WithCancel(context.Background())
		s.dashboardExitCtx, s.dashboardExitCancel = context.WithCancel(context.Background())
	}

	// Gate（市场质量/稳定性）
	s.gates = gates.New(&s.Config)

	// 注册订单回调（给 OMS 用）
	s.orderUpdateOnce.Do(func() {
		s.TradingService.OnOrderUpdate(services.OrderUpdateHandlerFunc(s.OnOrderUpdate))
	})

	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	// 兜底：注入顺序下 TradingService 可能晚于 Initialize
	if s.TradingService != nil {
		s.orderUpdateOnce.Do(func() {
			s.TradingService.OnOrderUpdate(services.OrderUpdateHandlerFunc(s.OnOrderUpdate))
		})
	}
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	// 启动子模块
	if s.brain != nil {
		s.brain.Start(ctx)
	}
	if s.oms != nil {
		s.oms.Start(ctx)
	}

	// 启动 Dashboard（若启用）
	if s.Config.DashboardEnabled && s.dash != nil {
		s.dash.SetExitCallback(func() {
			if s.dashboardExitCancel != nil {
				s.dashboardExitCancel()
			}
		})
		// 关键：Dashboard 用独立 ctx 启动，避免“周期切换触发 ctx cancel”导致 UI 停更。
		// 周期切换时 bbgo 会 cancel 当前 Run(ctx)，但 Strategy 实例仍会被复用并再次 Run。
		// 若 Dashboard 随 Run(ctx) 停止，而 dashboardUpdateLoop 又是 once，则会出现“新周期 UI 不再更新”的现象。
		startCtx := ctx
		if s.dashboardCtx != nil {
			startCtx = s.dashboardCtx
		}
		_ = s.dash.Start(startCtx)
		s.dashboardLoopOnce.Do(func() {
			if s.dashboardCtx != nil {
				go s.dashboardUpdateLoop(s.dashboardCtx)
			}
		})
	}

	// 等待 root ctx 或 UI 退出
	if s.dashboardExitCtx == nil {
		<-ctx.Done()
	} else {
		select {
		case <-ctx.Done():
		case <-s.dashboardExitCtx.Done():
			// 明确返回错误，便于上层识别“用户退出”
			return fmt.Errorf("Dashboard 已退出（用户退出 UI）")
		}
	}

	// 停止
	if s.brain != nil {
		s.brain.Stop()
	}
	if s.oms != nil {
		s.oms.Stop()
	}
	// 注意：不要在 Run 结束时停止 Dashboard 或 cancel dashboardCtx。
	// Run 会在周期切换时被 cancel 并重新启动；Dashboard 需要跨周期持续运行。

	return ctx.Err()
}

// Shutdown 实现 bbgo.StrategyShutdown（统一清理）
func (s *Strategy) Shutdown(ctx context.Context, wg *sync.WaitGroup) {
	_ = ctx
	_ = wg
	if s.brain != nil {
		s.brain.Stop()
	}
	if s.oms != nil {
		s.oms.Stop()
	}
	if s.dash != nil {
		s.dash.Stop()
	}
	if s.dashboardCancel != nil {
		s.dashboardCancel()
	}
}

// OnCycle 周期切换回调（由框架调用）
func (s *Strategy) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	// 重置周期状态
	s.mu.Lock()
	s.cycleStartTime = time.Now()
	s.lastTriggerTime = time.Time{}
	s.tradesThisCycle = 0
	s.mu.Unlock()

	// 通知子模块
	if s.brain != nil {
		s.brain.OnCycle(ctx, oldMarket, newMarket)
	}
	if s.oms != nil {
		s.oms.OnCycle(ctx, oldMarket, newMarket)
	}
	if s.capital != nil {
		// 与 velocityfollow 一致：尝试把旧周期持仓提前传入（如果能取到）
		var oldPositions []*domain.Position
		if oldMarket != nil && s.TradingService != nil {
			oldPositions = s.TradingService.GetOpenPositionsForMarket(oldMarket.Slug)
		}
		if oldMarket != nil && len(oldPositions) > 0 {
			s.capital.OnCycleWithPositions(ctx, oldMarket, newMarket, oldPositions)
		} else {
			s.capital.OnCycle(ctx, oldMarket, newMarket)
		}
	}

	// Dashboard：周期切换立即清屏并刷新（解决不同步）
	if s.dash != nil && s.Config.DashboardEnabled && newMarket != nil {
		s.dash.ReapplyLogRedirect()
		s.dash.ResetSnapshot(newMarket)
		s.dash.SendUpdate()
	}

	if s.gates != nil && newMarket != nil {
		s.gates.OnCycle(newMarket)
	}
}

// OnOrderUpdate 订单更新回调：转发给 OMS
func (s *Strategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	if s.oms != nil {
		_ = s.oms.OnOrderUpdate(ctx, order)
	}
	return nil
}

// OnPriceChanged 价格事件：更新样本 -> 决策 -> 执行（不包含“卖/出场”逻辑）
func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}

	// 关键：无论是否交易，都更新样本（供速度/看板/套利分析）
	if s.brain != nil {
		s.brain.UpdateSamplesFromPriceEvent(ctx, e)
	}

	// 风控优先：把 WS 价格变化实时转发给 OMS（用于 event-driven 止损/锁损），不受 gate 影响。
	if s.oms != nil {
		_ = s.oms.OnPriceChanged(ctx, e)
	}

	// 市场质量/稳定性 gate（职业交易员视角：先保证“盘口可交易”再谈信号）
	if s.gates != nil {
		ok, _ := s.gates.AllowTrade(ctx, s.TradingService, e.Market)
		if !ok {
			return nil
		}
	}

	// 周期/冷却/次数 gate（与 velocityfollow 口径对齐）
	now := time.Now()
	s.mu.Lock()
	// warmup
	if !s.cycleStartTime.IsZero() && now.Sub(s.cycleStartTime) < time.Duration(s.Config.WarmupMs)*time.Millisecond {
		s.mu.Unlock()
		return nil
	}
	// cooldown
	if !s.lastTriggerTime.IsZero() && now.Sub(s.lastTriggerTime) < time.Duration(s.Config.CooldownMs)*time.Millisecond {
		s.mu.Unlock()
		return nil
	}
	// trades limit
	if s.Config.MaxTradesPerCycle > 0 && s.tradesThisCycle >= s.Config.MaxTradesPerCycle {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	// 周期末保护：最后 N 分钟不再开新仓（更像职业交易系统：避免周期末无法完成对冲/结算异常）
	if s.Config.CycleEndProtectionMinutes > 0 {
		endAt := marketCycleEndTime(e.Market)
		if !endAt.IsZero() && time.Until(endAt) <= time.Duration(s.Config.CycleEndProtectionMinutes)*time.Minute {
			return nil
		}
	}

	// 未对冲风险 gate（与 velocityfollow 一致）
	if s.oms != nil {
		hasRisk, err := s.oms.HasUnhedgedRisk(e.Market.Slug)
		if err == nil && hasRisk {
			return nil
		}
	}

	// per-entry 预算触发的 market 冷静期：禁止新开仓，只允许风控/对冲流程继续跑
	if s.oms != nil {
		if inCD, _, _ := s.oms.IsMarketInCooldown(e.Market.Slug); inCD {
			return nil
		}
	}

	// 库存偏斜阈值：偏斜过大时禁止继续加仓（只允许风控/对冲流程去修复）
	if s.Config.InventoryThreshold > 0 && s.brain != nil {
		s.brain.UpdatePositionState(ctx, e.Market)
		ps := s.brain.GetPositionState(e.Market.Slug)
		if ps != nil {
			diff := math.Abs(ps.UpSize - ps.DownSize)
			if diff > s.Config.InventoryThreshold {
				return nil
			}
		}
	}

	// 决策
	if s.brain == nil {
		return nil
	}
	decision, err := s.brain.MakeDecision(ctx, e)
	if err != nil || decision == nil || !decision.ShouldTrade {
		return nil
	}

	// 动态下单量（只降不升）：根据市场质量/价差缩放，避免薄盘口重仓导致对冲失败与滑点放大
	decision.EntrySize, decision.HedgeSize = s.dynamicSizeForMarket(ctx, e.Market, decision.EntrySize, decision.HedgeSize)
	if decision.EntrySize <= 0 || decision.HedgeSize <= 0 {
		log.WithFields(logrus.Fields{
			"market": e.Market.Slug,
			"token":  e.TokenType,
			"dir":    decision.Direction,
			"reason": "dynamic_size_zero",
		}).Info("winbet: skip trade after dynamic sizing (size<=0)")
		return nil
	}

	// 执行
	if s.oms == nil {
		return nil
	}
	log.WithFields(logrus.Fields{
		"market":    e.Market.Slug,
		"token":     e.TokenType,
		"dir":       decision.Direction,
		"entrySize": decision.EntrySize,
		"hedgeSize": decision.HedgeSize,
	}).Info("winbet: decision ready, executing order")

	if err := s.oms.ExecuteOrder(ctx, e.Market, decision); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"market":    e.Market.Slug,
			"token":     e.TokenType,
			"dir":       decision.Direction,
			"entrySize": decision.EntrySize,
			"hedgeSize": decision.HedgeSize,
		}).Warn("winbet: ExecuteOrder failed")
		return nil
	}

	s.mu.Lock()
	s.lastTriggerTime = now
	s.tradesThisCycle++
	s.mu.Unlock()

	return nil
}

// dynamicSizeForMarket 根据市场质量保守缩放下单量（只减少，不增加）。
func (s *Strategy) dynamicSizeForMarket(ctx context.Context, market *domain.Market, entrySize, hedgeSize float64) (float64, float64) {
	if s == nil || s.TradingService == nil || market == nil {
		return entrySize, hedgeSize
	}
	if entrySize <= 0 || hedgeSize <= 0 {
		return entrySize, hedgeSize
	}

	// 计算最小基准（确保对冲对等）
	base := math.Min(entrySize, hedgeSize)

	// 取一次 market quality（短超时，失败就不缩放）
	mqCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()

	opt := services.MarketQualityOptions{
		MaxBookAge:     time.Duration(s.Config.MarketQualityMaxBookAgeMs) * time.Millisecond,
		MaxSpreadPips:  s.Config.MarketQualityMaxSpreadCents * 100,
		PreferWS:       true,
		FallbackToREST: true,
		AllowPartialWS: true,
	}
	mq, err := s.TradingService.GetMarketQuality(mqCtx, market, &opt)
	if err != nil || mq == nil {
		return base, base
	}

	factor := 1.0

	// score：越接近门槛越保守（只降不升）
	minScore := s.Config.MarketQualityMinScore
	if minScore < 0 {
		minScore = 0
	}
	if minScore > 100 {
		minScore = 100
	}
	if float64(mq.Score) < 100 && float64(mq.Score) >= minScore {
		span := 100.0 - minScore
		if span > 0 {
			rel := (float64(mq.Score) - minScore) / span // 0..1
			// factor in [0.5..1.0]
			factor *= 0.5 + 0.5*rel
		}
	}

	// spread：接近上限时进一步降仓
	maxSpread := float64(s.Config.MarketQualityMaxSpreadCents)
	spreadC := float64(max(mq.YesSpreadPips, mq.NoSpreadPips)) / 100.0
	if maxSpread > 0 && spreadC > 0 {
		if spreadC >= 0.75*maxSpread {
			factor *= 0.7
		} else if spreadC >= 0.5*maxSpread {
			factor *= 0.85
		}
	}

	// 数据不完整/不新鲜：更保守
	if !mq.Complete || !mq.Fresh {
		factor *= 0.7
	}

	if factor < 0.2 {
		factor = 0.2
	}
	if factor > 1.0 {
		factor = 1.0
	}

	newSize := base * factor
	// 轻量“整形”：保留一位小数，避免过细碎下单
	newSize = math.Floor(newSize*10.0) / 10.0
	if newSize <= 0 {
		return 0, 0
	}
	// 不超过基准
	if newSize > base {
		newSize = base
	}
	return newSize, newSize
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (s *Strategy) dashboardUpdateLoop(ctx context.Context) {
	refreshTicker := time.NewTicker(time.Duration(s.Config.DashboardRefreshIntervalMs) * time.Millisecond)
	defer refreshTicker.Stop()

	reconcileTicker := time.NewTicker(time.Duration(s.Config.DashboardPositionReconcileIntervalSeconds) * time.Second)
	defer reconcileTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-refreshTicker.C:
			s.updateDashboard(ctx, nil)
		case <-reconcileTicker.C:
			s.updateDashboard(ctx, nil)
		}
	}
}

func (s *Strategy) updateDashboard(ctx context.Context, market *domain.Market) {
	if s.dash == nil || s.TradingService == nil {
		return
	}

	// market 允许传入（周期切换时）；否则从 TradingService 取当前
	if market == nil {
		market = s.TradingService.GetCurrentMarketInfo()
		if market == nil || market.Slug == "" {
			return
		}
	}

	// 价格信息
	yesBid, yesAsk, noBid, noAsk, _, _ := s.TradingService.GetTopOfBook(ctx, market)
	yesBidF := yesBid.ToDecimal()
	yesAskF := yesAsk.ToDecimal()
	noBidF := noBid.ToDecimal()
	noAskF := noAsk.ToDecimal()

	// 速度信息
	var upVel, downVel float64
	var upMove, downMove int
	var dir string
	if s.brain != nil {
		vi := s.brain.GetVelocityInfo(ctx, market)
		if vi != nil {
			upVel, downVel, upMove, downMove, dir = vi.UpVelocity, vi.DownVelocity, vi.UpMove, vi.DownMove, vi.Direction
		}
	}

	// 持仓信息
	var posState *wbdash.PositionState
	if s.brain != nil {
		s.brain.UpdatePositionState(ctx, market)
		ps := s.brain.GetPositionState(market.Slug)
		if ps != nil {
			posState = &wbdash.PositionState{
				UpSize:       ps.UpSize,
				DownSize:     ps.DownSize,
				UpCost:       ps.UpCost,
				DownCost:     ps.DownCost,
				UpAvgPrice:   ps.UpAvgPrice,
				DownAvgPrice: ps.DownAvgPrice,
				IsHedged:     ps.IsHedged,
			}
		}
	}

	// 盈利（锁利）
	var totalCost, profitUp, profitDown float64
	var locked bool
	if posState != nil {
		totalCost = posState.UpCost + posState.DownCost
		profitUp = posState.UpSize*1.0 - posState.UpCost - posState.DownCost
		profitDown = posState.DownSize*1.0 - posState.UpCost - posState.DownCost
		locked = profitUp > 0 && profitDown > 0
	}

	// 交易统计
	s.mu.RLock()
	trades := s.tradesThisCycle
	last := s.lastTriggerTime
	cycleStart := s.cycleStartTime
	s.mu.RUnlock()

	// pending hedges / open orders
	pendingHedges := 0
	if s.oms != nil {
		pendingHedges = len(s.oms.GetPendingHedges())
	}
	openOrders := len(s.TradingService.GetActiveOrders())

	// 风控状态（RiskManager/HedgeReorder）
	var rm *wbdash.RiskManagementStatus
	if s.oms != nil {
		if st := s.oms.GetRiskManagementStatus(); st != nil {
			// 转换（字段名一致）
			riskExposures := make([]wbdash.RiskExposureInfo, 0, len(st.RiskExposures))
			for _, exp := range st.RiskExposures {
				riskExposures = append(riskExposures, wbdash.RiskExposureInfo{
					EntryOrderID:            exp.EntryOrderID,
					EntryTokenType:          exp.EntryTokenType,
					EntrySize:               exp.EntrySize,
					EntryPriceCents:         exp.EntryPriceCents,
					HedgeOrderID:            exp.HedgeOrderID,
					HedgeStatus:             exp.HedgeStatus,
					ExposureSeconds:         exp.ExposureSeconds,
					MaxLossCents:            exp.MaxLossCents,
					OriginalHedgePriceCents: exp.OriginalHedgePriceCents,
					NewHedgePriceCents:      exp.NewHedgePriceCents,
					CountdownSeconds:        exp.CountdownSeconds,
				})
			}
			rm = &wbdash.RiskManagementStatus{
				RiskExposuresCount:      st.RiskExposuresCount,
				RiskExposures:           riskExposures,
				CurrentAction:           st.CurrentAction,
				CurrentActionEntry:      st.CurrentActionEntry,
				CurrentActionHedge:      st.CurrentActionHedge,
				CurrentActionTime:       st.CurrentActionTime,
				CurrentActionDesc:       st.CurrentActionDesc,
				TotalReorders:           st.TotalReorders,
				TotalAggressiveHedges:   st.TotalAggressiveHedges,
				TotalFakEats:            st.TotalFakEats,
				RepriceOldPriceCents:    st.RepriceOldPriceCents,
				RepriceNewPriceCents:    st.RepriceNewPriceCents,
				RepricePriceChangeCents: st.RepricePriceChangeCents,
				RepriceStrategy:         st.RepriceStrategy,
				RepriceEntryCostCents:   st.RepriceEntryCostCents,
				RepriceMarketAskCents:   st.RepriceMarketAskCents,
				RepriceIdealPriceCents:  st.RepriceIdealPriceCents,
				RepriceTotalCostCents:   st.RepriceTotalCostCents,
				RepriceProfitCents:      st.RepriceProfitCents,
			}
		}
	}

	// 决策条件（用于左下角）
	var dc *wbdash.DecisionConditions
	if s.brain != nil {
		cooldownRemaining := 0.0
		if !last.IsZero() {
			cd := time.Duration(s.Config.CooldownMs) * time.Millisecond
			if since := time.Since(last); since < cd {
				cooldownRemaining = (cd - since).Seconds()
			}
		}
		warmupRemaining := 0.0
		if !cycleStart.IsZero() {
			wu := time.Duration(s.Config.WarmupMs) * time.Millisecond
			if since := time.Since(cycleStart); since < wu {
				warmupRemaining = (wu - since).Seconds()
			}
		}
		info := &wbbrain.StrategyInfo{
			CooldownRemaining: cooldownRemaining,
			WarmupRemaining:   warmupRemaining,
			TradesThisCycle:   trades,
			HasPendingHedge:   pendingHedges > 0,
		}
		// 用当前 UP 价格构造一个 event 仅用于展示条件（与 velocityfollow 一致）
		priceEvent := &events.PriceChangedEvent{
			Market:    market,
			TokenType: domain.TokenTypeUp,
			NewPrice:  domain.PriceFromDecimal((yesBidF + yesAskF) / 2),
		}
		raw := s.brain.GetDecisionConditions(ctx, priceEvent, info)
		if raw != nil {
			dc = &wbdash.DecisionConditions{
				UpVelocityOK:      raw.UpVelocityOK,
				UpVelocityValue:   raw.UpVelocityValue,
				UpMoveOK:          raw.UpMoveOK,
				UpMoveValue:       raw.UpMoveValue,
				DownVelocityOK:    raw.DownVelocityOK,
				DownVelocityValue: raw.DownVelocityValue,
				DownMoveOK:        raw.DownMoveOK,
				DownMoveValue:     raw.DownMoveValue,
				Direction:         raw.Direction,
				EntryPriceOK:      raw.EntryPriceOK,
				EntryPriceValue:   raw.EntryPriceValue,
				EntryPriceMin:     raw.EntryPriceMin,
				EntryPriceMax:     raw.EntryPriceMax,
				TotalCostOK:       raw.TotalCostOK,
				TotalCostValue:    raw.TotalCostValue,
				HedgePriceOK:      raw.HedgePriceOK,
				HedgePriceValue:   raw.HedgePriceValue,
				HasUnhedgedRisk:   raw.HasUnhedgedRisk,
				IsProfitLocked:    raw.IsProfitLocked,
				ProfitIfUpWin:     raw.ProfitIfUpWin,
				ProfitIfDownWin:   raw.ProfitIfDownWin,
				CooldownOK:        raw.CooldownOK,
				CooldownRemaining: raw.CooldownRemaining,
				WarmupOK:          raw.WarmupOK,
				WarmupRemaining:   raw.WarmupRemaining,
				TradesLimitOK:     raw.TradesLimitOK,
				TradesThisCycle:   raw.TradesThisCycle,
				MaxTradesPerCycle: raw.MaxTradesPerCycle,
				MarketValid:       raw.MarketValid,
				HasPendingHedge:   raw.HasPendingHedge,
				CanTrade:          raw.CanTrade,
				BlockReason:       raw.BlockReason,
			}
		}
	}

	// Gate 状态：复用最近一次 AllowTrade 结论，避免在 dashboard 中重复跑风控逻辑
	gateAllowed := true
	gateReason := ""
	if s.gates != nil {
		if allowed, reason, ok := s.gates.GetLastDecision(market.Slug); ok {
			gateAllowed = allowed
			gateReason = reason
		}
	}

	// merge 状态
	mergeCount := 0
	mergeStatus := ""
	mergeAmount := 0.0
	mergeTx := ""
	var lastMerge time.Time
	if s.capital != nil {
		mergeCount = s.capital.GetMergeCount()
		mergeStatus, mergeAmount, mergeTx, lastMerge = s.capital.GetMergeStatus()
	}

	ops := wboms.OpsMetrics{}
	if s.oms != nil {
		ops = s.oms.GetOpsMetrics(ctx, market.Slug)
	}

	update := &wbdash.UpdateData{
		YesPrice: (yesBidF + yesAskF) / 2,
		NoPrice:  (noBidF + noAskF) / 2,
		YesBid:   yesBidF,
		YesAsk:   yesAskF,
		NoBid:    noBidF,
		NoAsk:    noAskF,

		UpVelocity:   upVel,
		DownVelocity: downVel,
		UpMove:       upMove,
		DownMove:     downMove,
		Direction:    dir,

		PositionState:   posState,
		ProfitIfUpWin:   profitUp,
		ProfitIfDownWin: profitDown,
		TotalCost:       totalCost,
		IsProfitLocked:  locked,

		TradesThisCycle: trades,
		LastTriggerTime: last,

		PendingHedges:              pendingHedges,
		OpenOrders:                 openOrders,
		OMSQueueLen:                ops.QueueLen,
		HedgeEWMASec:               ops.HedgeEWMASec,
		ReorderBudgetSkips:         ops.ReorderBudgetSkips,
		FAKBudgetWarnings:          ops.FAKBudgetWarnings,
		MarketCooldownRemainingSec: ops.CooldownRemainingSec,
		MarketCooldownReason:       ops.CooldownReason,

		RiskManagement:     rm,
		DecisionConditions: dc,

		GateAllowed: gateAllowed,
		GateReason:  gateReason,

		MergeCount:    mergeCount,
		MergeStatus:   mergeStatus,
		MergeAmount:   mergeAmount,
		MergeTxHash:   mergeTx,
		LastMergeTime: lastMerge,

		// 让 UI 自己基于 CycleEndTime 实时倒计时
		CycleEndTime: marketCycleEndTime(market),
	}

	s.dash.UpdateSnapshot(ctx, market, update)
	s.dash.Render()
}

func marketCycleEndTime(market *domain.Market) time.Time {
	if market == nil || market.Timestamp <= 0 {
		return time.Time{}
	}
	start := time.Unix(market.Timestamp, 0)
	// 与 velocityfollow 同口径：从 slug 解析 timeframe，失败默认 15m
	// 这里复用 marketspec 的解析策略
	dur := 15 * time.Minute
	parts := strings.Split(market.Slug, "-")
	if len(parts) >= 3 {
		if tf, err := marketspec.ParseTimeframe(parts[2]); err == nil {
			dur = tf.Duration()
		}
	}
	return start.Add(dur)
}
