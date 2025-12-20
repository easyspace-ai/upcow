package grid

import "time"

func (p *HedgePlan) enterHedgeCanceling(now time.Time) {
	p.State = PlanHedgeCanceling
	p.StateAt = now
}

func (p *HedgePlan) enterRetryWaitFixed(now time.Time, delay time.Duration) {
	p.State = PlanRetryWait
	p.StateAt = now
	p.NextRetryAt = now.Add(delay)
}
