package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/metrics"
)

// startOrderStatusSync 定期同步订单状态（通过 API 查询）
// 如果 WebSocket 失败，会自动缩短同步间隔
func (os *OrderSyncService) startOrderStatusSyncImpl(ctx context.Context) {
	s := os.s
	// 获取配置的同步间隔（用于日志）
	withOrdersSeconds := s.orderStatusSyncIntervalWithOrders
	withoutOrdersSeconds := s.orderStatusSyncIntervalWithoutOrders

	log.Infof("🔄 [订单状态同步] 启动定期订单状态同步（有活跃订单时每%d秒，无活跃订单时每%d秒）",
		withOrdersSeconds, withoutOrdersSeconds)

	// 立即执行一次（不等待）
	os.syncAllOrderStatusImpl(ctx)

	// 使用 ticker 来定期同步，但需要动态调整间隔
	// 使用较短的 ticker 间隔（1秒），然后根据条件决定是否执行同步
	// 这样可以更灵活地响应配置变化
	ticker := time.NewTicker(1 * time.Second) // 每1秒检查一次
	defer ticker.Stop()

	lastSyncTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			log.Info("🔄 [订单状态同步] 订单状态同步已停止")
			return
		case <-ticker.C:
			// 检查是否有活跃订单（通过 OrderEngine 查询）
			openOrders := s.GetActiveOrders()
			hasActiveOrders := len(openOrders) > 0

			// 重新读取配置（支持运行时修改）
			currentSyncIntervalWithOrders := time.Duration(s.orderStatusSyncIntervalWithOrders) * time.Second
			currentSyncIntervalWithoutOrders := time.Duration(s.orderStatusSyncIntervalWithoutOrders) * time.Second

			// 根据是否有活跃订单选择同步间隔
			var syncInterval time.Duration
			if hasActiveOrders {
				syncInterval = currentSyncIntervalWithOrders
			} else {
				syncInterval = currentSyncIntervalWithoutOrders
			}

			// 检查是否到了同步时间
			if time.Since(lastSyncTime) >= syncInterval {
				os.syncAllOrderStatusImpl(ctx)
				lastSyncTime = time.Now()
			}
		}
	}
}

// syncAllOrderStatus 同步所有活跃订单的状态
func (os *OrderSyncService) syncAllOrderStatusImpl(ctx context.Context) {
	s := os.s
	metrics.ReconcileRuns.Add(1)

	// 获取当前市场（只同步当前周期的订单）
	currentMarketSlug := s.GetCurrentMarket()

	// 通过 OrderEngine 获取活跃订单
	openOrders := s.GetActiveOrders()

	// 过滤：只处理当前周期的订单
	filteredOrders := make([]*domain.Order, 0, len(openOrders))
	for _, order := range openOrders {
		if order == nil {
			continue
		}
		// 如果设置了当前市场，只处理当前周期的订单
		if currentMarketSlug != "" {
			if order.MarketSlug == "" || order.MarketSlug != currentMarketSlug {
				// 跳过非当前周期的订单（不记录日志，避免噪音）
				continue
			}
		}
		filteredOrders = append(filteredOrders, order)
	}

	orderIDs := make([]string, 0, len(filteredOrders))
	for _, order := range filteredOrders {
		orderIDs = append(orderIDs, order.OrderID)
	}

	if len(orderIDs) == 0 {
		log.Debugf("🔄 [订单状态同步] 没有活跃订单需要同步")
		return
	}

	log.Debugf("🔄 [订单状态同步] 开始同步 %d 个活跃订单的状态", len(orderIDs))

	// 获取所有开放订单
	openOrdersResp, err := s.clobClient.GetOpenOrders(ctx, nil)
	if err != nil {
		log.Warnf("🔄 [订单状态同步] 获取开放订单失败: %v", err)
		metrics.ReconcileErrors.Add(1)
		return
	}

	log.Debugf("🔄 [订单状态同步] API 返回 %d 个开放订单", len(openOrdersResp))

	// 构建开放订单 ID 集合（用于快速查找）
	openOrderIDs := make(map[string]bool)
	// 构建开放订单属性映射（用于通过属性匹配，处理订单 ID 不匹配的情况）
	openOrdersByAttrs := make(map[string]string) // key: "assetID:side:price", value: orderID
	for _, order := range openOrdersResp {
		openOrderIDs[order.ID] = true
		// 构建属性键（用于匹配）
		// order.Price 是 string 类型（来自 API），需要标准化格式
		// 解析价格并格式化为统一格式（保留4位小数）
		apiPrice, err := strconv.ParseFloat(order.Price, 64)
		if err != nil {
			log.Debugf("🔄 [订单状态同步] 解析API订单价格失败: orderID=%s, price=%s, error=%v", order.ID, order.Price, err)
			// 如果解析失败，使用原始字符串（可能格式不一致）
			attrsKey := fmt.Sprintf("%s:%s:%s", order.AssetID, order.Side, order.Price)
			openOrdersByAttrs[attrsKey] = order.ID
		} else {
			// 标准化价格格式（保留4位小数）
			normalizedPrice := fmt.Sprintf("%.4f", apiPrice)
			attrsKey := fmt.Sprintf("%s:%s:%s", order.AssetID, order.Side, normalizedPrice)
			openOrdersByAttrs[attrsKey] = order.ID
		}
	}

	// 检查本地订单是否还在开放订单列表中
	// 使用过滤后的订单列表（只包含当前周期的订单）
	localOrdersMap := make(map[string]*domain.Order)
	for _, order := range filteredOrders {
		localOrdersMap[order.OrderID] = order
	}

	filledCount := 0
	updatedOrderIDs := make(map[string]string) // oldID -> newID
	_ = updatedOrderIDs                        // 保留：用于未来输出/诊断

	for _, orderID := range orderIDs {
		order, exists := localOrdersMap[orderID]
		if !exists {
			continue
		}

		// 订单状态同步策略：谁先确认订单的最终状态（filled/canceled），以谁为准
		// 最终状态（filled/canceled）不应该被中间状态（open/pending）覆盖
		// 1. 如果订单已经是最终状态，且有时间戳（说明已经确认），不应该被覆盖
		// 2. 如果订单不在 API 的 open 列表中，且订单状态不是最终状态，应该更新为最终状态
		// 3. 如果订单在 API 的 open 列表中，但订单状态是最终状态，检查时间戳：
		//    - 如果有时间戳（WebSocket 先到），保持最终状态
		//    - 如果没有时间戳（API 先到），恢复为 open 状态
		if order.IsFinalStatus() {
			// 订单已经是最终状态
			if order.HasFinalStatusTimestamp() {
				// 有时间戳，说明已经确认了最终状态，不应该被覆盖
				if openOrderIDs[orderID] {
					// API 显示订单仍在 open 列表中，但订单已经有最终状态的时间戳
					// 说明 WebSocket 先确认了最终状态，保持最终状态
					log.Debugf("🔄 [订单状态同步] 订单已有最终状态时间戳（WebSocket先到），保持最终状态: orderID=%s status=%s",
						orderID, order.Status)
					s.orderStatusCache.Set(orderID, false)
					continue
				} else {
					// API 确认不在 open 列表中，状态一致，保持最终状态
					log.Debugf("🔄 [订单状态同步] 订单已确认最终状态，API确认不在开放列表中，状态一致: orderID=%s status=%s",
						orderID, order.Status)
					s.orderStatusCache.Set(orderID, false)
					continue
				}
			} else {
				// 没有时间戳，说明最终状态可能还未确认
				if openOrderIDs[orderID] {
					// API 显示订单仍在 open 列表中，恢复为 open 状态（以 API 为准）
					log.Warnf("⚠️ [状态一致性] 订单状态为最终状态但无时间戳，API显示仍在open列表中，恢复为open状态: orderID=%s status=%s",
						orderID, order.Status)
					order.Status = domain.OrderStatusOpen
					order.FilledAt = nil
					order.CanceledAt = nil
					updateCmd := &UpdateOrderCommand{
						id:    fmt.Sprintf("sync_revert_%s", orderID),
						Gen:   s.currentEngineGeneration(),
						Order: order,
					}
					s.orderEngine.SubmitCommand(updateCmd)
					s.orderStatusCache.Set(orderID, true)
					continue
				} else {
					// API 确认不在 open 列表中，设置时间戳确认最终状态
					log.Infof("🔄 [订单状态同步] 订单状态为最终状态但无时间戳，API确认不在开放列表中，设置时间戳确认: orderID=%s status=%s",
						orderID, order.Status)
					now := time.Now()
					if order.Status == domain.OrderStatusFilled && order.FilledAt == nil {
						order.FilledAt = &now
					} else if order.Status == domain.OrderStatusCanceled && order.CanceledAt == nil {
						order.CanceledAt = &now
					}
					updateCmd := &UpdateOrderCommand{
						id:    fmt.Sprintf("sync_confirm_%s", orderID),
						Gen:   s.currentEngineGeneration(),
						Order: order,
					}
					s.orderEngine.SubmitCommand(updateCmd)
					s.orderStatusCache.Set(orderID, false)
					continue
				}
			}
		}

		// 检查缓存（如果缓存显示订单已关闭，直接处理）
		if cachedIsOpen, exists := s.orderStatusCache.Get(orderID); exists && !cachedIsOpen {
			log.Debugf("🔄 [订单状态同步] 缓存显示订单已关闭: orderID=%s", orderID)
		}

		// 首先通过订单 ID 匹配
		if openOrderIDs[orderID] {
			// 订单仍在开放订单列表中，更新缓存
			s.orderStatusCache.Set(orderID, true)

			// 风险4修复：检查WebSocket状态和API状态是否一致
			if order.Status == domain.OrderStatusPending {
				log.Debugf("🔄 [订单状态同步] 订单状态一致: orderID=%s, WebSocket=pending, API=open (正常过渡状态)", orderID)
			} else if order.Status == domain.OrderStatusOpen {
				log.Debugf("🔄 [订单状态同步] 订单状态一致: orderID=%s, WebSocket=open, API=open", orderID)
			} else {
				log.Warnf("⚠️ [状态一致性] 订单状态可能不一致: orderID=%s, WebSocket状态=%s, API状态=open",
					orderID, order.Status)
			}
			continue
		}

		// 告警：订单长时间不在 open 列表，触发一次 SyncOrderStatus（并记录卡单）
		if order != nil && !s.dryRun {
			age := time.Since(order.CreatedAt)
			if age > 20*time.Second {
				log.Warnf("⚠️ [对账告警] 本地订单不在交易所 open 列表，触发 SyncOrderStatus: orderID=%s status=%s age=%v",
					orderID, order.Status, age)
			}
			_ = s.SyncOrderStatus(ctx, orderID)
		}

		// 如果订单 ID 不匹配，尝试通过属性匹配（assetID + side + price）
		priceStr := fmt.Sprintf("%.4f", order.Price.ToDecimal())
		attrsKey := fmt.Sprintf("%s:%s:%s", order.AssetID, string(order.Side), priceStr)

		// 首先尝试精确匹配（assetID + side + price）
		if matchedOrderID, exists := openOrdersByAttrs[attrsKey]; exists {
			log.Infof("🔄 [订单状态同步] 通过属性匹配找到订单: 本地ID=%s, 服务器ID=%s, assetID=%s, side=%s, price=%.4f",
				orderID, matchedOrderID, order.AssetID, order.Side, order.Price.ToDecimal())

			order.OrderID = matchedOrderID
			updatedOrderIDs[orderID] = matchedOrderID

			updateCmd := &UpdateOrderCommand{
				id:    fmt.Sprintf("sync_update_%s", orderID),
				Gen:   s.currentEngineGeneration(),
				Order: order,
			}
			s.orderEngine.SubmitCommand(updateCmd)

			// 更新缓存
			s.orderStatusCache.Delete(orderID)
			s.orderStatusCache.Set(matchedOrderID, true)

			log.Debugf("🔄 [订单状态同步] 订单 ID 已更新: %s -> %s", orderID, matchedOrderID)
			continue
		}

		// 风险5修复：改进订单ID匹配算法（业务规则匹配）
		matched := false
		var bestMatch *struct {
			orderID string
			price   int
			score   float64 // 匹配分数：价格差异越小，分数越高
		}

		if order.IsEntryOrder {
			// 入场订单：价格应该在 60-90 之间
			if order.Price.ToCents() >= 60 && order.Price.ToCents() <= 90 {
				for _, apiOrder := range openOrdersResp {
					apiPrice, err := strconv.ParseFloat(apiOrder.Price, 64)
					if err != nil {
						continue
					}
					apiPriceCents := int(apiPrice * 100)

					if apiOrder.AssetID == order.AssetID &&
						apiOrder.Side == string(order.Side) &&
						apiPriceCents >= 60 && apiPriceCents <= 90 {
						priceDiff := math.Abs(float64(apiPriceCents - order.Price.ToCents()))
						if priceDiff <= 2 {
							score := 1.0 / (1.0 + priceDiff)
							if bestMatch == nil || score > bestMatch.score {
								bestMatch = &struct {
									orderID string
									price   int
									score   float64
								}{
									orderID: apiOrder.ID,
									price:   apiPriceCents,
									score:   score,
								}
							}
						}
					}
				}
			}
		} else {
			// 对冲订单：价格应该在 1-40 之间
			if order.Price.ToCents() >= 1 && order.Price.ToCents() <= 40 {
				for _, apiOrder := range openOrdersResp {
					apiPrice, err := strconv.ParseFloat(apiOrder.Price, 64)
					if err != nil {
						continue
					}
					apiPriceCents := int(apiPrice * 100)

					if apiOrder.AssetID == order.AssetID &&
						apiOrder.Side == string(order.Side) &&
						apiPriceCents >= 1 && apiPriceCents <= 40 {
						priceDiff := math.Abs(float64(apiPriceCents - order.Price.ToCents()))
						if priceDiff <= 2 {
							score := 1.0 / (1.0 + priceDiff)
							if bestMatch == nil || score > bestMatch.score {
								bestMatch = &struct {
									orderID string
									price   int
									score   float64
								}{
									orderID: apiOrder.ID,
									price:   apiPriceCents,
									score:   score,
								}
							}
						}
					}
				}
			}
		}

		if bestMatch != nil {
			matchedOrderID := bestMatch.orderID
			matchedPriceCents := bestMatch.price
			orderType := "入场订单"
			if !order.IsEntryOrder {
				orderType = "对冲订单"
			}
			log.Infof("🔄 [订单状态同步] 通过业务规则匹配找到%s: 本地ID=%s, 服务器ID=%s, assetID=%s, side=%s, 本地价格=%dc, 服务器价格=%dc, 匹配分数=%.2f",
				orderType, orderID, matchedOrderID, order.AssetID, order.Side, order.Price.ToCents(), matchedPriceCents, bestMatch.score)

			order.OrderID = matchedOrderID
			order.Price = domain.Price{Pips: matchedPriceCents * 100} // 1 cent = 100 pips
			updatedOrderIDs[orderID] = matchedOrderID

			updateCmd := &UpdateOrderCommand{
				id:    fmt.Sprintf("sync_update_%s", orderID),
				Gen:   s.currentEngineGeneration(),
				Order: order,
			}
			s.orderEngine.SubmitCommand(updateCmd)

			s.orderStatusCache.Delete(orderID)
			s.orderStatusCache.Set(matchedOrderID, true)

			log.Debugf("🔄 [订单状态同步] %s ID 已更新: %s -> %s", orderType, orderID, matchedOrderID)
			matched = true
		} else {
			// 优化：只有在订单状态不是已成交/已取消时才记录匹配失败警告
			// 如果订单已经通过 WebSocket 更新为 filled，说明已经成交，不需要匹配
			if order.Status != domain.OrderStatusFilled && order.Status != domain.OrderStatusCanceled {
				if order.IsEntryOrder || (!order.IsEntryOrder && order.Price.ToCents() >= 1 && order.Price.ToCents() <= 40) {
					orderType := "入场订单"
					if !order.IsEntryOrder {
						orderType = "对冲订单"
					}
					// 降级为 Debug 级别，减少日志噪音
					log.Debugf("🔄 [订单状态同步] 无法通过业务规则匹配%s: orderID=%s, assetID=%s, side=%s, price=%dc, 可能订单已成交或取消",
						orderType, orderID, order.AssetID, order.Side, order.Price.ToCents())
				}
			}
		}

		if matched {
			continue
		}

		// 本地订单不在交易所 open 列表：视为成交/取消/失败（做一层安全判定）
		if order.Status == domain.OrderStatusFailed {
			log.Debugf("🔄 [订单状态同步] 订单已标记为失败，跳过同步: orderID=%s", orderID)
			continue
		}

		hasServerOrderID := order.OrderID != "" &&
			order.OrderID != orderID &&
			!isLocalGeneratedOrderID(order.OrderID)

		if order.Status == domain.OrderStatusPending && !hasServerOrderID {
			log.Warnf("⚠️ [订单状态同步] 订单可能提交失败: orderID=%s, 本地ID=%s, WebSocket状态=%s, API状态=不在开放列表中（可能是提交失败，而非已成交）",
				orderID, order.OrderID, order.Status)

			order.Status = domain.OrderStatusFailed
			s.orderEngine.SubmitCommand(&UpdateOrderCommand{
				id:    fmt.Sprintf("sync_failed_%s", orderID),
				Gen:   s.currentEngineGeneration(),
				Order: order,
			})
			s.orderStatusCache.Set(orderID, false)
			continue
		}

		// 本地订单不在交易所 open 列表：视为成交/取消/失败
		// 如果订单状态不是最终状态，更新为最终状态（以 API 为准）
		// 如果订单状态已经是最终状态，但无时间戳，设置时间戳确认
		if order.IsFinalStatus() {
			// 订单已经是最终状态，但不在 API 的 open 列表中
			// 如果无时间戳，设置时间戳确认（API 先确认）
			if !order.HasFinalStatusTimestamp() {
				log.Infof("🔄 [订单状态同步] 订单状态为最终状态但无时间戳，API确认不在开放列表中，设置时间戳确认（API先确认）: orderID=%s status=%s",
					orderID, order.Status)
				now := time.Now()
				if order.Status == domain.OrderStatusFilled && order.FilledAt == nil {
					order.FilledAt = &now
				} else if order.Status == domain.OrderStatusCanceled && order.CanceledAt == nil {
					order.CanceledAt = &now
				}
				updateCmd := &UpdateOrderCommand{
					id:    fmt.Sprintf("sync_confirm_%s", orderID),
					Gen:   s.currentEngineGeneration(),
					Order: order,
				}
				s.orderEngine.SubmitCommand(updateCmd)
				s.orderStatusCache.Set(orderID, false)
				continue
			} else {
				// 已有时间戳，状态一致
				log.Debugf("🔄 [订单状态同步] 订单已确认最终状态，API确认不在开放列表中，状态一致: orderID=%s status=%s",
					orderID, order.Status)
				s.orderStatusCache.Set(orderID, false)
				continue
			}
		}

		// 订单状态不是最终状态，但 API 显示不在 open 列表中
		// 在纸交易模式下，不应该强制将订单标记为Filled，因为订单不会真正提交到交易所
		// 订单状态应该由 io_executor.go 中的逻辑决定（基于真实市场价格）
		if s.dryRun {
			// 纸交易模式：跳过强制标记为Filled的逻辑
			// 订单状态应该由 io_executor.go 中的价格匹配逻辑决定
			log.Debugf("🔄 [订单状态同步] 纸交易模式：跳过强制标记为Filled，订单状态由价格匹配逻辑决定: orderID=%s status=%s",
				orderID, order.Status)
			s.orderStatusCache.Set(orderID, false)
			continue
		}

		// 真实交易模式：以 API 为准，更新订单状态为已成交（API 先确认）
		log.Infof("🔄 [订单状态同步] 订单已成交（API先确认）: orderID=%s, side=%s, price=%.4f, size=%.2f",
			orderID, order.Side, order.Price.ToDecimal(), order.Size)

		// 设置时间戳确认最终状态（API 先确认）
		now := time.Now()
		order.FilledAt = &now
		order.Status = domain.OrderStatusFilled
		// 如果 FilledSize 为 0，设置为 Size（完全成交）
		if order.FilledSize <= 0 {
			order.FilledSize = order.Size
		}

		s.orderEngine.SubmitCommand(&UpdateOrderCommand{
			id:    fmt.Sprintf("sync_filled_%s", orderID),
			Gen:   s.currentEngineGeneration(),
			Order: order,
		})
		filledCount++
		s.orderStatusCache.Set(orderID, false)
	}

	if filledCount > 0 {
		log.Debugf("🔄 [订单状态同步] 完成：发现 %d 个订单已成交", filledCount)
	} else {
		log.Debugf("🔄 [订单状态同步] 完成：所有 %d 个订单仍在开放订单列表中", len(orderIDs))
	}
}

func (os *OrderSyncService) syncOrderStatusImpl(ctx context.Context, orderID string) error {
	s := os.s
	order, err := s.clobClient.GetOrder(ctx, orderID)
	if err != nil {
		return fmt.Errorf("获取订单详情失败: %w", err)
	}

	openOrders := s.GetActiveOrders()
	var localOrder *domain.Order
	for _, o := range openOrders {
		if o.OrderID == orderID {
			localOrder = o
			break
		}
	}
	if localOrder == nil {
		return nil
	}

	originalSize, _ := strconv.ParseFloat(order.OriginalSize, 64)
	sizeMatched, _ := strconv.ParseFloat(order.SizeMatched, 64)

	// 重要：验证API返回的订单大小是否合理
	// 如果API返回的originalSize与本地订单的Size差异过大（超过50%），可能是订单匹配错误
	// 或者API返回了错误的数据，此时应该使用本地订单的Size作为上限
	if localOrder.Size > 0 {
		maxAllowedSize := localOrder.Size * 1.5 // 允许50%的误差
		if originalSize > maxAllowedSize {
			log.Warnf("⚠️ [订单状态同步] API返回的originalSize异常: orderID=%s localSize=%.2f apiOriginalSize=%.2f (差异过大，使用本地Size作为上限)",
				orderID, localOrder.Size, originalSize)
			originalSize = localOrder.Size
		}
		if sizeMatched > maxAllowedSize {
			log.Warnf("⚠️ [订单状态同步] API返回的sizeMatched异常: orderID=%s localSize=%.2f apiSizeMatched=%.2f (差异过大，使用本地Size作为上限)",
				orderID, localOrder.Size, sizeMatched)
			sizeMatched = localOrder.Size
		}
	}

	if originalSize > 0 && sizeMatched > 0 && sizeMatched < originalSize {
		// 关键：可能因为 WS 丢弃导致 trade 未进入 OrderEngine，这里用 delta-trade 补偿仓位/成交量
		// 但需要确保sizeMatched不超过本地订单的Size
		if localOrder.Size > 0 && sizeMatched > localOrder.Size {
			log.Warnf("⚠️ [订单状态同步] sizeMatched超过本地订单Size，使用本地Size: orderID=%s localSize=%.2f sizeMatched=%.2f",
				orderID, localOrder.Size, sizeMatched)
			sizeMatched = localOrder.Size
		}
		delta := sizeMatched - localOrder.FilledSize
		if delta > 0 {
			trade := &domain.Trade{
				ID:        fmt.Sprintf("reconcile:%s:%.4f", orderID, sizeMatched),
				OrderID:   orderID,
				AssetID:   localOrder.AssetID,
				Side:      localOrder.Side,
				Price:     localOrder.Price,
				Size:      delta,
				TokenType: localOrder.TokenType,
				Time:      time.Now(),
			}
			s.orderEngine.SubmitCommand(&ProcessTradeCommand{
				id:    fmt.Sprintf("reconcile_trade_%d", time.Now().UnixNano()),
				Gen:   s.currentEngineGeneration(),
				Trade: trade,
			})
		}
		if localOrder.Status != domain.OrderStatusFilled {
			localOrder.Status = domain.OrderStatusPartial
		}
		// 重要：保持本地订单的Size不变，不要被API返回的originalSize覆盖
		// 只有在本地Size为0时才使用API返回的originalSize
		if localOrder.Size <= 0 {
			localOrder.Size = originalSize
		}
		// 重要：FilledSize不能超过订单的Size
		if localOrder.Size > 0 && sizeMatched > localOrder.Size {
			sizeMatched = localOrder.Size
		}
		localOrder.FilledSize = sizeMatched

		s.orderEngine.SubmitCommand(&UpdateOrderCommand{
			id:    fmt.Sprintf("sync_status_%s", orderID),
			Gen:   s.currentEngineGeneration(),
			Order: localOrder,
		})
		return nil
	}

	if originalSize > 0 && sizeMatched >= originalSize && localOrder.Status != domain.OrderStatusFilled {
		// 重要：使用本地订单的Size作为最终成交数量，而不是API返回的originalSize
		// 因为API可能返回错误的数据（比如132），而本地订单的Size是正确的（比如5）
		finalFilledSize := localOrder.Size
		if finalFilledSize <= 0 {
			// 如果本地Size为0，才使用API返回的originalSize
			finalFilledSize = originalSize
		} else if originalSize > finalFilledSize*1.5 {
			// 如果API返回的originalSize与本地Size差异过大，使用本地Size
			log.Warnf("⚠️ [订单状态同步] API返回的originalSize与本地Size差异过大，使用本地Size: orderID=%s localSize=%.2f apiOriginalSize=%.2f",
				orderID, localOrder.Size, originalSize)
		}

		log.Infof("🔄 [订单状态同步] 订单已完全成交: orderID=%s, sizeMatched=%.2f, originalSize=%.2f, localSize=%.2f, finalFilledSize=%.2f",
			orderID, sizeMatched, originalSize, localOrder.Size, finalFilledSize)

		// delta-trade 补偿：只补齐未进入 OrderEngine 的成交部分
		delta := finalFilledSize - localOrder.FilledSize
		if delta > 0 {
			trade := &domain.Trade{
				ID:        fmt.Sprintf("reconcile:%s:%.4f", orderID, finalFilledSize),
				OrderID:   orderID,
				AssetID:   localOrder.AssetID,
				Side:      localOrder.Side,
				Price:     localOrder.Price,
				Size:      delta,
				TokenType: localOrder.TokenType,
				Time:      time.Now(),
			}
			s.orderEngine.SubmitCommand(&ProcessTradeCommand{
				id:    fmt.Sprintf("reconcile_trade_%d", time.Now().UnixNano()),
				Gen:   s.currentEngineGeneration(),
				Trade: trade,
			})
		}

		localOrder.Status = domain.OrderStatusFilled
		now := time.Now()
		localOrder.FilledAt = &now
		// 重要：保持本地订单的Size不变，不要被API返回的originalSize覆盖
		if localOrder.Size <= 0 {
			localOrder.Size = originalSize
		}
		// 重要：FilledSize使用本地订单的Size，而不是API返回的originalSize
		localOrder.FilledSize = finalFilledSize

		s.orderEngine.SubmitCommand(&UpdateOrderCommand{
			id:    fmt.Sprintf("sync_status_%s", orderID),
			Gen:   s.currentEngineGeneration(),
			Order: localOrder,
		})
	} else if order.Status == "CANCELLED" && localOrder.Status != domain.OrderStatusCanceled {
		log.Infof("🔄 [订单状态同步] 订单已取消（API先确认）: orderID=%s", orderID)
		localOrder.Status = domain.OrderStatusCanceled
		// 设置取消时间戳（API 先确认）
		if localOrder.CanceledAt == nil {
			now := time.Now()
			localOrder.CanceledAt = &now
		}

		s.orderEngine.SubmitCommand(&UpdateOrderCommand{
			id:    fmt.Sprintf("sync_status_%s", orderID),
			Gen:   s.currentEngineGeneration(),
			Order: localOrder,
		})
	}

	return nil
}

// startOrderConfirmationTimeoutCheck 启动订单确认超时检测
// 如果订单提交后30秒内未收到WebSocket确认，则通过API拉取持仓来校正
func (os *OrderSyncService) startOrderConfirmationTimeoutCheckImpl(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			os.checkOrderConfirmationTimeoutImpl(ctx)
		}
	}
}

// checkOrderConfirmationTimeout 检查订单确认超时（已简化，不再使用锁）
func (os *OrderSyncService) checkOrderConfirmationTimeoutImpl(ctx context.Context) {
	log.Debugf("订单确认超时检测已简化，现在通过 OrderEngine 管理")
}

// FetchUserPositionsFromAPI 从Polymarket Data API拉取用户持仓并校正本地状态
func (os *OrderSyncService) fetchUserPositionsFromAPIImpl(ctx context.Context) error {
	s := os.s
	if s.funderAddress == "" {
		return fmt.Errorf("funder地址未设置，无法拉取持仓")
	}

	apiURL := fmt.Sprintf("https://data-api.polymarket.com/positions?user=%s&sizeThreshold=0.01&limit=10", s.funderAddress)

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return fmt.Errorf("创建HTTP请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API返回错误状态码: %d", resp.StatusCode)
	}

	var positions []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&positions); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}

	log.Infof("📊 [仓位同步] 从API拉取到 %d 个持仓", len(positions))
	for _, pos := range positions {
		if asset, ok := pos["asset"].(string); ok {
			if size, ok := pos["size"].(string); ok {
				sizeFloat, _ := strconv.ParseFloat(size, 64)
				log.Debugf("📊 [仓位同步] 持仓: asset=%s, size=%.4f", asset, sizeFloat)
			}
		}
	}
	return nil
}
