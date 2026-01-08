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

var pmLog = logrus.WithField("module", "position_monitor")

// PositionMonitor 实时持仓监控器
// 负责实时分析持仓、盈利亏损情况，并在持仓不平衡时自动触发对冲
type PositionMonitor struct {
	tradingService *services.TradingService
	config         ConfigInterface
	riskCalculator *RiskProfitCalculator

	mu sync.RWMutex
	// 监控状态
	enabled              bool
	checkInterval        time.Duration
	maxExposureThreshold float64 // 最大允许的持仓不平衡阈值（shares）
	maxExposureRatio     float64 // 最大允许的持仓不平衡比例（0-1）
	maxLossCents         int     // 最大允许亏损（分）
	minHedgeSize         float64 // 小于该差异时不做“平衡性对冲”（除非触发亏损风险）
	hedgeCooldown        time.Duration

	// 状态跟踪
	lastCheckTime    time.Time
	lastAnalysis     *PositionAnalysis
	totalAutoHedges  int // 自动对冲次数
	lastAutoHedgeTime time.Time

	// 回调函数：当检测到风险需要对冲时调用
	onHedgeRequired func(ctx context.Context, market *domain.Market, analysis *PositionAnalysis) error
}

// PositionAnalysis 持仓分析结果
type PositionAnalysis struct {
	MarketSlug string
	Timestamp  time.Time

	// 持仓信息
	UpSize       float64
	DownSize     float64
	UpCost       float64
	DownCost     float64
	TotalCost    float64
	SizeDiff     float64 // UP 和 DOWN 的差异
	ExposureRatio float64 // 不平衡比例 (0-1)

	// 价格信息
	UpBidCents   int
	UpAskCents   int
	DownBidCents int
	DownAskCents int

	// 盈利亏损分析
	ProfitIfUpWins   float64
	ProfitIfDownWins  float64
	MinProfit         float64
	MaxProfit         float64
	CurrentLossCents  int // 当前亏损（分），基于当前市场价格

	// 风险状态
	IsHedged        bool
	IsAtRisk        bool // 是否处于风险状态
	RiskReason      string
	RequiresHedge   bool // 是否需要自动对冲
	HedgeDirection  domain.TokenType // 需要对冲的方向
	HedgeSize       float64 // 需要对冲的数量
}

// NewPositionMonitor 创建持仓监控器
func NewPositionMonitor(ts *services.TradingService, cfg ConfigInterface) *PositionMonitor {
	if ts == nil || cfg == nil {
		return nil
	}

	enabled := true
	if !cfg.GetPositionMonitorEnabled() {
		enabled = false
	}
	interval := 2 * time.Second
	if ms := cfg.GetPositionMonitorCheckIntervalMs(); ms > 0 {
		interval = time.Duration(ms) * time.Millisecond
	}
	maxExposureThreshold := cfg.GetPositionMonitorMaxExposureThreshold()
	if maxExposureThreshold <= 0 {
		maxExposureThreshold = 1.0
	}
	maxExposureRatio := cfg.GetPositionMonitorMaxExposureRatio()
	if maxExposureRatio <= 0 {
		maxExposureRatio = 0.1
	}
	maxLossCents := cfg.GetPositionMonitorMaxLossCents()
	if maxLossCents <= 0 {
		maxLossCents = 50
	}
	minHedge := cfg.GetPositionMonitorMinHedgeSize()
	if minHedge < 0 {
		minHedge = 0
	}
	cooldown := time.Duration(cfg.GetPositionMonitorCooldownMs()) * time.Millisecond
	if cooldown < 0 {
		cooldown = 0
	}

	pm := &PositionMonitor{
		tradingService:  ts,
		config:          cfg,
		riskCalculator:  NewRiskProfitCalculator(ts),
		enabled:         enabled,
		checkInterval:   interval,
		maxExposureThreshold: maxExposureThreshold,
		maxExposureRatio:     maxExposureRatio,
		maxLossCents:         maxLossCents,
		minHedgeSize:         minHedge,
		hedgeCooldown:        cooldown,
	}

	return pm
}

// SetHedgeCallback 设置对冲回调函数
func (pm *PositionMonitor) SetHedgeCallback(fn func(ctx context.Context, market *domain.Market, analysis *PositionAnalysis) error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.onHedgeRequired = fn
}

// AnalyzePosition 分析当前持仓
func (pm *PositionMonitor) AnalyzePosition(ctx context.Context, market *domain.Market) (*PositionAnalysis, error) {
	if pm == nil || pm.tradingService == nil || market == nil {
		return nil, fmt.Errorf("参数无效")
	}

	// 获取持仓状态
	positions := pm.tradingService.GetOpenPositionsForMarket(market.Slug)
	if len(positions) == 0 {
		return &PositionAnalysis{
			MarketSlug: market.Slug,
			Timestamp:  time.Now(),
			IsHedged:   true,
		}, nil
	}

	// 计算持仓汇总
	var upSize, downSize, upCost, downCost float64
	for _, pos := range positions {
		if pos == nil || !pos.IsOpen() || pos.Size <= 0 {
			continue
		}
		if pos.TokenType == domain.TokenTypeUp {
			upSize += pos.Size
			upCost += pos.CostBasis
		} else if pos.TokenType == domain.TokenTypeDown {
			downSize += pos.Size
			downCost += pos.CostBasis
		}
	}

	// 获取当前市场价格
	yesBid, yesAsk, noBid, noAsk, _, err := pm.tradingService.GetTopOfBook(ctx, market)
	if err != nil {
		pmLog.Warnf("⚠️ 获取订单簿价格失败: market=%s err=%v", market.Slug, err)
		// 使用持仓平均价格作为后备
		yesAsk = domain.Price{Pips: 5000} // 默认 50 cents
		noAsk = domain.Price{Pips: 5000}
	}

	yesBidCents := yesBid.ToCents()
	yesAskCents := yesAsk.ToCents()
	noBidCents := noBid.ToCents()
	noAskCents := noAsk.ToCents()

	// 计算盈利亏损
	profitIfUpWins := upSize*1.0 - upCost - downCost
	profitIfDownWins := downSize*1.0 - upCost - downCost
	minProfit := math.Min(profitIfUpWins, profitIfDownWins)
	maxProfit := math.Max(profitIfUpWins, profitIfDownWins)

	// 计算当前亏损（基于当前市场价格）
	// 如果 UP 更多，当前亏损 = (upSize - downSize) * (100 - noAskCents) / 100
	// 如果 DOWN 更多，当前亏损 = (downSize - upSize) * (100 - yesAskCents) / 100
	currentLossCents := 0
	if upSize > downSize {
		unhedgedSize := upSize - downSize
		if noAskCents > 0 {
			// 需要买 NO 来对冲，成本 = unhedgedSize * noAskCents / 100
			// 当前亏损 = 已投入成本 - 如果现在对冲的成本
			currentLossCents = int(unhedgedSize * float64(noAskCents) / 100.0 * 100.0)
		}
	} else if downSize > upSize {
		unhedgedSize := downSize - upSize
		if yesAskCents > 0 {
			currentLossCents = int(unhedgedSize * float64(yesAskCents) / 100.0 * 100.0)
		}
	}

	// 计算不平衡
	sizeDiff := math.Abs(upSize - downSize)
	maxSize := math.Max(upSize, downSize)
	exposureRatio := 0.0
	if maxSize > 0 {
		exposureRatio = sizeDiff / maxSize
	}

	// 判断是否对冲
	isHedged := upSize > 0 && downSize > 0 && sizeDiff < 1.0

	// 判断是否处于风险状态
	isAtRisk := false
	riskReason := ""
	requiresHedge := false
	hedgeDirection := domain.TokenTypeUp
	hedgeSize := 0.0

	pm.mu.RLock()
	maxExposureThreshold := pm.maxExposureThreshold
	maxExposureRatio := pm.maxExposureRatio
	maxLossCents := pm.maxLossCents
	pm.mu.RUnlock()

	if !isHedged {
		if sizeDiff > maxExposureThreshold || exposureRatio > maxExposureRatio {
			isAtRisk = true
			riskReason = fmt.Sprintf("持仓不平衡: diff=%.4f ratio=%.2f%%", sizeDiff, exposureRatio*100)
			requiresHedge = true

			if upSize > downSize {
				hedgeDirection = domain.TokenTypeDown // 需要买 DOWN 来对冲
				hedgeSize = sizeDiff
			} else if downSize > upSize {
				hedgeDirection = domain.TokenTypeUp // 需要买 UP 来对冲
				hedgeSize = sizeDiff
			}
		}
	}

	// 检查当前亏损是否超过阈值
	if currentLossCents > maxLossCents {
		isAtRisk = true
		if riskReason != "" {
			riskReason += fmt.Sprintf("; 当前亏损=%dc", currentLossCents)
		} else {
			riskReason = fmt.Sprintf("当前亏损过大: %dc", currentLossCents)
		}
		requiresHedge = true
	}

	analysis := &PositionAnalysis{
		MarketSlug:      market.Slug,
		Timestamp:       time.Now(),
		UpSize:          upSize,
		DownSize:        downSize,
		UpCost:          upCost,
		DownCost:        downCost,
		TotalCost:       upCost + downCost,
		SizeDiff:        sizeDiff,
		ExposureRatio:   exposureRatio,
		UpBidCents:      yesBidCents,
		UpAskCents:      yesAskCents,
		DownBidCents:    noBidCents,
		DownAskCents:    noAskCents,
		ProfitIfUpWins:  profitIfUpWins,
		ProfitIfDownWins: profitIfDownWins,
		MinProfit:       minProfit,
		MaxProfit:       maxProfit,
		CurrentLossCents: currentLossCents,
		IsHedged:        isHedged,
		IsAtRisk:        isAtRisk,
		RiskReason:      riskReason,
		RequiresHedge:   requiresHedge,
		HedgeDirection:  hedgeDirection,
		HedgeSize:       hedgeSize,
	}

	pm.mu.Lock()
	pm.lastCheckTime = time.Now()
	pm.lastAnalysis = analysis
	pm.mu.Unlock()

	return analysis, nil
}

// CheckAndHedge 检查持仓并在需要时触发对冲
func (pm *PositionMonitor) CheckAndHedge(ctx context.Context, market *domain.Market) error {
	if pm == nil || !pm.enabled {
		return nil
	}

	// 防抖：两次自动对冲之间必须间隔 hedgeCooldown
	pm.mu.RLock()
	cooldown := pm.hedgeCooldown
	lastHedge := pm.lastAutoHedgeTime
	pm.mu.RUnlock()
	if cooldown > 0 && !lastHedge.IsZero() && time.Since(lastHedge) < cooldown {
		return nil
	}

	analysis, err := pm.AnalyzePosition(ctx, market)
	if err != nil {
		return err
	}

	if analysis == nil {
		return nil
	}

	// 如果检测到风险且需要对冲
	if analysis.RequiresHedge && analysis.HedgeSize > 0 {
		// 小差异不做“平衡性对冲”（避免碎单抖动），除非已经触发亏损风险。
		pm.mu.RLock()
		minHedge := pm.minHedgeSize
		maxLoss := pm.maxLossCents
		pm.mu.RUnlock()
		if minHedge > 0 && analysis.HedgeSize < minHedge {
			// 如果当前亏损未超过阈值，认为属于“平衡性抖动”，跳过
			if analysis.CurrentLossCents <= maxLoss {
				pmLog.Debugf("⏭️ [PositionMonitor] 差异小于最小对冲阈值，跳过自动对冲: market=%s hedgeSize=%.4f minHedge=%.4f loss=%dc<=%dc",
					market.Slug, analysis.HedgeSize, minHedge, analysis.CurrentLossCents, maxLoss)
				return nil
			}
		}

		pmLog.Warnf("🚨 [PositionMonitor] 检测到持仓风险，需要自动对冲: market=%s reason=%s hedgeDirection=%s hedgeSize=%.4f",
			market.Slug, analysis.RiskReason, analysis.HedgeDirection, analysis.HedgeSize)

		pm.mu.RLock()
		onHedgeRequired := pm.onHedgeRequired
		pm.mu.RUnlock()

		if onHedgeRequired != nil {
			if err := onHedgeRequired(ctx, market, analysis); err != nil {
				pmLog.Errorf("❌ [PositionMonitor] 自动对冲失败: market=%s err=%v", market.Slug, err)
				return err
			}

			pm.mu.Lock()
			pm.totalAutoHedges++
			pm.lastAutoHedgeTime = time.Now()
			pm.mu.Unlock()

			pmLog.Infof("✅ [PositionMonitor] 自动对冲已触发: market=%s hedgeDirection=%s hedgeSize=%.4f totalAutoHedges=%d",
				market.Slug, analysis.HedgeDirection, analysis.HedgeSize, pm.totalAutoHedges)
		} else {
			pmLog.Warnf("⚠️ [PositionMonitor] 检测到风险但未设置对冲回调函数: market=%s", market.Slug)
		}
	} else if analysis.IsAtRisk {
		pmLog.Debugf("⚠️ [PositionMonitor] 持仓处于风险状态但暂不需要对冲: market=%s reason=%s",
			market.Slug, analysis.RiskReason)
	}

	return nil
}

// GetLastAnalysis 获取最后一次分析结果
func (pm *PositionMonitor) GetLastAnalysis() *PositionAnalysis {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.lastAnalysis
}

// GetStats 获取监控统计信息
func (pm *PositionMonitor) GetStats() (totalAutoHedges int, lastAutoHedgeTime time.Time) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.totalAutoHedges, pm.lastAutoHedgeTime
}

// SetMaxExposureThreshold 设置最大允许的持仓不平衡阈值
func (pm *PositionMonitor) SetMaxExposureThreshold(threshold float64) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.maxExposureThreshold = threshold
}

// SetMaxExposureRatio 设置最大允许的持仓不平衡比例
func (pm *PositionMonitor) SetMaxExposureRatio(ratio float64) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.maxExposureRatio = ratio
}

// SetMaxLossCents 设置最大允许亏损
func (pm *PositionMonitor) SetMaxLossCents(cents int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.maxLossCents = cents
}
