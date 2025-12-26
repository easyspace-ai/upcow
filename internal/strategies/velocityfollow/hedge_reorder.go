package velocityfollow

import (
	"context"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)

// monitorAndReorderHedge 监控对冲单成交状态，如果超时未成交则重新下单
func (s *Strategy) monitorAndReorderHedge(ctx context.Context, market *domain.Market,
	entryOrderID, hedgeOrderID, hedgeAsset string, hedgePrice domain.Price, hedgeShares float64,
	entryFilledTime time.Time, entryFilledSize float64, entryAskCents int, winner domain.TokenType) {

	timeout := time.Duration(s.HedgeReorderTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second // 默认 30 秒
	}

	deadline := entryFilledTime.Add(timeout)
	checkInterval := 1 * time.Second // 每秒检查一次

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()

			// 检查是否超时
			if now.After(deadline) {
				// 超时：检查对冲单状态
				if s.TradingService == nil {
					return
				}

				hedgeFilled := false
				if ord, ok := s.TradingService.GetOrder(hedgeOrderID); ok && ord != nil {
					hedgeFilled = ord.Status == domain.OrderStatusFilled
				}

				if hedgeFilled {
					// 对冲单已成交，停止监控
					log.Infof("✅ [%s] 对冲单监控结束：对冲单已成交 orderID=%s", ID, hedgeOrderID)
					return
				}

				// 对冲单未成交，取消旧单并重新下单
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
					s.mu.Unlock()
				}

				// 重新下单后，继续监控新订单（最多再等一次超时时间）
				hedgeOrderID = ""
				if newHedgeResult != nil && newHedgeResult.OrderID != "" {
					hedgeOrderID = newHedgeResult.OrderID
					deadline = time.Now().Add(timeout) // 重置超时时间
				} else {
					// 重新下单失败，停止监控
					return
				}
			} else {
				// 未超时，检查对冲单是否已成交
				if s.TradingService == nil {
					continue
				}

				if ord, ok := s.TradingService.GetOrder(hedgeOrderID); ok && ord != nil {
					if ord.Status == domain.OrderStatusFilled {
						// 对冲单已成交，停止监控
						log.Infof("✅ [%s] 对冲单监控结束：对冲单已成交 orderID=%s (耗时 %.1f秒)",
							ID, hedgeOrderID, time.Since(entryFilledTime).Seconds())
						return
					}
				}
			}
		}
	}
}
