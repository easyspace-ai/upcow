package paircostarb

import (
	"context"
	"math"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

func planSingle(ctx context.Context, cfg Config, pc PlanContext, est BuyCostEstimator) Plan {
	dq := cfg.TradeChunkShares
	if dq <= 0 {
		return Plan{Kind: PlanNone, Reason: "invalid_chunk"}
	}
	if est == nil || pc.Market == nil {
		return Plan{Kind: PlanNone, Reason: "missing_estimator"}
	}

	type singlePlan struct {
		side     domain.TokenType
		assetID  string
		vwapEff  float64
		costEff  float64
		limit    float64
		sim      Snapshot
		pairCost float64
	}

	try := func(side domain.TokenType, assetID string, vwapEff float64, costEff float64) (*singlePlan, bool) {
		if vwapEff <= 0 || costEff <= 0 {
			return nil, false
		}
		sim := pc.Base.Clone()
		sim.AddFill(side, Fill{Qty: dq, Price: vwapEff, CostUSD: costEff, Time: pc.Now})

		// first-leg rule
		if (side == domain.TokenTypeUp && pc.Base.Qd <= 0) || (side == domain.TokenTypeDown && pc.Base.Qu <= 0) {
			if vwapEff > cfg.FirstLegMaxPrice {
				return nil, false
			}
			if math.Abs(sim.Qu-sim.Qd) > cfg.MaxUnpairedShares {
				return nil, false
			}
		} else {
			if rr := shouldTradeSnapshot(cfg, sim); !rr.OK {
				return nil, false
			}
		}

		pcst := sim.PairCost(cfg)
		limit := clampPrice(vwapEff + applyPad(cfg.LimitPricePadCents, 0))
		return &singlePlan{
			side:     side,
			assetID:  assetID,
			vwapEff:  vwapEff,
			costEff:  costEff,
			limit:    limit,
			sim:      sim,
			pairCost: pcst,
		}, true
	}

	best := (*singlePlan)(nil)

	if v, c, ok := est(ctx, pc.Market.YesAssetID, dq); ok {
		if p, ok2 := try(domain.TokenTypeUp, pc.Market.YesAssetID, v, c); ok2 {
			best = p
		}
	}
	if v, c, ok := est(ctx, pc.Market.NoAssetID, dq); ok {
		if p, ok2 := try(domain.TokenTypeDown, pc.Market.NoAssetID, v, c); ok2 {
			if best == nil || p.pairCost < best.pairCost || (!isFinite(best.pairCost) && isFinite(p.pairCost)) {
				best = p
			}
		}
	}

	if best == nil || best.limit <= 0 {
		return Plan{Kind: PlanNone, Reason: "no_candidate"}
	}

	ot := orderTypeFromConfig(cfg)
	order := makeBuyOrder(pc.Market, best.assetID, best.side, dq, best.limit, ot, false)
	return Plan{
		Kind:   PlanOrders,
		Reason: "single",
		Orders: []domain.Order{order},
		Sim:    best.sim,
		Predicted: map[domain.TokenType]float64{
			best.side: best.vwapEff,
		},
		PauseFor: 150 * time.Millisecond,
	}
}
