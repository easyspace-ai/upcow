package brain

import (
	"context"
	"math"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/sirupsen/logrus"
)

var rpcLog = logrus.WithField("module", "risk_profit_calculator")

// PotentialTradeAnalysis 潜在交易分析结果
type PotentialTradeAnalysis struct {
	// 交易参数
	EntryPriceCents int     // Entry价格（分）
	HedgePriceCents int     // Hedge价格（分）
	EntrySize       float64 // Entry数量
	HedgeSize       float64 // Hedge数量

	// 收益分析
	TotalCostCents    int     // 总成本（分）
	ProfitIfUpWins    float64 // 如果UP胜出的收益（USDC）
	ProfitIfDownWins  float64 // 如果DOWN胜出的收益（USDC）
	MinProfit         float64 // 最小收益（无论哪方胜出）
	MaxProfit         float64 // 最大收益（无论哪方胜出）

	// 套利状态
	IsLocked          bool    // 是否完全锁定（无论哪方胜出都盈利）
	IsPerfectArbitrage bool   // 是否完美套利
	LockQuality       float64 // 锁定质量：minProfit / totalCost（0-1，越高越好）

	// 风险指标
	ExposureRatio float64 // 风险敞口比例：|entrySize - hedgeSize| / max(entrySize, hedgeSize)
	HedgedRatio   float64 // 对冲比例：min(entrySize, hedgeSize) / max(entrySize, hedgeSize)
}

// CurrentPositionAnalysis 当前持仓分析结果
type CurrentPositionAnalysis struct {
	MarketSlug string

	// 持仓信息
	UpShares   float64 // UP持仓数量
	DownShares float64 // DOWN持仓数量
	UpCost     float64 // UP总成本（USDC）
	DownCost   float64 // DOWN总成本（USDC）
	TotalCost  float64 // 总成本（USDC）

	// 当前价格（订单簿）
	UpBidCents   int // UP当前bid价（分）
	UpAskCents   int // UP当前ask价（分）
	DownBidCents int // DOWN当前bid价（分）
	DownAskCents int // DOWN当前ask价（分）

	// 收益分析
	ProfitIfUpWins   float64 // 如果UP胜出的收益（USDC）
	ProfitIfDownWins float64 // 如果DOWN胜出的收益（USDC）
	MinProfit        float64 // 最小收益（无论哪方胜出）
	MaxProfit        float64 // 最大收益（无论哪方胜出）

	// 套利状态
	IsLocked          bool    // 是否完全锁定
	IsPerfectArbitrage bool   // 是否完美套利
	LockQuality       float64 // 锁定质量

	// 风险指标
	ExposureRatio float64 // 风险敞口比例
	HedgedRatio   float64 // 对冲比例
}

// RiskProfitCalculator 风险利润计算器
type RiskProfitCalculator struct {
	tradingService *services.TradingService
}

// NewRiskProfitCalculator 创建风险利润计算器
func NewRiskProfitCalculator(ts *services.TradingService) *RiskProfitCalculator {
	return &RiskProfitCalculator{
		tradingService: ts,
	}
}

// CalculatePotentialTradeRiskProfit 计算潜在交易的风险利润
func (rpc *RiskProfitCalculator) CalculatePotentialTradeRiskProfit(
	entryPriceCents, hedgePriceCents int,
	entrySize, hedgeSize float64,
	direction domain.TokenType,
) *PotentialTradeAnalysis {
	if entryPriceCents <= 0 || hedgePriceCents <= 0 || entrySize <= 0 || hedgeSize <= 0 {
		return nil
	}

	totalCostCents := entryPriceCents + hedgePriceCents

	// 计算收益
	var profitIfUpWins, profitIfDownWins float64
	if direction == domain.TokenTypeUp {
		// Entry是UP，Hedge是DOWN
		// 如果UP胜出：UP=1.0, DOWN=0.0
		profitIfUpWins = entrySize*1.0 - float64(totalCostCents)/100.0
		// 如果DOWN胜出：UP=0.0, DOWN=1.0
		profitIfDownWins = hedgeSize*1.0 - float64(totalCostCents)/100.0
	} else {
		// Entry是DOWN，Hedge是UP
		// 如果UP胜出：UP=1.0, DOWN=0.0
		profitIfUpWins = hedgeSize*1.0 - float64(totalCostCents)/100.0
		// 如果DOWN胜出：UP=0.0, DOWN=1.0
		profitIfDownWins = entrySize*1.0 - float64(totalCostCents)/100.0
	}

	minProfit := math.Min(profitIfUpWins, profitIfDownWins)
	maxProfit := math.Max(profitIfUpWins, profitIfDownWins)

	// 判断是否完全锁定
	isLocked := minProfit > 0
	isPerfectArbitrage := isLocked && minProfit > 0

	// 计算锁定质量
	totalCostUSDC := float64(totalCostCents) / 100.0
	var lockQuality float64
	if totalCostUSDC > 0 {
		lockQuality = minProfit / totalCostUSDC
	}

	// 计算风险指标
	maxSize := math.Max(entrySize, hedgeSize)
	minSize := math.Min(entrySize, hedgeSize)
	var exposureRatio, hedgedRatio float64
	if maxSize > 0 {
		exposureRatio = math.Abs(entrySize-hedgeSize) / maxSize
		hedgedRatio = minSize / maxSize
	}

	return &PotentialTradeAnalysis{
		EntryPriceCents:    entryPriceCents,
		HedgePriceCents:    hedgePriceCents,
		EntrySize:          entrySize,
		HedgeSize:          hedgeSize,
		TotalCostCents:     totalCostCents,
		ProfitIfUpWins:     profitIfUpWins,
		ProfitIfDownWins:   profitIfDownWins,
		MinProfit:          minProfit,
		MaxProfit:          maxProfit,
		IsLocked:           isLocked,
		IsPerfectArbitrage: isPerfectArbitrage,
		LockQuality:        lockQuality,
		ExposureRatio:      exposureRatio,
		HedgedRatio:        hedgedRatio,
	}
}

// CalculateCurrentPositionRiskProfit 计算当前持仓的风险利润
func (rpc *RiskProfitCalculator) CalculateCurrentPositionRiskProfit(
	ctx context.Context,
	market *domain.Market,
	positionState *PositionState,
) *CurrentPositionAnalysis {
	if market == nil || positionState == nil || rpc.tradingService == nil {
		return nil
	}

	analysis := &CurrentPositionAnalysis{
		MarketSlug: market.Slug,
		UpShares:   positionState.UpSize,
		DownShares: positionState.DownSize,
		UpCost:     positionState.UpCost,
		DownCost:   positionState.DownCost,
		TotalCost:  positionState.UpCost + positionState.DownCost,
	}

	// 获取当前订单簿价格
	yesBid, yesAsk, noBid, noAsk, _, err := rpc.tradingService.GetTopOfBook(ctx, market)
	if err != nil {
		rpcLog.Warnf("⚠️ 获取订单簿价格失败: market=%s err=%v", market.Slug, err)
		// 使用持仓的平均价格作为fallback
		if positionState.UpSize > 0 && positionState.UpAvgPrice > 0 {
			analysis.UpBidCents = int(positionState.UpAvgPrice * 100)
			analysis.UpAskCents = analysis.UpBidCents
		}
		if positionState.DownSize > 0 && positionState.DownAvgPrice > 0 {
			analysis.DownBidCents = int(positionState.DownAvgPrice * 100)
			analysis.DownAskCents = analysis.DownBidCents
		}
	} else {
		analysis.UpBidCents = yesBid.ToCents()
		analysis.UpAskCents = yesAsk.ToCents()
		analysis.DownBidCents = noBid.ToCents()
		analysis.DownAskCents = noAsk.ToCents()
	}

	// 计算收益情况
	// 如果UP胜出：UP=1.0, DOWN=0.0
	analysis.ProfitIfUpWins = analysis.UpShares*1.0 - analysis.TotalCost

	// 如果DOWN胜出：UP=0.0, DOWN=1.0
	analysis.ProfitIfDownWins = analysis.DownShares*1.0 - analysis.TotalCost

	// 最小/最大收益
	analysis.MinProfit = math.Min(analysis.ProfitIfUpWins, analysis.ProfitIfDownWins)
	analysis.MaxProfit = math.Max(analysis.ProfitIfUpWins, analysis.ProfitIfDownWins)

	// 判断是否完全锁定
	analysis.IsLocked = analysis.MinProfit > 0
	analysis.IsPerfectArbitrage = analysis.IsLocked && analysis.MinProfit > 0

	// 计算锁定质量
	if analysis.TotalCost > 0 {
		analysis.LockQuality = analysis.MinProfit / analysis.TotalCost
	}

	// 计算风险指标
	maxShares := math.Max(analysis.UpShares, analysis.DownShares)
	minShares := math.Min(analysis.UpShares, analysis.DownShares)
	if maxShares > 0 {
		analysis.ExposureRatio = math.Abs(analysis.UpShares-analysis.DownShares) / maxShares
		analysis.HedgedRatio = minShares / maxShares
	}

	return analysis
}

// CalculateCombinedRiskProfit 计算当前持仓+潜在交易的组合风险利润
func (rpc *RiskProfitCalculator) CalculateCombinedRiskProfit(
	ctx context.Context,
	market *domain.Market,
	positionState *PositionState,
	potentialTrade *PotentialTradeAnalysis,
	direction domain.TokenType,
) *CurrentPositionAnalysis {
	if market == nil || potentialTrade == nil {
		return nil
	}

	// 创建组合持仓状态
	combinedState := &PositionState{
		MarketSlug: market.Slug,
		UpSize:     positionState.UpSize,
		DownSize:   positionState.DownSize,
		UpCost:     positionState.UpCost,
		DownCost:   positionState.DownCost,
	}

	// 添加潜在交易
	entrySizeUSDC := float64(potentialTrade.EntryPriceCents) / 100.0 * potentialTrade.EntrySize
	hedgeSizeUSDC := float64(potentialTrade.HedgePriceCents) / 100.0 * potentialTrade.HedgeSize

	if direction == domain.TokenTypeUp {
		combinedState.UpSize += potentialTrade.EntrySize
		combinedState.DownSize += potentialTrade.HedgeSize
		combinedState.UpCost += entrySizeUSDC
		combinedState.DownCost += hedgeSizeUSDC
	} else {
		combinedState.DownSize += potentialTrade.EntrySize
		combinedState.UpSize += potentialTrade.HedgeSize
		combinedState.DownCost += entrySizeUSDC
		combinedState.UpCost += hedgeSizeUSDC
	}

	// 计算组合后的风险利润
	return rpc.CalculateCurrentPositionRiskProfit(ctx, market, combinedState)
}
