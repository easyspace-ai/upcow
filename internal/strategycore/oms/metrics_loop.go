package oms

import (
	"context"
	"time"
)

// metricsLoop 定期输出关键运行指标（职业交易系统必需的可观测性）。
// 设计为 Debug 级别，避免正常运行刷屏；需要时可提升日志级别。
func (o *OMS) metricsLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.logMetricsOnce()
		}
	}
}

func (o *OMS) logMetricsOnce() {
	if o == nil {
		return
	}

	market := ""
	if o.tradingService != nil {
		if m := o.tradingService.GetCurrentMarketInfo(); m != nil {
			market = m.Slug
		}
	}

	metrics := o.GetOpsMetrics(context.Background(), market)
	log.Debugf("📊 [OMS Metrics] market=%s queue=%d pending=%d exposures=%d hedgeEWMA=%.1fs reorderSkips=%d fakWarn=%d",
		market,
		metrics.QueueLen,
		metrics.PendingHedges,
		metrics.Exposures,
		metrics.HedgeEWMASec,
		metrics.ReorderBudgetSkips,
		metrics.FAKBudgetWarnings,
	)
}

