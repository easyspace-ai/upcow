package metrics

import "expvar"

var (
	ReconcileRuns   = expvar.NewInt("reconcile_runs")
	ReconcileErrors = expvar.NewInt("reconcile_errors")
	SnapshotSaves   = expvar.NewInt("snapshot_saves")
	SnapshotLoads   = expvar.NewInt("snapshot_loads")

	// 交易执行（TradingService / OrderEngine 入口）
	PlaceOrderRuns                = expvar.NewInt("place_order_runs")
	PlaceOrderErrors              = expvar.NewInt("place_order_errors")
	PlaceOrderBlockedDedup        = expvar.NewInt("place_order_blocked_dedup")
	PlaceOrderBlockedCircuit      = expvar.NewInt("place_order_blocked_circuit_breaker")
	PlaceOrderLatencyLastMs       = expvar.NewInt("place_order_latency_last_ms")
	PlaceOrderLatencyMaxMs        = expvar.NewInt("place_order_latency_max_ms")
	PlaceOrderLatencyTotalMs      = expvar.NewInt("place_order_latency_total_ms")
	PlaceOrderLatencySamples      = expvar.NewInt("place_order_latency_samples")
	PlaceOrderBlockedInvalidInput = expvar.NewInt("place_order_blocked_invalid_input")
)

