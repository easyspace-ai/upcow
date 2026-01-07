package oms

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategycore/brain"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("module", "oms")

// OMS 订单管理系统
type OMS struct {
	tradingService *services.TradingService
	config         ConfigInterface

	q *queuedTrading
	hm *hedgeMetrics

	// per-market 预算/限频（更像职业交易执行：避免极端行情写操作风暴）
	// 注意：这里限的是“重下/FAK 这类高成本动作”，不会阻塞正常行情下的执行。
	reorderLimiter *perMarketLimiter
	fakLimiter     *perMarketLimiter

	metricsCancel context.CancelFunc

	reorderBudgetSkips atomic.Int64
	fakBudgetWarnings  atomic.Int64

	orderExecutor   *OrderExecutor
	positionManager *PositionManager
	riskManager     *RiskManager
	hedgeReorder    *HedgeReorder

	mu            sync.RWMutex
	pendingHedges map[string]string // entryOrderID -> hedgeOrderID

	// per-entry 预算 + per-market 冷静期（防止极端行情执行风暴）
	entryBudgets map[string]*entryBudget
	cooldowns    map[string]cooldownInfo

	capital CapitalInterface
}

// New 创建新的 OMS 实例（strategyID 用于多腿订单命名，避免不同策略混淆）
func New(ts *services.TradingService, cfg ConfigInterface, strategyID string) (*OMS, error) {
	if ts == nil {
		return nil, nil
	}

	// 串行化写操作，避免并发打架（默认 25ms 节流）
	q := newQueuedTrading(ts, 256, 25*time.Millisecond)

	oe := NewOrderExecutor(ts, cfg, strategyID)
	pm := NewPositionManager(ts, cfg)
	rm := NewRiskManager(ts, cfg)
	hr := NewHedgeReorder(ts, cfg, nil)

	oms := &OMS{
		tradingService:  ts,
		config:          cfg,
		q:               q,
		hm:              newHedgeMetrics(),
		reorderLimiter:  newPerMarketLimiter(30, 30), // 每 market：容量30，按分钟补给30（≈每2秒一次）
		fakLimiter:      newPerMarketLimiter(10, 10), // 每 market：FAK 更贵，容量10，按分钟补给10
		orderExecutor:   oe,
		positionManager: pm,
		riskManager:     rm,
		hedgeReorder:    hr,
		pendingHedges:   make(map[string]string),
	}

	oe.SetOMS(oms)
	hr.oms = oms
	rm.SetOMS(oms)

	return oms, nil
}

// hedgePriceExtraCents 动态提高 hedge 初始价格的可接受范围（仅在“允许负收益”模式下使用）。
// 目标：在对冲变慢/风险敞口存在时，提高成交确定性（更像职业交易执行）。
func (o *OMS) hedgePriceExtraCents(marketSlug string) int {
	if o == nil || o.hm == nil || marketSlug == "" {
		return 0
	}

	// 当前风险敞口与 pending hedges
	exposures := 0
	if o.riskManager != nil {
		exposures = len(o.riskManager.GetExposures())
	}
	pending := 0
	o.mu.RLock()
	pending = len(o.pendingHedges)
	o.mu.RUnlock()

	ewma := o.hm.getEWMASec(marketSlug)

	extra := 0
	if exposures > 0 {
		extra += 2
	}
	if pending > 0 {
		if pending >= 3 {
			extra += 3
		} else {
			extra += pending
		}
	}
	// ewma 耗时越长，越积极（上限 8c）
	switch {
	case ewma > 25:
		extra += 4
	case ewma > 15:
		extra += 2
	case ewma > 8:
		extra += 1
	}
	if extra > 8 {
		extra = 8
	}
	if extra < 0 {
		extra = 0
	}
	return extra
}

func (o *OMS) allowReorder(marketSlug string) bool {
	if o == nil || o.reorderLimiter == nil {
		return true
	}
	ok := o.reorderLimiter.Allow(marketSlug, 1)
	if !ok {
		o.reorderBudgetSkips.Add(1)
	}
	return ok
}

func (o *OMS) allowFAK(marketSlug string) bool {
	if o == nil || o.fakLimiter == nil {
		return true
	}
	ok := o.fakLimiter.Allow(marketSlug, 1)
	if !ok {
		o.fakBudgetWarnings.Add(1)
	}
	return ok
}

// 写操作统一入口（串行化）
func (o *OMS) placeOrder(ctx context.Context, order *domain.Order) (*domain.Order, error) {
	if o != nil && o.q != nil {
		return o.q.PlaceOrder(ctx, order)
	}
	return o.tradingService.PlaceOrder(ctx, order)
}

func (o *OMS) cancelOrder(ctx context.Context, orderID string) error {
	if o != nil && o.q != nil {
		return o.q.CancelOrder(ctx, orderID)
	}
	return o.tradingService.CancelOrder(ctx, orderID)
}

func (o *OMS) executeMultiLeg(ctx context.Context, req execution.MultiLegRequest) ([]*domain.Order, error) {
	if o != nil && o.q != nil {
		return o.q.ExecuteMultiLeg(ctx, req)
	}
	return o.tradingService.ExecuteMultiLeg(ctx, req)
}

func (o *OMS) SetCapital(capital CapitalInterface) {
	o.capital = capital
}

func (o *OMS) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.pendingHedges = make(map[string]string)
	o.entryBudgets = make(map[string]*entryBudget)
	o.cooldowns = make(map[string]cooldownInfo)
	if o.positionManager != nil {
		o.positionManager.OnCycle(ctx, oldMarket, newMarket)
	}
}

func (o *OMS) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	// 保持与原实现一致（复制自 velocityfollow/oms.go）
	if order == nil || order.OrderID == "" {
		return nil
	}

	if o.positionManager != nil {
		if err := o.positionManager.OnOrderUpdate(ctx, order); err != nil {
			log.Warnf("⚠️ [OMS] PositionManager 处理订单更新失败: %v", err)
		}
	}

	if o.riskManager != nil {
		isEntryOrder := order.IsEntryOrder
		if !isEntryOrder {
			o.mu.RLock()
			if _, exists := o.pendingHedges[order.OrderID]; exists {
				isEntryOrder = true
			}
			o.mu.RUnlock()
		}

		if isEntryOrder && order.IsFilled() {
			// 初始化 per-entry 预算账本（用于对冲重下/撤单/FAK 限制与冷静期）
			o.mu.Lock()
			at := time.Now()
			if order.FilledAt != nil {
				at = *order.FilledAt
			}
			o.initEntryBudget(order.OrderID, order.MarketSlug, at)
			o.mu.Unlock()

			// 记录 entry 成交时间（用于 hedge EWMA）
			if o.hm != nil {
				at := time.Now()
				if order.FilledAt != nil {
					at = *order.FilledAt
				}
				o.hm.recordEntryFilled(order.OrderID, order.MarketSlug, at)
			}

			hedgeOrderID := ""
			o.mu.RLock()
			if id, exists := o.pendingHedges[order.OrderID]; exists {
				hedgeOrderID = id
			}
			o.mu.RUnlock()

			if hedgeOrderID == "" && o.config != nil && o.config.GetOrderExecutionMode() == "sequential" {
				go func() {
					time.Sleep(100 * time.Millisecond)
					o.mu.RLock()
					if id, exists := o.pendingHedges[order.OrderID]; exists {
						hedgeOrderID = id
					}
					o.mu.RUnlock()

					if hedgeOrderID != "" && o.riskManager != nil {
						o.riskManager.UpdateHedgeOrderID(order.OrderID, hedgeOrderID)
						log.Debugf("🔄 [OMS] 延迟找到 Hedge 订单ID: entryOrderID=%s hedgeOrderID=%s", order.OrderID, hedgeOrderID)
					}
				}()
			}

			o.riskManager.RegisterEntry(order, hedgeOrderID)

			if hedgeOrderID != "" && o.hedgeReorder != nil {
				hedgeFilled := false
				if o.tradingService != nil {
					if hedgeOrder, ok := o.tradingService.GetOrder(hedgeOrderID); ok && hedgeOrder != nil {
						hedgeFilled = hedgeOrder.IsFilled()
					}
				}

				if !hedgeFilled {
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

						var hedgeAsset string
						var hedgePrice domain.Price
						var hedgeShares float64
						var winner domain.TokenType

						if hedgeOrder, ok := o.tradingService.GetOrder(hedgeOrderID); ok && hedgeOrder != nil {
							hedgeAsset = hedgeOrder.AssetID
							hedgePrice = hedgeOrder.Price
							hedgeShares = hedgeOrder.Size
						} else {
							if order.TokenType == domain.TokenTypeUp {
								hedgeAsset = market.NoAssetID
								winner = domain.TokenTypeUp
							} else {
								hedgeAsset = market.YesAssetID
								winner = domain.TokenTypeDown
							}
							hedgeShares = order.FilledSize
							hedgePrice = domain.Price{Pips: (100 - entryAskCents) * 100}
						}

						winner = order.TokenType

						go o.hedgeReorder.MonitorAndReorderHedge(
							context.Background(),
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
			o.riskManager.UpdateHedgeStatus(order.OrderID, order.Status)
		}
	}

	var shouldTriggerMerge bool
	var marketSlug string

	o.mu.Lock()
	if order.IsFilled() && !order.IsEntryOrder {
		// 记录 hedge 成交耗时（优先用 HedgeOrderID 关联 entry）
		entryForMetrics := ""
		if order.HedgeOrderID != nil && *order.HedgeOrderID != "" {
			entryForMetrics = *order.HedgeOrderID
		}

		foundInPendingHedges := false
		for entryID, hedgeID := range o.pendingHedges {
			if hedgeID == order.OrderID {
				if entryForMetrics == "" {
					entryForMetrics = entryID
				}
				delete(o.pendingHedges, entryID)
				o.clearEntryBudget(entryID)
				log.Infof("✅ [OMS] 对冲订单已成交: entryID=%s hedgeID=%s", entryID, hedgeID)
				foundInPendingHedges = true
				shouldTriggerMerge = true
				marketSlug = order.MarketSlug
				break
			}
		}

		if !foundInPendingHedges && order.HedgeOrderID != nil {
			entryOrderID := *order.HedgeOrderID
			if _, exists := o.pendingHedges[entryOrderID]; exists {
				if hedgeID, ok := o.pendingHedges[entryOrderID]; ok && hedgeID == order.OrderID {
					if entryForMetrics == "" {
						entryForMetrics = entryOrderID
					}
					delete(o.pendingHedges, entryOrderID)
					o.clearEntryBudget(entryOrderID)
					log.Infof("✅ [OMS] 对冲订单已成交（通过HedgeOrderID字段关联）: entryID=%s hedgeID=%s", entryOrderID, order.OrderID)
					foundInPendingHedges = true
					shouldTriggerMerge = true
					marketSlug = order.MarketSlug
				}
			}
		}

		if !foundInPendingHedges {
			log.Debugf("🔍 [OMS] 对冲订单成交但未在 pendingHedges 中找到: orderID=%s (可能是调价后的新订单，仍触发合并检查)", order.OrderID)
			shouldTriggerMerge = true
			marketSlug = order.MarketSlug
		}

		// 更新 EWMA（锁外执行）
		if o.hm != nil && entryForMetrics != "" {
			at := time.Now()
			if order.FilledAt != nil {
				at = *order.FilledAt
			}
			// 注意：这里不依赖 pendingHedges 是否存在，避免调价后映射丢失时漏统计
			go o.hm.recordHedgeFilled(entryForMetrics, at)
		}
	}
	o.mu.Unlock()

	if shouldTriggerMerge {
		if o.capital == nil {
			log.Warnf("⚠️ [OMS] capital 为 nil，无法触发合并")
		} else if marketSlug == "" {
			log.Warnf("⚠️ [OMS] marketSlug 为空，无法触发合并: orderID=%s", order.OrderID)
		} else {
			if o.tradingService != nil {
				market := o.tradingService.GetCurrentMarketInfo()
				if market != nil {
					log.Infof("🔄 [OMS] 对冲单完成，立即触发合并当前周期持仓: market=%s orderID=%s", market.Slug, order.OrderID)
					go func() {
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

func (o *OMS) HasUnhedgedRisk(marketSlug string) (bool, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if len(o.pendingHedges) > 0 {
		return true, nil
	}
	if o.positionManager != nil {
		return o.positionManager.HasUnhedgedRisk(marketSlug), nil
	}
	return false, nil
}

func (o *OMS) ExecuteOrder(ctx context.Context, market *domain.Market, decision *brain.Decision) error {
	if o == nil || o.tradingService == nil || o.config == nil {
		return nil
	}
	if market == nil || decision == nil {
		return nil
	}
	if o.config.GetOrderExecutionMode() == "parallel" {
		return o.orderExecutor.ExecuteParallel(ctx, market, decision)
	}
	return o.orderExecutor.ExecuteSequential(ctx, market, decision)
}

func (o *OMS) RecordPendingHedge(entryOrderID, hedgeOrderID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if entryOrderID != "" {
		o.pendingHedges[entryOrderID] = hedgeOrderID
		log.Debugf("📝 [OMS] 记录待处理对冲单: entryID=%s hedgeID=%s", entryOrderID, hedgeOrderID)
	}
}

func (o *OMS) GetPendingHedges() map[string]string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	result := make(map[string]string, len(o.pendingHedges))
	for k, v := range o.pendingHedges {
		result[k] = v
	}
	return result
}

func (o *OMS) Start(ctx context.Context) {
	// 关键修复：如果队列已关闭（周期切换时 Stop 会关闭），重新创建队列
	if o.q == nil || o.q.IsClosed() {
		if o.tradingService != nil {
			o.q = newQueuedTrading(o.tradingService, 256, 25*time.Millisecond)
			log.Info("🔄 [OMS] 交易队列已重新创建（周期切换后恢复）")
		}
	}

	if o.riskManager != nil {
		o.riskManager.Start(ctx)
	}
	go o.startMonitoringForExistingHedges(ctx)

	// 运行指标（debug）
	if o.metricsCancel == nil {
		metricsCtx, cancel := context.WithCancel(context.Background())
		o.metricsCancel = cancel
		go o.metricsLoop(metricsCtx)
	}
}

func (o *OMS) startMonitoringForExistingHedges(ctx context.Context) {
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
	for entryOrderID, hedgeOrderID := range pendingHedges {
		entryOrder, entryExists := o.tradingService.GetOrder(entryOrderID)
		if !entryExists || entryOrder == nil || !entryOrder.IsFilled() {
			continue
		}
		hedgeOrder, hedgeExists := o.tradingService.GetOrder(hedgeOrderID)
		if !hedgeExists || hedgeOrder == nil {
			continue
		}
		if hedgeOrder.IsFilled() {
			o.mu.Lock()
			delete(o.pendingHedges, entryOrderID)
			o.mu.Unlock()
			continue
		}
		if o.hedgeReorder != nil {
			entryFilledTime := time.Now()
			if entryOrder.FilledAt != nil {
				entryFilledTime = *entryOrder.FilledAt
			}
			entryAskCents := entryOrder.Price.ToCents()
			if entryOrder.FilledPrice != nil {
				entryAskCents = entryOrder.FilledPrice.ToCents()
			}
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

func (o *OMS) Stop() {
	if o.riskManager != nil {
		o.riskManager.Stop()
	}
	if o.q != nil {
		o.q.Close()
	}
	if o.metricsCancel != nil {
		o.metricsCancel()
		o.metricsCancel = nil
	}
}

func (o *OMS) GetRiskManager() *RiskManager { return o.riskManager }
func (o *OMS) GetHedgeReorder() *HedgeReorder { return o.hedgeReorder }

func (o *OMS) GetRiskManagementStatus() *RiskManagementStatus {
	status := &RiskManagementStatus{CurrentAction: "idle"}

	if o.riskManager != nil {
		exposures := o.riskManager.GetExposures()
		unhedged := make([]*RiskExposure, 0, len(exposures))
		for _, exp := range exposures {
			if exp.HedgeStatus != domain.OrderStatusFilled {
				unhedged = append(unhedged, exp)
			}
		}

		status.RiskExposuresCount = len(unhedged)
		status.RiskExposures = make([]RiskExposureInfo, 0, len(unhedged))

		aggressiveTimeoutSeconds := 60.0
		if o.riskManager.config != nil && o.riskManager.config.GetAggressiveHedgeTimeoutSeconds() > 0 {
			aggressiveTimeoutSeconds = float64(o.riskManager.config.GetAggressiveHedgeTimeoutSeconds())
		}

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

		for _, exp := range unhedged {
			countdownSeconds := aggressiveTimeoutSeconds - exp.ExposureSeconds
			if countdownSeconds < 0 {
				countdownSeconds = 0
			}

			originalHedgePriceCents := 0
			if exp.HedgeOrderID != "" && o.tradingService != nil {
				if hedgeOrder, ok := o.tradingService.GetOrder(exp.HedgeOrderID); ok && hedgeOrder != nil {
					originalHedgePriceCents = hedgeOrder.Price.ToCents()
				}
			}

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

		o.riskManager.mu.Lock()
		status.CurrentAction = o.riskManager.currentAction
		status.CurrentActionEntry = o.riskManager.currentActionEntry
		status.CurrentActionHedge = o.riskManager.currentActionHedge
		status.CurrentActionTime = o.riskManager.currentActionTime
		status.CurrentActionDesc = o.riskManager.currentActionDesc
		status.TotalAggressiveHedges = o.riskManager.totalAggressiveHedges
		o.riskManager.mu.Unlock()
	}

	if o.hedgeReorder != nil && status.CurrentAction == "idle" {
		o.hedgeReorder.mu.Lock()
		if o.hedgeReorder.currentAction != "idle" {
			status.CurrentAction = o.hedgeReorder.currentAction
			status.CurrentActionEntry = o.hedgeReorder.currentActionEntry
			status.CurrentActionHedge = o.hedgeReorder.currentActionHedge
			status.CurrentActionTime = o.hedgeReorder.currentActionTime
			status.CurrentActionDesc = o.hedgeReorder.currentActionDesc
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

// RiskManagementStatus 风控状态（避免循环导入）
type RiskManagementStatus struct {
	RiskExposuresCount    int
	RiskExposures         []RiskExposureInfo
	CurrentAction         string
	CurrentActionEntry    string
	CurrentActionHedge    string
	CurrentActionTime     time.Time
	CurrentActionDesc     string
	TotalReorders         int
	TotalAggressiveHedges int
	TotalFakEats          int

	RepriceOldPriceCents     int
	RepriceNewPriceCents     int
	RepricePriceChangeCents  int
	RepriceStrategy          string
	RepriceEntryCostCents    int
	RepriceMarketAskCents    int
	RepriceIdealPriceCents   int
	RepriceTotalCostCents    int
	RepriceProfitCents       int
}

type RiskExposureInfo struct {
	EntryOrderID    string
	EntryTokenType  string
	EntrySize       float64
	EntryPriceCents int
	HedgeOrderID    string
	HedgeStatus     string
	ExposureSeconds float64
	MaxLossCents    int

	OriginalHedgePriceCents int
	NewHedgePriceCents      int
	CountdownSeconds        float64
}

