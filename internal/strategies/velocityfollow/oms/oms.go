package oms

import (
	"context"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategies/velocityfollow/brain"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("module", "oms")

// OMS 订单管理系统
type OMS struct {
	tradingService *services.TradingService
	config         ConfigInterface

	// 子模块
	orderExecutor   *OrderExecutor
	positionManager *PositionManager
	riskManager     *RiskManager
	hedgeReorder    *HedgeReorder

	// 订单状态跟踪
	mu            sync.RWMutex
	pendingHedges map[string]string // entryOrderID -> hedgeOrderID

	// Capital 模块引用（用于在对冲单完成时触发 merge）
	capital CapitalInterface
}

// New 创建新的 OMS 实例
func New(ts *services.TradingService, cfg ConfigInterface) (*OMS, error) {
	if ts == nil {
		return nil, nil // 允许延迟初始化
	}

	oe := NewOrderExecutor(ts, cfg)
	pm := NewPositionManager(ts, cfg)
	rm := NewRiskManager(ts, cfg)
	hr := NewHedgeReorder(ts, cfg, nil) // 先创建，稍后设置反向引用

	oms := &OMS{
		tradingService:  ts,
		config:          cfg,
		orderExecutor:   oe,
		positionManager: pm,
		riskManager:     rm,
		hedgeReorder:    hr,
		pendingHedges:   make(map[string]string),
	}

	// 设置反向引用
	oe.SetOMS(oms)
	hr.oms = oms // 设置 HedgeReorder 的 OMS 引用
	rm.SetOMS(oms) // 设置 RiskManager 的 OMS 引用

	return oms, nil
}

// SetCapital 设置 Capital 模块引用（用于在对冲单完成时触发 merge）
func (o *OMS) SetCapital(capital CapitalInterface) {
	o.capital = capital
}

// OnCycle 周期切换回调
func (o *OMS) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	o.mu.Lock()
	defer o.mu.Unlock()

	// 清空 pendingHedges（新周期开始）
	o.pendingHedges = make(map[string]string)

	if o.positionManager != nil {
		o.positionManager.OnCycle(ctx, oldMarket, newMarket)
	}
}

// OnOrderUpdate 订单更新回调
func (o *OMS) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	if order == nil || order.OrderID == "" {
		return nil
	}
		
	// 转发给 PositionManager 处理
	if o.positionManager != nil {
		if err := o.positionManager.OnOrderUpdate(ctx, order); err != nil {
			log.Warnf("⚠️ [OMS] PositionManager 处理订单更新失败: %v", err)
		}
	}

	// 更新 RiskManager 并启动监控
	if o.riskManager != nil {
		// 关键修复：检查订单是否为 Entry 订单
		// 方法1：通过 IsEntryOrder 字段（如果已设置）
		// 方法2：通过检查订单是否在 pendingHedges 的 value 中（如果是 value，说明是对冲单；如果是 key，说明是 Entry 单）
		// 方法3：通过检查订单的 TokenType 和订单簿方向判断（Entry 通常是买入速度更快的一侧）
		isEntryOrder := order.IsEntryOrder
		if !isEntryOrder {
			// 如果 IsEntryOrder 未设置，通过 pendingHedges 判断
			// 如果订单ID在 pendingHedges 的 key 中，说明是 Entry 订单
			o.mu.RLock()
			if _, exists := o.pendingHedges[order.OrderID]; exists {
				isEntryOrder = true
			}
			o.mu.RUnlock()
		}
		
		if isEntryOrder && order.IsFilled() {
			// Entry订单成交，注册到RiskManager
			// 注意：在 sequential 模式下，OnOrderUpdate 回调可能在 RecordPendingHedge 之前执行
			// 所以如果第一次找不到 hedgeOrderID，延迟一小段时间再检查一次
			hedgeOrderID := ""
			o.mu.RLock()
			if id, exists := o.pendingHedges[order.OrderID]; exists {
				hedgeOrderID = id
			}
			o.mu.RUnlock()
			
			// 如果 hedgeOrderID 为空，可能是时序问题（sequential 模式下）
			// 延迟一小段时间再检查一次，给 RecordPendingHedge 机会执行
			if hedgeOrderID == "" && o.config != nil && o.config.GetOrderExecutionMode() == "sequential" {
				go func() {
					// 延迟 100ms 再检查一次
					time.Sleep(100 * time.Millisecond)
					o.mu.RLock()
					if id, exists := o.pendingHedges[order.OrderID]; exists {
						hedgeOrderID = id
					}
					o.mu.RUnlock()
					
					// 如果找到了 hedgeOrderID，更新风险敞口记录
					if hedgeOrderID != "" && o.riskManager != nil {
						o.riskManager.UpdateHedgeOrderID(order.OrderID, hedgeOrderID)
						log.Debugf("🔄 [OMS] 延迟找到 Hedge 订单ID: entryOrderID=%s hedgeOrderID=%s", order.OrderID, hedgeOrderID)
					}
				}()
			}
			
			o.riskManager.RegisterEntry(order, hedgeOrderID)
			
			// 启动对冲单重下监控（如果存在 Hedge 订单且未成交）
			// 这适用于 ExecuteParallel 模式，因为 ExecuteSequential 已经在订单执行时启动了监控
			if hedgeOrderID != "" && o.hedgeReorder != nil {
				// 检查 Hedge 订单是否已成交
				hedgeFilled := false
				if o.tradingService != nil {
					if hedgeOrder, ok := o.tradingService.GetOrder(hedgeOrderID); ok && hedgeOrder != nil {
						hedgeFilled = hedgeOrder.IsFilled()
					}
				}
				
				// 如果 Hedge 订单未成交，启动监控
				if !hedgeFilled {
					// 获取市场信息
					market := o.tradingService.GetCurrentMarketInfo()
					if market != nil {
						entryFilledTime := time.Now()
						if order.FilledAt != nil {
							entryFilledTime = *order.FilledAt
						}
						
						entryAskCents := order.Price.ToCents()
						if order.FilledPrice != nil {
							entryAskCents = order.FilledPrice.ToCents()
						}
						
						// 获取 Hedge 订单信息
						var hedgeAsset string
						var hedgePrice domain.Price
						var hedgeShares float64
						var winner domain.TokenType
						
						if hedgeOrder, ok := o.tradingService.GetOrder(hedgeOrderID); ok && hedgeOrder != nil {
							hedgeAsset = hedgeOrder.AssetID
							hedgePrice = hedgeOrder.Price
							hedgeShares = hedgeOrder.Size
						} else {
							// 如果无法获取 Hedge 订单，使用 Entry 订单的反向信息
							if order.TokenType == domain.TokenTypeUp {
								hedgeAsset = market.NoAssetID
								winner = domain.TokenTypeUp
							} else {
								hedgeAsset = market.YesAssetID
								winner = domain.TokenTypeDown
							}
							hedgeShares = order.FilledSize
							// 使用默认的 Hedge 价格（从决策中获取，这里简化处理）
							hedgePrice = domain.Price{Pips: (100 - entryAskCents) * 100}
						}
						
						// 确定 winner（Entry 的方向）
						winner = order.TokenType
						
						// 在 goroutine 中启动监控
						go o.hedgeReorder.MonitorAndReorderHedge(
							context.Background(), // 使用独立的 context
							market,
							order.OrderID,
							hedgeOrderID,
							hedgeAsset,
							hedgePrice,
							hedgeShares,
							entryFilledTime,
							order.FilledSize,
							entryAskCents,
							winner,
						)
						log.Debugf("🔄 [OMS] 已启动对冲单重下监控: entryOrderID=%s hedgeOrderID=%s", order.OrderID, hedgeOrderID)
					}
				}
			}
		} else if !order.IsEntryOrder {
			// Hedge订单状态更新
			o.riskManager.UpdateHedgeStatus(order.OrderID, order.Status)
		}
	}

	// 更新 pendingHedges 并检查是否需要触发 merge
	var shouldTriggerMerge bool
	var marketSlug string
	
	o.mu.Lock()
	// 如果是对冲订单成交，从 pendingHedges 中删除，并触发 merge
	if order.IsFilled() && !order.IsEntryOrder {
		// 检查是否是对冲订单（在 pendingHedges 中查找）
		foundInPendingHedges := false
		for entryID, hedgeID := range o.pendingHedges {
			if hedgeID == order.OrderID {
				delete(o.pendingHedges, entryID)
				log.Infof("✅ [OMS] 对冲订单已成交: entryID=%s hedgeID=%s", entryID, hedgeID)
				foundInPendingHedges = true
				shouldTriggerMerge = true
				marketSlug = order.MarketSlug
				break
			}
		}
		
		// 关键修复：也检查通过 HedgeOrderID 字段关联的情况
		// 如果 Entry 订单的 HedgeOrderID 字段指向这个对冲单，也应该清理
		if !foundInPendingHedges && order.HedgeOrderID != nil {
			entryOrderID := *order.HedgeOrderID
			if _, exists := o.pendingHedges[entryOrderID]; exists {
				// 检查这个 Entry 订单的对冲单是否就是这个订单
				if hedgeID, ok := o.pendingHedges[entryOrderID]; ok && hedgeID == order.OrderID {
					delete(o.pendingHedges, entryOrderID)
					log.Infof("✅ [OMS] 对冲订单已成交（通过HedgeOrderID字段关联）: entryID=%s hedgeID=%s", entryOrderID, order.OrderID)
					foundInPendingHedges = true
					shouldTriggerMerge = true
					marketSlug = order.MarketSlug
				}
			}
		}
		
		// 关键修复：即使不在 pendingHedges 中（可能是调价后的新订单），也应该触发 merge
		// 因为只要是对冲订单成交，就应该检查是否可以合并
		if !foundInPendingHedges {
			log.Debugf("🔍 [OMS] 对冲订单成交但未在 pendingHedges 中找到: orderID=%s (可能是调价后的新订单，仍触发合并检查)", order.OrderID)
			shouldTriggerMerge = true
			marketSlug = order.MarketSlug
		}
	}
	o.mu.Unlock()

	// 对冲单完成，立即触发 merge 当前周期的 complete sets（在锁外执行，避免阻塞）
	// 关键修复：不等待 Trade 事件，立即触发合并操作，确保状态快速更新
	if shouldTriggerMerge {
		if o.capital == nil {
			log.Warnf("⚠️ [OMS] capital 为 nil，无法触发合并")
		} else if marketSlug == "" {
			log.Warnf("⚠️ [OMS] marketSlug 为空，无法触发合并: orderID=%s", order.OrderID)
		} else {
			// 获取当前市场信息
			if o.tradingService != nil {
				market := o.tradingService.GetCurrentMarketInfo()
				if market != nil {
					log.Infof("🔄 [OMS] 对冲单完成，立即触发合并当前周期持仓: market=%s orderID=%s", market.Slug, order.OrderID)
					// 在 goroutine 中异步执行，避免阻塞 OnOrderUpdate 回调
					// 但立即启动，不等待 Trade 事件
					go func() {
						// 等待一小段时间（500ms），确保 Trade 事件已到达并更新持仓
						// 这样可以确保合并操作基于最新的持仓数据
						time.Sleep(500 * time.Millisecond)
						o.capital.TryMergeCurrentCycle(context.Background(), market)
						log.Debugf("✅ [OMS] 合并操作已触发: market=%s orderID=%s", market.Slug, order.OrderID)
					}()
				} else {
					log.Warnf("⚠️ [OMS] 无法获取当前市场信息，无法触发合并: marketSlug=%s", marketSlug)
				}
			} else {
				log.Warnf("⚠️ [OMS] tradingService 为 nil，无法触发合并")
			}
		}
	}

	return nil
}

// HasUnhedgedRisk 检查是否有未对冲风险
func (o *OMS) HasUnhedgedRisk(marketSlug string) (bool, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	// 检查是否有未完成的对冲单
	if len(o.pendingHedges) > 0 {
		return true, nil
	}

	// 检查实际持仓（通过 PositionManager）
	if o.positionManager != nil {
		return o.positionManager.HasUnhedgedRisk(marketSlug), nil
	}

	return false, nil
}

// ExecuteOrder 执行订单
func (o *OMS) ExecuteOrder(ctx context.Context, market *domain.Market, decision *brain.Decision) error {
	if o == nil || o.tradingService == nil || o.config == nil {
		return nil
	}

	if market == nil || decision == nil {
		return nil
	}

	// 根据执行模式选择执行方式
	if o.config.GetOrderExecutionMode() == "parallel" {
		return o.orderExecutor.ExecuteParallel(ctx, market, decision)
	} else {
		return o.orderExecutor.ExecuteSequential(ctx, market, decision)
	}
}

// RecordPendingHedge 记录待处理的对冲单
func (o *OMS) RecordPendingHedge(entryOrderID, hedgeOrderID string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if entryOrderID != "" {
		o.pendingHedges[entryOrderID] = hedgeOrderID
		log.Debugf("📝 [OMS] 记录待处理对冲单: entryID=%s hedgeID=%s", entryOrderID, hedgeOrderID)
	}
}

// GetPendingHedges 获取待处理的对冲单（线程安全）
func (o *OMS) GetPendingHedges() map[string]string {
	o.mu.RLock()
	defer o.mu.RUnlock()

	// 返回副本，避免外部修改
	result := make(map[string]string, len(o.pendingHedges))
	for k, v := range o.pendingHedges {
		result[k] = v
	}
	return result
}

// Start 启动 OMS 子模块（RiskManager等）
func (o *OMS) Start(ctx context.Context) {
	if o.riskManager != nil {
		o.riskManager.Start(ctx)
	}
	
	// 为所有现有的未完成对冲单启动监控（处理代码修改前已存在的订单）
	go o.startMonitoringForExistingHedges(ctx)
}

// startMonitoringForExistingHedges 为现有的未完成对冲单启动监控
func (o *OMS) startMonitoringForExistingHedges(ctx context.Context) {
	// 等待一下，确保所有订单状态都已同步
	time.Sleep(2 * time.Second)
	
	o.mu.RLock()
	pendingHedges := make(map[string]string, len(o.pendingHedges))
	for entryID, hedgeID := range o.pendingHedges {
		pendingHedges[entryID] = hedgeID
	}
	o.mu.RUnlock()
	
	if len(pendingHedges) == 0 {
		return
	}
	
	market := o.tradingService.GetCurrentMarketInfo()
	if market == nil {
		return
	}
	
	log.Debugf("🔄 [OMS] 为 %d 个现有未完成对冲单启动监控", len(pendingHedges))
	
	// 为每个未完成的对冲单启动监控
	for entryOrderID, hedgeOrderID := range pendingHedges {
		// 检查 Entry 订单是否已成交
		entryOrder, entryExists := o.tradingService.GetOrder(entryOrderID)
		if !entryExists || entryOrder == nil || !entryOrder.IsFilled() {
			continue
		}
		
		// 检查 Hedge 订单是否已成交
		hedgeOrder, hedgeExists := o.tradingService.GetOrder(hedgeOrderID)
		if !hedgeExists || hedgeOrder == nil {
			continue
		}
		
		if hedgeOrder.IsFilled() {
			// Hedge 已成交，从 pendingHedges 中删除
			o.mu.Lock()
			delete(o.pendingHedges, entryOrderID)
			o.mu.Unlock()
			continue
		}
		
		// Entry 已成交但 Hedge 未成交，启动监控
		if o.hedgeReorder != nil {
			entryFilledTime := time.Now()
			if entryOrder.FilledAt != nil {
				entryFilledTime = *entryOrder.FilledAt
			}
			
			entryAskCents := entryOrder.Price.ToCents()
			if entryOrder.FilledPrice != nil {
				entryAskCents = entryOrder.FilledPrice.ToCents()
			}
			
			// 在 goroutine 中启动监控
			go o.hedgeReorder.MonitorAndReorderHedge(
				ctx,
				market,
				entryOrderID,
				hedgeOrderID,
				hedgeOrder.AssetID,
				hedgeOrder.Price,
				hedgeOrder.Size,
				entryFilledTime,
				entryOrder.FilledSize,
				entryAskCents,
				entryOrder.TokenType,
			)
			log.Debugf("🔄 [OMS] 已为现有订单启动监控: entryOrderID=%s hedgeOrderID=%s", entryOrderID, hedgeOrderID)
		}
	}
}

// Stop 停止 OMS 子模块
func (o *OMS) Stop() {
	if o.riskManager != nil {
		o.riskManager.Stop()
	}
}

// GetRiskManager 获取 RiskManager（供外部使用）
func (o *OMS) GetRiskManager() *RiskManager {
	return o.riskManager
}

// GetHedgeReorder 获取 HedgeReorder（供外部使用）
func (o *OMS) GetHedgeReorder() *HedgeReorder {
	return o.hedgeReorder
}

// GetRiskManagementStatus 获取风控状态（用于 Dashboard 显示）
func (o *OMS) GetRiskManagementStatus() *RiskManagementStatus {
	status := &RiskManagementStatus{
		CurrentAction: "idle",
	}

	// 从 RiskManager 获取风险敞口
	if o.riskManager != nil {
		exposures := o.riskManager.GetExposures()
		// 过滤已对冲的风险敞口，只显示未对冲的
		unhedgedExposures := make([]*RiskExposure, 0, len(exposures))
		for _, exp := range exposures {
			// 只显示未对冲的风险敞口（HedgeStatus != Filled）
			if exp.HedgeStatus != domain.OrderStatusFilled {
				unhedgedExposures = append(unhedgedExposures, exp)
			}
		}
		
		status.RiskExposuresCount = len(unhedgedExposures)
		status.RiskExposures = make([]RiskExposureInfo, 0, len(unhedgedExposures))
		
		// 获取激进对冲超时时间（用于计算倒计时）
		aggressiveTimeoutSeconds := 60.0 // 默认 60 秒
		if o.riskManager.config != nil && o.riskManager.config.GetAggressiveHedgeTimeoutSeconds() > 0 {
			aggressiveTimeoutSeconds = float64(o.riskManager.config.GetAggressiveHedgeTimeoutSeconds())
		}
		
		// 获取 HedgeReorder 的调价信息（用于关联到对应的风险敞口）
		var reorderInfo map[string]struct {
			oldPrice int
			newPrice int
		}
		if o.hedgeReorder != nil {
			o.hedgeReorder.mu.Lock()
			reorderInfo = make(map[string]struct {
				oldPrice int
				newPrice int
			})
			// 如果当前有调价操作，记录调价信息
			if o.hedgeReorder.currentActionEntry != "" {
				reorderInfo[o.hedgeReorder.currentActionEntry] = struct {
					oldPrice int
					newPrice int
				}{
					oldPrice: o.hedgeReorder.repriceOldPriceCents,
					newPrice: o.hedgeReorder.repriceNewPriceCents,
				}
			}
			o.hedgeReorder.mu.Unlock()
		}
		
		for _, exp := range unhedgedExposures {
			// 计算倒计时（到激进对冲超时的时间）
			countdownSeconds := aggressiveTimeoutSeconds - exp.ExposureSeconds
			if countdownSeconds < 0 {
				countdownSeconds = 0 // 已经超时
			}
			
			// 获取原对冲单价格（从订单中获取）
			originalHedgePriceCents := 0
			if exp.HedgeOrderID != "" && o.tradingService != nil {
				if hedgeOrder, ok := o.tradingService.GetOrder(exp.HedgeOrderID); ok && hedgeOrder != nil {
					originalHedgePriceCents = hedgeOrder.Price.ToCents()
				}
			}
			
			// 获取新对冲单价格（如果重新下单了）
			newHedgePriceCents := 0
			if reorderInfo != nil {
				if info, exists := reorderInfo[exp.EntryOrderID]; exists {
					newHedgePriceCents = info.newPrice
					if originalHedgePriceCents == 0 {
						originalHedgePriceCents = info.oldPrice
					}
				}
			}
			
			status.RiskExposures = append(status.RiskExposures, RiskExposureInfo{
				EntryOrderID:            exp.EntryOrderID,
				EntryTokenType:          string(exp.EntryTokenType),
				EntrySize:               exp.EntrySize,
				EntryPriceCents:         exp.EntryPriceCents,
				HedgeOrderID:            exp.HedgeOrderID,
				HedgeStatus:             string(exp.HedgeStatus),
				ExposureSeconds:         exp.ExposureSeconds,
				MaxLossCents:            exp.MaxLossCents,
				OriginalHedgePriceCents: originalHedgePriceCents,
				NewHedgePriceCents:      newHedgePriceCents,
				CountdownSeconds:        countdownSeconds,
			})
		}

		// 获取当前操作状态
		o.riskManager.mu.Lock()
		status.CurrentAction = o.riskManager.currentAction
		status.CurrentActionEntry = o.riskManager.currentActionEntry
		status.CurrentActionHedge = o.riskManager.currentActionHedge
		status.CurrentActionTime = o.riskManager.currentActionTime
		status.CurrentActionDesc = o.riskManager.currentActionDesc
		status.TotalAggressiveHedges = o.riskManager.totalAggressiveHedges
		o.riskManager.mu.Unlock()
	}

	// 从 HedgeReorder 获取重下状态（如果 RiskManager 没有活动操作，使用 HedgeReorder 的状态）
	if o.hedgeReorder != nil && status.CurrentAction == "idle" {
		o.hedgeReorder.mu.Lock()
		if o.hedgeReorder.currentAction != "idle" {
			status.CurrentAction = o.hedgeReorder.currentAction
			status.CurrentActionEntry = o.hedgeReorder.currentActionEntry
			status.CurrentActionHedge = o.hedgeReorder.currentActionHedge
			status.CurrentActionTime = o.hedgeReorder.currentActionTime
			status.CurrentActionDesc = o.hedgeReorder.currentActionDesc
			// 传递调价详情
			status.RepriceOldPriceCents = o.hedgeReorder.repriceOldPriceCents
			status.RepriceNewPriceCents = o.hedgeReorder.repriceNewPriceCents
			status.RepricePriceChangeCents = o.hedgeReorder.repricePriceChangeCents
			status.RepriceStrategy = o.hedgeReorder.repriceStrategy
			status.RepriceEntryCostCents = o.hedgeReorder.repriceEntryCostCents
			status.RepriceMarketAskCents = o.hedgeReorder.repriceMarketAskCents
			status.RepriceIdealPriceCents = o.hedgeReorder.repriceIdealPriceCents
			status.RepriceTotalCostCents = o.hedgeReorder.repriceTotalCostCents
			status.RepriceProfitCents = o.hedgeReorder.repriceProfitCents
		}
		status.TotalReorders = o.hedgeReorder.totalReorders
		status.TotalFakEats = o.hedgeReorder.totalFakEats
		o.hedgeReorder.mu.Unlock()
	}

	return status
}

// RiskManagementStatus 风控状态（临时定义，避免循环导入）
type RiskManagementStatus struct {
	RiskExposuresCount int
	RiskExposures      []RiskExposureInfo
	CurrentAction      string
	CurrentActionEntry string
	CurrentActionHedge string
	CurrentActionTime  time.Time
	CurrentActionDesc  string
	TotalReorders      int
	TotalAggressiveHedges int
	TotalFakEats       int
	
	// 调价详情（用于 UI 显示）
	RepriceOldPriceCents    int    // 原价格（分）
	RepriceNewPriceCents    int    // 新价格（分）
	RepricePriceChangeCents int    // 价格变化（分）
	RepriceStrategy         string // 调价策略描述
	RepriceEntryCostCents   int    // Entry成本（分）
	RepriceMarketAskCents   int    // 市场ask价格（分）
	RepriceIdealPriceCents  int    // 理想价格（分）
	RepriceTotalCostCents   int    // 总成本（分）
	RepriceProfitCents      int    // 利润（分）
}

// RiskExposureInfo 风险敞口信息（临时定义，避免循环导入）
type RiskExposureInfo struct {
	EntryOrderID    string
	EntryTokenType  string
	EntrySize       float64
	EntryPriceCents int
	HedgeOrderID    string
	HedgeStatus     string
	ExposureSeconds float64
	MaxLossCents    int
	// 调价信息（如果重新下单了）
	OriginalHedgePriceCents int     // 原对冲单价格（分）
	NewHedgePriceCents      int     // 新对冲单价格（分），如果为0表示未重新下单
	CountdownSeconds        float64 // 倒计时（秒），到激进对冲超时的时间
}
