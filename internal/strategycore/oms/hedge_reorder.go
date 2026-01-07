package oms

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategycore/brain"
	"github.com/sirupsen/logrus"
)

var reorderLog = logrus.WithField("module", "hedge_reorder")

// HedgeReorder 对冲单重下管理器
type HedgeReorder struct {
	tradingService       *services.TradingService
	config               ConfigInterface
	oms                  *OMS
	riskProfitCalculator *brain.RiskProfitCalculator

	mu                 sync.Mutex
	currentAction      string
	currentActionEntry string
	currentActionHedge string
	currentActionTime  time.Time
	currentActionDesc  string
	totalReorders      int
	totalFakEats       int

	repriceOldPriceCents    int
	repriceNewPriceCents    int
	repricePriceChangeCents int
	repriceStrategy         string
	repriceEntryCostCents   int
	repriceMarketAskCents   int
	repriceIdealPriceCents  int
	repriceTotalCostCents   int
	repriceProfitCents      int
}

func NewHedgeReorder(ts *services.TradingService, cfg ConfigInterface, oms *OMS) *HedgeReorder {
	return &HedgeReorder{
		tradingService:       ts,
		config:               cfg,
		oms:                  oms,
		riskProfitCalculator: brain.NewRiskProfitCalculator(ts),
	}
}

func (hr *HedgeReorder) MonitorAndReorderHedge(ctx context.Context, market *domain.Market,
	entryOrderID, hedgeOrderID, hedgeAsset string, hedgePrice domain.Price, hedgeShares float64,
	entryFilledTime time.Time, entryFilledSize float64, entryAskCents int, winner domain.TokenType) {

	_ = entryFilledSize

	reorderTimeout := time.Duration(hr.config.GetHedgeReorderTimeoutSeconds()) * time.Second
	if reorderTimeout <= 0 {
		reorderTimeout = 15 * time.Second
	}

	fakTimeout := time.Duration(hr.config.GetHedgeTimeoutFakSeconds()) * time.Second
	fakDeadline := time.Time{}
	if fakTimeout > 0 {
		fakDeadline = entryFilledTime.Add(fakTimeout)
	}

	reorderDeadline := entryFilledTime.Add(reorderTimeout)
	checkInterval := 1 * time.Second
	reorderDone := false
	maxReorderAttempts := 10
	reorderAttempts := 0

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

			if hr.tradingService == nil {
				continue
			}

			// per-entry 最大存活时间：到点仍未完成对冲，直接走安全底线（FAK）并触发冷静期，避免“拖延->风暴”
			if hr.oms != nil && market != nil {
				_, _, _, maxAge, _ := hr.oms.entryGuardParams()
				if maxAge > 0 && time.Since(entryFilledTime) > maxAge {
					reorderLog.Warnf("⏳ [per-entry] entry 超过最大存活时间，触发 FAK 安全对冲并进入冷静期: entryOrderID=%s age=%.1fs maxAge=%.1fs",
						entryOrderID, time.Since(entryFilledTime).Seconds(), maxAge.Seconds())
					hr.oms.RecordFAK(entryOrderID, market.Slug, entryFilledTime)
					hr.handleFakTimeout(ctx, market, entryOrderID, hedgeOrderID, hedgeAsset, hedgeShares, winner)
					return
				}
			}

			hedgeFilled := false
			if hedgeOrderID != "" {
				if ord, ok := hr.tradingService.GetOrder(hedgeOrderID); ok && ord != nil {
					hedgeFilled = ord.Status == domain.OrderStatusFilled
					if hedgeFilled {
						reorderLog.Debugf("✅ [调价监控] 对冲单已成交: entryOrderID=%s hedgeOrderID=%s elapsed=%.1fs (未触发调价，订单在%.1f秒内成交)",
							entryOrderID, hedgeOrderID, elapsed, elapsed)
						if hr.oms != nil {
							hr.oms.mu.Lock()
							if hr.oms.pendingHedges != nil {
								// 并发安全：只在映射仍指向“当前监控的 hedgeOrderID”时删除，
								// 避免外部协程（如价格止损）换了 hedge 订单后被误删。
								if cur, exists := hr.oms.pendingHedges[entryOrderID]; exists && cur == hedgeOrderID {
									delete(hr.oms.pendingHedges, entryOrderID)
									reorderLog.Debugf("✅ 对冲单已成交，清除未完成跟踪: entryOrderID=%s hedgeOrderID=%s",
										entryOrderID, hedgeOrderID)
								}
							}
							hr.oms.mu.Unlock()
						}
						return
					}

					if ord.Status == domain.OrderStatusCanceled || ord.Status == domain.OrderStatusFailed {
						reorderLog.Warnf("⚠️ [调价监控] 对冲单已取消或失败，停止监控: orderID=%s status=%s elapsed=%.1fs",
							hedgeOrderID, ord.Status, elapsed)
						if hr.oms != nil {
							hr.oms.mu.Lock()
							if hr.oms.pendingHedges != nil {
								// 并发安全：仅删除当前映射仍指向该 hedgeOrderID 的情况
								if cur, exists := hr.oms.pendingHedges[entryOrderID]; exists && cur == hedgeOrderID {
									delete(hr.oms.pendingHedges, entryOrderID)
								}
							}
							hr.oms.mu.Unlock()
						}
						return
					}

					if int(elapsed)%5 == 0 && ord.Status == domain.OrderStatusOpen {
						reorderLog.Debugf("🔍 [调价监控] 订单仍在开放中: entryOrderID=%s hedgeOrderID=%s status=%s elapsed=%.1fs deadline=%.1fs",
							entryOrderID, hedgeOrderID, ord.Status, elapsed, reorderTimeout.Seconds())
					}
				} else {
					reorderLog.Debugf("⚠️ [调价监控] 无法查询订单状态: hedgeOrderID=%s elapsed=%.1fs (订单可能不存在或查询失败)",
						hedgeOrderID, elapsed)
				}
			}

			if fakTimeout > 0 && !fakDeadline.IsZero() && now.After(fakDeadline) && !hedgeFilled {
				hr.handleFakTimeout(ctx, market, entryOrderID, hedgeOrderID, hedgeAsset, hedgeShares, winner)
				return
			}

			shouldReorder := now.After(reorderDeadline) && !reorderDone && !hedgeFilled
			if shouldReorder {
				reorderLog.Infof("⏰ [调价触发] 达到重下超时: now=%s deadline=%s elapsed=%.1fs entryOrderID=%s hedgeOrderID=%s",
					now.Format("15:04:05"), reorderDeadline.Format("15:04:05"), elapsed, entryOrderID, hedgeOrderID)

				// per-entry 预算：单笔最多重下 N 次；超限后不再重下（只等待 FAK/风控兜底），同时触发冷静期阻止新开仓
				if hr.oms != nil && market != nil {
					if !hr.oms.ConsumeReorderAttempt(entryOrderID, market.Slug, entryFilledTime) {
						reorderLog.Warnf("⏸️ [per-entry] entry 重下预算耗尽，停止重下并等待风控/FAK: entryOrderID=%s", entryOrderID)
						reorderDeadline = time.Now().Add(5 * time.Second)
						reorderDone = false
						continue
					}
				}

				// 预算保护：超出预算则不计入 attempts，只延迟再检查，避免把系统拖进“重下风暴”
				if hr.oms != nil && market != nil && !hr.oms.allowReorder(market.Slug) {
					reorderLog.Warnf("⏸️ [重下预算] market=%s reorder budget exceeded, postpone", market.Slug)
					reorderDeadline = time.Now().Add(3 * time.Second)
					reorderDone = false
					continue
				}

				if reorderAttempts >= maxReorderAttempts {
					reorderLog.Errorf("🚨 对冲单重下次数已达上限（%d次），停止重试: entryOrderID=%s hedgeOrderID=%s",
						maxReorderAttempts, entryOrderID, hedgeOrderID)
					if fakTimeout <= 0 || fakDeadline.IsZero() || now.After(fakDeadline) {
						return
					}
					continue
				}

				newHedgeOrderID, success := hr.reorderHedge(ctx, market, entryOrderID, hedgeOrderID, hedgeAsset,
					hedgePrice, hedgeShares, entryFilledTime, entryAskCents, winner)
				if success && newHedgeOrderID != "" {
					hedgeOrderID = newHedgeOrderID
					reorderDeadline = time.Now().Add(reorderTimeout)
					reorderDone = false
					reorderAttempts++
				} else {
					retryDelay := 5 * time.Second
					reorderDeadline = time.Now().Add(retryDelay)
					reorderDone = false
					reorderAttempts++
				}
			}
		}
	}
}

func (hr *HedgeReorder) handleFakTimeout(ctx context.Context, market *domain.Market,
	entryOrderID, hedgeOrderID, hedgeAsset string, hedgeShares float64, winner domain.TokenType) {

	reorderLog.Warnf("⏰ 对冲单超时未成交（%d秒），撤单并以FAK吃单: entryOrderID=%s hedgeOrderID=%s",
		hr.config.GetHedgeTimeoutFakSeconds(), entryOrderID, hedgeOrderID)

	// per-entry 记录：FAK 属于安全底线，不阻断，但用于触发冷静期与统计
	if hr.oms != nil && market != nil {
		hr.oms.RecordFAK(entryOrderID, market.Slug, time.Now())
	}

	// FAK 是安全底线：如果预算耗尽，仍执行，但打告警（避免“为了限频而不对冲”）。
	if hr.oms != nil && market != nil && !hr.oms.allowFAK(market.Slug) {
		reorderLog.Warnf("⚠️ [FAK预算] market=%s FAK budget exceeded, still proceeding (safety first)", market.Slug)
	}

	if hedgeOrderID != "" {
		var err error
		if hr.oms != nil {
			if market != nil {
				hr.oms.RecordCancel(entryOrderID, market.Slug, time.Now())
			}
			err = hr.oms.cancelOrder(ctx, hedgeOrderID)
		} else {
			err = hr.tradingService.CancelOrder(ctx, hedgeOrderID)
		}
		if err != nil {
			reorderLog.Warnf("⚠️ 取消对冲单失败: orderID=%s err=%v", hedgeOrderID, err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	fakCtx, fakCancel := context.WithTimeout(ctx, 5*time.Second)
	defer fakCancel()

	_, yesAsk, _, noAsk, source, err := hr.tradingService.GetTopOfBook(fakCtx, market)
	if err != nil {
		reorderLog.Errorf("❌ 获取订单簿价格失败，无法以FAK吃单: err=%v", err)
		return
	}

	var hedgeAskPrice domain.Price
	if winner == domain.TokenTypeUp {
		hedgeAskPrice = noAsk
	} else {
		hedgeAskPrice = yesAsk
	}
	if hedgeAskPrice.Pips <= 0 {
		reorderLog.Errorf("❌ 订单簿ask价格无效，无法以FAK吃单: hedgeAskPrice=%d", hedgeAskPrice.Pips)
		return
	}
	hedgeAskCents := hedgeAskPrice.ToCents()
	reorderLog.Debugf("💰 准备以FAK吃单: price=%dc (ask) size=%.4f source=%s", hedgeAskCents, hedgeShares, source)

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

	var fakHedgeResult *domain.Order
	if hr.oms != nil {
		fakHedgeResult, err = hr.oms.placeOrder(fakCtx, fakHedgeOrder)
	} else {
		fakHedgeResult, err = hr.tradingService.PlaceOrder(fakCtx, fakHedgeOrder)
	}
	if err != nil {
		reorderLog.Errorf("❌ 以FAK吃单失败: err=%v (主单已成交，存在风险敞口)", err)
		return
	}

	if fakHedgeResult != nil && fakHedgeResult.OrderID != "" {
		reorderLog.Debugf("✅ 已以FAK吃单: orderID=%s price=%dc (原对冲单=%s)",
			fakHedgeResult.OrderID, hedgeAskCents, hedgeOrderID)

		if hr.oms != nil {
			hr.oms.mu.Lock()
			if fakHedgeResult.Status == domain.OrderStatusFilled {
				if hr.oms.pendingHedges != nil {
					// 并发安全：只有当当前映射仍是“旧 hedgeOrderID 或空”时才删除；
					// 若外部已经切换到新的 hedgeID，这里不应误删。
					if cur, exists := hr.oms.pendingHedges[entryOrderID]; !exists || cur == hedgeOrderID || cur == fakHedgeResult.OrderID {
						delete(hr.oms.pendingHedges, entryOrderID)
					}
				}
			} else {
				hr.oms.pendingHedges[entryOrderID] = fakHedgeResult.OrderID
			}
			hr.oms.mu.Unlock()
		}

		hr.mu.Lock()
		hr.totalFakEats++
		hr.currentAction = "idle"
		hr.currentActionDesc = ""
		hr.mu.Unlock()
	} else {
		hr.mu.Lock()
		hr.currentAction = "idle"
		hr.currentActionDesc = ""
		hr.mu.Unlock()
	}
}

func (hr *HedgeReorder) reorderHedge(ctx context.Context, market *domain.Market,
	entryOrderID, hedgeOrderID, hedgeAsset string, hedgePrice domain.Price, hedgeShares float64,
	entryFilledTime time.Time, entryAskCents int, winner domain.TokenType) (string, bool) {

	elapsed := time.Since(entryFilledTime).Seconds()
	reorderLog.Infof("🔄 [调价执行] 对冲单超时未成交（已等待%.1f秒，超时阈值=%d秒），取消旧单并重新计算价格: entryOrderID=%s hedgeOrderID=%s",
		elapsed, hr.config.GetHedgeReorderTimeoutSeconds(), entryOrderID, hedgeOrderID)

	oldHedgePriceCents := hedgePrice.ToCents()
	hr.mu.Lock()
	hr.currentAction = "canceling"
	hr.currentActionEntry = entryOrderID
	hr.currentActionHedge = hedgeOrderID
	hr.currentActionTime = time.Now()
	hr.currentActionDesc = "取消旧对冲单"
	hr.repriceOldPriceCents = oldHedgePriceCents
	hr.mu.Unlock()

	if hedgeOrderID != "" {
		reorderLog.Infof("🔄 [调价步骤1-撤单] 开始取消旧对冲单: hedgeOrderID=%s 原价格=%dc", hedgeOrderID, hedgePrice.ToCents())
		var err error
		if hr.oms != nil {
			if market != nil {
				hr.oms.RecordCancel(entryOrderID, market.Slug, entryFilledTime)
			}
			err = hr.oms.cancelOrder(ctx, hedgeOrderID)
		} else {
			err = hr.tradingService.CancelOrder(ctx, hedgeOrderID)
		}
		if err != nil {
			reorderLog.Errorf("❌ [调价步骤1-撤单] 取消旧对冲单失败: orderID=%s err=%v", hedgeOrderID, err)
			return "", false
		}
		reorderLog.Infof("✅ [调价步骤1-撤单] 旧对冲单已取消: hedgeOrderID=%s", hedgeOrderID)
		time.Sleep(500 * time.Millisecond)
	}

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
	reorderLog.Infof("📊 [调价步骤2-获取价格] 当前订单簿价格: YES_ask=%dc NO_ask=%dc source=%s", yesAskCents, noAskCents, source)

	hedgeAskCentsDirect := int(yesAskCents)
	if winner == domain.TokenTypeUp {
		hedgeAskCentsDirect = int(noAskCents)
	}

	idealHedgeCents := 100 - entryAskCents - hr.config.GetHedgeOffsetCents()
	reorderLog.Infof("💰 [调价步骤3-计算价格] Entry成本=%dc offset=%dc => 理想对冲价格=%dc 市场ask价格=%dc",
		entryAskCents, hr.config.GetHedgeOffsetCents(), idealHedgeCents, hedgeAskCentsDirect)

	hr.mu.Lock()
	hr.repriceEntryCostCents = entryAskCents
	hr.repriceMarketAskCents = hedgeAskCentsDirect
	hr.repriceIdealPriceCents = idealHedgeCents
	hr.mu.Unlock()

	var newLimitCents int
	var priceStrategy string
	if hr.config.GetAllowNegativeProfitOnHedgeReorder() {
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

		hr.mu.Lock()
		hr.repriceStrategy = priceStrategy
		hr.repriceTotalCostCents = totalCostCents
		hr.repriceProfitCents = profitCents
		hr.mu.Unlock()
	} else {
		newLimitCents = idealHedgeCents
		if hedgeAskCentsDirect > 0 && newLimitCents >= hedgeAskCentsDirect {
			newLimitCents = hedgeAskCentsDirect - 1
			priceStrategy = fmt.Sprintf("互补挂单（市场ask=%dc，调整为=%dc）", hedgeAskCentsDirect, newLimitCents)
		} else {
			priceStrategy = fmt.Sprintf("互补挂单（理想价格=%dc，市场ask=%dc）", idealHedgeCents, hedgeAskCentsDirect)
		}

		reorderLog.Infof("💰 [调价步骤3-计算价格] 不允许负收益模式: %s => 新价格=%dc", priceStrategy, newLimitCents)

		totalCostCents := entryAskCents + newLimitCents
		profitCents := 100 - totalCostCents
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

	if hr.riskProfitCalculator != nil {
		potentialTrade := hr.riskProfitCalculator.CalculatePotentialTradeRiskProfit(entryAskCents, newLimitCents, hedgeShares, hedgeShares, winner)
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
	priceChange := newLimitCents - oldHedgePriceCents
	reorderLog.Infof("🔄 [调价步骤4-重新下单] 准备重新下单: 原价格=%dc 新价格=%dc 价格变化=%+dc size=%.4f",
		oldHedgePriceCents, newLimitCents, priceChange, hedgeShares)

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
	var newHedgeResult *domain.Order
	if hr.oms != nil {
		newHedgeResult, err = hr.oms.placeOrder(reorderCtx, newHedgeOrder)
	} else {
		newHedgeResult, err = hr.tradingService.PlaceOrder(reorderCtx, newHedgeOrder)
	}
	if err != nil {
		reorderLog.Errorf("❌ [调价步骤4-重新下单] 重新下对冲单失败: err=%v", err)
		return "", false
	}

	if newHedgeResult != nil && newHedgeResult.OrderID != "" {
		reorderLog.Infof("✅ [调价成功] 对冲单已重新提交: orderID=%s (原订单=%s) 新价格=%dc 原价格=%dc 价格变化=%+dc source=%s",
			newHedgeResult.OrderID, hedgeOrderID, newLimitCents, oldHedgePriceCents, priceChange, source)

		if hr.oms != nil {
			hr.oms.mu.Lock()
			if hr.oms.pendingHedges != nil {
				hr.oms.pendingHedges[entryOrderID] = newHedgeResult.OrderID
			}
			hr.oms.mu.Unlock()

			if hr.oms.riskManager != nil {
				hr.oms.riskManager.UpdateHedgeOrderID(entryOrderID, newHedgeResult.OrderID)
				reorderLog.Debugf("🔄 [调价] 已通知 RiskManager 更新订单ID: entryID=%s oldHedgeID=%s newHedgeID=%s",
					entryOrderID, hedgeOrderID, newHedgeResult.OrderID)
			}
		}

		hr.mu.Lock()
		hr.totalReorders++
		hr.currentAction = "reordering"
		hr.currentActionEntry = entryOrderID
		hr.currentActionHedge = newHedgeResult.OrderID
		hr.currentActionTime = time.Now()
		hr.currentActionDesc = fmt.Sprintf("已重新下单，新价格=%dc", newLimitCents)
		hr.repriceOldPriceCents = oldHedgePriceCents
		hr.repriceNewPriceCents = newLimitCents
		hr.repricePriceChangeCents = priceChange
		hr.mu.Unlock()

		go func() {
			time.Sleep(5 * time.Second)
			hr.mu.Lock()
			hr.currentAction = "idle"
			hr.currentActionDesc = ""
			hr.mu.Unlock()
		}()

		return newHedgeResult.OrderID, true
	}

	hr.mu.Lock()
	hr.currentAction = "idle"
	hr.currentActionDesc = ""
	hr.mu.Unlock()
	return "", false
}

func opposite(tokenType domain.TokenType) domain.TokenType {
	if tokenType == domain.TokenTypeUp {
		return domain.TokenTypeDown
	}
	return domain.TokenTypeUp
}
