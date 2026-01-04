package velocityfollow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/sirupsen/logrus"
)

var brainLog = logrus.WithField("component", "arbitrage_brain")

// ArbitrageAnalysis 套利分析结果
type ArbitrageAnalysis struct {
	MarketSlug        string
	Timestamp         time.Time
	
	// 持仓信息
	UpShares          float64   // UP持仓数量
	DownShares        float64   // DOWN持仓数量
	UpCostUSDC        float64   // UP总成本（USDC）
	DownCostUSDC      float64   // DOWN总成本（USDC）
	TotalCostUSDC     float64   // 总成本（USDC）
	
	// 价格信息（当前订单簿）
	UpBidCents        int       // UP当前bid价（分）
	UpAskCents        int       // UP当前ask价（分）
	DownBidCents      int       // DOWN当前bid价（分）
	DownAskCents      int       // DOWN当前ask价（分）
	
	// 收益分析
	ProfitIfUpWins    float64   // 如果UP胜出（UP=1.0, DOWN=0.0）的收益（USDC）
	ProfitIfDownWins  float64   // 如果DOWN胜出（UP=0.0, DOWN=1.0）的收益（USDC）
	MinProfit         float64   // 最小收益（无论哪方胜出）
	MaxProfit         float64   // 最大收益（无论哪方胜出）
	
	// 套利状态
	IsLocked          bool      // 是否完全锁定（无论哪方胜出都盈利）
	IsPerfectArbitrage bool    // 是否完美套利（完全锁定且收益为正）
	LockQuality       float64   // 锁定质量：minProfit / totalCost（0-1，越高越好）
	
	// 风险指标
	ExposureRatio     float64   // 风险敞口比例：|upShares - downShares| / max(upShares, downShares)
	HedgedRatio       float64   // 对冲比例：min(upShares, downShares) / max(upShares, downShares)
	
	// 建议
	Recommendation    string    // 建议操作
}

// ArbitrageBrain 套利分析大脑模块
type ArbitrageBrain struct {
	mu             sync.Mutex
	tradingService *services.TradingService
	analyses       map[string]*ArbitrageAnalysis // key=marketSlug
	updateInterval time.Duration
	enabled        bool
	stopChan       chan struct{}
	stopped        bool
}

// NewArbitrageBrain 创建套利分析大脑
func NewArbitrageBrain(ts *services.TradingService, updateIntervalSeconds int) *ArbitrageBrain {
	interval := time.Duration(updateIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 10 * time.Second // 默认 10 秒
	}

	return &ArbitrageBrain{
		tradingService: ts,
		analyses:       make(map[string]*ArbitrageAnalysis),
		updateInterval: interval,
		enabled:        true,
		stopChan:       make(chan struct{}),
		stopped:        false,
	}
}

// Start 启动套利分析大脑
func (ab *ArbitrageBrain) Start() {
	if !ab.enabled {
		return
	}

	ab.mu.Lock()
	if ab.stopped {
		ab.mu.Unlock()
		return
	}
	ab.mu.Unlock()

	go ab.analysisLoop()
	brainLog.Infof("✅ 套利分析大脑已启动: updateInterval=%v", ab.updateInterval)
}

// Stop 停止套利分析大脑
func (ab *ArbitrageBrain) Stop() {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	if ab.stopped {
		return
	}
	ab.stopped = true
	close(ab.stopChan)
	brainLog.Infof("🛑 套利分析大脑已停止")
}

// AnalyzeMarket 分析指定市场的套利情况
func (ab *ArbitrageBrain) AnalyzeMarket(marketSlug string, market *domain.Market) *ArbitrageAnalysis {
	if ab.tradingService == nil || market == nil || !market.IsValid() {
		return nil
	}

	analysis := &ArbitrageAnalysis{
		MarketSlug: marketSlug,
		Timestamp:  time.Now(),
	}

	// 1. 计算持仓
	positions := ab.tradingService.GetOpenPositionsForMarket(marketSlug)
	var upShares, downShares, upCostUSDC, downCostUSDC float64

	for _, p := range positions {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}

		// 计算成本（优先使用最准确的数据源）
		var cost float64
		
		// 优先级1: 使用 AvgPrice（最准确，已经考虑了所有成交的加权平均）
		if p.AvgPrice > 0 {
			cost = p.AvgPrice * p.Size
		} else if p.TotalFilledSize > 0 && p.CostBasis > 0 {
			// 优先级2: 使用 CostBasis 计算平均价格
			avgPriceFromCostBasis := p.CostBasis / p.TotalFilledSize
			cost = avgPriceFromCostBasis * p.Size
		} else if p.EntryPrice.Pips > 0 {
			// 优先级3: 使用入场价格（向后兼容，但可能不准确）
			cost = p.EntryPrice.ToDecimal() * p.Size
		} else {
			// 无成本信息，记录警告并跳过（保守处理）
			brainLog.Warnf("⚠️ 持仓无成本信息，跳过利润计算: positionID=%s tokenType=%s size=%.4f costBasis=%.4f avgPrice=%.4f totalFilledSize=%.4f",
				p.ID, p.TokenType, p.Size, p.CostBasis, p.AvgPrice, p.TotalFilledSize)
			continue
		}
		
		// 验证成本计算的合理性
		if cost <= 0 {
			brainLog.Warnf("⚠️ 计算出的成本为0或负数，使用EntryPrice作为fallback: positionID=%s tokenType=%s size=%.4f cost=%.4f",
				p.ID, p.TokenType, p.Size, cost)
			if p.EntryPrice.Pips > 0 {
				cost = p.EntryPrice.ToDecimal() * p.Size
			} else {
				// 如果连EntryPrice都没有，跳过这个持仓
				brainLog.Warnf("⚠️ 持仓完全无价格信息，跳过: positionID=%s", p.ID)
				continue
			}
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

	// 2. 获取当前订单簿价格
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	yesBid, yesAsk, noBid, noAsk, _, err := ab.tradingService.GetTopOfBook(ctx, market)
	if err != nil {
		brainLog.Warnf("⚠️ 获取订单簿价格失败: market=%s err=%v", marketSlug, err)
		// 使用持仓的平均价格作为fallback
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

	// 3. 计算收益情况
	// 如果UP胜出：UP=1.0, DOWN=0.0
	// 收益 = UP持仓数量 * 1.0 - 总成本
	analysis.ProfitIfUpWins = upShares*1.0 - analysis.TotalCostUSDC

	// 如果DOWN胜出：UP=0.0, DOWN=1.0
	// 收益 = DOWN持仓数量 * 1.0 - 总成本
	analysis.ProfitIfDownWins = downShares*1.0 - analysis.TotalCostUSDC
	
	// 调试日志：输出成本计算详情
	if analysis.TotalCostUSDC > 0 {
		brainLog.Debugf("💰 [套利分析] market=%s UP持仓=%.4f(成本=%.4f) DOWN持仓=%.4f(成本=%.4f) 总成本=%.4f UP胜利润=%.4f DOWN胜利润=%.4f",
			marketSlug, upShares, upCostUSDC, downShares, downCostUSDC, analysis.TotalCostUSDC,
			analysis.ProfitIfUpWins, analysis.ProfitIfDownWins)
	} else {
		brainLog.Warnf("⚠️ [套利分析] 总成本为0，利润计算可能不准确: market=%s UP持仓=%.4f DOWN持仓=%.4f UP成本=%.4f DOWN成本=%.4f",
			marketSlug, upShares, downShares, upCostUSDC, downCostUSDC)
	}

	// 最小/最大收益
	analysis.MinProfit = min(analysis.ProfitIfUpWins, analysis.ProfitIfDownWins)
	analysis.MaxProfit = max(analysis.ProfitIfUpWins, analysis.ProfitIfDownWins)

	// 4. 判断是否完全锁定
	analysis.IsLocked = analysis.MinProfit > 0
	analysis.IsPerfectArbitrage = analysis.IsLocked && analysis.MinProfit > 0

	// 5. 计算锁定质量
	if analysis.TotalCostUSDC > 0 {
		analysis.LockQuality = analysis.MinProfit / analysis.TotalCostUSDC
	} else {
		analysis.LockQuality = 0
	}

	// 6. 计算风险指标
	maxShares := max(upShares, downShares)
	minShares := min(upShares, downShares)
	
	if maxShares > 0 {
		analysis.ExposureRatio = abs(upShares-downShares) / maxShares
		analysis.HedgedRatio = minShares / maxShares
	} else {
		analysis.ExposureRatio = 0
		analysis.HedgedRatio = 0
	}

	// 7. 生成建议
	analysis.Recommendation = ab.generateRecommendation(analysis)

	return analysis
}

// generateRecommendation 生成操作建议
func (ab *ArbitrageBrain) generateRecommendation(analysis *ArbitrageAnalysis) string {
	if analysis.UpShares == 0 && analysis.DownShares == 0 {
		return "无持仓"
	}

	if analysis.IsPerfectArbitrage {
		return fmt.Sprintf("✅ 完美套利锁定！无论哪方胜出都盈利: minProfit=%.4f USDC (%.2f%%)",
			analysis.MinProfit, analysis.LockQuality*100)
	}

	if analysis.IsLocked {
		return fmt.Sprintf("✅ 完全锁定！无论哪方胜出都盈利: minProfit=%.4f USDC",
			analysis.MinProfit)
	}

	// 未完全锁定，分析风险
	if analysis.ProfitIfUpWins > 0 && analysis.ProfitIfDownWins < 0 {
		// UP胜出盈利，DOWN胜出亏损
		loss := -analysis.ProfitIfDownWins
		return fmt.Sprintf("⚠️ 风险敞口：UP胜出盈利(%.4f)，DOWN胜出亏损(%.4f)。建议：增加DOWN持仓对冲",
			analysis.ProfitIfUpWins, loss)
	}

	if analysis.ProfitIfDownWins > 0 && analysis.ProfitIfUpWins < 0 {
		// DOWN胜出盈利，UP胜出亏损
		loss := -analysis.ProfitIfUpWins
		return fmt.Sprintf("⚠️ 风险敞口：DOWN胜出盈利(%.4f)，UP胜出亏损(%.4f)。建议：增加UP持仓对冲",
			analysis.ProfitIfDownWins, loss)
	}

	if analysis.ProfitIfUpWins < 0 && analysis.ProfitIfDownWins < 0 {
		// 无论哪方胜出都亏损
		return fmt.Sprintf("❌ 风险：无论哪方胜出都亏损！UP胜出亏损=%.4f，DOWN胜出亏损=%.4f。建议：尽快平仓或对冲",
			-analysis.ProfitIfUpWins, -analysis.ProfitIfDownWins)
	}

	return "持仓分析完成"
}

// analysisLoop 分析循环
func (ab *ArbitrageBrain) analysisLoop() {
	ticker := time.NewTicker(ab.updateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ab.stopChan:
			return
		case <-ticker.C:
			ab.updateAllMarkets()
		}
	}
}

// updateAllMarkets 更新所有市场的分析
func (ab *ArbitrageBrain) updateAllMarkets() {
	if ab.tradingService == nil {
		return
	}

	// 获取所有持仓，提取marketSlug
	allPositions := ab.tradingService.GetOpenPositions()
	marketSet := make(map[string]*domain.Market)

	for _, p := range allPositions {
		if p == nil || !p.IsOpen() || p.Market == nil {
			continue
		}
		marketSet[p.MarketSlug] = p.Market
	}

	// 分析每个市场
	ab.mu.Lock()
	for marketSlug, market := range marketSet {
		analysis := ab.AnalyzeMarket(marketSlug, market)
		if analysis != nil {
			ab.analyses[marketSlug] = analysis
			
			// 打印分析结果（带限流）
			ab.logAnalysis(analysis)
		}
	}
	ab.mu.Unlock()
}

// logAnalysis 打印分析结果（带限流，避免刷屏）
func (ab *ArbitrageBrain) logAnalysis(analysis *ArbitrageAnalysis) {
	// 只在关键状态变化时打印，或定期打印
	// 这里简化处理，每次更新都打印（实际可以添加限流逻辑）
	
	if analysis.IsPerfectArbitrage {
		brainLog.Infof("🧠 [%s] 完美套利锁定！UP=%.4f(成本%.4f) DOWN=%.4f(成本%.4f) | UP胜出收益=%.4f DOWN胜出收益=%.4f 最小收益=%.4f(%.2f%%)",
			analysis.MarketSlug,
			analysis.UpShares, analysis.UpCostUSDC,
			analysis.DownShares, analysis.DownCostUSDC,
			analysis.ProfitIfUpWins, analysis.ProfitIfDownWins,
			analysis.MinProfit, analysis.LockQuality*100)
	} else if analysis.IsLocked {
		brainLog.Infof("🧠 [%s] 完全锁定！UP=%.4f DOWN=%.4f | UP胜出收益=%.4f DOWN胜出收益=%.4f 最小收益=%.4f",
			analysis.MarketSlug,
			analysis.UpShares, analysis.DownShares,
			analysis.ProfitIfUpWins, analysis.ProfitIfDownWins,
			analysis.MinProfit)
	} else {
		brainLog.Debugf("🧠 [%s] %s | UP=%.4f DOWN=%.4f | UP胜出收益=%.4f DOWN胜出收益=%.4f",
			analysis.MarketSlug, analysis.Recommendation,
			analysis.UpShares, analysis.DownShares,
			analysis.ProfitIfUpWins, analysis.ProfitIfDownWins)
	}
}

// GetAnalysis 获取指定市场的分析结果
func (ab *ArbitrageBrain) GetAnalysis(marketSlug string) *ArbitrageAnalysis {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	return ab.analyses[marketSlug]
}

// GetAllAnalyses 获取所有市场的分析结果
func (ab *ArbitrageBrain) GetAllAnalyses() map[string]*ArbitrageAnalysis {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	
	result := make(map[string]*ArbitrageAnalysis)
	for k, v := range ab.analyses {
		result[k] = v
	}
	return result
}

// GetSummary 获取摘要信息
func (ab *ArbitrageBrain) GetSummary() string {
	analyses := ab.GetAllAnalyses()
	if len(analyses) == 0 {
		return "无持仓分析"
	}

	var summary string
	perfectCount := 0
	lockedCount := 0
	totalMinProfit := 0.0

	for _, analysis := range analyses {
		if analysis.IsPerfectArbitrage {
			perfectCount++
		}
		if analysis.IsLocked {
			lockedCount++
		}
		totalMinProfit += analysis.MinProfit
	}

	summary = fmt.Sprintf("市场数=%d 完美套利=%d 完全锁定=%d 总最小收益=%.4f USDC",
		len(analyses), perfectCount, lockedCount, totalMinProfit)

	return summary
}

// 辅助函数
func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
