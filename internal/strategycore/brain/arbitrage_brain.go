package brain

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/sirupsen/logrus"
)

var arbLog = logrus.WithField("module", "arbitrage_brain")

// ArbitrageAnalysis 套利分析结果
type ArbitrageAnalysis struct {
	MarketSlug string
	Timestamp  time.Time

	UpShares      float64
	DownShares    float64
	UpCostUSDC    float64
	DownCostUSDC  float64
	TotalCostUSDC float64

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

	Recommendation string
}

type ArbitrageBrain struct {
	mu                   sync.Mutex
	tradingService       *services.TradingService
	analyses             map[string]*ArbitrageAnalysis
	updateInterval       time.Duration
	enabled              bool
	stopChan             chan struct{}
	stopped              bool
	config               ConfigInterface
	riskProfitCalculator *RiskProfitCalculator
}

func NewArbitrageBrain(ts *services.TradingService, cfg ConfigInterface) *ArbitrageBrain {
	interval := time.Duration(cfg.GetArbitrageBrainUpdateIntervalSeconds()) * time.Second
	if interval <= 0 {
		interval = 10 * time.Second
	}

	enabled := cfg.GetArbitrageBrainEnabled()
	if !enabled {
		enabled = true
	}

	return &ArbitrageBrain{
		tradingService:       ts,
		analyses:             make(map[string]*ArbitrageAnalysis),
		updateInterval:       interval,
		enabled:              enabled,
		stopChan:             make(chan struct{}),
		stopped:              false,
		config:               cfg,
		riskProfitCalculator: NewRiskProfitCalculator(ts),
	}
}

func (ab *ArbitrageBrain) Start(ctx context.Context) {
	if !ab.enabled {
		return
	}
	ab.mu.Lock()
	if ab.stopped {
		ab.mu.Unlock()
		return
	}
	ab.mu.Unlock()
	go ab.analysisLoop(ctx)
	arbLog.Infof("✅ 套利分析大脑已启动: updateInterval=%v", ab.updateInterval)
}

func (ab *ArbitrageBrain) Stop() {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	if ab.stopped {
		return
	}
	ab.stopped = true
	close(ab.stopChan)
	arbLog.Infof("🛑 套利分析大脑已停止")
}

func (ab *ArbitrageBrain) AnalyzeMarket(marketSlug string, market *domain.Market) *ArbitrageAnalysis {
	if ab.tradingService == nil || market == nil || !market.IsValid() {
		return nil
	}

	analysis := &ArbitrageAnalysis{MarketSlug: marketSlug, Timestamp: time.Now()}

	positions := ab.tradingService.GetOpenPositionsForMarket(marketSlug)
	var upShares, downShares, upCostUSDC, downCostUSDC float64

	for _, p := range positions {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}
		var cost float64
		if p.AvgPrice > 0 {
			cost = p.AvgPrice * p.Size
		} else if p.TotalFilledSize > 0 && p.CostBasis > 0 {
			avgPriceFromCostBasis := p.CostBasis / p.TotalFilledSize
			cost = avgPriceFromCostBasis * p.Size
		} else if p.EntryPrice.Pips > 0 {
			cost = p.EntryPrice.ToDecimal() * p.Size
		} else {
			continue
		}
		if cost <= 0 {
			continue
		}
		switch p.TokenType {
		case domain.TokenTypeUp:
			upShares += p.Size
			upCostUSDC += cost
		case domain.TokenTypeDown:
			downShares += p.Size
			downCostUSDC += cost
		}
	}

	analysis.UpShares = upShares
	analysis.DownShares = downShares
	analysis.UpCostUSDC = upCostUSDC
	analysis.DownCostUSDC = downCostUSDC
	analysis.TotalCostUSDC = upCostUSDC + downCostUSDC

	ctx2, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	yesBid, yesAsk, noBid, noAsk, _, err := ab.tradingService.GetTopOfBook(ctx2, market)
	if err != nil {
		arbLog.Warnf("⚠️ 获取订单簿价格失败: market=%s err=%v", marketSlug, err)
		if upShares > 0 {
			analysis.UpBidCents = int((upCostUSDC / upShares) * 100)
			analysis.UpAskCents = analysis.UpBidCents
		}
		if downShares > 0 {
			analysis.DownBidCents = int((downCostUSDC / downShares) * 100)
			analysis.DownAskCents = analysis.DownBidCents
		}
	} else {
		analysis.UpBidCents = yesBid.ToCents()
		analysis.UpAskCents = yesAsk.ToCents()
		analysis.DownBidCents = noBid.ToCents()
		analysis.DownAskCents = noAsk.ToCents()
	}

	analysis.ProfitIfUpWins = upShares*1.0 - analysis.TotalCostUSDC
	analysis.ProfitIfDownWins = downShares*1.0 - analysis.TotalCostUSDC
	analysis.MinProfit = math.Min(analysis.ProfitIfUpWins, analysis.ProfitIfDownWins)
	analysis.MaxProfit = math.Max(analysis.ProfitIfUpWins, analysis.ProfitIfDownWins)

	analysis.IsLocked = analysis.MinProfit > 0
	analysis.IsPerfectArbitrage = analysis.IsLocked && analysis.MinProfit > 0

	if analysis.TotalCostUSDC > 0 {
		analysis.LockQuality = analysis.MinProfit / analysis.TotalCostUSDC
	}

	maxShares := math.Max(upShares, downShares)
	minShares := math.Min(upShares, downShares)
	if maxShares > 0 {
		analysis.ExposureRatio = math.Abs(upShares-downShares) / maxShares
		analysis.HedgedRatio = minShares / maxShares
	}

	analysis.Recommendation = ab.generateRecommendation(analysis)
	return analysis
}

func (ab *ArbitrageBrain) generateRecommendation(analysis *ArbitrageAnalysis) string {
	if analysis.UpShares == 0 && analysis.DownShares == 0 {
		return "无持仓"
	}
	if analysis.IsPerfectArbitrage {
		return fmt.Sprintf("✅ 完美套利锁定！无论哪方胜出都盈利: minProfit=%.4f USDC (%.2f%%)",
			analysis.MinProfit, analysis.LockQuality*100)
	}
	if analysis.IsLocked {
		return fmt.Sprintf("✅ 完全锁定！无论哪方胜出都盈利: minProfit=%.4f USDC", analysis.MinProfit)
	}
	if analysis.ProfitIfUpWins > 0 && analysis.ProfitIfDownWins < 0 {
		loss := -analysis.ProfitIfDownWins
		return fmt.Sprintf("⚠️ 风险敞口：UP胜出盈利(%.4f)，DOWN胜出亏损(%.4f)。建议：增加DOWN持仓对冲",
			analysis.ProfitIfUpWins, loss)
	}
	if analysis.ProfitIfDownWins > 0 && analysis.ProfitIfUpWins < 0 {
		loss := -analysis.ProfitIfUpWins
		return fmt.Sprintf("⚠️ 风险敞口：DOWN胜出盈利(%.4f)，UP胜出亏损(%.4f)。建议：增加UP持仓对冲",
			analysis.ProfitIfDownWins, loss)
	}
	if analysis.ProfitIfUpWins < 0 && analysis.ProfitIfDownWins < 0 {
		return fmt.Sprintf("❌ 风险：无论哪方胜出都亏损！UP胜出亏损=%.4f，DOWN胜出亏损=%.4f。建议：尽快平仓或对冲",
			-analysis.ProfitIfUpWins, -analysis.ProfitIfDownWins)
	}
	return "持仓分析完成"
}

func (ab *ArbitrageBrain) analysisLoop(ctx context.Context) {
	ticker := time.NewTicker(ab.updateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ab.stopChan:
			return
		case <-ticker.C:
			ab.updateAllMarkets()
		}
	}
}

func (ab *ArbitrageBrain) updateAllMarkets() {
	if ab.tradingService == nil {
		return
	}
	allPositions := ab.tradingService.GetOpenPositions()
	marketSet := make(map[string]*domain.Market)
	for _, p := range allPositions {
		if p == nil || !p.IsOpen() || p.Market == nil {
			continue
		}
		marketSet[p.MarketSlug] = p.Market
	}
	ab.mu.Lock()
	for marketSlug, market := range marketSet {
		analysis := ab.AnalyzeMarket(marketSlug, market)
		if analysis != nil {
			ab.analyses[marketSlug] = analysis
			ab.logAnalysis(analysis)
		}
	}
	ab.mu.Unlock()
}

func (ab *ArbitrageBrain) logAnalysis(analysis *ArbitrageAnalysis) {
	if analysis.IsPerfectArbitrage {
		arbLog.Infof("🧠 [%s] 完美套利锁定！UP=%.4f(成本%.4f) DOWN=%.4f(成本%.4f) | UP胜出收益=%.4f DOWN胜出收益=%.4f 最小收益=%.4f(%.2f%%)",
			analysis.MarketSlug,
			analysis.UpShares, analysis.UpCostUSDC,
			analysis.DownShares, analysis.DownCostUSDC,
			analysis.ProfitIfUpWins, analysis.ProfitIfDownWins,
			analysis.MinProfit, analysis.LockQuality*100)
	} else if analysis.IsLocked {
		arbLog.Infof("🧠 [%s] 完全锁定！UP=%.4f DOWN=%.4f | UP胜出收益=%.4f DOWN胜出收益=%.4f 最小收益=%.4f",
			analysis.MarketSlug,
			analysis.UpShares, analysis.DownShares,
			analysis.ProfitIfUpWins, analysis.ProfitIfDownWins,
			analysis.MinProfit)
	} else {
		arbLog.Debugf("🧠 [%s] %s | UP=%.4f DOWN=%.4f | UP胜出收益=%.4f DOWN胜出收益=%.4f",
			analysis.MarketSlug, analysis.Recommendation,
			analysis.UpShares, analysis.DownShares,
			analysis.ProfitIfUpWins, analysis.ProfitIfDownWins)
	}
}

func (ab *ArbitrageBrain) GetAnalysis(marketSlug string) *ArbitrageAnalysis {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	return ab.analyses[marketSlug]
}

func (ab *ArbitrageBrain) GetAllAnalyses() map[string]*ArbitrageAnalysis {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	result := make(map[string]*ArbitrageAnalysis)
	for k, v := range ab.analyses {
		result[k] = v
	}
	return result
}

func (ab *ArbitrageBrain) CalculatePotentialTradeRiskProfit(
	entryPriceCents, hedgePriceCents int,
	entrySize, hedgeSize float64,
	direction domain.TokenType,
) *PotentialTradeAnalysis {
	if ab.riskProfitCalculator == nil {
		return nil
	}
	return ab.riskProfitCalculator.CalculatePotentialTradeRiskProfit(entryPriceCents, hedgePriceCents, entrySize, hedgeSize, direction)
}

func (ab *ArbitrageBrain) CalculateCurrentPositionRiskProfit(
	ctx context.Context,
	market *domain.Market,
	positionState *PositionState,
) *CurrentPositionAnalysis {
	if ab.riskProfitCalculator == nil {
		return nil
	}
	return ab.riskProfitCalculator.CalculateCurrentPositionRiskProfit(ctx, market, positionState)
}

func (ab *ArbitrageBrain) CalculateCombinedRiskProfit(
	ctx context.Context,
	market *domain.Market,
	positionState *PositionState,
	potentialTrade *PotentialTradeAnalysis,
	direction domain.TokenType,
) *CurrentPositionAnalysis {
	if ab.riskProfitCalculator == nil {
		return nil
	}
	return ab.riskProfitCalculator.CalculateCombinedRiskProfit(ctx, market, positionState, potentialTrade, direction)
}

