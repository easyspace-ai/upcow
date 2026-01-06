package brain

import (
	"context"
	"sync"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/sirupsen/logrus"
)

var ptLog = logrus.WithField("module", "position_tracker")

// PositionState 持仓状态
type PositionState struct {
	MarketSlug   string
	UpSize       float64 // UP 持仓数量
	DownSize     float64 // DOWN 持仓数量
	UpCost       float64 // UP 总成本（USDC）
	DownCost     float64 // DOWN 总成本（USDC）
	UpAvgPrice   float64 // UP 平均价格
	DownAvgPrice float64 // DOWN 平均价格
	IsHedged     bool    // 是否完全对冲
}

// PositionTracker 持仓跟踪器
type PositionTracker struct {
	tradingService *services.TradingService
	mu             sync.RWMutex
	positions      map[string]*PositionState // marketSlug -> state
}

// NewPositionTracker 创建新的持仓跟踪器
func NewPositionTracker(ts *services.TradingService) *PositionTracker {
	return &PositionTracker{
		tradingService: ts,
		positions:      make(map[string]*PositionState),
	}
}

// OnCycle 周期切换回调
func (pt *PositionTracker) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	// 清理旧周期的持仓状态（可选，如果需要保留历史数据可以注释掉）
	if oldMarket != nil {
		delete(pt.positions, oldMarket.Slug)
	}
}

// UpdatePositions 更新持仓状态
func (pt *PositionTracker) UpdatePositions(ctx context.Context, market *domain.Market) {
	if pt.tradingService == nil || market == nil {
		return
	}

	// 从 TradingService 获取持仓
	positions := pt.tradingService.GetOpenPositionsForMarket(market.Slug)

	pt.mu.Lock()
	defer pt.mu.Unlock()

	state := &PositionState{
		MarketSlug: market.Slug,
	}

	// 计算 UP/DOWN 持仓
	for _, pos := range positions {
		if pos == nil || !pos.IsOpen() || pos.Size <= 0 {
			continue
		}

		if pos.TokenType == domain.TokenTypeUp {
			state.UpSize += pos.Size
			state.UpCost += pos.CostBasis
			if pos.TotalFilledSize > 0 {
				state.UpAvgPrice = pos.AvgPrice
			}
		} else if pos.TokenType == domain.TokenTypeDown {
			state.DownSize += pos.Size
			state.DownCost += pos.CostBasis
			if pos.TotalFilledSize > 0 {
				state.DownAvgPrice = pos.AvgPrice
			}
		}
	}

	// 判断是否完全对冲（UP 和 DOWN 数量相等）
	state.IsHedged = state.UpSize > 0 && state.DownSize > 0 &&
		abs(state.UpSize-state.DownSize) < 1 // 允许小的浮点误差

	pt.positions[market.Slug] = state

	ptLog.Debugf("📊 [PositionTracker] 更新持仓: market=%s UP=%.4f DOWN=%.4f hedged=%v",
		market.Slug, state.UpSize, state.DownSize, state.IsHedged)
}

// GetPositionState 获取持仓状态
func (pt *PositionTracker) GetPositionState(marketSlug string) *PositionState {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	state, ok := pt.positions[marketSlug]
	if !ok {
		return &PositionState{MarketSlug: marketSlug}
	}

	// 返回副本，避免并发修改
	return &PositionState{
		MarketSlug:   state.MarketSlug,
		UpSize:       state.UpSize,
		DownSize:     state.DownSize,
		UpCost:       state.UpCost,
		DownCost:     state.DownCost,
		UpAvgPrice:   state.UpAvgPrice,
		DownAvgPrice: state.DownAvgPrice,
		IsHedged:     state.IsHedged,
	}
}

// abs 计算绝对值
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
