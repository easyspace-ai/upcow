package ports

import (
	"context"

	"github.com/betbot/gobet/internal/domain"
)

// Shared, small interfaces for strategies to depend on (avoid per-strategy duplication).

type BestPriceGetter interface {
	GetBestPrice(ctx context.Context, assetID string) (bestBid float64, bestAsk float64, err error)
}

type OrderPlacer interface {
	PlaceOrder(ctx context.Context, order *domain.Order) (*domain.Order, error)
}

type OrderCanceler interface {
	CancelOrder(ctx context.Context, orderID string) error
}

type ActiveOrdersGetter interface {
	GetActiveOrders() []*domain.Order
}

type OpenPositionsGetter interface {
	GetOpenPositions() []*domain.Position
}

type PositionCreator interface {
	CreatePosition(ctx context.Context, position *domain.Position) error
}

type PositionUpdater interface {
	UpdatePosition(ctx context.Context, positionID string, updater func(*domain.Position)) error
}

type PositionCloser interface {
	ClosePosition(ctx context.Context, positionID string, exitPrice domain.Price, exitOrder *domain.Order) error
}

type OrderStatusSyncer interface {
	SyncOrderStatus(ctx context.Context, orderID string) error
}

// Composite convenience interfaces.

type MomentumTradingService interface {
	OrderPlacer
	BestPriceGetter
}

type BasicTradingService interface {
	OrderPlacer
	OrderCanceler
	OpenPositionsGetter
	BestPriceGetter
}

type PairLockTradingService interface {
	OrderPlacer
	OrderCanceler
	ActiveOrdersGetter
	BestPriceGetter
}

type GridTradingService interface {
	OrderPlacer
	OrderCanceler
	PositionCreator
	PositionUpdater
	PositionCloser
	OpenPositionsGetter
	ActiveOrdersGetter
	BestPriceGetter
	OrderStatusSyncer
}
