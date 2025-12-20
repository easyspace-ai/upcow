package services

import (
	"context"
	"fmt"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

// GetPosition 获取仓位（通过 OrderEngine 查询）
func (s *TradingService) GetPosition(positionID string) (*domain.Position, error) {
	reply := make(chan *StateSnapshot, 1)
	cmd := &QueryStateCommand{
		id:    fmt.Sprintf("query_position_%d", time.Now().UnixNano()),
		Query: QueryPosition,
		Reply: reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case snapshot := <-reply:
		if snapshot.Position != nil && snapshot.Position.ID == positionID {
			return snapshot.Position, nil
		}
		return nil, fmt.Errorf("仓位不存在: %s", positionID)
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("查询仓位超时: %s", positionID)
	}
}

// CreatePosition 创建仓位（通过 OrderEngine）
func (s *TradingService) CreatePosition(ctx context.Context, position *domain.Position) error {
	reply := make(chan error, 1)
	cmd := &CreatePositionCommand{
		id:       fmt.Sprintf("create_position_%d", time.Now().UnixNano()),
		Position: position,
		Reply:    reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// UpdatePosition 更新仓位（通过 OrderEngine）
func (s *TradingService) UpdatePosition(ctx context.Context, positionID string, updater func(*domain.Position)) error {
	reply := make(chan error, 1)
	cmd := &UpdatePositionCommand{
		id:         fmt.Sprintf("update_position_%d", time.Now().UnixNano()),
		PositionID: positionID,
		Updater:    updater,
		Reply:      reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ClosePosition 关闭仓位（通过 OrderEngine）
func (s *TradingService) ClosePosition(ctx context.Context, positionID string, exitPrice domain.Price, exitOrder *domain.Order) error {
	reply := make(chan error, 1)
	cmd := &ClosePositionCommand{
		id:         fmt.Sprintf("close_position_%d", time.Now().UnixNano()),
		PositionID: positionID,
		ExitPrice:  exitPrice,
		ExitOrder:  exitOrder,
		Reply:      reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// GetAllPositions 获取所有仓位（通过 OrderEngine 查询）
func (s *TradingService) GetAllPositions() []*domain.Position {
	reply := make(chan *StateSnapshot, 1)
	cmd := &QueryStateCommand{
		id:    fmt.Sprintf("query_all_positions_%d", time.Now().UnixNano()),
		Query: QueryAllPositions,
		Reply: reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case snapshot := <-reply:
		return snapshot.Positions
	case <-time.After(5 * time.Second):
		return []*domain.Position{} // 超时返回空列表
	}
}

// GetOpenPositions 获取开放仓位（通过 OrderEngine 查询）
func (s *TradingService) GetOpenPositions() []*domain.Position {
	reply := make(chan *StateSnapshot, 1)
	cmd := &QueryStateCommand{
		id:    fmt.Sprintf("query_open_positions_%d", time.Now().UnixNano()),
		Query: QueryOpenPositions,
		Reply: reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case snapshot := <-reply:
		return snapshot.Positions
	case <-time.After(5 * time.Second):
		return []*domain.Position{} // 超时返回空列表
	}
}

// GetOpenPositionsForMarket 只返回指定 marketSlug 的开放仓位
func (s *TradingService) GetOpenPositionsForMarket(marketSlug string) []*domain.Position {
	positions := s.GetOpenPositions()
	if marketSlug == "" {
		return positions
	}
	out := make([]*domain.Position, 0, len(positions))
	for _, p := range positions {
		if p == nil {
			continue
		}
		slug := p.MarketSlug
		if slug == "" && p.Market != nil {
			slug = p.Market.Slug
		}
		if slug == "" && p.EntryOrder != nil {
			slug = p.EntryOrder.MarketSlug
		}
		if slug == marketSlug {
			out = append(out, p)
		}
	}
	return out
}
