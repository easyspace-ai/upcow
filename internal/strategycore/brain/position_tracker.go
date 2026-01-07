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

func NewPositionTracker(ts *services.TradingService) *PositionTracker {
	return &PositionTracker{
		tradingService: ts,
		positions:      make(map[string]*PositionState),
	}
}

func (pt *PositionTracker) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	_ = ctx
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if oldMarket != nil {
		delete(pt.positions, oldMarket.Slug)
	}
}

func (pt *PositionTracker) UpdatePositions(ctx context.Context, market *domain.Market) {
	if pt.tradingService == nil || market == nil {
		return
	}
	positions := pt.tradingService.GetOpenPositionsForMarket(market.Slug)

	pt.mu.Lock()
	defer pt.mu.Unlock()

	state := &PositionState{MarketSlug: market.Slug}

	for _, pos := range positions {
		if pos == nil || !pos.IsOpen() || pos.Size <= 0 {
			continue
		}
		if pos.TokenType == domain.TokenTypeUp {
			state.UpSize += pos.Size
			state.UpCost += pos.CostBasis
			// 注意：不在这里设置 UpAvgPrice，而是在循环后统一计算加权平均
		} else if pos.TokenType == domain.TokenTypeDown {
			state.DownSize += pos.Size
			state.DownCost += pos.CostBasis
			// 注意：不在这里设置 DownAvgPrice，而是在循环后统一计算加权平均
		}
	}

	// 计算加权平均价格（总成本 / 总数量）
	if state.UpSize > 0 && state.UpCost > 0 {
		state.UpAvgPrice = state.UpCost / state.UpSize
	}
	if state.DownSize > 0 && state.DownCost > 0 {
		state.DownAvgPrice = state.DownCost / state.DownSize
	}

	state.IsHedged = state.UpSize > 0 && state.DownSize > 0 &&
		abs(state.UpSize-state.DownSize) < 1

	pt.positions[market.Slug] = state

	// 如果 size 不一致，记录警告日志
	if state.UpSize > 0 && state.DownSize > 0 {
		diff := abs(state.UpSize - state.DownSize)
		if diff >= 1.0 {
			ptLog.Warnf("⚠️ [PositionTracker] UP/DOWN size 不一致: market=%s UP=%.4f DOWN=%.4f diff=%.4f hedged=%v",
				market.Slug, state.UpSize, state.DownSize, diff, state.IsHedged)
		}
	}

	ptLog.Debugf("📊 [PositionTracker] 更新持仓: market=%s UP=%.4f DOWN=%.4f hedged=%v",
		market.Slug, state.UpSize, state.DownSize, state.IsHedged)
}

func (pt *PositionTracker) GetPositionState(marketSlug string) *PositionState {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	state, ok := pt.positions[marketSlug]
	if !ok {
		return &PositionState{MarketSlug: marketSlug}
	}
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

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

