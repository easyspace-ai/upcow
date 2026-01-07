package oms

import "context"

// OpsMetrics 提供给策略/dashboard 的运行指标快照。
type OpsMetrics struct {
	QueueLen int

	PendingHedges int
	Exposures     int

	HedgeEWMASec float64

	ReorderBudgetSkips int64
	FAKBudgetWarnings  int64

	CooldownRemainingSec float64
	CooldownReason       string
}

func (o *OMS) GetOpsMetrics(ctx context.Context, marketSlug string) OpsMetrics {
	_ = ctx
	if o == nil {
		return OpsMetrics{}
	}
	m := OpsMetrics{
		ReorderBudgetSkips: o.reorderBudgetSkips.Load(),
		FAKBudgetWarnings:  o.fakBudgetWarnings.Load(),
	}
	if o.q != nil && o.q.ch != nil {
		m.QueueLen = len(o.q.ch)
	}
	o.mu.RLock()
	m.PendingHedges = len(o.pendingHedges)
	o.mu.RUnlock()
	if o.riskManager != nil {
		m.Exposures = len(o.riskManager.GetExposures())
	}
	if o.hm != nil && marketSlug != "" {
		m.HedgeEWMASec = o.hm.getEWMASec(marketSlug)
	}
	if marketSlug != "" {
		if inCD, remaining, reason := o.IsMarketInCooldown(marketSlug); inCD {
			m.CooldownRemainingSec = remaining.Seconds()
			m.CooldownReason = reason
		}
	}
	return m
}

