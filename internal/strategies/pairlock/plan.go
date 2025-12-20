package pairlock

import (
	"time"

	"github.com/betbot/gobet/internal/domain"
)

type planState string

const (
	planIdle     planState = "idle"
	planSubmitting         = "submitting"
	planWaiting            = "waiting"   // 等待订单更新（成交/取消/部分成交）
	planSupplementing      = "supplementing"
	planCompleted          = "completed"
	planFailed             = "failed"
)

// pairLockPlan 表示“一轮成对交易”的状态机
//
// 目标：在一个 round 内买入 YES/NO 同数量 shares，保证总成本 <= 100 - ProfitTargetCents。
// 如果一腿成交而另一腿未成交，进入补齐流程，直到补齐或失败（失败则暂停进一步开轮）。
type pairLockPlan struct {
	ID        string
	MarketSlug string

	CreatedAt time.Time

	TargetSize float64

	YesTemplate *domain.Order
	NoTemplate  *domain.Order

	YesCreatedID string
	NoCreatedID  string

	// 本轮累计成交数量（按 asset 维度累计）
	YesFilled float64
	NoFilled  float64

	// 本轮关联订单集合（用于把 order update 归属到当前 plan）
	// key=orderID, value=up(YES) / down(NO)
	OrderIDs map[string]tokenKey

	State   planState
	StateAt time.Time

	SupplementAttempts int
	LastSupplementAt   time.Time

	LastError string
}

func (p *pairLockPlan) matchedSize() float64 {
	if p == nil {
		return 0
	}
	if p.YesFilled < p.NoFilled {
		return p.YesFilled
	}
	return p.NoFilled
}

func (p *pairLockPlan) imbalance() float64 {
	if p == nil {
		return 0
	}
	if p.YesFilled > p.NoFilled {
		return p.YesFilled - p.NoFilled
	}
	return p.NoFilled - p.YesFilled
}

