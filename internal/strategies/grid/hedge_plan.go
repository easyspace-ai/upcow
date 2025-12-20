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
	PlanHedgeCanceling  HedgePlanState = "hedge_canceling"
	PlanRetryWait       HedgePlanState = "retry_wait"
	PlanSupplementing   HedgePlanState = "supplementing"
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
	StateAt   time.Time

	EntryTemplate *domain.Order
	HedgeTemplate *domain.Order

	EntryCreated *domain.Order
	HedgeCreated *domain.Order

	EntryOrderID string
	HedgeOrderID string
	EntryStatus  domain.OrderStatus
	HedgeStatus  domain.OrderStatus

	// 重试/超时自愈
	EntryAttempts int
	HedgeAttempts int
	MaxAttempts   int
	NextRetryAt   time.Time
	LastSyncAt    time.Time
	LastCancelAt  time.Time

	// 补仓/强对冲（minProfit 驱动）
	SupplementInFlight bool
	LastSupplementAt   time.Time

	LastError string
}

