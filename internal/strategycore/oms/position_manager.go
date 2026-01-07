package oms

import (
	"context"
	"sync"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
)

// PositionManager 仓位管理器
type PositionManager struct {
	tradingService *services.TradingService

	mu sync.RWMutex
	orderPairs map[string]string // entryOrderID -> hedgeOrderID
}

func NewPositionManager(ts *services.TradingService, cfg interface{}) *PositionManager {
	_ = cfg
	return &PositionManager{
		tradingService: ts,
		orderPairs:     make(map[string]string),
	}
}

func (pm *PositionManager) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	_ = ctx
	_ = oldMarket
	_ = newMarket
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.orderPairs = make(map[string]string)
}

func (pm *PositionManager) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	_ = ctx
	if order == nil || order.OrderID == "" {
		return nil
	}
	if !order.IsEntryOrder && order.PairOrderID != nil {
		pm.mu.Lock()
		pm.orderPairs[*order.PairOrderID] = order.OrderID
		pm.mu.Unlock()
	}
	return nil
}

func (pm *PositionManager) HasUnhedgedRisk(marketSlug string) bool {
	if pm.tradingService == nil {
		return false
	}
	positions := pm.tradingService.GetOpenPositionsForMarket(marketSlug)

	var upSize, downSize float64
	for _, pos := range positions {
		if pos == nil || !pos.IsOpen() || pos.Size <= 0 {
			continue
		}
		if pos.TokenType == domain.TokenTypeUp {
			upSize += pos.Size
		} else if pos.TokenType == domain.TokenTypeDown {
			downSize += pos.Size
		}
	}

	if upSize > 0 && downSize == 0 {
		return true
	}
	if downSize > 0 && upSize == 0 {
		return true
	}
	if upSize > 0 && downSize > 0 {
		diff := upSize - downSize
		if diff < 0 {
			diff = -diff
		}
		if diff > 0.0001 {
			return true
		}
	}

	return false
}

