package oms

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/betbot/gobet/clob/types"
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

	q  *queuedTrading
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

	// 价格盯盘止损（事件驱动）：entryOrderID -> watch state
	priceStopWatches map[string]*priceStopWatch

	// per-entry 预算 + per-market 冷静期（防止极端行情执行风暴）
	entryBudgets map[string]*entryBudget
	cooldowns    map[string]cooldownInfo

	// 兜底机制去重：防止同一个 entryOrderID 重复创建对冲订单
	hedgeFallbackOnce map[string]*sync.Once

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
		tradingService:    ts,
		config:            cfg,
		q:                 q,
		hm:                newHedgeMetrics(),
		reorderLimiter:    newPerMarketLimiter(30, 30), // 每 market：容量30，按分钟补给30（≈每2秒一次）
		fakLimiter:        newPerMarketLimiter(10, 10), // 每 market：FAK 更贵，容量10，按分钟补给10
		orderExecutor:     oe,
		positionManager:   pm,
		riskManager:       rm,
		hedgeReorder:      hr,
		pendingHedges:     make(map[string]string),
		priceStopWatches:  make(map[string]*priceStopWatch),
		hedgeFallbackOnce: make(map[string]*sync.Once),
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
	o.priceStopWatches = make(map[string]*priceStopWatch)
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
				// 兜底机制：延迟检查一次，如果还是没有 Hedge 订单，则自动创建
				// ✅ 使用 sync.Once 防止重复创建（同一个 entryOrderID 可能触发多次 OnOrderUpdate）
				o.mu.Lock()
				onceKey := fmt.Sprintf("hedge_fallback_%s", order.OrderID)
				once, exists := o.hedgeFallbackOnce[onceKey]
				if !exists {
					once = &sync.Once{}
					if o.hedgeFallbackOnce == nil {
						o.hedgeFallbackOnce = make(map[string]*sync.Once)
					}
					o.hedgeFallbackOnce[onceKey] = once
				}
				o.mu.Unlock()
				
				go func() {
					once.Do(func() {
						time.Sleep(100 * time.Millisecond)
						o.mu.RLock()
						if id, exists := o.pendingHedges[order.OrderID]; exists {
							hedgeOrderID = id
						}
						o.mu.RUnlock()

						if hedgeOrderID != "" && o.riskManager != nil {
							o.riskManager.UpdateHedgeOrderID(order.OrderID, hedgeOrderID)
							log.Debugf("🔄 [OMS] 延迟找到 Hedge 订单ID: entryOrderID=%s hedgeOrderID=%s", order.OrderID, hedgeOrderID)
						} else {
							// 再次检查，避免并发创建
							o.mu.RLock()
							if id, exists := o.pendingHedges[order.OrderID]; exists {
								hedgeOrderID = id
							}
							o.mu.RUnlock()
							
							if hedgeOrderID != "" {
								log.Debugf("🔄 [OMS] 二次检查找到 Hedge 订单ID: entryOrderID=%s hedgeOrderID=%s", order.OrderID, hedgeOrderID)
								return
							}
							
							// 兜底：Entry 订单成交但没有 Hedge 订单，自动创建 Hedge 订单
							log.Warnf("🚨 [OMS] 检测到 Entry 订单成交但无 Hedge 订单，自动创建对冲单: entryOrderID=%s direction=%s filledSize=%.4f",
								order.OrderID, order.TokenType, order.FilledSize)
							
							market := o.tradingService.GetCurrentMarketInfo()
							if market != nil && market.Slug == order.MarketSlug {
								// 确定对冲方向
								var hedgeDirection domain.TokenType
								if order.TokenType == domain.TokenTypeUp {
									hedgeDirection = domain.TokenTypeDown
								} else {
									hedgeDirection = domain.TokenTypeUp
								}
								
								// 使用实际成交数量
								hedgeSize := order.FilledSize
								if hedgeSize <= 0 {
									hedgeSize = order.Size
								}
								
								// 创建对冲订单
								hedgeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
								defer cancel()
								if err := o.AutoHedgePosition(hedgeCtx, market, hedgeDirection, hedgeSize, order); err != nil {
									log.Errorf("❌ [OMS] 自动创建对冲单失败: entryOrderID=%s err=%v", order.OrderID, err)
								} else {
									log.Infof("✅ [OMS] 已自动创建对冲单（兜底机制）: entryOrderID=%s hedgeDirection=%s hedgeSize=%.4f",
										order.OrderID, hedgeDirection, hedgeSize)
								}
							}
						}
					})
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

			// 价格止损：Entry 成交后启动盯价协程（优先用价格触发，不再依赖纯时间）
			if hedgeOrderID != "" {
				o.startPriceStopWatcher(order, hedgeOrderID)
			}
		} else if !order.IsEntryOrder {
			o.riskManager.UpdateHedgeStatus(order.OrderID, order.Status)
		}
	}

	var shouldTriggerMerge bool
	var marketSlug string
	var entryForMetrics string

	o.mu.Lock()
	if order.IsFilled() && !order.IsEntryOrder {
		// 记录 hedge 成交耗时（优先用 HedgeOrderID 关联 entry）
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
					log.Infof("🔄 [OMS] 对冲单完成，立即触发合并当前周期持仓: market=%s entryOrderID=%s hedgeOrderID=%s", 
						market.Slug, entryForMetrics, order.OrderID)
					go func() {
						mergeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						
						// ✅ 从配置获取 merge 触发延迟时间（默认 15 秒）
						mergeDelaySeconds := 15
						if o.config != nil {
							autoMerge := o.config.GetAutoMerge()
							if autoMerge.MergeTriggerDelaySeconds > 0 {
								mergeDelaySeconds = autoMerge.MergeTriggerDelaySeconds
							}
						}
						
						// ✅ 延迟等待，确保持仓数据完全同步到交易所和 Data API
						log.Infof("⏳ [OMS] 等待 %d 秒，确保持仓数据完全同步: market=%s", mergeDelaySeconds, market.Slug)
						time.Sleep(time.Duration(mergeDelaySeconds) * time.Second)
						
						// ✅ 主动同步持仓数据：从 Data API 获取最新持仓并更新到 OrderEngine
						log.Infof("🔄 [OMS] 开始同步持仓数据: market=%s", market.Slug)
						if err := o.tradingService.ReconcileMarketPositionsFromDataAPI(mergeCtx, market); err != nil {
							log.Warnf("⚠️ [OMS] 同步持仓数据失败: market=%s err=%v (继续执行 merge)", market.Slug, err)
						} else {
							log.Infof("✅ [OMS] 持仓数据同步完成: market=%s", market.Slug)
						}
						
						// 短暂延迟，确保持仓数据已写入 OrderEngine
						time.Sleep(500 * time.Millisecond)
						
						// 在 merge 前再次检查持仓，记录详细日志
						positions := o.tradingService.GetOpenPositionsForMarket(market.Slug)
						log.Infof("🔍 [OMS] Merge 前持仓检查: market=%s 持仓数量=%d", market.Slug, len(positions))
						for i, pos := range positions {
							if pos != nil {
								log.Infof("🔍 [OMS] 持仓[%d]: positionID=%s tokenType=%s size=%.4f status=%s", 
									i, pos.ID, pos.TokenType, pos.Size, pos.Status)
							}
						}
						
						o.capital.TryMergeCurrentCycle(mergeCtx, market)
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
	o.mu.Lock()
	o.priceStopWatches = make(map[string]*priceStopWatch)
	o.mu.Unlock()
	if o.metricsCancel != nil {
		o.metricsCancel()
		o.metricsCancel = nil
	}
}

func (o *OMS) GetRiskManager() *RiskManager   { return o.riskManager }
func (o *OMS) GetHedgeReorder() *HedgeReorder { return o.hedgeReorder }

// AutoHedgePosition 自动对冲持仓不平衡（由 PositionMonitor 或策略层调用）
// hedgeDirection: 要买的对冲方向（TokenTypeUp=买UP，TokenTypeDown=买DOWN）
// entryOrder: 主单订单（用于启动价格盯盘），如果为 nil 则不启动价格盯盘
func (o *OMS) AutoHedgePosition(ctx context.Context, market *domain.Market, hedgeDirection domain.TokenType, size float64, entryOrder *domain.Order) error {
	if o == nil || market == nil || size <= 0 {
		return fmt.Errorf("参数无效")
	}

	// 获取当前市场价格
	_, yesAsk, _, noAsk, _, err := o.tradingService.GetTopOfBook(ctx, market)
	if err != nil {
		return fmt.Errorf("获取订单簿价格失败: %w", err)
	}

	// ✅ 确定对冲订单参数：根据要买的对冲方向选择 AssetID 和价格
	var hedgeAssetID string
	var hedgePrice domain.Price
	var hedgeTokenType domain.TokenType
	var entryPriceCents int
	if hedgeDirection == domain.TokenTypeUp {
		// 要买 UP 来对冲（对冲 DOWN 持仓）
		hedgeAssetID = market.YesAssetID
		hedgePrice = yesAsk
		hedgeTokenType = domain.TokenTypeUp
	} else {
		// 要买 DOWN 来对冲（对冲 UP 持仓）
		hedgeAssetID = market.NoAssetID
		hedgePrice = noAsk
		hedgeTokenType = domain.TokenTypeDown
	}

	// ✅ 计算理想对冲价格：使用 entry 订单的成交价格（如果有）来计算理想对冲价格
	// 理想对冲价格 = 100 - entry价格 - hedgeOffsetCents
	// 这样可以确保对冲后的净收益接近 hedgeOffsetCents
	if entryOrder != nil && entryOrder.FilledPrice != nil {
		entryPriceCents = entryOrder.FilledPrice.ToCents()
	} else if entryOrder != nil {
		entryPriceCents = entryOrder.Price.ToCents()
	}
	
	// 如果配置了 hedgeOffsetCents，且 entry 订单价格已知，计算理想对冲价格
	if entryPriceCents > 0 && o.config != nil {
		hedgeOffsetCents := o.config.GetHedgeOffsetCents()
		idealHedgeCents := 100 - entryPriceCents - hedgeOffsetCents
		if idealHedgeCents >= 1 && idealHedgeCents <= 99 {
			// 使用理想价格和市场价格中的较小值（更保守，避免过度支付）
			currentHedgeCents := hedgePrice.ToCents()
			if idealHedgeCents < currentHedgeCents {
				// 从 cents 转换为 Price：1 cent = 0.01，使用 PriceFromDecimal
				hedgePrice = domain.PriceFromDecimal(float64(idealHedgeCents) / 100.0)
				log.Debugf("💰 [OMS] 使用理想对冲价格: entryPrice=%dc offset=%dc idealHedge=%dc marketAsk=%dc finalPrice=%dc",
					entryPriceCents, hedgeOffsetCents, idealHedgeCents, currentHedgeCents, idealHedgeCents)
			} else {
				log.Debugf("💰 [OMS] 理想对冲价格高于市场价，使用市场价: entryPrice=%dc offset=%dc idealHedge=%dc marketAsk=%dc finalPrice=%dc",
					entryPriceCents, hedgeOffsetCents, idealHedgeCents, currentHedgeCents, currentHedgeCents)
			}
		}
	}

	if hedgePrice.Pips <= 0 {
		return fmt.Errorf("对冲价格无效: %d", hedgePrice.Pips)
	}

	// ✅ GTC 订单精度要求（对冲单使用 GTC）：
	// - Price: 2位小数（tick size 0.01）
	// - Size (taker amount): 2位小数（GTC订单要求）
	// - USDC金额 (maker amount): 4位小数（GTC订单要求）
	// - 最小金额: $1 USDC
	// - 最小 size: 5 shares（Polymarket 要求）
	priceDecimal := hedgePrice.ToDecimal()
	
	// 确保价格是 2 位小数
	priceDecimal = float64(int(priceDecimal*100+0.5)) / 100
	
	// ✅ 修复：GTC订单要求 taker amount (token) 最多2位小数，不是4位小数
	// 先舍入到2位小数
	hedgeSize := float64(int(size*100+0.5)) / 100
	
	// 计算 USDC 金额（maker amount），GTC订单要求最多4位小数
	usdcValue := hedgeSize * priceDecimal
	usdcValue = float64(int(usdcValue*10000+0.5)) / 10000 // 舍入到4位小数
	
	// ✅ 修复：检查最小 size 要求（Polymarket 要求 GTC 订单最小 size 为 5）
	const minGTCShareSize = 5.0
	if hedgeSize < minGTCShareSize {
		hedgeSize = minGTCShareSize
		// 重新计算 USDC 金额
		usdcValue = hedgeSize * priceDecimal
		usdcValue = float64(int(usdcValue*10000+0.5)) / 10000
		log.Warnf("⚠️ [OMS] 对冲订单 size 小于最小值 5，自动调整: size=%.2f → %.2f (GTC订单要求)",
			size, hedgeSize)
	}
	
	// 如果买入订单，确保最小订单金额
	// ⚠️ 重要：Polymarket 要求市场买入订单（FAK/GTC BUY）的最小金额为 $1 USDC
	minOrderUSDC := 1.01 // 默认值（留一点余量，避免舍入误差）
	if o.config != nil {
		configMinOrderUSDC := o.config.GetMinOrderUSDC()
		if configMinOrderUSDC > 0 {
			minOrderUSDC = configMinOrderUSDC
		}
	}
	
	// ✅ 修复：对于对冲订单（entryOrder != nil），需要满足最小size要求（5），但尽量保持接近Entry订单大小
	// 如果Entry订单size < 5，需要调整到5以满足Polymarket要求
	if entryOrder != nil {
		originalEntrySize := entryOrder.FilledSize
		if originalEntrySize <= 0 {
			originalEntrySize = entryOrder.Size
		}
		
		// 如果调整后的size与原始Entry size差异较大，记录警告
		if hedgeSize > originalEntrySize*1.2 { // 允许20%的差异
			log.Warnf("⚠️ [OMS] 对冲订单 size 因最小要求调整: entrySize=%.4f hedgeSize=%.2f (GTC订单最小size=5)",
				originalEntrySize, hedgeSize)
		}
		
		// 检查金额是否满足最小要求
		if usdcValue < minOrderUSDC {
			log.Warnf("⚠️ [OMS] 对冲订单金额不足最小要求: size=%.2f price=%.2f usdcValue=%.4f minOrderUSDC=%.2f entrySize=%.4f",
				hedgeSize, priceDecimal, usdcValue, minOrderUSDC, originalEntrySize)
		}
	} else {
		// 非对冲订单（PositionMonitor 场景），可以调整 size 以满足最小金额
		if priceDecimal > 0 {
			// 迭代调整，确保最终金额满足最小要求
			maxIterations := 5
			for i := 0; i < maxIterations; i++ {
				usdcValue = hedgeSize * priceDecimal
				usdcValue = float64(int(usdcValue*10000+0.5)) / 10000 // GTC订单：4位小数
				
				if usdcValue >= minOrderUSDC {
					break // 满足要求，退出循环
				}
				
				// 不满足要求，调整 size（GTC订单：2位小数）
				requiredSize := minOrderUSDC / priceDecimal
				hedgeSize = float64(int(requiredSize*100+0.5)) / 100 // 舍入到2位小数
				
				// 确保最小 size（5）
				if hedgeSize < minGTCShareSize {
					hedgeSize = minGTCShareSize
				}
			}
			
			// 最终检查：如果仍然不满足，强制调整
			usdcValue = hedgeSize * priceDecimal
			usdcValue = float64(int(usdcValue*10000+0.5)) / 10000
			if usdcValue < minOrderUSDC {
				// 强制调整到至少满足最小金额要求
				requiredSize := minOrderUSDC / priceDecimal
				hedgeSize = float64(int(requiredSize*100+0.5)) / 100 // GTC订单：2位小数
				if hedgeSize < minGTCShareSize {
					hedgeSize = minGTCShareSize
				}
				usdcValue = hedgeSize * priceDecimal
				usdcValue = float64(int(usdcValue*10000+0.5)) / 10000
				log.Warnf("⚠️ [OMS] 强制调整对冲订单大小以满足最小金额要求: size=%.2f price=%.2f usdcValue=%.4f minOrderUSDC=%.2f",
					hedgeSize, priceDecimal, usdcValue, minOrderUSDC)
			}
		}
	}
	
	// 将价格转换回 Price 类型
	hedgePrice = domain.PriceFromDecimal(priceDecimal)

	// ✅ 修复：对冲单使用 GTC 而不是 FAK
	// FAK 订单要求立即匹配，如果订单簿没有匹配的订单会被取消
	// 对于对冲单，我们使用 GTC 订单，让它留在订单簿中等待成交
	// 这样即使当前订单簿暂时没有匹配，订单也会保留，后续有匹配时自动成交
	// 如果确实需要快速成交，可以使用略高于 ask 的价格（但这里使用 ask 价格即可）
	hedgeOrder := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      hedgeAssetID,
		TokenType:    hedgeTokenType, // ✅ 修复：使用正确的 TokenType（要买的对冲方向）
		Side:         types.SideBuy,
		Price:        hedgePrice,
		Size:         hedgeSize,
		OrderType:    types.OrderTypeGTC, // ✅ 使用 GTC 而不是 FAK，让订单留在订单簿中等待成交
		IsEntryOrder: false,
		BypassRiskOff: true, // 风控动作：允许绕过短时 risk-off
		DisableSizeAdjust: true, // 严格一对一：避免系统自动放大 size
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
	}

	result, err := o.placeOrder(ctx, hedgeOrder)
	if err != nil {
		return fmt.Errorf("下对冲单失败: %w", err)
	}

	if result == nil || result.OrderID == "" {
		return fmt.Errorf("对冲订单创建失败")
	}

	log.Infof("✅ [OMS] 自动对冲已执行: market=%s hedgeDirection=%s hedgeTokenType=%s size=%.4f price=%.2f usdcValue=%.2f orderID=%s",
		market.Slug, hedgeDirection, hedgeTokenType, hedgeSize, priceDecimal, usdcValue, result.OrderID)

	// ✅ 注册到 pendingHedges：确保对冲单能被正确识别为系统订单，避免递归对冲
	if entryOrder != nil && entryOrder.OrderID != "" {
		o.RecordPendingHedge(entryOrder.OrderID, result.OrderID)
		log.Debugf("📝 [OMS] 已注册对冲单到 pendingHedges: entryOrderID=%s hedgeOrderID=%s", entryOrder.OrderID, result.OrderID)
	}

	// ✅ 启动价格盯盘：如果提供了 entry 订单，在对冲单创建成功后启动价格盯盘
	// 价格盯盘会实时监控价格变化，一旦超过价格区间（soft/hard stop 或 take profit），立即市价锁定
	if entryOrder != nil && entryOrder.OrderID != "" {
		o.startPriceStopWatcher(entryOrder, result.OrderID)
		log.Debugf("📉 [OMS] 已启动价格盯盘: entryOrderID=%s hedgeOrderID=%s", entryOrder.OrderID, result.OrderID)
	}

	return nil
}

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

	RepriceOldPriceCents    int
	RepriceNewPriceCents    int
	RepricePriceChangeCents int
	RepriceStrategy         string
	RepriceEntryCostCents   int
	RepriceMarketAskCents   int
	RepriceIdealPriceCents  int
	RepriceTotalCostCents   int
	RepriceProfitCents      int
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
