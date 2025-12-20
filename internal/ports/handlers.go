package ports

import (
	"context"

	"github.com/betbot/gobet/internal/domain"
)

// OrderUpdateHandler handles order updates (serial delivery recommended).
//
// NOTE: This interface is intentionally defined in a "neutral" package to avoid
// circular dependencies between runtime (bbgo), services, and infrastructure (websocket).
type OrderUpdateHandler interface {
	OnOrderUpdate(ctx context.Context, order *domain.Order) error
}

// TradeUpdateHandler handles trade updates.
type TradeUpdateHandler interface {
	HandleTrade(ctx context.Context, trade *domain.Trade)
}

