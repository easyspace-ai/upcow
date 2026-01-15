package paircostarb

import (
	"context"
	"time"
)

func PlanNextAction(ctx context.Context, cfg Config, pc PlanContext, est BuyCostEstimator) Plan {
	if !cfg.Enabled {
		return Plan{Kind: PlanNone, Reason: "disabled"}
	}
	if pc.Market == nil || !pc.Market.IsValid() {
		return Plan{Kind: PlanNone, Reason: "invalid_market"}
	}
	if rr := allowTickBasic(cfg, pc.Now, pc.Base, pc.InFlight, pc.TradesThisCycle, pc.InEndProtection, cfg.RequireBinanceSignal, pc.SigActive); !rr.OK {
		return Plan{Kind: PlanNone, Reason: rr.Reason}
	}

	// top-of-book quick filter: best asks already exceed maxPairCost -> skip
	if pc.YesAsk > 0 && pc.NoAsk > 0 && pc.YesAsk+pc.NoAsk > cfg.MaxPairCost {
		return Plan{Kind: PlanNone, Reason: "top_book_cost_too_high"}
	}

	// stop condition
	if gp := pc.Base.GuaranteedProfitUSD(cfg); gp >= cfg.MinProfitUSD {
		return Plan{Kind: PlanStop, Reason: "min_profit_reached", PauseFor: time.Duration(cfg.CooldownAfterStopSeconds) * time.Second}
	}

	if cfg.ExecutionMode == "paired" {
		return planPaired(ctx, cfg, pc, est)
	}
	return planSingle(ctx, cfg, pc, est)
}
