package grid

import "time"

const planMaxBackoffShift = 3

// hedgeRetryDelay returns the retry delay for a given attempt count.
// It preserves the original behavior: 2s/4s/8s... capped by shift=3.
func hedgeRetryDelay(attempts int) time.Duration {
	return time.Duration(1<<minInt(attempts, planMaxBackoffShift)) * time.Second
}

func (p *HedgePlan) enterHedgeSubmitting(now time.Time) {
	p.State = PlanHedgeSubmitting
	p.StateAt = now
	p.HedgeAttempts++
}

func (p *HedgePlan) enterRetryWait(now time.Time) {
	p.State = PlanRetryWait
	p.StateAt = now
	p.NextRetryAt = now.Add(hedgeRetryDelay(p.HedgeAttempts))
}
