package oms

import (
	"context"
	"sync"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/sirupsen/logrus"
)

var pmLog = logrus.WithField("module", "position_manager")

// PositionManager 仓位管理器
type PositionManager struct {
	tradingService *services.TradingService

	mu sync.RWMutex
	// 跟踪 Entry/Hedge 订单配对
	orderPairs map[string]string // entryOrderID -> hedgeOrderID
}

// NewPositionManager 创建新的仓位管理器
func NewPositionManager(ts *services.TradingService, cfg interface{}) *PositionManager {
	return &PositionManager{
		tradingService: ts,
		orderPairs:     make(map[string]string),
	}
}

// OnCycle 周期切换回调
func (pm *PositionManager) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// 清空订单配对（新周期开始）
	pm.orderPairs = make(map[string]string)
}

// OnOrderUpdate 订单更新回调
func (pm *PositionManager) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	if order == nil || order.OrderID == "" {
		return nil
	}

	// 更新订单配对（如果是对冲订单，记录与 Entry 订单的关联）
	if !order.IsEntryOrder && order.PairOrderID != nil {
		pm.mu.Lock()
		pm.orderPairs[*order.PairOrderID] = order.OrderID
		pm.mu.Unlock()
	}

	return nil
}

// HasUnhedgedRisk 检查是否有未对冲风险
func (pm *PositionManager) HasUnhedgedRisk(marketSlug string) bool {
	if pm.tradingService == nil {
		return false
	}

	// 获取持仓
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

	// 如果 UP 和 DOWN 数量不相等，存在未对冲风险
	if upSize > 0 && downSize == 0 {
		return true // 只有 UP，未对冲
	}
	if downSize > 0 && upSize == 0 {
		return true // 只有 DOWN，未对冲
	}
	if upSize > 0 && downSize > 0 {
		// 检查数量是否相等（允许小的浮点误差）
		diff := upSize - downSize
		if diff < 0 {
			diff = -diff
		}
		if diff > 0.0001 {
			return true // 数量不相等，存在未对冲风险
		}
	}

	return false
}
