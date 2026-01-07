package brain

import (
	"context"

	"github.com/betbot/gobet/internal/events"
)

// DecisionConditions 决策条件状态
type DecisionConditions struct {
	// 速度条件
	UpVelocityOK      bool
	UpVelocityValue   float64
	UpMoveOK          bool
	UpMoveValue       int
	DownVelocityOK    bool
	DownVelocityValue float64
	DownMoveOK        bool
	DownMoveValue     int
	Direction         string

	// 价格条件
	EntryPriceOK    bool
	EntryPriceValue float64
	EntryPriceMin   float64
	EntryPriceMax   float64
	TotalCostOK     bool
	TotalCostValue  float64
	HedgePriceOK    bool
	HedgePriceValue float64

	// 持仓条件
	HasUnhedgedRisk bool
	IsProfitLocked  bool
	ProfitIfUpWin   float64
	ProfitIfDownWin float64

	// 周期条件
	CooldownOK        bool
	CooldownRemaining float64
	WarmupOK          bool
	WarmupRemaining   float64
	TradesLimitOK     bool
	TradesThisCycle   int
	MaxTradesPerCycle int

	// 市场条件
	MarketValid     bool
	HasPendingHedge bool

	// 总体状态
	CanTrade    bool
	BlockReason string
}

// StrategyInfo 包含策略级别的信息（冷却时间、交易次数等），避免循环依赖
type StrategyInfo struct {
	CooldownRemaining float64
	WarmupRemaining   float64
	TradesThisCycle   int
	HasPendingHedge   bool
}

func (b *Brain) GetDecisionConditions(ctx context.Context, e *events.PriceChangedEvent, strategyInfo *StrategyInfo) *DecisionConditions {
	conditions := &DecisionConditions{MarketValid: true}

	if b == nil || b.tradingService == nil || b.config == nil || e == nil || e.Market == nil {
		conditions.CanTrade = false
		conditions.BlockReason = "Brain 未初始化或市场信息无效"
		return conditions
	}

	// 1. 速度条件
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

	// 2. 价格条件
	if conditions.Direction != "" {
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

			hedgeOffsetCents := b.config.GetHedgeOffsetCents()
			hedgePriceCents := 100 - entryPriceCents - hedgeOffsetCents
			conditions.HedgePriceValue = float64(hedgePriceCents) / 100.0
			conditions.HedgePriceOK = hedgePriceCents > 0 && hedgePriceCents < 100

			totalCostCents := entryPriceCents + hedgePriceCents
			conditions.TotalCostValue = float64(totalCostCents) / 100.0
			conditions.TotalCostOK = totalCostCents <= 100
		}
	}

	// 3. 持仓条件
	if b.positionTracker != nil {
		positionState := b.positionTracker.GetPositionState(e.Market.Slug)
		if positionState != nil {
			conditions.ProfitIfUpWin = positionState.UpSize*1.0 - positionState.UpCost - positionState.DownCost
			conditions.ProfitIfDownWin = positionState.DownSize*1.0 - positionState.UpCost - positionState.DownCost
			conditions.IsProfitLocked = conditions.ProfitIfUpWin > 0 && conditions.ProfitIfDownWin > 0
		}
	}

	// 4. 周期条件
	if strategyInfo != nil {
		conditions.CooldownRemaining = strategyInfo.CooldownRemaining
		conditions.CooldownOK = strategyInfo.CooldownRemaining <= 0
		conditions.WarmupRemaining = strategyInfo.WarmupRemaining
		conditions.WarmupOK = strategyInfo.WarmupRemaining <= 0
		conditions.TradesThisCycle = strategyInfo.TradesThisCycle
		conditions.HasPendingHedge = strategyInfo.HasPendingHedge
	} else {
		conditions.CooldownOK = true
		conditions.WarmupOK = true
		conditions.HasPendingHedge = false
	}

	// 交易次数限制
	maxTradesPerCycle := b.config.GetMaxTradesPerCycle()
	conditions.MaxTradesPerCycle = maxTradesPerCycle
	if maxTradesPerCycle <= 0 {
		conditions.TradesLimitOK = true
	} else {
		conditions.TradesLimitOK = conditions.TradesThisCycle < maxTradesPerCycle
	}

	// 综合判断
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
		conditions.BlockReason = reasons[0]
	} else {
		conditions.BlockReason = ""
	}
	return conditions
}

