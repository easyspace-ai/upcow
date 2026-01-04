package velocityfollow

import (
	"context"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)

// monitorAndReorderHedge 监控对冲单成交状态，如果超时未成交则重新下单
// 支持两个超时机制：
// 1. HedgeReorderTimeoutSeconds (默认30秒): 重新下GTC限价单
// 2. HedgeTimeoutFakSeconds (默认0=禁用): 撤单并以FAK吃单
func (s *Strategy) monitorAndReorderHedge(ctx context.Context, market *domain.Market,
	entryOrderID, hedgeOrderID, hedgeAsset string, hedgePrice domain.Price, hedgeShares float64,
	entryFilledTime time.Time, entryFilledSize float64, entryAskCents int, winner domain.TokenType) {

	reorderTimeout := time.Duration(s.HedgeReorderTimeoutSeconds) * time.Second
	if reorderTimeout <= 0 {
		reorderTimeout = 30 * time.Second // 默认 30 秒
	}

	fakTimeout := time.Duration(s.HedgeTimeoutFakSeconds) * time.Second
	fakDeadline := time.Time{}
	if fakTimeout > 0 {
		fakDeadline = entryFilledTime.Add(fakTimeout)
	}

	reorderDeadline := entryFilledTime.Add(reorderTimeout)
	checkInterval := 1 * time.Second // 每秒检查一次
	reorderDone := false // 标记是否已经执行过重下操作

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()

			// 检查对冲单是否已成交
			if s.TradingService == nil {
				continue
			}

			hedgeFilled := false
			if ord, ok := s.TradingService.GetOrder(hedgeOrderID); ok && ord != nil {
				hedgeFilled = ord.Status == domain.OrderStatusFilled
				if hedgeFilled {
					// 对冲单已成交，清除未完成的对冲单跟踪，允许开启下一轮交易
					s.mu.Lock()
					if s.pendingHedges != nil {
						if _, exists := s.pendingHedges[entryOrderID]; exists {
							delete(s.pendingHedges, entryOrderID)
							log.Infof("✅ [%s] 对冲单已成交，清除未完成跟踪，允许开启下一轮交易: entryOrderID=%s hedgeOrderID=%s",
								ID, entryOrderID, hedgeOrderID)
						}
					}
					s.mu.Unlock()

					// 对冲单已成交，停止监控
					log.Infof("✅ [%s] 对冲单监控结束：对冲单已成交 orderID=%s (耗时 %.1f秒)",
						ID, hedgeOrderID, time.Since(entryFilledTime).Seconds())
					return
				}
			}

			// 检查是否达到60秒FAK吃单超时
			if fakTimeout > 0 && !fakDeadline.IsZero() && now.After(fakDeadline) && !hedgeFilled {
				// 60秒超时：撤单并以FAK吃单
				log.Warnf("⏰ [%s] 对冲单超时未成交（%d秒），撤单并以FAK吃单: orderID=%s",
					ID, s.HedgeTimeoutFakSeconds, hedgeOrderID)

				// 先取消对冲单
				if err := s.TradingService.CancelOrder(ctx, hedgeOrderID); err != nil {
					log.Warnf("⚠️ [%s] 取消对冲单失败: orderID=%s err=%v", ID, hedgeOrderID, err)
					// 即使取消失败，也尝试继续（可能订单已经不存在）
				} else {
					log.Infof("✅ [%s] 已取消对冲单: orderID=%s", ID, hedgeOrderID)
				}

				// 确认撤单成功：轮询检查订单状态
				cancelConfirmed := false
				maxCancelWait := 3 * time.Second
				cancelCheckDeadline := time.Now().Add(maxCancelWait)
				cancelCheckInterval := 200 * time.Millisecond

				for time.Now().Before(cancelCheckDeadline) {
					if ord, ok := s.TradingService.GetOrder(hedgeOrderID); ok && ord != nil {
						if ord.Status == domain.OrderStatusCanceled || ord.Status == domain.OrderStatusFailed {
							cancelConfirmed = true
							log.Infof("✅ [%s] 已确认对冲单撤单成功: orderID=%s status=%s", ID, hedgeOrderID, ord.Status)
							break
						}
					} else {
						// 订单不存在，视为已取消
						cancelConfirmed = true
						log.Infof("✅ [%s] 对冲单已不存在，视为已取消: orderID=%s", ID, hedgeOrderID)
						break
					}
					time.Sleep(cancelCheckInterval)
				}

				if !cancelConfirmed {
					log.Warnf("⚠️ [%s] 无法确认对冲单撤单状态，但仍尝试以FAK吃单: orderID=%s", ID, hedgeOrderID)
				}

				// 获取当前卖一价（ask）
				fakCtx, fakCancel := context.WithTimeout(ctx, 5*time.Second)
				defer fakCancel()

				_, yesAsk, _, noAsk, source, err := s.TradingService.GetTopOfBook(fakCtx, market)
				if err != nil {
					log.Errorf("❌ [%s] 获取订单簿价格失败，无法以FAK吃单: err=%v", ID, err)
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
					log.Errorf("❌ [%s] 订单簿ask价格无效，无法以FAK吃单: hedgeAskPrice=%d", ID, hedgeAskPrice.Pips)
					return
				}

				hedgeAskCents := hedgeAskPrice.ToCents()
				log.Infof("💰 [%s] 准备以FAK吃单: price=%dc (ask) size=%.4f source=%s", ID, hedgeAskCents, hedgeShares, source)

				// 获取市场精度信息（从缓存）
				var fakTickSize types.TickSize
				var fakNegRisk *bool
				if s.currentPrecision != nil {
					if parsed, err := ParseTickSize(s.currentPrecision.TickSize); err == nil {
						fakTickSize = parsed
					}
					fakNegRisk = boolPtr(s.currentPrecision.NegRisk)
				}

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
					HedgeOrderID: &entryOrderID,
					Status:       domain.OrderStatusPending,
					TickSize:     fakTickSize,
					NegRisk:      fakNegRisk,
					CreatedAt:    time.Now(),
				}

				fakHedgeResult, err := s.TradingService.PlaceOrder(fakCtx, fakHedgeOrder)
				if err != nil {
					log.Errorf("❌ [%s] 以FAK吃单失败: err=%v (主单已成交，存在风险敞口)", ID, err)
				} else if fakHedgeResult != nil && fakHedgeResult.OrderID != "" {
					log.Infof("✅ [%s] 已以FAK吃单: orderID=%s price=%dc (原对冲单=%s)",
						ID, fakHedgeResult.OrderID, hedgeAskCents, hedgeOrderID)

					// 更新跟踪状态
					s.mu.Lock()
					s.lastHedgeOrderID = fakHedgeResult.OrderID
					// FAK订单通常立即成交，如果已成交则清除未完成的对冲单跟踪
					if fakHedgeResult.Status == domain.OrderStatusFilled {
						if s.pendingHedges != nil {
							if _, exists := s.pendingHedges[entryOrderID]; exists {
								delete(s.pendingHedges, entryOrderID)
								log.Infof("✅ [%s] FAK对冲单已立即成交，清除未完成跟踪，允许开启下一轮交易: entryOrderID=%s hedgeOrderID=%s",
									ID, entryOrderID, fakHedgeResult.OrderID)
							}
						}
					}
					s.mu.Unlock()

					// FAK订单通常立即成交，检查一下
					if fakHedgeResult.Status == domain.OrderStatusFilled {
						log.Infof("✅ [%s] FAK对冲单已立即成交: orderID=%s", ID, fakHedgeResult.OrderID)
					}
				} else {
					log.Errorf("❌ [%s] 以FAK吃单失败: 订单ID为空", ID)
				}

				// FAK吃单后，停止监控
				return
			}

			// 检查是否达到30秒重下超时
			if now.After(reorderDeadline) && !reorderDone && !hedgeFilled {
				// 30秒超时：取消旧单并重新下单
				reorderDone = true
				log.Warnf("⏰ [%s] 对冲单超时未成交（%d秒），取消旧单并重新下单: orderID=%s",
					ID, s.HedgeReorderTimeoutSeconds, hedgeOrderID)

				// 取消旧对冲单
				if err := s.TradingService.CancelOrder(ctx, hedgeOrderID); err != nil {
					log.Warnf("⚠️ [%s] 取消旧对冲单失败: orderID=%s err=%v", ID, hedgeOrderID, err)
				} else {
					log.Infof("✅ [%s] 已取消旧对冲单: orderID=%s", ID, hedgeOrderID)
				}

				// 重新获取订单簿价格（确保价格是最新的）
				reorderCtx, reorderCancel := context.WithTimeout(ctx, 5*time.Second)
				defer reorderCancel()

				_, yesAsk, _, noAsk, source, err := s.TradingService.GetTopOfBook(reorderCtx, market)
				if err != nil {
					log.Warnf("⚠️ [%s] 重新获取订单簿价格失败，使用原价格: err=%v", ID, err)
					// 使用原价格继续
				} else {
					// ✅ 修复：对冲单重下也必须遵守“互补挂单”原则，避免追价买到 ask 导致结构性必亏
					oldPriceCents := int(hedgePrice.ToDecimal()*100 + 0.5)
					hedgeAskCentsDirect := int(yesAsk.ToCents())
					if winner == domain.TokenTypeUp {
						// Hedge 是 DOWN
						hedgeAskCentsDirect = noAsk.ToCents()
					}

					// 基于 Entry 成本约束的最大对冲价格（cents）
					// 注：entryAskCents 是 Entry 下单时的实际 ask（FAK）；用它来约束 hedge 的最坏成本。
					maxHedgeCents := 100 - entryAskCents - s.HedgeOffsetCents
					newLimitCents := maxHedgeCents
					if hedgeAskCentsDirect > 0 && newLimitCents >= hedgeAskCentsDirect {
						newLimitCents = hedgeAskCentsDirect - 1
					}
					if newLimitCents <= 0 || newLimitCents >= 100 {
						log.Errorf("🚨 [%s] 对冲重下失败：互补挂单价格无效: entryAsk=%dc hedgeOffset=%dc => maxHedge=%dc (hedgeAsk=%dc)",
							ID, entryAskCents, s.HedgeOffsetCents, maxHedgeCents, hedgeAskCentsDirect)
						// 保守处理：停止重下，维持未对冲风险提示
						return
					}

					hedgePrice = domain.Price{Pips: newLimitCents * 100}
					log.Infof("💰 [%s] 重新计算对冲单价格: 原=%dc 新=%dc (max=%dc hedgeAsk=%dc source=%s)",
						ID, oldPriceCents, newLimitCents, maxHedgeCents, hedgeAskCentsDirect, source)
				}

				// 重新下单
				// 获取市场精度信息（从缓存）
				var newTickSize types.TickSize
				var newNegRisk *bool
				if s.currentPrecision != nil {
					if parsed, err := ParseTickSize(s.currentPrecision.TickSize); err == nil {
						newTickSize = parsed
					}
					newNegRisk = boolPtr(s.currentPrecision.NegRisk)
				}

				newHedgeOrder := &domain.Order{
					MarketSlug:   market.Slug,
					AssetID:      hedgeAsset,
					TokenType:    opposite(winner),
					Side:         types.SideBuy,
					Price:        hedgePrice,
					Size:         hedgeShares,
					OrderType:    types.OrderTypeGTC,
					IsEntryOrder: false,
					HedgeOrderID: &entryOrderID,
					Status:       domain.OrderStatusPending,
					TickSize:     newTickSize, // 使用缓存的精度信息
					NegRisk:      newNegRisk,  // 使用缓存的 neg_risk 信息
					CreatedAt:    time.Now(),
				}

				newHedgeResult, err := s.TradingService.PlaceOrder(reorderCtx, newHedgeOrder)
				if err != nil {
					log.Errorf("❌ [%s] 重新下对冲单失败: err=%v (主单已成交，存在风险敞口)", ID, err)
				} else if newHedgeResult != nil && newHedgeResult.OrderID != "" {
					log.Infof("✅ [%s] 对冲单已重新提交: orderID=%s (原订单=%s)",
						ID, newHedgeResult.OrderID, hedgeOrderID)

					// 更新跟踪状态
					s.mu.Lock()
					s.lastHedgeOrderID = newHedgeResult.OrderID
					// 更新 pendingHedges 中的 hedgeOrderID（如果存在）
					if s.pendingHedges != nil {
						if _, exists := s.pendingHedges[entryOrderID]; exists {
							s.pendingHedges[entryOrderID] = newHedgeResult.OrderID
							log.Debugf("📝 [%s] 更新未完成的对冲单跟踪: entryOrderID=%s oldHedgeOrderID=%s newHedgeOrderID=%s",
								ID, entryOrderID, hedgeOrderID, newHedgeResult.OrderID)
						}
					}
					s.mu.Unlock()
				}

				// 重新下单后，继续监控新订单
				hedgeOrderID = ""
				if newHedgeResult != nil && newHedgeResult.OrderID != "" {
					hedgeOrderID = newHedgeResult.OrderID
					reorderDeadline = time.Now().Add(reorderTimeout) // 重置重下超时时间
					reorderDone = false // 重置标记，允许再次重下
					// 如果配置了FAK超时，也需要更新FAK超时时间（从Entry成交时间开始计算）
					if fakTimeout > 0 {
						fakDeadline = entryFilledTime.Add(fakTimeout)
					}
				} else {
					// 重新下单失败，但如果还有FAK超时，继续等待FAK超时
					if fakTimeout <= 0 || fakDeadline.IsZero() || now.After(fakDeadline) {
						return
					}
					// 否则继续等待FAK超时
				}
			}
		}
	}
}
