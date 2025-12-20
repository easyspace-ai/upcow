package services

import (
	"context"
	"fmt"

	"github.com/betbot/gobet/internal/domain"
)

func (s *TradingService) GetPosition(positionID string) (*domain.Position, error) {
	if s.positions == nil {
		return nil, fmt.Errorf("positions service not initialized")
	}
	return s.positions.GetPosition(positionID)
}

func (s *TradingService) CreatePosition(ctx context.Context, position *domain.Position) error {
	if s.positions == nil {
		return fmt.Errorf("positions service not initialized")
	}
	return s.positions.CreatePosition(ctx, position)
}

func (s *TradingService) UpdatePosition(ctx context.Context, positionID string, updater func(*domain.Position)) error {
	if s.positions == nil {
		return fmt.Errorf("positions service not initialized")
	}
	return s.positions.UpdatePosition(ctx, positionID, updater)
}

func (s *TradingService) ClosePosition(ctx context.Context, positionID string, exitPrice domain.Price, exitOrder *domain.Order) error {
	if s.positions == nil {
		return fmt.Errorf("positions service not initialized")
	}
	return s.positions.ClosePosition(ctx, positionID, exitPrice, exitOrder)
}

func (s *TradingService) GetAllPositions() []*domain.Position {
	if s.positions == nil {
		return []*domain.Position{}
	}
	return s.positions.GetAllPositions()
}

func (s *TradingService) GetOpenPositions() []*domain.Position {
	if s.positions == nil {
		return []*domain.Position{}
	}
	return s.positions.GetOpenPositions()
}

func (s *TradingService) GetOpenPositionsForMarket(marketSlug string) []*domain.Position {
	if s.positions == nil {
		return []*domain.Position{}
	}
	return s.positions.GetOpenPositionsForMarket(marketSlug)
}
