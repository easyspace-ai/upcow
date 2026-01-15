package paircostarb

import (
	"math"
	"time"
)

// RiskResult 用于解释“为什么不交易”（便于后续精细化管理/观测）。
type RiskResult struct {
	OK     bool
	Reason string
}

func allowTickBasic(cfg Config, now time.Time, snap Snapshot, inFlight bool, tradesThisCycle int, inEndProtection bool, signalRequired bool, signalActive bool) RiskResult {
	if !cfg.Enabled {
		return RiskResult{OK: false, Reason: "disabled"}
	}
	if inFlight {
		return RiskResult{OK: false, Reason: "inflight"}
	}
	if cfg.MaxTradesPerCycle > 0 && tradesThisCycle >= cfg.MaxTradesPerCycle {
		return RiskResult{OK: false, Reason: "max_trades_per_cycle"}
	}
	if inEndProtection {
		return RiskResult{OK: false, Reason: "cycle_end_protection"}
	}
	if signalRequired && !signalActive {
		return RiskResult{OK: false, Reason: "signal_required"}
	}

	_ = snap
	return RiskResult{OK: true}
}

func shouldTradeSnapshot(cfg Config, snap Snapshot) RiskResult {
	// 未配对库存上限（绝对值）
	if cfg.MaxUnpairedShares > 0 {
		if math.Abs(snap.Qu-snap.Qd) > cfg.MaxUnpairedShares {
			return RiskResult{OK: false, Reason: "max_unpaired_exceeded"}
		}
	}

	imb := snap.Imbalance()
	if !isFinite(imb) || imb > cfg.MaxImbalance {
		return RiskResult{OK: false, Reason: "max_imbalance"}
	}

	pc := snap.PairCost(cfg)
	if !isFinite(pc) || pc > cfg.MaxPairCost {
		return RiskResult{OK: false, Reason: "max_pair_cost"}
	}

	return RiskResult{OK: true}
}
