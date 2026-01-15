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

	type candidate struct {
		primary  domain.TokenType
		sim      Snapshot
		upPad    float64
		downPad  float64
		gp       float64
		pairCost float64
		qPair    float64
	}

	now := pc.Now
	upFill := Fill{Qty: dq, Price: vwapUpEff, CostUSD: costUpEff, Time: now}
	downFill := Fill{Qty: dq, Price: vwapDownEff, CostUSD: costDownEff, Time: now}

	build := func(primary domain.TokenType) (*candidate, bool) {
		sim := pc.Base.Clone()
		if primary == domain.TokenTypeUp {
			sim.AddFill(domain.TokenTypeUp, upFill)
			sim.AddFill(domain.TokenTypeDown, downFill)
		} else {
			sim.AddFill(domain.TokenTypeDown, downFill)
			sim.AddFill(domain.TokenTypeUp, upFill)
		}
		if rr := shouldTradeSnapshot(cfg, sim); !rr.OK {
			return nil, false
		}
		// primaryPad 用于“更想成交的那条腿”
		upPad := applyPad(cfg.LimitPricePadCents, cfg.HedgePadCents)
		downPad := applyPad(cfg.LimitPricePadCents, cfg.HedgePadCents)
		if primary == domain.TokenTypeUp {
			upPad = applyPad(cfg.LimitPricePadCents, cfg.PrimaryPadCents)
		} else {
			downPad = applyPad(cfg.LimitPricePadCents, cfg.PrimaryPadCents)
		}
		return &candidate{
			primary:  primary,
			sim:      sim,
			upPad:    upPad,
			downPad:  downPad,
			gp:       sim.GuaranteedProfitUSD(cfg),
			pairCost: sim.PairCost(cfg),
			qPair:    sim.QPairValue(),
		}, true
	}

	var best *candidate
	if c, ok := build(domain.TokenTypeUp); ok {
		best = c
	}
	if c, ok := build(domain.TokenTypeDown); ok {
		if best == nil || c.gp > best.gp || (c.gp == best.gp && c.pairCost < best.pairCost) || (c.gp == best.gp && c.pairCost == best.pairCost && c.qPair > best.qPair) {
			best = c
		}
	}
	if best == nil {
		return Plan{Kind: PlanNone, Reason: "risk_blocked"}
	}

	upLimit := clampPrice(vwapUpEff + best.upPad)
	downLimit := clampPrice(vwapDownEff + best.downPad)
	if upLimit <= 0 || downLimit <= 0 {
		return Plan{Kind: PlanNone, Reason: "invalid_limit"}
	}

	ot := orderTypeFromConfig(cfg)
	upOrder := makeBuyOrder(pc.Market, pc.Market.YesAssetID, domain.TokenTypeUp, dq, upLimit, ot, false)
	downOrder := makeBuyOrder(pc.Market, pc.Market.NoAssetID, domain.TokenTypeDown, dq, downLimit, ot, false)

	return Plan{
		Kind:   PlanOrders,
		Reason: "paired",
		Orders: []domain.Order{upOrder, downOrder},
		Sim:    best.sim,
		Predicted: map[domain.TokenType]float64{
			domain.TokenTypeUp:   vwapUpEff,
			domain.TokenTypeDown: vwapDownEff,
		},
		PauseFor: 150 * time.Millisecond,
	}
}
