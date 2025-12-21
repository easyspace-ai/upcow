package ports

import (
	"context"

	"github.com/betbot/gobet/internal/domain"
)

// Small capability interfaces shared across layers (strategies/execution/services).

type BestPriceGetter interface {
	// GetBestPrice returns (bestBid, bestAsk) as decimal floats.
	GetBestPrice(ctx context.Context, assetID string) (bestBid float64, bestAsk float64, err error)
}

type OrderPlacer interface {
	PlaceOrder(ctx context.Context, order *domain.Order) (*domain.Order, error)
}

type OrderCanceler interface {
	CancelOrder(ctx context.Context, orderID string) error
}

