package grid

import (
	"time"

	"github.com/betbot/gobet/internal/domain"
)

type HedgePlanState string

const (
	PlanEntrySubmitting HedgePlanState = "entry_submitting"
	PlanEntryOpen       HedgePlanState = "entry_open"
	PlanHedgeSubmitting HedgePlanState = "hedge_submitting"
	PlanHedgeOpen       HedgePlanState = "hedge_open"
	PlanDone            HedgePlanState = "done"
	PlanFailed          HedgePlanState = "failed"
)

// HedgePlan 用于把“入场 -> 对冲”的生命周期显式化。
// 下一步会继续把所有网格下单路径都收敛到该状态机（包括补仓/强对冲等）。
type HedgePlan struct {
	ID       string
	LevelKey string

	State     HedgePlanState
	CreatedAt time.Time

	EntryTemplate *domain.Order
	HedgeTemplate *domain.Order

	EntryCreated *domain.Order
	HedgeCreated *domain.Order

	LastError string
}

