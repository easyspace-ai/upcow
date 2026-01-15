package paircostarb

import (
	"context"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

// executePlan 只做“计划执行 + 状态更新”，不参与决策计算。
// 设计目标：让 planner 可纯函数化/更易测试。
func (s *Strategy) executePlan(ctx context.Context, plan Plan) {
	if plan.Kind == PlanNone {
		return
	}

	switch plan.Kind {
	case PlanStop:
		// stop: 设置冷却，并按配置触发 autoMerge
		s.st.mu.Lock()
		if plan.PauseFor > 0 {
			s.st.rt.pausedUntil = time.Now().Add(plan.PauseFor)
		}
		mkt := s.st.rt.market
		cfg := s.Config.AutoMerge
		ctl := &s.st.rt.autoMergeCtl
		s.st.mu.Unlock()

		if cfg.Enabled && s.TradingService != nil && mkt != nil {
			ctl.MaybeAutoMerge(
				context.Background(),
				s.TradingService,
				mkt,
				cfg,
				func(format string, args ...any) { s.log.Infof(format, args...) },
				nil,
			)
		}
		return

	case PlanOrders:
		if len(plan.Orders) == 0 {
			return
		}
		if s.Config.DecisionOnly {
			s.log.Infof("🧪 decisionOnly：plan=%s orders=%d pairCost'=%.4f imb'=%.3f qPair'=%.2f gp'=%.4f",
				plan.Reason, len(plan.Orders), plan.Sim.PairCost(s.Config), plan.Sim.Imbalance(), plan.Sim.QPairValue(), plan.Sim.GuaranteedProfitUSD(s.Config))
			s.st.mu.Lock()
			s.st.rt.tradesThisCycle += len(plan.Orders)
			if plan.PauseFor > 0 {
				s.st.rt.pausedUntil = time.Now().Add(plan.PauseFor)
			}
			s.st.mu.Unlock()
			return
		}

		if s.orderExecutor == nil {
			return
		}

		submitCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()

		created, err := s.orderExecutor.SubmitOrders(submitCtx, plan.Orders...)
		if err != nil || len(created) == 0 {
			return
		}

		s.st.mu.Lock()
		if s.st.rt.inFlightIDs == nil {
			s.st.rt.inFlightIDs = make(map[string]domain.TokenType, 4)
		}
		for _, o := range created {
			if o == nil || o.OrderID == "" {
				continue
			}
			s.st.rt.inFlightIDs[o.OrderID] = o.TokenType
		}
		s.st.rt.tradesThisCycle += len(created)
		if plan.PauseFor > 0 {
			s.st.rt.pausedUntil = time.Now().Add(plan.PauseFor)
		}
		s.st.mu.Unlock()

		s.log.Infof("✅ 执行计划=%s orders=%d pairCost'=%.4f imb'=%.3f qPair'=%.2f gp'=%.4f",
			plan.Reason, len(plan.Orders), plan.Sim.PairCost(s.Config), plan.Sim.Imbalance(), plan.Sim.QPairValue(), plan.Sim.GuaranteedProfitUSD(s.Config))
		return

	default:
		return
	}
}
