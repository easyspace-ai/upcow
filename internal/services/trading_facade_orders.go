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

// GetBestPriceWithMaxSpread 获取 bestBid/bestAsk，并允许策略指定最大可接受价差（cents）。
// - maxSpreadCents <= 0：不限制价差（仍要求双边价格都存在）
// - 用途：做市/挂单类策略在大价差环境下获取 top-of-book
func (s *TradingService) GetBestPriceWithMaxSpread(ctx context.Context, assetID string, maxSpreadCents int) (bestBid float64, bestAsk float64, err error) {
	if s.orders == nil {
		return 0, 0, fmt.Errorf("orders service not initialized")
	}
	return s.orders.GetBestPriceWithMaxSpread(ctx, assetID, maxSpreadCents)
}
