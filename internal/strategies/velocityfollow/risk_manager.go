package velocityfollow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/sirupsen/logrus"
)

var riskLog = logrus.WithField("component", "risk_manager")

// RiskExposure 风险敞口信息
type RiskExposure struct {
	MarketSlug      string
	EntryOrderID    string
	EntryTokenType  domain.TokenType
	EntrySize       float64
	EntryPriceCents int
	EntryFilledTime time.Time
	HedgeOrderID    string
	HedgeStatus      domain.OrderStatus
	ExposureSeconds float64 // 风险敞口持续时间（秒）
	MaxLossCents     int    // 如果以当前ask价对冲，最大亏损（分）
}

// RiskManager 风险管理系统
type RiskManager struct {
	mu              sync.Mutex
	tradingService  *services.TradingService
	exposures       map[string]*RiskExposure // key=entryOrderID
	checkInterval   time.Duration
	aggressiveTimeout time.Duration
	maxAcceptableLossCents int
	enabled         bool
	stopChan        chan struct{}
	stopped         bool
}

// NewRiskManager 创建风险管理器
func NewRiskManager(ts *services.TradingService, cfg Config) *RiskManager {
	// 默认启用风险管理系统（如果未设置）
	enabled := cfg.RiskManagementEnabled
	if !enabled {
		// 如果未显式设置，默认启用
		enabled = true
	}

	rm := &RiskManager{
		tradingService:  ts,
		exposures:       make(map[string]*RiskExposure),
		enabled:         enabled,
		stopChan:        make(chan struct{}),
		stopped:         false,
		maxAcceptableLossCents: cfg.MaxAcceptableLossCents,
	}

	// 设置检查间隔
	if cfg.RiskManagementCheckIntervalMs > 0 {
		rm.checkInterval = time.Duration(cfg.RiskManagementCheckIntervalMs) * time.Millisecond
	} else {
		rm.checkInterval = 5 * time.Second // 默认 5 秒
	}

	// 设置激进对冲超时
	if cfg.AggressiveHedgeTimeoutSeconds > 0 {
		rm.aggressiveTimeout = time.Duration(cfg.AggressiveHedgeTimeoutSeconds) * time.Second
	} else {
		rm.aggressiveTimeout = 60 * time.Second // 默认 60 秒
	}

	// 设置最大可接受亏损
	if rm.maxAcceptableLossCents <= 0 {
		rm.maxAcceptableLossCents = 5 // 默认 5 分（0.05 USDC per share）
	}

	return rm
}

// Start 启动风险监控
func (rm *RiskManager) Start() {
	if !rm.enabled {
		return
	}

	rm.mu.Lock()
	if rm.stopped {
		rm.mu.Unlock()
		return
	}
	rm.mu.Unlock()

	go rm.monitorLoop()
	riskLog.Infof("✅ 风险管理系统已启动: checkInterval=%v aggressiveTimeout=%v maxLoss=%dc",
		rm.checkInterval, rm.aggressiveTimeout, rm.maxAcceptableLossCents)
}

// Stop 停止风险监控
func (rm *RiskManager) Stop() {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.stopped {
		return
	}
	rm.stopped = true
	close(rm.stopChan)
	riskLog.Infof("🛑 风险管理系统已停止")
}

// RegisterEntry 注册Entry订单（当Entry订单成交时调用）
func (rm *RiskManager) RegisterEntry(entryOrder *domain.Order, hedgeOrderID string) {
	if !rm.enabled || entryOrder == nil {
		return
	}

	if entryOrder.Status != domain.OrderStatusFilled {
		return
	}

	rm.mu.Lock()
	defer rm.mu.Unlock()

	exposure := &RiskExposure{
		MarketSlug:      entryOrder.MarketSlug,
		EntryOrderID:    entryOrder.OrderID,
		EntryTokenType:  entryOrder.TokenType,
		EntrySize:       entryOrder.FilledSize,
		EntryPriceCents: entryOrder.Price.ToCents(),
		EntryFilledTime: time.Now(),
		HedgeOrderID:    hedgeOrderID,
		HedgeStatus:     domain.OrderStatusPending,
	}

	rm.exposures[entryOrder.OrderID] = exposure
	riskLog.Infof("📝 注册风险敞口: entryOrderID=%s tokenType=%s size=%.4f price=%dc hedgeOrderID=%s",
		entryOrder.OrderID, entryOrder.TokenType, entryOrder.FilledSize, entryOrder.Price.ToCents(), hedgeOrderID)
}

// UpdateHedgeStatus 更新Hedge订单状态
func (rm *RiskManager) UpdateHedgeStatus(hedgeOrderID string, status domain.OrderStatus) {
	if !rm.enabled {
		return
	}

	rm.mu.Lock()
	defer rm.mu.Unlock()

	// 查找对应的风险敞口
	for entryID, exposure := range rm.exposures {
		if exposure.HedgeOrderID == hedgeOrderID {
			exposure.HedgeStatus = status
			if status == domain.OrderStatusFilled {
				// Hedge已成交，移除风险敞口
				delete(rm.exposures, entryID)
				riskLog.Infof("✅ 风险敞口已消除: entryOrderID=%s hedgeOrderID=%s", entryID, hedgeOrderID)
			} else {
				riskLog.Debugf("📊 更新Hedge状态: entryOrderID=%s hedgeOrderID=%s status=%s",
					entryID, hedgeOrderID, status)
			}
			return
		}
	}
}

// RemoveExposure 移除风险敞口（当Entry订单被平仓时）
func (rm *RiskManager) RemoveExposure(entryOrderID string) {
	if !rm.enabled {
		return
	}

	rm.mu.Lock()
	defer rm.mu.Unlock()

	if _, exists := rm.exposures[entryOrderID]; exists {
		delete(rm.exposures, entryOrderID)
		riskLog.Infof("🗑️ 移除风险敞口: entryOrderID=%s", entryOrderID)
	}
}

// GetExposures 获取所有风险敞口（用于日志/监控）
func (rm *RiskManager) GetExposures() []*RiskExposure {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	exposures := make([]*RiskExposure, 0, len(rm.exposures))
	for _, exp := range rm.exposures {
		exposures = append(exposures, exp)
	}
	return exposures
}

// monitorLoop 风险监控循环
func (rm *RiskManager) monitorLoop() {
	ticker := time.NewTicker(rm.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-rm.stopChan:
			return
		case <-ticker.C:
			rm.checkAndHandleRisks()
		}
	}
}

// checkAndHandleRisks 检查并处理风险
func (rm *RiskManager) checkAndHandleRisks() {
	if rm.tradingService == nil {
		return
	}

	rm.mu.Lock()
	exposures := make([]*RiskExposure, 0, len(rm.exposures))
	now := time.Now()
	for _, exp := range rm.exposures {
		// 更新风险敞口持续时间
		exp.ExposureSeconds = now.Sub(exp.EntryFilledTime).Seconds()

		// 检查Hedge订单状态
		if hedgeOrder, ok := rm.tradingService.GetOrder(exp.HedgeOrderID); ok && hedgeOrder != nil {
			exp.HedgeStatus = hedgeOrder.Status
			if hedgeOrder.Status == domain.OrderStatusFilled {
				// Hedge已成交，移除风险敞口
				delete(rm.exposures, exp.EntryOrderID)
				riskLog.Infof("✅ 风险敞口已消除（Hedge已成交）: entryOrderID=%s", exp.EntryOrderID)
				continue
			}
		}

		exposures = append(exposures, exp)
	}
	rm.mu.Unlock()

	// 处理每个风险敞口
	for _, exp := range exposures {
		rm.handleExposure(exp)
	}
}

// handleExposure 处理单个风险敞口
func (rm *RiskManager) handleExposure(exp *RiskExposure) {
	// 检查是否超过激进对冲超时时间
	if exp.ExposureSeconds < rm.aggressiveTimeout.Seconds() {
		return
	}

	// 检查Hedge订单是否仍然未成交
	if exp.HedgeStatus == domain.OrderStatusFilled {
		rm.mu.Lock()
		delete(rm.exposures, exp.EntryOrderID)
		rm.mu.Unlock()
		return
	}

	// 检查Hedge订单是否仍然存在且未成交
	hedgeOrder, ok := rm.tradingService.GetOrder(exp.HedgeOrderID)
	if !ok || hedgeOrder == nil {
		riskLog.Warnf("⚠️ Hedge订单不存在: hedgeOrderID=%s entryOrderID=%s", exp.HedgeOrderID, exp.EntryOrderID)
		return
	}

	if hedgeOrder.Status == domain.OrderStatusFilled {
		rm.mu.Lock()
		delete(rm.exposures, exp.EntryOrderID)
		rm.mu.Unlock()
		return
	}

	// 超过超时时间，触发激进对冲
	riskLog.Warnf("🚨 检测到风险敞口超时: entryOrderID=%s exposure=%.1f秒 hedgeOrderID=%s hedgeStatus=%s",
		exp.EntryOrderID, exp.ExposureSeconds, exp.HedgeOrderID, hedgeOrder.Status)

	// 在goroutine中执行激进对冲，避免阻塞监控循环
	go rm.aggressiveHedge(exp, hedgeOrder)
}

// aggressiveHedge 激进对冲：撤单并以ask价FAK吃单
func (rm *RiskManager) aggressiveHedge(exp *RiskExposure, hedgeOrder *domain.Order) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 获取market对象（从持仓中获取）
	positions := rm.tradingService.GetOpenPositionsForMarket(exp.MarketSlug)
	var market *domain.Market
	for _, p := range positions {
		if p != nil && p.Market != nil && p.Market.IsValid() {
			market = p.Market
			break
		}
	}

	if market == nil {
		riskLog.Errorf("❌ 无法获取market对象，无法执行激进对冲: marketSlug=%s", exp.MarketSlug)
		return
	}

	// 1. 取消旧的Hedge订单
	riskLog.Infof("🔄 取消旧Hedge订单: hedgeOrderID=%s", exp.HedgeOrderID)
	if err := rm.tradingService.CancelOrder(ctx, exp.HedgeOrderID); err != nil {
		riskLog.Warnf("⚠️ 取消Hedge订单失败: hedgeOrderID=%s err=%v", exp.HedgeOrderID, err)
		// 即使取消失败，也继续尝试（可能订单已经不存在）
	}

	// 等待一小段时间，确认撤单
	time.Sleep(500 * time.Millisecond)

	// 2. 获取当前订单簿价格
	_, yesAsk, _, noAsk, source, err := rm.tradingService.GetTopOfBook(ctx, market)
	if err != nil {
		riskLog.Errorf("❌ 获取订单簿价格失败，无法执行激进对冲: err=%v", err)
		return
	}

	// 确定对冲单的ask价格
	var hedgeAskPrice domain.Price
	var hedgeAssetID string
	if exp.EntryTokenType == domain.TokenTypeUp {
		// Entry是UP，Hedge是DOWN，使用noAsk
		hedgeAskPrice = noAsk
		hedgeAssetID = market.NoAssetID
	} else {
		// Entry是DOWN，Hedge是UP，使用yesAsk
		hedgeAskPrice = yesAsk
		hedgeAssetID = market.YesAssetID
	}

	if hedgeAskPrice.Pips <= 0 {
		riskLog.Errorf("❌ 订单簿ask价格无效，无法执行激进对冲: hedgeAskPrice=%d", hedgeAskPrice.Pips)
		return
	}

	hedgeAskCents := hedgeAskPrice.ToCents()

	// 3. 计算预期亏损
	// 亏损 = (Entry价格 + Hedge价格) - 100
	// 如果Entry是UP @ 70c，Hedge是DOWN @ 35c，总成本 = 70 + 35 = 105c，亏损 = 5c
	totalCostCents := exp.EntryPriceCents + hedgeAskCents
	expectedLossCents := totalCostCents - 100

	riskLog.Infof("💰 激进对冲价格分析: entryPrice=%dc hedgeAsk=%dc totalCost=%dc expectedLoss=%dc maxAcceptable=%dc",
		exp.EntryPriceCents, hedgeAskCents, totalCostCents, expectedLossCents, rm.maxAcceptableLossCents)

	// 4. 检查亏损是否在可接受范围内
	if expectedLossCents > rm.maxAcceptableLossCents {
		riskLog.Warnf("⚠️ 预期亏损超过最大可接受值，但仍执行对冲（避免更大风险）: expectedLoss=%dc maxAcceptable=%dc",
			expectedLossCents, rm.maxAcceptableLossCents)
		// 即使亏损超过阈值，也执行对冲（避免更大的风险敞口）
	}

	// 5. 以ask价下FAK买单
	riskLog.Infof("🚀 执行激进对冲: 以ask价FAK吃单 price=%dc size=%.4f source=%s expectedLoss=%dc",
		hedgeAskCents, exp.EntrySize, source, expectedLossCents)

	// 获取市场精度信息（从策略中获取，这里简化处理）
	fakHedgeOrder := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      hedgeAssetID,
		TokenType:    opposite(exp.EntryTokenType),
		Side:         types.SideBuy,
		Price:        hedgeAskPrice,
		Size:         exp.EntrySize,
		OrderType:    types.OrderTypeFAK,
		IsEntryOrder: false,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
	}

	// 设置关联的Entry订单ID
	entryOrderID := exp.EntryOrderID
	fakHedgeOrder.HedgeOrderID = &entryOrderID

	hedgeResult, err := rm.tradingService.PlaceOrder(ctx, fakHedgeOrder)
	if err != nil {
		riskLog.Errorf("❌ 激进对冲下单失败: err=%v (Entry已成交，存在风险敞口)", err)
		return
	}

	if hedgeResult == nil || hedgeResult.OrderID == "" {
		riskLog.Errorf("❌ 激进对冲下单失败: 订单ID为空")
		return
	}

	riskLog.Infof("✅ 激进对冲订单已提交: orderID=%s price=%dc size=%.4f expectedLoss=%dc",
		hedgeResult.OrderID, hedgeAskCents, exp.EntrySize, expectedLossCents)

	// 6. 更新风险敞口记录
	rm.mu.Lock()
	if exp, exists := rm.exposures[exp.EntryOrderID]; exists {
		exp.HedgeOrderID = hedgeResult.OrderID
		exp.HedgeStatus = hedgeResult.Status
		exp.MaxLossCents = expectedLossCents
	}
	rm.mu.Unlock()

	// 7. 如果FAK订单立即成交，移除风险敞口
	if hedgeResult.Status == domain.OrderStatusFilled {
		rm.mu.Lock()
		delete(rm.exposures, exp.EntryOrderID)
		rm.mu.Unlock()
		riskLog.Infof("✅ 激进对冲订单已立即成交，风险敞口已消除: orderID=%s expectedLoss=%dc",
			hedgeResult.OrderID, expectedLossCents)
	}
}

// CalculateRiskMetrics 计算风险指标（用于日志/监控）
func (rm *RiskManager) CalculateRiskMetrics(marketSlug string) (totalExposures int, totalExposureSize float64, avgExposureSeconds float64) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	totalExposures = len(rm.exposures)
	for _, exp := range rm.exposures {
		if marketSlug == "" || exp.MarketSlug == marketSlug {
			totalExposureSize += exp.EntrySize
			avgExposureSeconds += exp.ExposureSeconds
		}
	}

	if totalExposures > 0 {
		avgExposureSeconds /= float64(totalExposures)
	}

	return totalExposures, totalExposureSize, avgExposureSeconds
}

// GetRiskSummary 获取风险摘要（用于日志）
func (rm *RiskManager) GetRiskSummary() string {
	exposures := rm.GetExposures()
	if len(exposures) == 0 {
		return "无风险敞口"
	}

	var summary string
	for _, exp := range exposures {
		summary += fmt.Sprintf("entry=%s exposure=%.1fs hedge=%s(%s) ", 
			exp.EntryOrderID, exp.ExposureSeconds, exp.HedgeOrderID, exp.HedgeStatus)
	}
	return summary
}
