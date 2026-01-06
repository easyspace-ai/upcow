package oms

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategies/velocityfollow/brain"
	"github.com/sirupsen/logrus"
)

var reorderLog = logrus.WithField("module", "hedge_reorder")

// HedgeReorder 对冲单重下管理器
type HedgeReorder struct {
	tradingService      *services.TradingService
	config              ConfigInterface
	oms                 *OMS // 反向引用，用于更新 pendingHedges
	riskProfitCalculator *brain.RiskProfitCalculator
	
	// 状态跟踪（用于 UI 显示）
	mu                  sync.Mutex
	currentAction      string // "idle" | "canceling" | "reordering" | "fak_eating"
	currentActionEntry string
	currentActionHedge string
	currentActionTime  time.Time
	currentActionDesc  string
	totalReorders      int // 总重下次数
	totalFakEats       int // 总 FAK 吃单次数
	
	// 调价详情（用于 UI 显示）
	repriceOldPriceCents    int    // 原价格（分）
	repriceNewPriceCents    int    // 新价格（分）
	repricePriceChangeCents int    // 价格变化（分）
	repriceStrategy         string // 调价策略描述
	repriceEntryCostCents   int    // Entry成本（分）
	repriceMarketAskCents   int    // 市场ask价格（分）
	repriceIdealPriceCents  int    // 理想价格（分）
	repriceTotalCostCents   int    // 总成本（分）
	repriceProfitCents      int    // 利润（分）
}

// NewHedgeReorder 创建对冲单重下管理器
func NewHedgeReorder(ts *services.TradingService, cfg ConfigInterface, oms *OMS) *HedgeReorder {
	return &HedgeReorder{
		tradingService:       ts,
		config:               cfg,
		oms:                  oms,
		riskProfitCalculator: brain.NewRiskProfitCalculator(ts),
	}
}

// MonitorAndReorderHedge 监控对冲单成交状态，如果超时未成交则重新下单
// 支持两个超时机制（分阶段处理）：
// 1. HedgeReorderTimeoutSeconds (默认15秒): 重新下GTC限价单（重新计算价格，允许负收益）
// 2. HedgeTimeoutFakSeconds (默认0=禁用): 撤单并以FAK吃单（强制立即成交，防止亏损过多）
func (hr *HedgeReorder) MonitorAndReorderHedge(ctx context.Context, market *domain.Market,
	entryOrderID, hedgeOrderID, hedgeAsset string, hedgePrice domain.Price, hedgeShares float64,
	entryFilledTime time.Time, entryFilledSize float64, entryAskCents int, winner domain.TokenType) {

	reorderTimeout := time.Duration(hr.config.GetHedgeReorderTimeoutSeconds()) * time.Second
	if reorderTimeout <= 0 {
		reorderTimeout = 15 * time.Second // 默认 15 秒
	}

	fakTimeout := time.Duration(hr.config.GetHedgeTimeoutFakSeconds()) * time.Second
	fakDeadline := time.Time{}
	if fakTimeout > 0 {
		fakDeadline = entryFilledTime.Add(fakTimeout)
	}

	reorderDeadline := entryFilledTime.Add(reorderTimeout)
	checkInterval := 1 * time.Second // 每秒检查一次
	reorderDone := false              // 标记是否已经执行过重下操作
	maxReorderAttempts := 10         // 最大重试次数，防止无限重试
	reorderAttempts := 0             // 当前重试次数

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	reorderLog.Infof("🔍 [调价监控] 开始监控对冲单: entryOrderID=%s hedgeOrderID=%s reorderTimeout=%ds fakTimeout=%ds entryFilledTime=%s",
		entryOrderID, hedgeOrderID, hr.config.GetHedgeReorderTimeoutSeconds(), hr.config.GetHedgeTimeoutFakSeconds(), entryFilledTime.Format("15:04:05"))

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			elapsed := now.Sub(entryFilledTime).Seconds()

			// 检查对冲单是否已成交
			if hr.tradingService == nil {
				continue
			}

			hedgeFilled := false
			// 只有当hedgeOrderID不为空时才检查订单状态
			if hedgeOrderID != "" {
				if ord, ok := hr.tradingService.GetOrder(hedgeOrderID); ok && ord != nil {
					hedgeFilled = ord.Status == domain.OrderStatusFilled

					if hedgeFilled {
						// 记录成交时间和耗时
						reorderLog.Debugf("✅ [调价监控] 对冲单已成交: entryOrderID=%s hedgeOrderID=%s elapsed=%.1fs (未触发调价，订单在%.1f秒内成交)",
							entryOrderID, hedgeOrderID, elapsed, elapsed)
						// 对冲单已成交，清除未完成的对冲单跟踪，允许开启下一轮交易
						if hr.oms != nil {
							hr.oms.mu.Lock()
							if hr.oms.pendingHedges != nil {
								if _, exists := hr.oms.pendingHedges[entryOrderID]; exists {
									delete(hr.oms.pendingHedges, entryOrderID)
									reorderLog.Debugf("✅ 对冲单已成交，清除未完成跟踪: entryOrderID=%s hedgeOrderID=%s",
										entryOrderID, hedgeOrderID)
								}
							}
							hr.oms.mu.Unlock()
						}
						return
					}

					// 如果订单已取消或失败，也应该停止监控
					if ord.Status == domain.OrderStatusCanceled || ord.Status == domain.OrderStatusFailed {
						reorderLog.Warnf("⚠️ [调价监控] 对冲单已取消或失败，停止监控: orderID=%s status=%s elapsed=%.1fs", 
							hedgeOrderID, ord.Status, elapsed)
						if hr.oms != nil {
							hr.oms.mu.Lock()
							if hr.oms.pendingHedges != nil {
								delete(hr.oms.pendingHedges, entryOrderID)
							}
							hr.oms.mu.Unlock()
						}
						return
					}
					
					// 每5秒记录一次订单状态（用于调试）
					if int(elapsed)%5 == 0 && ord.Status == domain.OrderStatusOpen {
						reorderLog.Debugf("🔍 [调价监控] 订单仍在开放中: entryOrderID=%s hedgeOrderID=%s status=%s elapsed=%.1fs deadline=%.1fs",
							entryOrderID, hedgeOrderID, ord.Status, elapsed, reorderTimeout.Seconds())
					}
				} else {
					// 订单查询失败或订单不存在
					reorderLog.Debugf("⚠️ [调价监控] 无法查询订单状态: hedgeOrderID=%s elapsed=%.1fs (订单可能不存在或查询失败)",
						hedgeOrderID, elapsed)
				}
			}

			// 检查是否达到FAK吃单超时
			if fakTimeout > 0 && !fakDeadline.IsZero() && now.After(fakDeadline) && !hedgeFilled {
				hr.handleFakTimeout(ctx, market, entryOrderID, hedgeOrderID, hedgeAsset, hedgeShares, winner)
				return
			}

			// 检查是否达到重下超时
			shouldReorder := now.After(reorderDeadline) && !reorderDone && !hedgeFilled
			if shouldReorder {
				elapsed := now.Sub(entryFilledTime).Seconds()
				reorderLog.Infof("⏰ [调价触发] 达到重下超时: now=%s deadline=%s elapsed=%.1fs entryOrderID=%s hedgeOrderID=%s",
					now.Format("15:04:05"), reorderDeadline.Format("15:04:05"), elapsed, entryOrderID, hedgeOrderID)
				// 检查是否超过最大重试次数
				if reorderAttempts >= maxReorderAttempts {
					reorderLog.Errorf("🚨 对冲单重下次数已达上限（%d次），停止重试: entryOrderID=%s hedgeOrderID=%s",
						maxReorderAttempts, entryOrderID, hedgeOrderID)
					// 如果还有FAK超时，继续等待FAK处理；否则返回
					if fakTimeout <= 0 || fakDeadline.IsZero() || now.After(fakDeadline) {
						return
					}
					continue
				}

				// 执行重下逻辑
				newHedgeOrderID, success := hr.reorderHedge(ctx, market, entryOrderID, hedgeOrderID, hedgeAsset,
					hedgePrice, hedgeShares, entryFilledTime, entryAskCents, winner)
				if success && newHedgeOrderID != "" {
					hedgeOrderID = newHedgeOrderID
					reorderDeadline = time.Now().Add(reorderTimeout) // 重置重下超时时间
					reorderDone = false                              // 重置标记，允许再次重下
					reorderAttempts++
				} else {
					// 重下失败，等待一段时间后重试
					retryDelay := 5 * time.Second
					reorderDeadline = time.Now().Add(retryDelay)
					reorderDone = false
					reorderAttempts++
				}
			}
		}
	}
}

// handleFakTimeout 处理FAK超时：撤单并以FAK吃单
func (hr *HedgeReorder) handleFakTimeout(ctx context.Context, market *domain.Market,
	entryOrderID, hedgeOrderID, hedgeAsset string, hedgeShares float64, winner domain.TokenType) {

	reorderLog.Warnf("⏰ 对冲单超时未成交（%d秒），撤单并以FAK吃单: entryOrderID=%s hedgeOrderID=%s",
		hr.config.GetHedgeTimeoutFakSeconds(), entryOrderID, hedgeOrderID)

	// 先取消对冲单（如果hedgeOrderID不为空）
	if hedgeOrderID != "" {
		if err := hr.tradingService.CancelOrder(ctx, hedgeOrderID); err != nil {
			reorderLog.Warnf("⚠️ 取消对冲单失败: orderID=%s err=%v", hedgeOrderID, err)
		}
		time.Sleep(500 * time.Millisecond) // 等待撤单确认
	}

	// 获取当前卖一价（ask）
	fakCtx, fakCancel := context.WithTimeout(ctx, 5*time.Second)
	defer fakCancel()

	_, yesAsk, _, noAsk, source, err := hr.tradingService.GetTopOfBook(fakCtx, market)
	if err != nil {
		reorderLog.Errorf("❌ 获取订单簿价格失败，无法以FAK吃单: err=%v", err)
		return
	}

	// 确定对冲单的ask价格
	var hedgeAskPrice domain.Price
	if winner == domain.TokenTypeUp {
		// Entry是UP，Hedge是DOWN，使用noAsk
		hedgeAskPrice = noAsk
	} else {
		// Entry是DOWN，Hedge是UP，使用yesAsk
		hedgeAskPrice = yesAsk
	}

	if hedgeAskPrice.Pips <= 0 {
		reorderLog.Errorf("❌ 订单簿ask价格无效，无法以FAK吃单: hedgeAskPrice=%d", hedgeAskPrice.Pips)
		return
	}

	hedgeAskCents := hedgeAskPrice.ToCents()
	reorderLog.Debugf("💰 准备以FAK吃单: price=%dc (ask) size=%.4f source=%s", hedgeAskCents, hedgeShares, source)

	// 以卖一价下FAK买单
	fakHedgeOrder := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      hedgeAsset,
		TokenType:    opposite(winner),
		Side:         types.SideBuy,
		Price:        hedgeAskPrice,
		Size:         hedgeShares,
		OrderType:    types.OrderTypeFAK,
		IsEntryOrder: false,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
	}

	entryOrderIDRef := entryOrderID
	fakHedgeOrder.HedgeOrderID = &entryOrderIDRef

	fakHedgeResult, err := hr.tradingService.PlaceOrder(fakCtx, fakHedgeOrder)
	if err != nil {
		reorderLog.Errorf("❌ 以FAK吃单失败: err=%v (主单已成交，存在风险敞口)", err)
		return
	}

	if fakHedgeResult != nil && fakHedgeResult.OrderID != "" {
		reorderLog.Debugf("✅ 已以FAK吃单: orderID=%s price=%dc (原对冲单=%s)",
			fakHedgeResult.OrderID, hedgeAskCents, hedgeOrderID)

		// 更新跟踪状态
		if hr.oms != nil {
			hr.oms.mu.Lock()
			if fakHedgeResult.Status == domain.OrderStatusFilled {
				if hr.oms.pendingHedges != nil {
					delete(hr.oms.pendingHedges, entryOrderID)
				}
			} else {
				hr.oms.pendingHedges[entryOrderID] = fakHedgeResult.OrderID
			}
			hr.oms.mu.Unlock()
		}

		// 更新状态：完成FAK吃单
		hr.mu.Lock()
		hr.totalFakEats++
		hr.currentAction = "idle"
		hr.currentActionDesc = ""
		hr.mu.Unlock()
	} else {
		// FAK吃单失败，重置状态
		hr.mu.Lock()
		hr.currentAction = "idle"
		hr.currentActionDesc = ""
		hr.mu.Unlock()
	}
}

// reorderHedge 重新下对冲单
func (hr *HedgeReorder) reorderHedge(ctx context.Context, market *domain.Market,
	entryOrderID, hedgeOrderID, hedgeAsset string, hedgePrice domain.Price, hedgeShares float64,
	entryFilledTime time.Time, entryAskCents int, winner domain.TokenType) (string, bool) {

	elapsed := time.Since(entryFilledTime).Seconds()
	reorderLog.Infof("🔄 [调价执行] 对冲单超时未成交（已等待%.1f秒，超时阈值=%d秒），取消旧单并重新计算价格: entryOrderID=%s hedgeOrderID=%s",
		elapsed, hr.config.GetHedgeReorderTimeoutSeconds(), entryOrderID, hedgeOrderID)
	
	// 更新状态：正在撤单
	oldHedgePriceCents := hedgePrice.ToCents()
	hr.mu.Lock()
	hr.currentAction = "canceling"
	hr.currentActionEntry = entryOrderID
	hr.currentActionHedge = hedgeOrderID
	hr.currentActionTime = time.Now()
	hr.currentActionDesc = "取消旧对冲单"
	// 记录原价格（用于UI显示）
	hr.repriceOldPriceCents = oldHedgePriceCents
	hr.mu.Unlock()

	// 取消旧对冲单
	if hedgeOrderID != "" {
		reorderLog.Infof("🔄 [调价步骤1-撤单] 开始取消旧对冲单: hedgeOrderID=%s 原价格=%dc",
			hedgeOrderID, hedgePrice.ToCents())
		if err := hr.tradingService.CancelOrder(ctx, hedgeOrderID); err != nil {
			reorderLog.Errorf("❌ [调价步骤1-撤单] 取消旧对冲单失败: orderID=%s err=%v", hedgeOrderID, err)
			return "", false
		}
		reorderLog.Infof("✅ [调价步骤1-撤单] 旧对冲单已取消: hedgeOrderID=%s", hedgeOrderID)
		time.Sleep(500 * time.Millisecond) // 等待撤单确认
	}

	// 重新获取订单簿价格
	reorderCtx, reorderCancel := context.WithTimeout(ctx, 5*time.Second)
	defer reorderCancel()

	reorderLog.Infof("🔄 [调价步骤2-获取价格] 重新获取订单簿价格...")
	_, yesAsk, _, noAsk, source, err := hr.tradingService.GetTopOfBook(reorderCtx, market)
	if err != nil {
		reorderLog.Errorf("❌ [调价步骤2-获取价格] 重新获取订单簿价格失败: err=%v", err)
		return "", false
	}
	
	yesAskCents := yesAsk.ToCents()
	noAskCents := noAsk.ToCents()
	reorderLog.Infof("📊 [调价步骤2-获取价格] 当前订单簿价格: YES_ask=%dc NO_ask=%dc source=%s",
		yesAskCents, noAskCents, source)

	// 重新计算对冲价格
	hedgeAskCentsDirect := int(yesAskCents)
	if winner == domain.TokenTypeUp {
		// Hedge 是 DOWN
		hedgeAskCentsDirect = int(noAskCents)
	}

	// 基于 Entry 成本约束的理想对冲价格
	idealHedgeCents := 100 - entryAskCents - hr.config.GetHedgeOffsetCents()
	reorderLog.Infof("💰 [调价步骤3-计算价格] Entry成本=%dc offset=%dc => 理想对冲价格=%dc 市场ask价格=%dc",
		entryAskCents, hr.config.GetHedgeOffsetCents(), idealHedgeCents, hedgeAskCentsDirect)
	
	// 记录调价计算信息（用于UI显示）
	hr.mu.Lock()
	hr.repriceEntryCostCents = entryAskCents
	hr.repriceMarketAskCents = hedgeAskCentsDirect
	hr.repriceIdealPriceCents = idealHedgeCents
	hr.mu.Unlock()

	// 计算新的对冲价格
	var newLimitCents int
	var priceStrategy string
	if hr.config.GetAllowNegativeProfitOnHedgeReorder() {
		// 允许负收益：可以接受更高的对冲价格
		maxAllowedHedgeCents := idealHedgeCents + hr.config.GetMaxNegativeProfitCents()
		if hedgeAskCentsDirect > 0 && hedgeAskCentsDirect <= maxAllowedHedgeCents {
			newLimitCents = hedgeAskCentsDirect
			priceStrategy = "使用市场ask价格"
		} else {
			newLimitCents = maxAllowedHedgeCents
			priceStrategy = fmt.Sprintf("使用最大允许价格（市场ask=%dc > 最大允许=%dc）", hedgeAskCentsDirect, maxAllowedHedgeCents)
		}

		totalCostCents := entryAskCents + newLimitCents
		profitCents := 100 - totalCostCents
		reorderLog.Infof("💰 [调价步骤3-计算价格] 允许负收益模式: %s => 新价格=%dc 总成本=%dc 利润=%dc",
			priceStrategy, newLimitCents, totalCostCents, profitCents)
		if profitCents < 0 {
			reorderLog.Warnf("⚠️ [调价步骤3-计算价格] 允许负收益重新下单: entryAsk=%dc newHedge=%dc totalCost=%dc profit=%dc",
				entryAskCents, newLimitCents, totalCostCents, profitCents)
		}
		
		// 记录调价详情（用于UI显示）
		hr.mu.Lock()
		hr.repriceStrategy = priceStrategy
		hr.repriceTotalCostCents = totalCostCents
		hr.repriceProfitCents = profitCents
		hr.mu.Unlock()
	} else {
		// 不允许负收益：必须遵守"互补挂单"原则
		newLimitCents = idealHedgeCents
		if hedgeAskCentsDirect > 0 && newLimitCents >= hedgeAskCentsDirect {
			newLimitCents = hedgeAskCentsDirect - 1
			priceStrategy = fmt.Sprintf("互补挂单（市场ask=%dc，调整为=%dc）", hedgeAskCentsDirect, newLimitCents)
		} else {
			priceStrategy = fmt.Sprintf("互补挂单（理想价格=%dc，市场ask=%dc）", idealHedgeCents, hedgeAskCentsDirect)
		}

		reorderLog.Infof("💰 [调价步骤3-计算价格] 不允许负收益模式: %s => 新价格=%dc", priceStrategy, newLimitCents)
		
		// 计算总成本和利润（不允许负收益模式）
		totalCostCents := entryAskCents + newLimitCents
		profitCents := 100 - totalCostCents
		
		// 记录调价详情（用于UI显示）
		hr.mu.Lock()
		hr.repriceStrategy = priceStrategy
		hr.repriceTotalCostCents = totalCostCents
		hr.repriceProfitCents = profitCents
		hr.mu.Unlock()

		if newLimitCents <= 0 || newLimitCents >= 100 {
			reorderLog.Errorf("❌ [调价步骤3-计算价格] 对冲重下失败：互补挂单价格无效: entryAsk=%dc hedgeOffset=%dc => idealHedge=%dc",
				entryAskCents, hr.config.GetHedgeOffsetCents(), idealHedgeCents)
			return "", false
		}
	}

	// 使用风险利润计算器验证重下后的利润锁定情况
	if hr.riskProfitCalculator != nil {
		potentialTrade := hr.riskProfitCalculator.CalculatePotentialTradeRiskProfit(
			entryAskCents, newLimitCents, hedgeShares, hedgeShares, winner)
		if potentialTrade != nil {
			if potentialTrade.IsLocked {
				reorderLog.Debugf("✅ 重下后仍可锁定利润: minProfit=%.4f lockQuality=%.2f%%",
					potentialTrade.MinProfit, potentialTrade.LockQuality*100)
			} else {
				reorderLog.Warnf("⚠️ 重下后无法锁定利润: minProfit=%.4f totalCost=%dc",
					potentialTrade.MinProfit, potentialTrade.TotalCostCents)
			}
		}
	}

	if newLimitCents <= 0 || newLimitCents >= 100 {
		reorderLog.Errorf("🚨 对冲重下失败：计算出的价格无效: newLimitCents=%dc", newLimitCents)
		return "", false
	}

	newHedgePrice := domain.Price{Pips: newLimitCents * 100}
	// oldHedgePriceCents 已在函数开始时定义
	priceChange := newLimitCents - oldHedgePriceCents

	reorderLog.Infof("🔄 [调价步骤4-重新下单] 准备重新下单: 原价格=%dc 新价格=%dc 价格变化=%+dc size=%.4f",
		oldHedgePriceCents, newLimitCents, priceChange, hedgeShares)

	// 重新下单
	newHedgeOrder := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      hedgeAsset,
		TokenType:    opposite(winner),
		Side:         types.SideBuy,
		Price:        newHedgePrice,
		Size:         hedgeShares,
		OrderType:    types.OrderTypeGTC,
		IsEntryOrder: false,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
	}

	entryOrderIDRef := entryOrderID
	newHedgeOrder.HedgeOrderID = &entryOrderIDRef

	reorderLog.Infof("📤 [调价步骤4-重新下单] 提交新对冲单到交易服务...")
	newHedgeResult, err := hr.tradingService.PlaceOrder(reorderCtx, newHedgeOrder)
	if err != nil {
		reorderLog.Errorf("❌ [调价步骤4-重新下单] 重新下对冲单失败: err=%v", err)
		return "", false
	}

	if newHedgeResult != nil && newHedgeResult.OrderID != "" {
		reorderLog.Infof("✅ [调价成功] 对冲单已重新提交: orderID=%s (原订单=%s) 新价格=%dc 原价格=%dc 价格变化=%+dc source=%s",
			newHedgeResult.OrderID, hedgeOrderID, newLimitCents, oldHedgePriceCents, priceChange, source)

		// 更新跟踪状态
		if hr.oms != nil {
			hr.oms.mu.Lock()
			if hr.oms.pendingHedges != nil {
				hr.oms.pendingHedges[entryOrderID] = newHedgeResult.OrderID
			}
			hr.oms.mu.Unlock()
			
			// 关键修复：通知 RiskManager 更新订单ID，确保状态同步
			if hr.oms.riskManager != nil {
				hr.oms.riskManager.UpdateHedgeOrderID(entryOrderID, newHedgeResult.OrderID)
				reorderLog.Debugf("🔄 [调价] 已通知 RiskManager 更新订单ID: entryID=%s oldHedgeID=%s newHedgeID=%s",
					entryOrderID, hedgeOrderID, newHedgeResult.OrderID)
			}
		}

		// 更新状态：完成重下
		hr.mu.Lock()
		hr.totalReorders++
		hr.currentAction = "reordering"
		hr.currentActionEntry = entryOrderID
		hr.currentActionHedge = newHedgeResult.OrderID
		hr.currentActionTime = time.Now()
		hr.currentActionDesc = fmt.Sprintf("已重新下单，新价格=%dc", newLimitCents)
		// 记录调价详情（用于UI显示）
		hr.repriceOldPriceCents = oldHedgePriceCents
		hr.repriceNewPriceCents = newLimitCents
		hr.repricePriceChangeCents = priceChange
		hr.mu.Unlock()
		
		// 延迟重置状态为 idle（给 UI 时间显示）
		go func() {
			time.Sleep(5 * time.Second) // 延长显示时间，让用户能看到调价详情
			hr.mu.Lock()
			hr.currentAction = "idle"
			hr.currentActionDesc = ""
			// 保留调价详情一段时间，即使action变为idle
			hr.mu.Unlock()
		}()

		return newHedgeResult.OrderID, true
	}

	// 重下失败，重置状态
	hr.mu.Lock()
	hr.currentAction = "idle"
	hr.currentActionDesc = ""
	hr.mu.Unlock()

	return "", false
}

// opposite 获取相反方向的 TokenType
func opposite(tokenType domain.TokenType) domain.TokenType {
	if tokenType == domain.TokenTypeUp {
		return domain.TokenTypeDown
	}
	return domain.TokenTypeUp
}
