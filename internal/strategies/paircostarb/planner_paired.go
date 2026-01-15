package paircostarb

import (
	"context"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

func planPaired(ctx context.Context, cfg Config, pc PlanContext, est BuyCostEstimator) Plan {
	dq := cfg.TradeChunkShares
	if dq <= 0 {
		return Plan{Kind: PlanNone, Reason: "invalid_chunk"}
	}
	if est == nil || pc.Market == nil {
		return Plan{Kind: PlanNone, Reason: "missing_estimator"}
	}

	vwapUpEff, costUpEff, okUp := est(ctx, pc.Market.YesAssetID, dq)
	vwapDownEff, costDownEff, okDown := est(ctx, pc.Market.NoAssetID, dq)
	if !okUp || !okDown {
		return Plan{Kind: PlanNone, Reason: "no_vwap"}
	}

	primary := preferredPrimary(pc.SigDir, pc.SigActive, vwapUpEff, vwapDownEff)
	sim := pc.Base.Clone()
	now := pc.Now
	upFill := Fill{Qty: dq, Price: vwapUpEff, CostUSD: costUpEff, Time: now}
	downFill := Fill{Qty: dq, Price: vwapDownEff, CostUSD: costDownEff, Time: now}
	if primary == domain.TokenTypeUp {
		sim.AddFill(domain.TokenTypeUp, upFill)
		sim.AddFill(domain.TokenTypeDown, downFill)
	} else {
		sim.AddFill(domain.TokenTypeDown, downFill)
		sim.AddFill(domain.TokenTypeUp, upFill)
	}

	if rr := shouldTradeSnapshot(cfg, sim); !rr.OK {
		return Plan{Kind: PlanNone, Reason: rr.Reason}
	}

	// build orders
	upPad := applyPad(cfg.LimitPricePadCents, cfg.HedgePadCents)
	downPad := applyPad(cfg.LimitPricePadCents, cfg.HedgePadCents)
	if primary == domain.TokenTypeUp {
		upPad = applyPad(cfg.LimitPricePadCents, cfg.PrimaryPadCents)
	} else {
		downPad = applyPad(cfg.LimitPricePadCents, cfg.PrimaryPadCents)
	}
	upLimit := clampPrice(vwapUpEff + upPad)
	downLimit := clampPrice(vwapDownEff + downPad)
	if upLimit <= 0 || downLimit <= 0 {
		return Plan{Kind: PlanNone, Reason: "invalid_limit"}
	}

	ot := orderTypeFromConfig(cfg)
	upOrder := makeBuyOrder(pc.Market, pc.Market.YesAssetID, domain.TokenTypeUp, dq, upLimit, ot, false)
	downOrder := makeBuyOrder(pc.Market, pc.Market.NoAssetID, domain.TokenTypeDown, dq, downLimit, ot, false)
	_ = primary

	return Plan{
		Kind:     PlanOrders,
		Reason:   "paired",
		Orders:   []domain.Order{upOrder, downOrder},
		Sim:      sim,
		PauseFor: 150 * time.Millisecond,
	}
}
