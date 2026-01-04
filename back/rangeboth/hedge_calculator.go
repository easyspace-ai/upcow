package rangeboth

import (
	"math"
)

// HedgeState 当前持仓状态
type HedgeState struct {
	UpShares   float64 // UP持仓数量
	DownShares float64 // DOWN持仓数量
	UpCost     float64 // UP总成本（USDC）
	DownCost   float64 // DOWN总成本（USDC）
	TotalCost  float64 // 总成本（USDC）
	MinProfit  float64 // 当前最小收益（USDC）
}

// CalculateHedgeNeeds 计算需要补多少单才能达到对冲目标
// 输入：
//   - state: 当前持仓状态
//   - upPrice: UP当前价格（小数形式，如0.50表示50分）
//   - downPrice: DOWN当前价格（小数形式，如0.50表示50分）
//   - targetMinProfit: 目标最小收益（USDC）
//   - maxOrderSize: 单次补单最大数量（shares）
// 输出：
//   - upNeeded: 需要补的UP数量（shares）
//   - downNeeded: 需要补的DOWN数量（shares）
func CalculateHedgeNeeds(state HedgeState, upPrice, downPrice, targetMinProfit, maxOrderSize float64) (upNeeded, downNeeded float64) {
	totalCost := state.UpCost + state.DownCost
	if totalCost <= 0 {
		return 0, 0
	}

	// 计算当前收益
	profitIfUpWin := state.UpShares*1.0 - totalCost
	profitIfDownWin := state.DownShares*1.0 - totalCost
	currentMinProfit := math.Min(profitIfUpWin, profitIfDownWin)

	// 如果已达到目标，不需要补单
	if currentMinProfit >= targetMinProfit {
		return 0, 0
	}

	// 计算需要补的数量
	// 目标：确保补单后 min(Profit_up', Profit_down') >= targetMinProfit
	// 
	// 补单后：
	//   Q_up' = Q_up + ΔQ_up
	//   Q_down' = Q_down + ΔQ_down
	//   C_total' = C_total + ΔQ_up * P_up + ΔQ_down * P_down
	//
	// 需要满足：
	//   Q_up' - C_total' >= targetMinProfit
	//   Q_down' - C_total' >= targetMinProfit
	//
	// 简化：优先补不足的一方
	//   如果 Profit_up < targetMinProfit：
	//     Q_up + ΔQ_up - (C_total + ΔQ_up * P_up + ΔQ_down * P_down) >= targetMinProfit
	//     假设只补UP（ΔQ_down = 0）：
	//     Q_up + ΔQ_up - C_total - ΔQ_up * P_up >= targetMinProfit
	//     ΔQ_up * (1 - P_up) >= targetMinProfit - (Q_up - C_total)
	//     ΔQ_up >= (targetMinProfit - Profit_up) / (1 - P_up)

	if profitIfUpWin < targetMinProfit {
		// UP不足，需要补UP
		profitGap := targetMinProfit - profitIfUpWin
		upNeeded = profitGap / (1.0 - upPrice)
		// 考虑补UP后对DOWN收益的影响
		// 补UP后总成本增加：ΔC = upNeeded * upPrice
		// DOWN收益变为：Q_down - (C_total + ΔC)
		// 如果DOWN收益仍然不足，需要同时补DOWN
		newTotalCost := totalCost + upNeeded*upPrice
		newProfitIfDownWin := state.DownShares*1.0 - newTotalCost
		if newProfitIfDownWin < targetMinProfit {
			// DOWN也需要补
			downGap := targetMinProfit - newProfitIfDownWin
			downNeeded = downGap / (1.0 - downPrice)
		}
	} else if profitIfDownWin < targetMinProfit {
		// DOWN不足，需要补DOWN
		profitGap := targetMinProfit - profitIfDownWin
		downNeeded = profitGap / (1.0 - downPrice)
		// 考虑补DOWN后对UP收益的影响
		newTotalCost := totalCost + downNeeded*downPrice
		newProfitIfUpWin := state.UpShares*1.0 - newTotalCost
		if newProfitIfUpWin < targetMinProfit {
			// UP也需要补
			upGap := targetMinProfit - newProfitIfUpWin
			upNeeded = upGap / (1.0 - upPrice)
		}
	}

	// 限制单次补单数量
	if upNeeded > maxOrderSize {
		upNeeded = maxOrderSize
	}
	if downNeeded > maxOrderSize {
		downNeeded = maxOrderSize
	}

	// 确保非负
	if upNeeded < 0 {
		upNeeded = 0
	}
	if downNeeded < 0 {
		downNeeded = 0
	}

	return upNeeded, downNeeded
}

// IsHedged 判断是否已对冲（MinProfit >= 目标值）
func IsHedged(state HedgeState, targetMinProfit float64) bool {
	return state.MinProfit >= targetMinProfit
}

// CalculateHedgeState 从持仓数据计算HedgeState
func CalculateHedgeState(upShares, downShares, upCost, downCost float64) HedgeState {
	totalCost := upCost + downCost
	profitIfUpWin := upShares*1.0 - totalCost
	profitIfDownWin := downShares*1.0 - totalCost
	minProfit := math.Min(profitIfUpWin, profitIfDownWin)

	return HedgeState{
		UpShares:   upShares,
		DownShares: downShares,
		UpCost:     upCost,
		DownCost:   downCost,
		TotalCost:  totalCost,
		MinProfit:  minProfit,
	}
}
