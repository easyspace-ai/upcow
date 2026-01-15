package paircostarb

import (
	"context"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

type PlanKind string

const (
	PlanNone   PlanKind = "none"
	PlanStop   PlanKind = "stop"
	PlanOrders PlanKind = "orders"
)

// BuyCostEstimator returns (vwapEff, costEff, ok) for buying qty shares.
type BuyCostEstimator func(ctx context.Context, assetID string, qty float64) (vwapEff float64, costEff float64, ok bool)

type PlanContext struct {
	Now             time.Time
	Market          *domain.Market
	Base            Snapshot
	TradesThisCycle int
	InFlight        bool
	InEndProtection bool

	SigDir    domain.TokenType
	SigActive bool

	YesAsk float64
	NoAsk  float64

	// Execution quality snapshot (EWMA)
	FillRatioEWMA float64
	SlipAbsMax    float64
}

type Plan struct {
	Kind   PlanKind
	Reason string

	// For orders
	Orders []domain.Order
	Sim    Snapshot // simulated snapshot after fills

	// Execution quality: predicted prices for legs (vwapEff) used for slippage monitoring
	Predicted map[domain.TokenType]float64

	// For execution bookkeeping
	PauseFor time.Duration
}
