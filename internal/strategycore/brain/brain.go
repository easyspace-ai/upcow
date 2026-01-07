package brain

import (
	"context"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/services"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("module", "brain")

// Decision 决策结果
type Decision struct {
	ShouldTrade bool            // 是否应该交易
	Direction   domain.TokenType // 交易方向（UP 或 DOWN）
	EntryPrice  domain.Price     // Entry 价格
	HedgePrice  domain.Price     // Hedge 价格
	EntrySize   float64          // Entry 数量
	HedgeSize   float64          // Hedge 数量
	Reason      string           // 决策原因
}

// Brain 控制大脑模块
type Brain struct {
	tradingService *services.TradingService
	config         ConfigInterface

	// 子模块
	positionTracker *PositionTracker
	decisionEngine  *DecisionEngine
	arbitrageBrain  *ArbitrageBrain
	positionMonitor *PositionMonitor // 实时持仓监控器
}

// New 创建新的 Brain 实例
func New(ts *services.TradingService, cfg ConfigInterface) (*Brain, error) {
	if ts == nil {
		return nil, nil // 允许延迟初始化
	}

	pt := NewPositionTracker(ts)
	de := NewDecisionEngine(cfg)
	de.SetTradingService(ts) // 注入 TradingService
	ab := NewArbitrageBrain(ts, cfg)
	pm := NewPositionMonitor(ts, cfg)

	return &Brain{
		tradingService:  ts,
		config:          cfg,
		positionTracker: pt,
		decisionEngine:  de,
		arbitrageBrain:  ab,
		positionMonitor: pm,
	}, nil
}

// OnCycle 周期切换回调
func (b *Brain) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	if b.decisionEngine != nil {
		b.decisionEngine.OnCycle(ctx, oldMarket, newMarket)
	}
	if b.positionTracker != nil {
		b.positionTracker.OnCycle(ctx, oldMarket, newMarket)
	}
}

// MakeDecision 做出交易决策
func (b *Brain) MakeDecision(ctx context.Context, e *events.PriceChangedEvent) (*Decision, error) {
	if b == nil || b.tradingService == nil || b.config == nil {
		return &Decision{ShouldTrade: false, Reason: "Brain 未初始化"}, nil
	}

	// 1. 更新持仓状态
	if b.positionTracker != nil {
		b.positionTracker.UpdatePositions(ctx, e.Market)
	}

	// 1.5. 实时监控持仓并自动对冲（在决策前检查，避免继续加仓不平衡的持仓）
	if b.positionMonitor != nil {
		_ = b.positionMonitor.CheckAndHedge(ctx, e.Market)
	}

	// 2. 计算速度并选择方向
	direction, velocity, err := b.decisionEngine.CalculateVelocityAndDirection(ctx, e)
	if err != nil {
		return &Decision{ShouldTrade: false, Reason: "速度计算失败: " + err.Error()}, nil
	}
	if direction == "" {
		return &Decision{ShouldTrade: false, Reason: "未满足速度条件"}, nil
	}

	// 3. 获取当前持仓状态
	positionState := b.positionTracker.GetPositionState(e.Market.Slug)

	// 4. 检查是否已锁定利润
	if b.config != nil && b.config.GetArbitrageBrainEnabled() {
		isLocked, totalCost := b.checkProfitLocked(positionState)
		if isLocked {
			log.Debugf("💰 [Brain] 已锁定利润，总成本=%.2f", totalCost)
			// 可选：如果已锁定，可以停止开新单
			// return &Decision{ShouldTrade: false, Reason: "已锁定利润"}, nil
		}
	}

	// 5. 决策引擎评估（市场质量、价格稳定性等，根据速度状态选择策略）
	shouldTrade, reason, entryPrice, hedgePrice, entrySize, hedgeSize := b.decisionEngine.Evaluate(
		ctx, e, direction, velocity, positionState)
	if !shouldTrade {
		return &Decision{ShouldTrade: false, Reason: reason}, nil
	}

	// 6. 计算潜在交易的风险利润
	var potentialTradeAnalysis *PotentialTradeAnalysis
	if b.arbitrageBrain != nil {
		entryPriceCents := entryPrice.ToCents()
		hedgePriceCents := hedgePrice.ToCents()
		potentialTradeAnalysis = b.arbitrageBrain.CalculatePotentialTradeRiskProfit(
			entryPriceCents, hedgePriceCents, entrySize, hedgeSize, direction)

		if potentialTradeAnalysis != nil {
			// 如果潜在交易无法锁定利润，可以考虑拒绝或警告
			if !potentialTradeAnalysis.IsLocked {
				log.Debugf("⚠️ [Brain] 潜在交易未锁定利润: minProfit=%.4f totalCost=%dc",
					potentialTradeAnalysis.MinProfit, potentialTradeAnalysis.TotalCostCents)
				// 可以选择拒绝或继续（这里继续，因为可能还有其他持仓）
			} else {
				log.Debugf("✅ [Brain] 潜在交易可锁定利润: minProfit=%.4f lockQuality=%.2f%%",
					potentialTradeAnalysis.MinProfit, potentialTradeAnalysis.LockQuality*100)
			}
		}
	}

	// 7. 计算组合风险利润（当前持仓 + 潜在交易）
	if b.arbitrageBrain != nil && positionState != nil && potentialTradeAnalysis != nil {
		combinedAnalysis := b.arbitrageBrain.CalculateCombinedRiskProfit(
			ctx, e.Market, positionState, potentialTradeAnalysis, direction)
		if combinedAnalysis != nil {
			if combinedAnalysis.IsLocked {
				log.Debugf("✅ [Brain] 组合后锁定利润: minProfit=%.4f lockQuality=%.2f%%",
					combinedAnalysis.MinProfit, combinedAnalysis.LockQuality*100)
			} else {
				log.Debugf("⚠️ [Brain] 组合后未锁定利润: minProfit=%.4f",
					combinedAnalysis.MinProfit)
			}
		}
	}

	return &Decision{
		ShouldTrade: true,
		Direction:   direction,
		EntryPrice:  entryPrice,
		HedgePrice:  hedgePrice,
		EntrySize:   entrySize,
		HedgeSize:   hedgeSize,
		Reason:      reason,
	}, nil
}

// checkProfitLocked 检查是否已锁定利润
func (b *Brain) checkProfitLocked(state *PositionState) (bool, float64) {
	if state == nil {
		return false, 0
	}

	// 如果没有持仓，未锁定
	if state.UpSize <= 0 || state.DownSize <= 0 {
		return false, 0
	}

	// 计算总成本（UP 成本 + DOWN 成本）
	totalCost := state.UpCost + state.DownCost

	// 分别计算 UP win 和 DOWN win 的利润
	// UP win 的利润 = UP shares * 1.0 - UP总成本 - DOWN总成本
	profitIfUpWin := state.UpSize*1.0 - state.UpCost - state.DownCost

	// DOWN win 的利润 = DOWN shares * 1.0 - UP总成本 - DOWN总成本
	profitIfDownWin := state.DownSize*1.0 - state.UpCost - state.DownCost

	// 如果无论哪方胜出都有利润，表示已锁定利润
	locked := profitIfUpWin > 0 && profitIfDownWin > 0

	return locked, totalCost
}

// GetPositionState 获取持仓状态（供外部查询）
func (b *Brain) GetPositionState(marketSlug string) *PositionState {
	if b.positionTracker == nil {
		return nil
	}
	return b.positionTracker.GetPositionState(marketSlug)
}

// UpdatePositionState 更新持仓状态（供外部调用，用于周期切换后立即更新）
func (b *Brain) UpdatePositionState(ctx context.Context, market *domain.Market) {
	if b.positionTracker != nil && market != nil {
		b.positionTracker.UpdatePositions(ctx, market)
	}
}

// VelocityInfo 速度信息
type VelocityInfo struct {
	UpVelocity   float64
	DownVelocity float64
	UpMove       int
	DownMove     int
	Direction    string
}

// UpdateSamplesFromPriceEvent 从价格事件更新样本（供 Dashboard 实时更新速度）
func (b *Brain) UpdateSamplesFromPriceEvent(ctx context.Context, e *events.PriceChangedEvent) {
	if b.decisionEngine == nil || e == nil || e.Market == nil || b.tradingService == nil {
		return
	}

	// 更新样本（不触发决策，只更新数据）
	b.decisionEngine.UpdateSamplesFromPriceEvent(ctx, e)
}

// GetVelocityInfo 获取当前速度信息（供 Dashboard 显示）
func (b *Brain) GetVelocityInfo(ctx context.Context, market *domain.Market) *VelocityInfo {
	if b.decisionEngine == nil || market == nil {
		return &VelocityInfo{}
	}

	upVel, downVel, upMove, downMove, direction, err := b.decisionEngine.GetCurrentVelocity(ctx, market)
	if err != nil {
		log.Warnf("获取速度信息失败: %v", err)
		return &VelocityInfo{}
	}

	return &VelocityInfo{
		UpVelocity:   upVel,
		DownVelocity: downVel,
		UpMove:       upMove,
		DownMove:     downMove,
		Direction:    direction,
	}
}

// Start 启动 Brain 子模块（ArbitrageBrain等）
func (b *Brain) Start(ctx context.Context) {
	if b.arbitrageBrain != nil {
		b.arbitrageBrain.Start(ctx)
	}
}

// Stop 停止 Brain 子模块
func (b *Brain) Stop() {
	if b.arbitrageBrain != nil {
		b.arbitrageBrain.Stop()
	}
}

// GetArbitrageBrain 获取 ArbitrageBrain（供外部使用）
func (b *Brain) GetArbitrageBrain() *ArbitrageBrain {
	return b.arbitrageBrain
}

// GetPositionMonitor 获取 PositionMonitor（供外部使用）
func (b *Brain) GetPositionMonitor() *PositionMonitor {
	return b.positionMonitor
}

// SetPositionMonitorHedgeCallback 设置持仓监控器的对冲回调
func (b *Brain) SetPositionMonitorHedgeCallback(fn func(ctx context.Context, market *domain.Market, analysis *PositionAnalysis) error) {
	if b.positionMonitor != nil {
		b.positionMonitor.SetHedgeCallback(fn)
	}
}
