package services

import (
	"context"
	"fmt"

	"github.com/betbot/gobet/internal/domain"
)

// Facade methods: keep TradingService public API stable.

func (s *TradingService) PlaceOrder(ctx context.Context, order *domain.Order) (*domain.Order, error) {
	if s.orders == nil {
		return nil, fmt.Errorf("orders service not initialized")
	}
	return s.orders.PlaceOrder(ctx, order)
}

func (s *TradingService) CancelOrder(ctx context.Context, orderID string) error {
	if s.orders == nil {
		return fmt.Errorf("orders service not initialized")
	}
	return s.orders.CancelOrder(ctx, orderID)
}

func (s *TradingService) GetBestPrice(ctx context.Context, assetID string) (bestBid float64, bestAsk float64, err error) {
	if s.orders == nil {
		return 0, 0, fmt.Errorf("orders service not initialized")
	}
	return s.orders.GetBestPrice(ctx, assetID)
}
