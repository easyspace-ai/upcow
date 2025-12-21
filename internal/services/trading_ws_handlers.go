package services

import (
	"context"
	"fmt"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)

// handleOrderPlaced 处理订单下单事件（通过 OrderEngine）
func (s *TradingService) handleOrderPlaced(order *domain.Order, market *domain.Market) error {
	log.Debugf("📥 [WebSocket] 订单已下单: orderID=%s, status=%s", order.OrderID, order.Status)

	// 发送 UpdateOrderCommand 到 OrderEngine
	updateCmd := &UpdateOrderCommand{
		id:    fmt.Sprintf("websocket_placed_%s", order.OrderID),
		Gen:   s.currentEngineGeneration(),
		Order: order,
	}
	s.orderEngine.SubmitCommand(updateCmd)

	// 更新缓存
	if order.Status == domain.OrderStatusOpen {
		s.orderStatusCache.Set(order.OrderID, true)
	}

	// 如果订单状态是 open，检查价格偏差
	if order.Status == domain.OrderStatusOpen && market != nil {
		// 在 goroutine 中异步检查价格偏差，避免阻塞
		go s.checkAndCorrectOrderPrice(context.Background(), order, market)
	}

	return nil
}

// checkAndCorrectOrderPrice 检查订单价格偏差并自动修正
func (s *TradingService) checkAndCorrectOrderPrice(ctx context.Context, order *domain.Order, market *domain.Market) {
	// 获取当前订单簿最佳价格
	var currentBestPrice float64
	var err error

	if order.Side == types.SideBuy {
		// 买入订单：使用最佳卖价（best ask）
		_, currentBestPrice, err = s.GetBestPrice(ctx, order.AssetID)
	} else {
		// 卖出订单：使用最佳买价（best bid）
		currentBestPrice, _, err = s.GetBestPrice(ctx, order.AssetID)
	}

	if err != nil {
		log.Warnf("⚠️ 无法获取订单簿价格，跳过价格偏差检查: orderID=%s, error=%v", order.OrderID, err)
		return
	}

	if currentBestPrice <= 0 {
		log.Warnf("⚠️ 订单簿价格无效，跳过价格偏差检查: orderID=%s", order.OrderID)
		return
	}

	// 计算价格偏差（分）
	expectedPrice := order.Price.ToDecimal()
	priceDeviationCents := int((currentBestPrice - expectedPrice) * 100)
	if priceDeviationCents < 0 {
		priceDeviationCents = -priceDeviationCents
	}

	// 价格偏差阈值：默认 2 cents
	deviationThreshold := 2

	// 如果价格偏差超过阈值，撤单并重新下单
	if priceDeviationCents > deviationThreshold {
		log.Warnf("⚠️ 订单价格偏差过大: orderID=%s, 预期价格=%.4f, 当前最佳价格=%.4f, 偏差=%dc (阈值=%dc)",
			order.OrderID, expectedPrice, currentBestPrice, priceDeviationCents, deviationThreshold)

		// 检查订单是否仍然存在且状态为 open（通过 OrderEngine 查询）
		openOrders := s.GetActiveOrders()
		var existingOrder *domain.Order
		for _, o := range openOrders {
			if o.OrderID == order.OrderID {
				existingOrder = o
				break
			}
		}

		if existingOrder == nil || existingOrder.Status != domain.OrderStatusOpen {
			log.Debugf("订单状态已变化，跳过价格修正: orderID=%s", order.OrderID)
			return
		}

		// 撤单
		if err := s.CancelOrder(ctx, order.OrderID); err != nil {
			log.Errorf("❌ 撤单失败: orderID=%s, error=%v", order.OrderID, err)
			return
		}

		log.Infof("✅ 已撤单: orderID=%s (价格偏差过大: %dc)", order.OrderID, priceDeviationCents)

		// 等待一小段时间，确保撤单完成
		time.Sleep(500 * time.Millisecond)

		// 使用最新价格重新下单
		newPrice := domain.PriceFromDecimal(currentBestPrice)

		// 创建新的订单（使用新的订单 ID）
		newOrder := &domain.Order{
			OrderID:      fmt.Sprintf("%s-corrected-%d", order.OrderID, time.Now().UnixNano()),
			MarketSlug:   order.MarketSlug,
			AssetID:      order.AssetID,
			Side:         order.Side,
			Price:        newPrice,
			Size:         order.Size,
			GridLevel:    order.GridLevel,
			TokenType:    order.TokenType,
			HedgeOrderID: order.HedgeOrderID,
			IsEntryOrder: order.IsEntryOrder,
			PairOrderID:  order.PairOrderID,
			Status:       domain.OrderStatusPending,
			CreatedAt:    time.Now(),
		}

		// 如果是配对订单（entry/hedge），需要同时处理对冲订单
		if order.PairOrderID != nil {
			// 通过 OrderEngine 查询配对订单
			openOrders := s.GetActiveOrders()
			var pairOrder *domain.Order
			for _, o := range openOrders {
				if o.OrderID == *order.PairOrderID {
					pairOrder = o
					break
				}
			}

			if pairOrder != nil && pairOrder.Status == domain.OrderStatusOpen {
				// 获取对冲订单的最佳价格
				var hedgeBestPrice float64
				if pairOrder.Side == types.SideBuy {
					_, hedgeBestPrice, err = s.GetBestPrice(ctx, pairOrder.AssetID)
				} else {
					hedgeBestPrice, _, err = s.GetBestPrice(ctx, pairOrder.AssetID)
				}

				if err == nil && hedgeBestPrice > 0 {
					hedgeExpectedPrice := pairOrder.Price.ToDecimal()
					hedgeDeviationCents := int((hedgeBestPrice - hedgeExpectedPrice) * 100)
					if hedgeDeviationCents < 0 {
						hedgeDeviationCents = -hedgeDeviationCents
					}

					if hedgeDeviationCents > deviationThreshold {
						log.Warnf("⚠️ 对冲订单价格偏差过大: orderID=%s, 预期价格=%.4f, 当前最佳价格=%.4f, 偏差=%dc (阈值=%dc)",
							pairOrder.OrderID, hedgeExpectedPrice, hedgeBestPrice, hedgeDeviationCents, deviationThreshold)

						// 撤单对冲订单
						if err := s.CancelOrder(ctx, pairOrder.OrderID); err != nil {
							log.Errorf("❌ 撤单对冲订单失败: orderID=%s, error=%v", pairOrder.OrderID, err)
						} else {
							log.Infof("✅ 已撤单对冲订单: orderID=%s (价格偏差过大: %dc)", pairOrder.OrderID, hedgeDeviationCents)

							// 等待撤单完成
							time.Sleep(500 * time.Millisecond)

							// 创建新的对冲订单（使用最新价格）
							hedgeNewPrice := domain.PriceFromDecimal(hedgeBestPrice)
							newHedgeOrder := &domain.Order{
								OrderID:      fmt.Sprintf("%s-corrected-%d", pairOrder.OrderID, time.Now().UnixNano()),
								MarketSlug:   pairOrder.MarketSlug,
								AssetID:      pairOrder.AssetID,
								Side:         pairOrder.Side,
								Price:        hedgeNewPrice,
								Size:         pairOrder.Size,
								GridLevel:    pairOrder.GridLevel,
								TokenType:    pairOrder.TokenType,
								HedgeOrderID: pairOrder.HedgeOrderID,
								IsEntryOrder: pairOrder.IsEntryOrder,
								PairOrderID:  &newOrder.OrderID, // 更新配对订单 ID
								Status:       domain.OrderStatusPending,
								CreatedAt:    time.Now(),
							}

							// 更新配对关系
							newOrder.PairOrderID = &newHedgeOrder.OrderID
							newOrder.HedgeOrderID = &newHedgeOrder.OrderID
							newHedgeOrder.HedgeOrderID = &newOrder.OrderID

							// 先重新下单对冲订单
							_, err := s.PlaceOrder(ctx, newHedgeOrder)
							if err != nil {
								log.Errorf("❌ 重新下单对冲订单失败: error=%v", err)
							} else {
								log.Infof("✅ 已重新下单对冲订单: orderID=%s, 新价格=%.4f (原价格=%.4f, 偏差=%dc)",
									newHedgeOrder.OrderID, hedgeBestPrice, hedgeExpectedPrice, hedgeDeviationCents)
							}
						}
					} else {
						// 对冲订单价格正常，但需要更新配对关系
						newOrder.PairOrderID = &pairOrder.OrderID
						newOrder.HedgeOrderID = &pairOrder.OrderID
						log.Debugf("对冲订单价格正常，保持配对关系: pairOrderID=%s, 偏差=%dc (阈值=%dc)",
							pairOrder.OrderID, hedgeDeviationCents, deviationThreshold)
					}
				}
			}
		}

		// 重新下单
		_, err := s.PlaceOrder(ctx, newOrder)
		if err != nil {
			log.Errorf("❌ 重新下单失败: error=%v", err)
		} else {
			log.Infof("✅ 已重新下单: orderID=%s, 新价格=%.4f (原价格=%.4f, 偏差=%dc)",
				newOrder.OrderID, currentBestPrice, expectedPrice, priceDeviationCents)
		}
	} else {
		log.Debugf("✅ 订单价格正常: orderID=%s, 预期价格=%.4f, 当前最佳价格=%.4f, 偏差=%dc (阈值=%dc)",
			order.OrderID, expectedPrice, currentBestPrice, priceDeviationCents, deviationThreshold)
	}
}

// handleOrderFilled 处理订单成交事件（通过 OrderEngine）
func (s *TradingService) handleOrderFilled(order *domain.Order, market *domain.Market) error {
	// 确保 FilledAt 已设置
	if order.FilledAt == nil {
		now := time.Now()
		order.FilledAt = &now
	}
	if order.MarketSlug == "" && market != nil {
		order.MarketSlug = market.Slug
	}

	// 更新订单状态
	order.Status = domain.OrderStatusFilled
	order.FilledSize = order.Size

	// 发送 UpdateOrderCommand 到 OrderEngine
	updateCmd := &UpdateOrderCommand{
		id:    fmt.Sprintf("websocket_filled_%s", order.OrderID),
		Gen:   s.currentEngineGeneration(),
		Order: order,
	}
	s.orderEngine.SubmitCommand(updateCmd)

	// 更新缓存（标记为已关闭）
	s.orderStatusCache.Set(order.OrderID, false)

	log.Infof("✅ [WebSocket] 订单已成交: orderID=%s, size=%.2f", order.OrderID, order.Size)

	return nil
}

// HandleTrade 处理交易事件（通过 OrderEngine）
func (s *TradingService) HandleTrade(ctx context.Context, trade *domain.Trade) {
	log.Debugf("📥 [WebSocket] 收到交易事件: tradeID=%s, orderID=%s, size=%.2f", trade.ID, trade.OrderID, trade.Size)

	// 发送 ProcessTradeCommand 到 OrderEngine
	cmd := &ProcessTradeCommand{
		id:    fmt.Sprintf("process_trade_%d", time.Now().UnixNano()),
		Gen:   s.currentEngineGeneration(),
		Trade: trade,
	}
	s.orderEngine.SubmitCommand(cmd)
}

// handleOrderCanceled 处理订单取消事件（通过 OrderEngine）
func (s *TradingService) handleOrderCanceled(order *domain.Order) error {
	// 更新订单状态
	order.Status = domain.OrderStatusCanceled
	// 尽量补齐 market slug，避免跨周期串单
	if order.MarketSlug == "" {
		// 这里无法可靠拿到 market，只能保留为空
	}

	// 发送 UpdateOrderCommand 到 OrderEngine
	updateCmd := &UpdateOrderCommand{
		id:    fmt.Sprintf("websocket_canceled_%s", order.OrderID),
		Gen:   s.currentEngineGeneration(),
		Order: order,
	}
	s.orderEngine.SubmitCommand(updateCmd)

	log.Infof("❌ [WebSocket] 订单已取消: orderID=%s", order.OrderID)

	return nil
}
