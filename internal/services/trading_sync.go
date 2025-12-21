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

		// 风险4修复：WebSocket和API状态一致性检查
		// 如果订单已经通过 WebSocket 更新为已成交或已取消，优先使用WebSocket状态
		if order.Status == domain.OrderStatusFilled || order.Status == domain.OrderStatusCanceled {
			// 检查API返回的开放订单列表中是否还有这个订单（状态不一致）
			if openOrderIDs[orderID] {
				log.Warnf("⚠️ [状态一致性] WebSocket和API状态不一致: orderID=%s, WebSocket状态=%s, API状态=open",
					orderID, order.Status)
			}
			log.Debugf("🔄 [订单状态同步] 订单已通过WebSocket更新为 %s，跳过同步: orderID=%s", order.Status, orderID)
			// 更新缓存（标记为已关闭）
			s.orderStatusCache.Set(orderID, false)
			// 发送 UpdateOrderCommand 更新 OrderEngine 状态
			updateCmd := &UpdateOrderCommand{
				id:    fmt.Sprintf("sync_update_%s", orderID),
				Gen:   s.currentEngineGeneration(),
				Order: order,
			}
			s.orderEngine.SubmitCommand(updateCmd)
			continue
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
			if order.Price.Cents >= 60 && order.Price.Cents <= 90 {
				for _, apiOrder := range openOrdersResp {
					apiPrice, err := strconv.ParseFloat(apiOrder.Price, 64)
					if err != nil {
						continue
					}
					apiPriceCents := int(apiPrice * 100)

					if apiOrder.AssetID == order.AssetID &&
						apiOrder.Side == string(order.Side) &&
						apiPriceCents >= 60 && apiPriceCents <= 90 {
						priceDiff := math.Abs(float64(apiPriceCents - order.Price.Cents))
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
			if order.Price.Cents >= 1 && order.Price.Cents <= 40 {
				for _, apiOrder := range openOrdersResp {
					apiPrice, err := strconv.ParseFloat(apiOrder.Price, 64)
					if err != nil {
						continue
					}
					apiPriceCents := int(apiPrice * 100)

					if apiOrder.AssetID == order.AssetID &&
						apiOrder.Side == string(order.Side) &&
						apiPriceCents >= 1 && apiPriceCents <= 40 {
						priceDiff := math.Abs(float64(apiPriceCents - order.Price.Cents))
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
				orderType, orderID, matchedOrderID, order.AssetID, order.Side, order.Price.Cents, matchedPriceCents, bestMatch.score)

			order.OrderID = matchedOrderID
			order.Price = domain.Price{Cents: matchedPriceCents}
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
		} else if order.IsEntryOrder || (!order.IsEntryOrder && order.Price.Cents >= 1 && order.Price.Cents <= 40) {
			orderType := "入场订单"
			if !order.IsEntryOrder {
				orderType = "对冲订单"
			}
			log.Warnf("⚠️ [订单匹配失败] 无法通过业务规则匹配%s: orderID=%s, assetID=%s, side=%s, price=%dc, 可能订单已成交或取消",
				orderType, orderID, order.AssetID, order.Side, order.Price.Cents)
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

		if order.Status == domain.OrderStatusFilled {
			log.Debugf("🔄 [订单状态同步] 订单已通过WebSocket更新为已成交，API确认不在开放列表中，状态一致: orderID=%s", orderID)
			continue
		} else if order.Status == domain.OrderStatusOpen || order.Status == domain.OrderStatusPending {
			log.Warnf("⚠️ [状态一致性] WebSocket和API状态不一致: orderID=%s, WebSocket状态=%s, API状态=已成交/已取消",
				orderID, order.Status)
		}

		log.Infof("🔄 [订单状态同步] 订单已成交: orderID=%s, side=%s, price=%.4f, size=%.2f",
			orderID, order.Side, order.Price.ToDecimal(), order.Size)

		order.Status = domain.OrderStatusFilled
		now := time.Now()
		order.FilledAt = &now

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

	if originalSize > 0 && sizeMatched > 0 && sizeMatched < originalSize {
		// 关键：可能因为 WS 丢弃导致 trade 未进入 OrderEngine，这里用 delta-trade 补偿仓位/成交量
		delta := sizeMatched - localOrder.FilledSize
		if delta > 0 {
			trade := &domain.Trade{
				ID:      fmt.Sprintf("reconcile:%s:%.4f", orderID, sizeMatched),
				OrderID: orderID,
				AssetID: localOrder.AssetID,
				Side:    localOrder.Side,
				Price:   localOrder.Price,
				Size:    delta,
				TokenType: localOrder.TokenType,
				Time:    time.Now(),
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
		localOrder.Size = originalSize
		localOrder.FilledSize = sizeMatched

		s.orderEngine.SubmitCommand(&UpdateOrderCommand{
			id:    fmt.Sprintf("sync_status_%s", orderID),
			Gen:   s.currentEngineGeneration(),
			Order: localOrder,
		})
		return nil
	}

	if originalSize > 0 && sizeMatched >= originalSize && localOrder.Status != domain.OrderStatusFilled {
		log.Infof("🔄 [订单状态同步] 订单已完全成交: orderID=%s, sizeMatched=%.2f, originalSize=%.2f",
			orderID, sizeMatched, originalSize)

		// delta-trade 补偿：只补齐未进入 OrderEngine 的成交部分
		delta := originalSize - localOrder.FilledSize
		if delta > 0 {
			trade := &domain.Trade{
				ID:      fmt.Sprintf("reconcile:%s:%.4f", orderID, originalSize),
				OrderID: orderID,
				AssetID: localOrder.AssetID,
				Side:    localOrder.Side,
				Price:   localOrder.Price,
				Size:    delta,
				TokenType: localOrder.TokenType,
				Time:    time.Now(),
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
		localOrder.Size = originalSize
		localOrder.FilledSize = originalSize

		s.orderEngine.SubmitCommand(&UpdateOrderCommand{
			id:    fmt.Sprintf("sync_status_%s", orderID),
			Gen:   s.currentEngineGeneration(),
			Order: localOrder,
		})
	} else if order.Status == "CANCELLED" && localOrder.Status != domain.OrderStatusCanceled {
		log.Infof("🔄 [订单状态同步] 订单已取消: orderID=%s", orderID)
		localOrder.Status = domain.OrderStatusCanceled

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

	apiURL := fmt.Sprintf("https://data-api.polymarket.com/positions?user=%s&sizeThreshold=0.01&limit=500", s.funderAddress)

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
