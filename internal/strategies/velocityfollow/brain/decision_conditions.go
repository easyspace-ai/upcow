package brain

import (
	"context"

	"github.com/betbot/gobet/internal/events"
)

// DecisionConditions 决策条件状态
type DecisionConditions struct {
	// 速度条件
	UpVelocityOK       bool    // UP速度是否满足
	UpVelocityValue    float64 // UP速度值
	UpMoveOK           bool    // UP位移是否满足
	UpMoveValue        int     // UP位移值
	DownVelocityOK     bool    // DOWN速度是否满足
	DownVelocityValue  float64 // DOWN速度值
	DownMoveOK         bool    // DOWN位移是否满足
	DownMoveValue      int     // DOWN位移值
	Direction          string  // 选择的方向 "UP" | "DOWN" | ""

	// 价格条件
	EntryPriceOK       bool    // Entry价格是否在范围内
	EntryPriceValue    float64 // Entry价格值
	EntryPriceMin      float64 // Entry价格下限
	EntryPriceMax      float64 // Entry价格上限
	TotalCostOK        bool    // 总成本是否 <= 100c
	TotalCostValue     float64 // 总成本值
	HedgePriceOK       bool    // Hedge价格是否有效
	HedgePriceValue    float64 // Hedge价格值

	// 持仓条件
	HasUnhedgedRisk    bool    // 是否有未对冲风险
	IsProfitLocked     bool    // 是否已锁定利润
	ProfitIfUpWin      float64 // UP胜出时的利润
	ProfitIfDownWin    float64 // DOWN胜出时的利润

	// 周期条件
	CooldownOK         bool    // 冷却时间是否通过
	CooldownRemaining  float64 // 剩余冷却时间（秒）
	WarmupOK           bool    // 预热是否完成
	WarmupRemaining    float64 // 剩余预热时间（秒）
	TradesLimitOK      bool    // 是否未超过每周期最大交易次数
	TradesThisCycle    int     // 本周期已交易次数
	MaxTradesPerCycle  int     // 每周期最大交易次数

	// 市场条件
	MarketValid        bool    // 市场是否有效
	HasPendingHedge    bool    // 是否有待处理的对冲单

	// 总体状态
	CanTrade           bool    // 是否可以交易
	BlockReason        string  // 如果不可交易，原因是什么
}

// GetDecisionConditions 获取当前决策条件状态（用于 Dashboard 显示）
// strategyInfo 包含策略级别的信息（冷却时间、交易次数等），避免循环依赖
type StrategyInfo struct {
	CooldownRemaining  float64 // 剩余冷却时间（秒）
	WarmupRemaining    float64 // 剩余预热时间（秒）
	TradesThisCycle    int     // 本周期已交易次数
	HasPendingHedge    bool    // 是否有待处理的对冲单
}

// GetDecisionConditions 获取当前决策条件状态（用于 Dashboard 显示）
func (b *Brain) GetDecisionConditions(ctx context.Context, e *events.PriceChangedEvent, strategyInfo *StrategyInfo) *DecisionConditions {
	conditions := &DecisionConditions{
		MarketValid: true,
	}

	if b == nil || b.tradingService == nil || b.config == nil || e == nil || e.Market == nil {
		conditions.CanTrade = false
		conditions.BlockReason = "Brain 未初始化或市场信息无效"
		return conditions
	}

	// 1. 速度条件检查
	if b.decisionEngine != nil {
		upVel, downVel, upMove, downMove, direction, err := b.decisionEngine.GetCurrentVelocity(ctx, e.Market)
		if err == nil {
			conditions.UpVelocityValue = upVel
			conditions.DownVelocityValue = downVel
			conditions.UpMoveValue = upMove
			conditions.DownMoveValue = downMove
			conditions.Direction = direction

			minMoveCents := b.config.GetMinMoveCents()
			minVelocity := b.config.GetMinVelocityCentsPerSec()

			conditions.UpMoveOK = upMove >= minMoveCents
			conditions.UpVelocityOK = upVel >= minVelocity
			conditions.DownMoveOK = downMove >= minMoveCents
			conditions.DownVelocityOK = downVel >= minVelocity
		}
	}

	// 2. 价格条件检查（如果方向已确定）
	if conditions.Direction != "" {
		// 获取 Entry 价格
		_, yesAsk, err1 := b.tradingService.GetBestPrice(ctx, e.Market.YesAssetID)
		_, noAsk, err2 := b.tradingService.GetBestPrice(ctx, e.Market.NoAssetID)
		if err1 == nil && err2 == nil {
			var entryPrice float64
			if conditions.Direction == "UP" {
				entryPrice = yesAsk
			} else {
				entryPrice = noAsk
			}

			conditions.EntryPriceValue = entryPrice
			conditions.EntryPriceMin = float64(b.config.GetMinEntryPriceCents()) / 100.0
			conditions.EntryPriceMax = float64(b.config.GetMaxEntryPriceCents()) / 100.0

			entryPriceCents := int(entryPrice * 100)
			minEntryCents := b.config.GetMinEntryPriceCents()
			maxEntryCents := b.config.GetMaxEntryPriceCents()

			conditions.EntryPriceOK = (minEntryCents <= 0 || entryPriceCents >= minEntryCents) &&
				(maxEntryCents <= 0 || entryPriceCents <= maxEntryCents)

			// 计算 Hedge 价格
			hedgeOffsetCents := b.config.GetHedgeOffsetCents()
			hedgePriceCents := 100 - entryPriceCents - hedgeOffsetCents
			conditions.HedgePriceValue = float64(hedgePriceCents) / 100.0
			conditions.HedgePriceOK = hedgePriceCents > 0 && hedgePriceCents < 100

			// 计算总成本
			totalCostCents := entryPriceCents + hedgePriceCents
			conditions.TotalCostValue = float64(totalCostCents) / 100.0
			conditions.TotalCostOK = totalCostCents <= 100
		}
	}

	// 3. 持仓条件检查
	if b.positionTracker != nil {
		positionState := b.positionTracker.GetPositionState(e.Market.Slug)
		if positionState != nil {
			conditions.ProfitIfUpWin = positionState.UpSize*1.0 - positionState.UpCost - positionState.DownCost
			conditions.ProfitIfDownWin = positionState.DownSize*1.0 - positionState.UpCost - positionState.DownCost
			conditions.IsProfitLocked = conditions.ProfitIfUpWin > 0 && conditions.ProfitIfDownWin > 0
		}
	}

	// 4. 周期条件检查
	if strategyInfo != nil {
		conditions.CooldownRemaining = strategyInfo.CooldownRemaining
		conditions.CooldownOK = strategyInfo.CooldownRemaining <= 0
		conditions.WarmupRemaining = strategyInfo.WarmupRemaining
		conditions.WarmupOK = strategyInfo.WarmupRemaining <= 0
		conditions.TradesThisCycle = strategyInfo.TradesThisCycle
		conditions.HasPendingHedge = strategyInfo.HasPendingHedge
	} else {
		// 如果没有提供 strategyInfo，使用默认值
		conditions.CooldownOK = true
		conditions.WarmupOK = true
		conditions.HasPendingHedge = false
	}

	// 交易次数限制检查
	maxTradesPerCycle := b.config.GetMaxTradesPerCycle()
	conditions.MaxTradesPerCycle = maxTradesPerCycle
	if maxTradesPerCycle <= 0 {
		conditions.TradesLimitOK = true // 没有限制
	} else {
		conditions.TradesLimitOK = conditions.TradesThisCycle < maxTradesPerCycle
	}

	// 6. 综合判断是否可以交易
	conditions.CanTrade = true
	var reasons []string

	if conditions.Direction == "" {
		conditions.CanTrade = false
		reasons = append(reasons, "未满足速度条件")
	}

	if !conditions.EntryPriceOK {
		conditions.CanTrade = false
		reasons = append(reasons, "Entry价格不在范围内")
	}

	if !conditions.TotalCostOK {
		conditions.CanTrade = false
		reasons = append(reasons, "总成本超过100c")
	}

	if !conditions.HedgePriceOK {
		conditions.CanTrade = false
		reasons = append(reasons, "Hedge价格无效")
	}

	if !conditions.WarmupOK {
		conditions.CanTrade = false
		reasons = append(reasons, "预热未完成")
	}

	if !conditions.TradesLimitOK {
		conditions.CanTrade = false
		reasons = append(reasons, "超过每周期最大交易次数")
	}

	if conditions.HasPendingHedge {
		conditions.CanTrade = false
		reasons = append(reasons, "有待处理的对冲单")
	}

	if len(reasons) > 0 {
		conditions.BlockReason = reasons[0] // 显示第一个原因
	} else {
		conditions.BlockReason = ""
	}

	return conditions
}

