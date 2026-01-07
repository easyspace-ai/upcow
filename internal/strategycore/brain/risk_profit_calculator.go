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
	EntryPriceCents int
	HedgePriceCents int
	EntrySize       float64
	HedgeSize       float64

	TotalCostCents   int
	ProfitIfUpWins   float64
	ProfitIfDownWins float64
	MinProfit        float64
	MaxProfit        float64

	IsLocked           bool
	IsPerfectArbitrage bool
	LockQuality        float64

	ExposureRatio float64
	HedgedRatio   float64
}

// CurrentPositionAnalysis 当前持仓分析结果
type CurrentPositionAnalysis struct {
	MarketSlug string

	UpShares   float64
	DownShares float64
	UpCost     float64
	DownCost   float64
	TotalCost  float64

	UpBidCents   int
	UpAskCents   int
	DownBidCents int
	DownAskCents int

	ProfitIfUpWins   float64
	ProfitIfDownWins float64
	MinProfit        float64
	MaxProfit        float64

	IsLocked           bool
	IsPerfectArbitrage bool
	LockQuality        float64

	ExposureRatio float64
	HedgedRatio   float64
}

type RiskProfitCalculator struct {
	tradingService *services.TradingService
}

func NewRiskProfitCalculator(ts *services.TradingService) *RiskProfitCalculator {
	return &RiskProfitCalculator{tradingService: ts}
}

func (rpc *RiskProfitCalculator) CalculatePotentialTradeRiskProfit(
	entryPriceCents, hedgePriceCents int,
	entrySize, hedgeSize float64,
	direction domain.TokenType,
) *PotentialTradeAnalysis {
	if entryPriceCents <= 0 || hedgePriceCents <= 0 || entrySize <= 0 || hedgeSize <= 0 {
		return nil
	}

	totalCostCents := entryPriceCents + hedgePriceCents

	var profitIfUpWins, profitIfDownWins float64
	if direction == domain.TokenTypeUp {
		profitIfUpWins = entrySize*1.0 - float64(totalCostCents)/100.0
		profitIfDownWins = hedgeSize*1.0 - float64(totalCostCents)/100.0
	} else {
		profitIfUpWins = hedgeSize*1.0 - float64(totalCostCents)/100.0
		profitIfDownWins = entrySize*1.0 - float64(totalCostCents)/100.0
	}

	minProfit := math.Min(profitIfUpWins, profitIfDownWins)
	maxProfit := math.Max(profitIfUpWins, profitIfDownWins)

	isLocked := minProfit > 0
	isPerfectArbitrage := isLocked && minProfit > 0

	totalCostUSDC := float64(totalCostCents) / 100.0
	var lockQuality float64
	if totalCostUSDC > 0 {
		lockQuality = minProfit / totalCostUSDC
	}

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

	yesBid, yesAsk, noBid, noAsk, _, err := rpc.tradingService.GetTopOfBook(ctx, market)
	if err != nil {
		rpcLog.Warnf("⚠️ 获取订单簿价格失败: market=%s err=%v", market.Slug, err)
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

	analysis.ProfitIfUpWins = analysis.UpShares*1.0 - analysis.TotalCost
	analysis.ProfitIfDownWins = analysis.DownShares*1.0 - analysis.TotalCost
	analysis.MinProfit = math.Min(analysis.ProfitIfUpWins, analysis.ProfitIfDownWins)
	analysis.MaxProfit = math.Max(analysis.ProfitIfUpWins, analysis.ProfitIfDownWins)

	analysis.IsLocked = analysis.MinProfit > 0
	analysis.IsPerfectArbitrage = analysis.IsLocked && analysis.MinProfit > 0

	if analysis.TotalCost > 0 {
		analysis.LockQuality = analysis.MinProfit / analysis.TotalCost
	}

	maxShares := math.Max(analysis.UpShares, analysis.DownShares)
	minShares := math.Min(analysis.UpShares, analysis.DownShares)
	if maxShares > 0 {
		analysis.ExposureRatio = math.Abs(analysis.UpShares-analysis.DownShares) / maxShares
		analysis.HedgedRatio = minShares / maxShares
	}

	return analysis
}

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

	combinedState := &PositionState{
		MarketSlug: market.Slug,
		UpSize:     positionState.UpSize,
		DownSize:   positionState.DownSize,
		UpCost:     positionState.UpCost,
		DownCost:   positionState.DownCost,
	}

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

	return rpc.CalculateCurrentPositionRiskProfit(ctx, market, combinedState)
}

