package grid

import "time"

func (p *HedgePlan) retryDue(now time.Time) bool {
	return p.State == PlanRetryWait && !p.NextRetryAt.IsZero() && now.After(p.NextRetryAt)
}
