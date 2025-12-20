package grid

import (
	"time"

	"github.com/betbot/gobet/internal/domain"
)

const (
	planSubmitTimeout    = 35 * time.Second
	planSyncInterval     = 5 * time.Second
	planHedgeOpenTimeout = 10 * time.Second
	planCancelTimeout    = 12 * time.Second
)

func (p *HedgePlan) submittingTimedOut(now time.Time) bool {
	return (p.State == PlanEntrySubmitting || p.State == PlanHedgeSubmitting) && now.Sub(p.StateAt) > planSubmitTimeout
}

func (p *HedgePlan) shouldSyncEntry(now time.Time) bool {
	return p.State == PlanEntryOpen &&
		p.EntryOrderID != "" &&
		now.Sub(p.LastSyncAt) > planSyncInterval &&
		now.Sub(p.StateAt) > planSyncInterval
}

func (p *HedgePlan) shouldSyncHedge(now time.Time) bool {
	return p.State == PlanHedgeOpen &&
		p.HedgeOrderID != "" &&
		now.Sub(p.LastSyncAt) > planSyncInterval &&
		now.Sub(p.StateAt) > planSyncInterval
}

func (p *HedgePlan) shouldCancelStaleHedge(now time.Time) bool {
	if p.State != PlanHedgeOpen || p.HedgeOrderID == "" {
		return false
	}
	if p.HedgeStatus != domain.OrderStatusOpen && p.HedgeStatus != domain.OrderStatusPending {
		return false
	}
	return now.Sub(p.StateAt) > planHedgeOpenTimeout && now.Sub(p.LastCancelAt) > planHedgeOpenTimeout
}

func (p *HedgePlan) cancelTimedOut(now time.Time) bool {
	return p.State == PlanHedgeCanceling && now.Sub(p.StateAt) > planCancelTimeout
}
