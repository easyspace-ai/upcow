package paircostarb

import (
	"context"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/execution"
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

		var created []*domain.Order
		// 专业化：paired 多腿优先走 ExecutionEngine 并发提交，减少腿间时差
		if len(plan.Orders) >= 2 && s.TradingService != nil && s.TradingService.ExecutionEngine() != nil {
			legs := make([]execution.LegIntent, 0, len(plan.Orders))
			for _, o := range plan.Orders {
				legs = append(legs, execution.LegIntent{
					Name:              string(o.TokenType),
					AssetID:           o.AssetID,
					TokenType:         o.TokenType,
					Side:              o.Side,
					Price:             o.Price,
					Size:              o.Size,
					OrderType:         o.OrderType,
					IsEntry:           o.IsEntryOrder,
					BypassRiskOff:     o.BypassRiskOff,
					DisableSizeAdjust: o.DisableSizeAdjust,
				})
			}
			req := execution.MultiLegRequest{
				Name:       "paircostarb",
				MarketSlug: plan.Orders[0].MarketSlug,
				Legs:       legs,
			}
			submitCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
			ticket, err := s.TradingService.ExecutionEngine().Submit(submitCtx, req)
			if err != nil {
				cancel()
				return
			}
			select {
			case res := <-ticket.ResultC:
				cancel()
				if res.Err != nil || len(res.Created) == 0 {
					return
				}
				created = res.Created
			case <-submitCtx.Done():
				cancel()
				return
			}
		} else {
			submitCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
			defer cancel()
			created2, err := s.orderExecutor.SubmitOrders(submitCtx, plan.Orders...)
			if err != nil || len(created2) == 0 {
				return
			}
			created = created2
		}

		s.st.mu.Lock()
		if s.st.rt.inFlightIDs == nil {
			s.st.rt.inFlightIDs = make(map[string]domain.TokenType, 4)
		}
		if s.st.rt.predictedByOrder == nil {
			s.st.rt.predictedByOrder = make(map[string]float64, 64)
		}
		for _, o := range created {
			if o == nil || o.OrderID == "" {
				continue
			}
			s.st.rt.inFlightIDs[o.OrderID] = o.TokenType
			if plan.Predicted != nil {
				if p, ok := plan.Predicted[o.TokenType]; ok && p > 0 {
					s.st.rt.predictedByOrder[o.OrderID] = p
				}
			}
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
