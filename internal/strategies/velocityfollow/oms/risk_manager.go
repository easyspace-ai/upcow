package oms

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

var riskLog = logrus.WithField("module", "risk_manager")

// RiskExposure 风险敞口信息
type RiskExposure struct {
	MarketSlug      string
	EntryOrderID    string
	EntryTokenType  domain.TokenType
	EntrySize       float64
	EntryPriceCents int
	EntryFilledTime time.Time
	HedgeOrderID    string
	HedgeStatus     domain.OrderStatus
	ExposureSeconds float64 // 风险敞口持续时间（秒）
	MaxLossCents     int    // 如果以当前ask价对冲，最大亏损（分）
	AggressiveHedgeTriggered bool   // 是否已触发激进对冲（防止重复触发）
	AggressiveHedgeTime       time.Time // 激进对冲触发时间
}

// RiskManager 风险管理系统
type RiskManager struct {
	mu                   sync.Mutex
	tradingService       *services.TradingService
	oms                  *OMS // 添加 OMS 引用，用于注册 pendingHedges
	exposures            map[string]*RiskExposure // key=entryOrderID
	checkInterval        time.Duration
	aggressiveTimeout    time.Duration
	maxAcceptableLossCents int
	enabled              bool
	stopChan             chan struct{}
	stopped              bool
	config               ConfigInterface
	
	// 状态跟踪（用于 UI 显示）
	currentAction      string // "idle" | "canceling" | "aggressive_hedging"
	currentActionEntry string
	currentActionHedge string
	currentActionTime  time.Time
	currentActionDesc  string
	totalAggressiveHedges int // 总激进对冲次数
}

// NewRiskManager 创建风险管理器
func NewRiskManager(ts *services.TradingService, cfg ConfigInterface) *RiskManager {
	// 默认启用风险管理系统（如果未设置）
	enabled := cfg.GetRiskManagementEnabled()
	if !enabled {
		// 如果未显式设置，默认启用
		enabled = true
	}

	rm := &RiskManager{
		tradingService:        ts,
		exposures:             make(map[string]*RiskExposure),
		enabled:               enabled,
		stopChan:              make(chan struct{}),
		stopped:               false,
		maxAcceptableLossCents: cfg.GetMaxAcceptableLossCents(),
		config:                cfg,
	}

	// 设置检查间隔
	if cfg.GetRiskManagementCheckIntervalMs() > 0 {
		rm.checkInterval = time.Duration(cfg.GetRiskManagementCheckIntervalMs()) * time.Millisecond
	} else {
		rm.checkInterval = 5 * time.Second // 默认 5 秒
	}

	// 设置激进对冲超时
	if cfg.GetAggressiveHedgeTimeoutSeconds() > 0 {
		rm.aggressiveTimeout = time.Duration(cfg.GetAggressiveHedgeTimeoutSeconds()) * time.Second
	} else {
		rm.aggressiveTimeout = 60 * time.Second // 默认 60 秒
	}

	// 设置最大可接受亏损
	if rm.maxAcceptableLossCents <= 0 {
		rm.maxAcceptableLossCents = 5 // 默认 5 分（0.05 USDC per share）
	}

	return rm
}

// SetOMS 设置 OMS 引用（用于注册 pendingHedges）
func (rm *RiskManager) SetOMS(oms *OMS) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.oms = oms
}

// Start 启动风险监控
func (rm *RiskManager) Start(ctx context.Context) {
	if !rm.enabled {
		return
	}

	rm.mu.Lock()
	if rm.stopped {
		rm.mu.Unlock()
		return
	}
	rm.mu.Unlock()

	go rm.monitorLoop(ctx)
	riskLog.Debugf("✅ 风险管理系统已启动: checkInterval=%v aggressiveTimeout=%v maxLoss=%dc",
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
	riskLog.Debugf("🛑 风险管理系统已停止")
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
				riskLog.Debugf("✅ 风险敞口已消除: entryOrderID=%s hedgeOrderID=%s", entryID, hedgeOrderID)
			} else {
				riskLog.Debugf("📊 更新Hedge状态: entryOrderID=%s hedgeOrderID=%s status=%s",
					entryID, hedgeOrderID, status)
			}
			return
		}
	}
}

// UpdateHedgeOrderID 更新已注册风险敞口的 Hedge 订单ID（用于时序问题修复）
func (rm *RiskManager) UpdateHedgeOrderID(entryOrderID, hedgeOrderID string) {
	if !rm.enabled || entryOrderID == "" || hedgeOrderID == "" {
		return
	}

	rm.mu.Lock()
	defer rm.mu.Unlock()

	if exp, exists := rm.exposures[entryOrderID]; exists {
		if exp.HedgeOrderID == "" {
			exp.HedgeOrderID = hedgeOrderID
			exp.HedgeStatus = domain.OrderStatusPending
			riskLog.Debugf("🔄 更新风险敞口的 Hedge 订单ID: entryOrderID=%s hedgeOrderID=%s", entryOrderID, hedgeOrderID)
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
		riskLog.Debugf("🗑️ 移除风险敞口: entryOrderID=%s", entryOrderID)
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
func (rm *RiskManager) monitorLoop(ctx context.Context) {
	ticker := time.NewTicker(rm.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-rm.stopChan:
			return
		case <-ticker.C:
			rm.checkAndHandleRisks(ctx)
		}
	}
}

// checkAndHandleRisks 检查并处理风险
func (rm *RiskManager) checkAndHandleRisks(ctx context.Context) {
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
				riskLog.Debugf("✅ 风险敞口已消除（Hedge已成交）: entryOrderID=%s", exp.EntryOrderID)
				continue
			}
		}

		exposures = append(exposures, exp)
	}
	rm.mu.Unlock()

	// 处理每个风险敞口
	for _, exp := range exposures {
		rm.handleExposure(ctx, exp)
	}
}

// handleExposure 处理单个风险敞口
func (rm *RiskManager) handleExposure(ctx context.Context, exp *RiskExposure) {
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
	// 如果 hedgeOrderID 为空，说明 Entry 成交时还没有创建 Hedge 订单（时序问题）
	// 这种情况下应该立即触发激进对冲，因为没有对冲单，风险更大
	if exp.HedgeOrderID == "" {
		riskLog.Warnf("🚨 检测到风险敞口且无对冲单: entryOrderID=%s exposure=%.1f秒（Entry成交时Hedge订单尚未创建）",
			exp.EntryOrderID, exp.ExposureSeconds)
		
		// 获取market对象（多种方式，带重试和降级方案）
		market, source := rm.getMarketForAggressiveHedge(ctx, exp, nil)
		if market == nil {
			riskLog.Errorf("❌ 无法获取market对象，无法执行激进对冲: marketSlug=%s source=%s", exp.MarketSlug, source)
			return
		}
		
		riskLog.Infof("✅ [获取Market] 成功获取market对象: marketSlug=%s source=%s", exp.MarketSlug, source)
		
		// 创建一个临时的 hedgeOrder 对象用于激进对冲（实际上没有订单，直接下FAK）
		// 确定对冲单的资产和方向
		var hedgeAssetID string
		var hedgeTokenType domain.TokenType
		if exp.EntryTokenType == domain.TokenTypeUp {
			hedgeAssetID = market.NoAssetID
			hedgeTokenType = domain.TokenTypeDown
		} else {
			hedgeAssetID = market.YesAssetID
			hedgeTokenType = domain.TokenTypeUp
		}
		
		// 创建一个临时的订单对象（仅用于传递信息）
		dummyHedgeOrder := &domain.Order{
			OrderID:     "", // 空订单ID
			MarketSlug:  market.Slug,
			AssetID:     hedgeAssetID,
			TokenType:   hedgeTokenType,
			Status:      domain.OrderStatusPending, // 标记为未成交
		}
		
		// 立即触发激进对冲
		go rm.aggressiveHedge(ctx, exp, dummyHedgeOrder)
		return
	}
	
	// 检查是否已经触发过激进对冲（防止重复触发）
	rm.mu.Lock()
	alreadyTriggered := exp.AggressiveHedgeTriggered
	rm.mu.Unlock()
	
	if alreadyTriggered {
		// 已经触发过，检查是否已经完成（通过检查新的对冲订单状态）
		if exp.HedgeOrderID != "" {
			if newHedgeOrder, ok := rm.tradingService.GetOrder(exp.HedgeOrderID); ok && newHedgeOrder != nil {
				if newHedgeOrder.Status == domain.OrderStatusFilled {
					// 新的对冲订单已成交，移除风险敞口
					rm.mu.Lock()
					delete(rm.exposures, exp.EntryOrderID)
					rm.mu.Unlock()
					riskLog.Debugf("✅ 激进对冲订单已成交，风险敞口已消除: entryOrderID=%s hedgeOrderID=%s", 
						exp.EntryOrderID, exp.HedgeOrderID)
					return
				}
			}
		}
		// 已触发但未完成，等待中
		return
	}

	hedgeOrder, ok := rm.tradingService.GetOrder(exp.HedgeOrderID)
	if !ok || hedgeOrder == nil {
		// 关键修复：如果 Hedge 订单不存在，也应该触发激进对冲（订单可能已被取消或不存在）
		riskLog.Warnf("🚨 Hedge订单不存在，触发激进对冲: hedgeOrderID=%s entryOrderID=%s exposure=%.1f秒",
			exp.HedgeOrderID, exp.EntryOrderID, exp.ExposureSeconds)
		
		// 获取market对象（多种方式，带重试和降级方案）
		market, source := rm.getMarketForAggressiveHedge(ctx, exp, nil)
		if market == nil {
			riskLog.Errorf("❌ 无法获取market对象，无法执行激进对冲: marketSlug=%s source=%s", exp.MarketSlug, source)
			return
		}
		
		riskLog.Infof("✅ [获取Market] 成功获取market对象: marketSlug=%s source=%s", exp.MarketSlug, source)
		
		// 创建一个临时的 hedgeOrder 对象用于激进对冲
		var hedgeAssetID string
		var hedgeTokenType domain.TokenType
		if exp.EntryTokenType == domain.TokenTypeUp {
			hedgeAssetID = market.NoAssetID
			hedgeTokenType = domain.TokenTypeDown
		} else {
			hedgeAssetID = market.YesAssetID
			hedgeTokenType = domain.TokenTypeUp
		}
		
		dummyHedgeOrder := &domain.Order{
			OrderID:     exp.HedgeOrderID, // 保留原订单ID（可能用于取消）
			MarketSlug:  market.Slug,
			AssetID:     hedgeAssetID,
			TokenType:   hedgeTokenType,
			Status:      domain.OrderStatusPending,
		}
		
		// 标记已触发，防止重复
		rm.mu.Lock()
		exp.AggressiveHedgeTriggered = true
		exp.AggressiveHedgeTime = time.Now()
		rm.mu.Unlock()
		
		// 触发激进对冲
		go rm.aggressiveHedge(ctx, exp, dummyHedgeOrder)
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

	// 标记已触发，防止重复
	rm.mu.Lock()
	exp.AggressiveHedgeTriggered = true
	exp.AggressiveHedgeTime = time.Now()
	rm.mu.Unlock()

	// 在goroutine中执行激进对冲，避免阻塞监控循环
	go rm.aggressiveHedge(ctx, exp, hedgeOrder)
}

// getMarketForAggressiveHedge 获取market对象（多种方式，带重试和降级方案）
func (rm *RiskManager) getMarketForAggressiveHedge(ctx context.Context, exp *RiskExposure, hedgeOrder *domain.Order) (*domain.Market, string) {
	// 重试配置
	maxRetries := 3
	retryDelays := []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second}
	
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 等待重试
			select {
			case <-ctx.Done():
				return nil, "context_cancelled"
			case <-time.After(retryDelays[attempt-1]):
				riskLog.Debugf("🔄 [获取Market] 重试第%d次: marketSlug=%s", attempt, exp.MarketSlug)
			}
		}
		
		// 方式1：从持仓中获取（现有方式，最可靠）
		positions := rm.tradingService.GetOpenPositionsForMarket(exp.MarketSlug)
		for _, p := range positions {
			if p != nil && p.Market != nil && p.Market.IsValid() {
				riskLog.Debugf("✅ [获取Market] 方式1成功（从持仓）: marketSlug=%s attempt=%d", exp.MarketSlug, attempt)
				return p.Market, "from_positions"
			}
		}
		
		// 方式2：从Entry订单中获取market信息
		if entryOrder, ok := rm.tradingService.GetOrder(exp.EntryOrderID); ok && entryOrder != nil {
			if entryOrder.MarketSlug != "" && entryOrder.AssetID != "" {
				// 尝试从Hedge订单获取另一个AssetID
				var yesAssetID, noAssetID string
				if hedgeOrder != nil && hedgeOrder.AssetID != "" {
					// 有Hedge订单，可以推断出两个AssetID
					if entryOrder.TokenType == domain.TokenTypeUp {
						yesAssetID = entryOrder.AssetID
						noAssetID = hedgeOrder.AssetID
					} else {
						yesAssetID = hedgeOrder.AssetID
						noAssetID = entryOrder.AssetID
					}
				} else if entryOrder.TokenType != "" {
					// 只有Entry订单，但知道TokenType，可以尝试从持仓中获取另一个AssetID
					positions := rm.tradingService.GetOpenPositionsForMarket(exp.MarketSlug)
					for _, p := range positions {
						if p != nil && p.TokenType != "" && p.TokenType != entryOrder.TokenType {
							if p.EntryOrder != nil && p.EntryOrder.AssetID != "" {
								if entryOrder.TokenType == domain.TokenTypeUp {
									yesAssetID = entryOrder.AssetID
									noAssetID = p.EntryOrder.AssetID
								} else {
									yesAssetID = p.EntryOrder.AssetID
									noAssetID = entryOrder.AssetID
								}
								break
							}
						}
					}
				}
				
				if yesAssetID != "" && noAssetID != "" {
					market := &domain.Market{
						Slug:       entryOrder.MarketSlug,
						YesAssetID: yesAssetID,
						NoAssetID:  noAssetID,
						Timestamp:  time.Now().Unix(),
					}
					if market.IsValid() {
						riskLog.Debugf("✅ [获取Market] 方式2成功（从Entry订单推断）: marketSlug=%s attempt=%d", exp.MarketSlug, attempt)
						return market, "from_entry_order"
					}
				}
			}
		}
		
		// 方式3：从Hedge订单中获取market信息
		if hedgeOrder != nil && hedgeOrder.MarketSlug != "" && hedgeOrder.AssetID != "" {
			if entryOrder, ok := rm.tradingService.GetOrder(exp.EntryOrderID); ok && entryOrder != nil && entryOrder.AssetID != "" {
				var yesAssetID, noAssetID string
				if entryOrder.TokenType == domain.TokenTypeUp {
					yesAssetID = entryOrder.AssetID
					noAssetID = hedgeOrder.AssetID
				} else if hedgeOrder.TokenType == domain.TokenTypeUp {
					yesAssetID = hedgeOrder.AssetID
					noAssetID = entryOrder.AssetID
				} else {
					// 无法确定，尝试从持仓推断
					positions := rm.tradingService.GetOpenPositionsForMarket(exp.MarketSlug)
					for _, p := range positions {
						if p != nil && p.TokenType != "" && p.EntryOrder != nil && p.EntryOrder.AssetID != "" {
							if p.TokenType == domain.TokenTypeUp {
								yesAssetID = p.EntryOrder.AssetID
							} else {
								noAssetID = p.EntryOrder.AssetID
							}
						}
					}
					// 如果还缺少一个，使用订单中的AssetID
					if yesAssetID == "" {
						yesAssetID = entryOrder.AssetID
					}
					if noAssetID == "" {
						noAssetID = hedgeOrder.AssetID
					}
				}
				
				if yesAssetID != "" && noAssetID != "" {
					market := &domain.Market{
						Slug:       hedgeOrder.MarketSlug,
						YesAssetID: yesAssetID,
						NoAssetID:  noAssetID,
						Timestamp:  time.Now().Unix(),
					}
					if market.IsValid() {
						riskLog.Debugf("✅ [获取Market] 方式3成功（从Hedge订单推断）: marketSlug=%s attempt=%d", exp.MarketSlug, attempt)
						return market, "from_hedge_order"
					}
				}
			}
		}
		
		// 降级方案：使用订单信息构建最小可用的Market对象
		if attempt == maxRetries {
			riskLog.Warnf("⚠️ [获取Market] 所有方式都失败，尝试降级方案: marketSlug=%s", exp.MarketSlug)
			
			// 从Entry订单获取基本信息
			entryOrder, entryOk := rm.tradingService.GetOrder(exp.EntryOrderID)
			if entryOk && entryOrder != nil && entryOrder.MarketSlug != "" && entryOrder.AssetID != "" {
				var yesAssetID, noAssetID string
				
				// 根据Entry订单的TokenType推断
				if entryOrder.TokenType == domain.TokenTypeUp {
					yesAssetID = entryOrder.AssetID
					// 尝试从Hedge订单获取NoAssetID
					if hedgeOrder != nil && hedgeOrder.AssetID != "" {
						noAssetID = hedgeOrder.AssetID
					}
				} else if entryOrder.TokenType == domain.TokenTypeDown {
					noAssetID = entryOrder.AssetID
					// 尝试从Hedge订单获取YesAssetID
					if hedgeOrder != nil && hedgeOrder.AssetID != "" {
						yesAssetID = hedgeOrder.AssetID
					}
				}
				
				// 如果还缺少一个AssetID，尝试从持仓中获取
				if yesAssetID == "" || noAssetID == "" {
					positions := rm.tradingService.GetOpenPositionsForMarket(exp.MarketSlug)
					for _, p := range positions {
						if p != nil && p.EntryOrder != nil && p.EntryOrder.AssetID != "" {
							if yesAssetID == "" && p.TokenType == domain.TokenTypeUp {
								yesAssetID = p.EntryOrder.AssetID
							} else if noAssetID == "" && p.TokenType == domain.TokenTypeDown {
								noAssetID = p.EntryOrder.AssetID
							}
						}
					}
				}
				
				// 如果仍然缺少，使用exp中的信息
				if yesAssetID == "" || noAssetID == "" {
					// 根据EntryTokenType推断
					if exp.EntryTokenType == domain.TokenTypeUp {
						if yesAssetID == "" {
							yesAssetID = entryOrder.AssetID
						}
						if noAssetID == "" && hedgeOrder != nil && hedgeOrder.AssetID != "" {
							noAssetID = hedgeOrder.AssetID
						}
					} else {
						if noAssetID == "" {
							noAssetID = entryOrder.AssetID
						}
						if yesAssetID == "" && hedgeOrder != nil && hedgeOrder.AssetID != "" {
							yesAssetID = hedgeOrder.AssetID
						}
					}
				}
				
				// 构建降级Market对象（即使缺少部分信息也尝试使用）
				if entryOrder.MarketSlug != "" && yesAssetID != "" && noAssetID != "" {
					market := &domain.Market{
						Slug:       entryOrder.MarketSlug,
						YesAssetID: yesAssetID,
						NoAssetID:  noAssetID,
						Timestamp:  time.Now().Unix(),
					}
					riskLog.Warnf("⚠️ [获取Market] 降级方案：使用推断的Market对象（可能不完整）: marketSlug=%s yesAssetID=%s noAssetID=%s", 
						exp.MarketSlug, yesAssetID, noAssetID)
					return market, "fallback_inferred"
				}
			}
		}
	}
	
	// 所有方式都失败
	riskLog.Errorf("❌ [获取Market] 所有方式都失败，无法获取market对象: marketSlug=%s entryOrderID=%s hedgeOrderID=%s", 
		exp.MarketSlug, exp.EntryOrderID, func() string {
			if hedgeOrder != nil {
				return hedgeOrder.OrderID
			}
			return exp.HedgeOrderID
		}())
	return nil, "all_failed"
}

// aggressiveHedge 激进对冲：撤单并以ask价FAK吃单
func (rm *RiskManager) aggressiveHedge(ctx context.Context, exp *RiskExposure, hedgeOrder *domain.Order) {
	hedgeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 获取market对象（多种方式，带重试和降级方案）
	market, source := rm.getMarketForAggressiveHedge(ctx, exp, hedgeOrder)
	if market == nil {
		riskLog.Errorf("❌ 无法获取market对象，无法执行激进对冲: marketSlug=%s source=%s", exp.MarketSlug, source)
		return
	}
	
	riskLog.Infof("✅ [获取Market] 成功获取market对象: marketSlug=%s source=%s", exp.MarketSlug, source)

	// 更新状态：正在撤单（如果存在旧订单）
	if hedgeOrder.OrderID != "" {
		rm.mu.Lock()
		rm.currentAction = "aggressive_hedging"
		rm.currentActionDesc = "正在取消旧Hedge订单并执行激进对冲"
		rm.currentActionEntry = exp.EntryOrderID
		rm.currentActionHedge = hedgeOrder.OrderID
		rm.currentActionTime = time.Now()
		rm.mu.Unlock()

		// 1. 取消旧的Hedge订单
		riskLog.Debugf("🔄 取消旧Hedge订单: hedgeOrderID=%s", hedgeOrder.OrderID)
		if err := rm.tradingService.CancelOrder(hedgeCtx, hedgeOrder.OrderID); err != nil {
			riskLog.Warnf("⚠️ 取消Hedge订单失败: hedgeOrderID=%s err=%v", hedgeOrder.OrderID, err)
			// 即使取消失败，也继续尝试（可能订单已经不存在）
		}

		// 等待一小段时间，确认撤单
		time.Sleep(500 * time.Millisecond)
	} else {
		// Hedge订单ID为空，说明从未创建过对冲单，直接跳过撤单步骤
		rm.mu.Lock()
		rm.currentAction = "fak_eating"
		rm.currentActionDesc = "无旧订单，直接下FAK对冲单"
		rm.mu.Unlock()
		riskLog.Debugf("🔄 无旧Hedge订单需要取消（hedgeOrderID为空），直接下FAK对冲单")
	}

	// 2. 获取当前订单簿价格
	_, yesAsk, _, noAsk, source, err := rm.tradingService.GetTopOfBook(hedgeCtx, market)
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

	// 获取相反方向的 TokenType
	var hedgeTokenType domain.TokenType
	if exp.EntryTokenType == domain.TokenTypeUp {
		hedgeTokenType = domain.TokenTypeDown
	} else {
		hedgeTokenType = domain.TokenTypeUp
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

	riskLog.Debugf("💰 激进对冲价格分析: entryPrice=%dc hedgeAsk=%dc totalCost=%dc expectedLoss=%dc maxAcceptable=%dc",
		exp.EntryPriceCents, hedgeAskCents, totalCostCents, expectedLossCents, rm.maxAcceptableLossCents)

	// 4. 检查亏损是否在可接受范围内
	if expectedLossCents > rm.maxAcceptableLossCents {
		// 计算亏损倍数（相对于最大可接受亏损）
		lossMultiplier := float64(expectedLossCents) / float64(rm.maxAcceptableLossCents)
		
		// 策略选择：
		// 1. 如果亏损 <= 2倍阈值：仍然执行对冲（小亏总比大亏好，避免价格继续恶化）
		// 2. 如果亏损 > 2倍阈值：拒绝执行对冲，记录严重警告
		//    原因：如果价格已经跑得太远，对冲可能造成巨大亏损，不如等待价格回调或手动处理
		if lossMultiplier > 2.0 {
			riskLog.Errorf("🚨 拒绝激进对冲：预期亏损严重超过阈值 (%.1fx)，价格已跑得太远: expectedLoss=%dc maxAcceptable=%dc multiplier=%.2f",
				lossMultiplier, expectedLossCents, rm.maxAcceptableLossCents, lossMultiplier)
			riskLog.Errorf("🚨 建议：等待价格回调或手动处理，避免造成更大亏损")
			
			// 更新状态：拒绝执行
			rm.mu.Lock()
			rm.currentAction = "idle"
			rm.currentActionDesc = fmt.Sprintf("拒绝对冲：亏损过大 (%.1fx阈值)", lossMultiplier)
			rm.mu.Unlock()
			
			return // 拒绝执行对冲
		} else {
			// 亏损超过阈值但 <= 2倍阈值：仍然执行（避免更大风险）
			riskLog.Warnf("⚠️ 预期亏损超过最大可接受值，但仍执行对冲（避免更大风险）: expectedLoss=%dc maxAcceptable=%dc multiplier=%.2f",
				expectedLossCents, rm.maxAcceptableLossCents, lossMultiplier)
		}
	}

	// 5. 以ask价下FAK买单
	riskLog.Debugf("🚀 执行激进对冲: 以ask价FAK吃单 price=%dc size=%.4f source=%s expectedLoss=%dc",
		hedgeAskCents, exp.EntrySize, source, expectedLossCents)

	// 获取市场精度信息（简化处理，使用默认值）
	fakHedgeOrder := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      hedgeAssetID,
		TokenType:    hedgeTokenType,
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

	hedgeResult, err := rm.tradingService.PlaceOrder(hedgeCtx, fakHedgeOrder)
	if err != nil {
		riskLog.Errorf("❌ 激进对冲下单失败: err=%v (Entry已成交，存在风险敞口)", err)
		return
	}

	if hedgeResult == nil || hedgeResult.OrderID == "" {
		riskLog.Errorf("❌ 激进对冲下单失败: 订单ID为空")
		return
	}

	riskLog.Debugf("✅ 激进对冲订单已提交: orderID=%s price=%dc size=%.4f expectedLoss=%dc",
		hedgeResult.OrderID, hedgeAskCents, exp.EntrySize, expectedLossCents)

	// 7. 注册到 pendingHedges（关键修复：确保订单成交后能正确触发 merge 和仓位更新）
	if rm.oms != nil {
		rm.oms.RecordPendingHedge(exp.EntryOrderID, hedgeResult.OrderID)
		riskLog.Infof("📝 [激进对冲] 已注册到 pendingHedges: entryID=%s hedgeID=%s", exp.EntryOrderID, hedgeResult.OrderID)
	}

	// 8. 更新风险敞口记录和状态
	rm.mu.Lock()
	if exp, exists := rm.exposures[exp.EntryOrderID]; exists {
		exp.HedgeOrderID = hedgeResult.OrderID
		exp.HedgeStatus = hedgeResult.Status
		exp.MaxLossCents = expectedLossCents
	}
	rm.totalAggressiveHedges++
	rm.currentAction = "idle"
	rm.currentActionDesc = ""
	rm.mu.Unlock()

	// 9. 如果FAK订单立即成交，移除风险敞口并立即触发状态更新
	if hedgeResult.Status == domain.OrderStatusFilled {
		rm.mu.Lock()
		delete(rm.exposures, exp.EntryOrderID)
		rm.mu.Unlock()
		riskLog.Debugf("✅ 激进对冲订单已立即成交，风险敞口已消除: orderID=%s expectedLoss=%dc",
			hedgeResult.OrderID, expectedLossCents)
		
		// 关键修复：订单立即成交后，立即清理 pendingHedges 并触发合并操作
		// 不等待 OnOrderUpdate 回调，因为可能延迟到达
		if rm.oms != nil {
			// 立即清理 pendingHedges
			rm.oms.mu.Lock()
			if rm.oms.pendingHedges != nil {
				delete(rm.oms.pendingHedges, exp.EntryOrderID)
				riskLog.Infof("✅ [激进对冲] 已清理 pendingHedges: entryID=%s hedgeID=%s", exp.EntryOrderID, hedgeResult.OrderID)
			}
			rm.oms.mu.Unlock()
			
			// 立即触发合并操作（不等待 OnOrderUpdate 回调）
			if rm.oms.capital != nil {
				go func() {
					// 等待一小段时间，确保 Trade 事件已到达并更新持仓
					// 然后立即触发合并操作
					time.Sleep(500 * time.Millisecond)
					riskLog.Infof("🔄 [激进对冲] 立即触发合并操作: market=%s hedgeOrderID=%s", market.Slug, hedgeResult.OrderID)
					rm.oms.capital.TryMergeCurrentCycle(context.Background(), market)
					
					// 再等待一小段时间，确保合并操作完成
					time.Sleep(500 * time.Millisecond)
					riskLog.Debugf("✅ [激进对冲] 合并操作应已完成，持仓状态应已更新: hedgeOrderID=%s", hedgeResult.OrderID)
				}()
			} else {
				riskLog.Warnf("⚠️ [激进对冲] capital 为 nil，无法触发合并")
			}
		}
		
		// 记录状态更新完成（用于调试）
		riskLog.Debugf("✅ [激进对冲] 状态更新流程已启动: hedgeOrderID=%s entryID=%s (pendingHedges已清理，合并操作已触发)", 
			hedgeResult.OrderID, exp.EntryOrderID)
	} else {
		// FAK 订单未立即成交，等待 Trade 事件更新仓位
		// 但也要确保 OnOrderUpdate 能正确处理（通过 pendingHedges）
		riskLog.Debugf("⏳ 激进对冲订单未立即成交，等待 Trade 事件: orderID=%s status=%s",
			hedgeResult.OrderID, hedgeResult.Status)
	}
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
