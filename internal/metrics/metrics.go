package metrics

import "expvar"

var (
	ReconcileRuns   = expvar.NewInt("reconcile_runs")
	ReconcileErrors = expvar.NewInt("reconcile_errors")
	SnapshotSaves   = expvar.NewInt("snapshot_saves")
	SnapshotLoads   = expvar.NewInt("snapshot_loads")
)

