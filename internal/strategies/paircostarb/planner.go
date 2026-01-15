package paircostarb

import (
	"context"
	"math"
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

	// quality gate: if fill ratio is very low, skip (avoid trading in liquidity cliff)
	if cfg.EnableAdaptiveBuffers && cfg.MinFillRatio > 0 && pc.FillRatioEWMA > 0 && pc.FillRatioEWMA < cfg.MinFillRatio {
		return Plan{Kind: PlanStop, Reason: "low_fill_ratio", PauseFor: time.Duration(cfg.QualityCooldownSeconds) * time.Second}
	}

	// dynamic chunk scaling
	dq := cfg.TradeChunkShares
	if cfg.EnableDynamicChunk && dq > 0 {
		scale := 1.0
		if pc.FillRatioEWMA > 0 {
			// 成交率越低，越缩小 chunk
			scale = math.Max(cfg.DynamicChunkMinMultiplier, math.Min(1.0, pc.FillRatioEWMA))
		}
		if cfg.EnableAdaptiveBuffers && cfg.MaxAdaptiveSlippagePad > 0 && pc.SlipAbsMax > 0 {
			// 滑点误差越大，越缩小 chunk（线性衰减）
			slipScale := 1.0 - pc.SlipAbsMax/cfg.MaxAdaptiveSlippagePad
			if slipScale < cfg.DynamicChunkMinMultiplier {
				slipScale = cfg.DynamicChunkMinMultiplier
			}
			if slipScale > 1.0 {
				slipScale = 1.0
			}
			if slipScale < scale {
				scale = slipScale
			}
		}
		dq = dq * scale
		if cfg.MinTradeChunkShares > 0 && dq < cfg.MinTradeChunkShares {
			dq = cfg.MinTradeChunkShares
		}
		if cfg.MaxTradeChunkShares > 0 && dq > cfg.MaxTradeChunkShares {
			dq = cfg.MaxTradeChunkShares
		}
	}

	// pass effective chunk to sub-planners by overriding cfg copy
	cfg2 := cfg
	cfg2.TradeChunkShares = dq

	mode := cfg2.ExecutionMode
	if mode == "auto" {
		if pc.SigActive {
			mode = "paired"
		} else {
			mode = "single"
		}
	}

	if mode == "paired" {
		return planPaired(ctx, cfg2, pc, est)
	}
	return planSingle(ctx, cfg2, pc, est)
}
