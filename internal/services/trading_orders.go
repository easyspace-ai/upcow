package services

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/metrics"
)

// boolPtr 返回 bool 指针
func boolPtr(b bool) *bool {
	return &b
}

// PlaceOrder 下单（通过 OrderEngine 发送命令）
func (o *OrdersService) PlaceOrder(ctx context.Context, order *domain.Order) (created *domain.Order, err error) {
	s := o.s
	start := time.Now()
	metrics.PlaceOrderRuns.Add(1)

	if order == nil {
		metrics.PlaceOrderBlockedInvalidInput.Add(1)
		return nil, fmt.Errorf("order 不能为空")
	}
	// 只管理本周期：强制要求所有策略下单都带 MarketSlug
	// 否则订单更新无法可靠过滤，容易跨周期串单
	if order.MarketSlug == "" {
		metrics.PlaceOrderBlockedInvalidInput.Add(1)
		return nil, fmt.Errorf("order.MarketSlug 不能为空（只管理本周期）")
	}

	// 执行层风控：断路器快路径
	if s.circuitBreaker != nil {
		if e := s.circuitBreaker.AllowTrading(); e != nil {
			metrics.PlaceOrderBlockedCircuit.Add(1)
			return nil, e
		}
	}

	// 执行层去重：同一订单 key 的短窗口去重（避免重复下单/重复 IO）
	dedupKey := fmt.Sprintf(
		"%s|%s|%s|%dc|%.4f|%s",
		order.MarketSlug,
		order.AssetID,
		order.Side,
		order.Price.Cents,
		order.Size,
		order.OrderType,
	)
	if s.inFlightDeduper != nil {
		if e := s.inFlightDeduper.TryAcquire(dedupKey); e != nil {
			if e == execution.ErrDuplicateInFlight {
				metrics.PlaceOrderBlockedDedup.Add(1)
			}
			return nil, e
		}
		// 如果下单失败/被取消，允许尽快重试；成功则靠 TTL 自动过期。
		defer func() {
			if err != nil {
				s.inFlightDeduper.Release(dedupKey)
			}
		}()
	}

	// 调整订单大小（在发送命令前）
	order = o.adjustOrderSize(order)

	// 发送下单命令到 OrderEngine
	reply := make(chan *PlaceOrderResult, 1)
	cmd := &PlaceOrderCommand{
		id:      fmt.Sprintf("place_%d", time.Now().UnixNano()),
		Gen:     s.currentEngineGeneration(),
		Order:   order,
		Reply:   reply,
		Context: ctx,
	}

	s.orderEngine.SubmitCommand(cmd)

	// 等待结果
	select {
	case result := <-reply:
		created, err = result.Order, result.Error
	case <-ctx.Done():
		created, err = nil, ctx.Err()
	}

	// 指标：延迟（毫秒）
	latencyMs := time.Since(start).Milliseconds()
	metrics.PlaceOrderLatencyLastMs.Set(latencyMs)
	metrics.PlaceOrderLatencyTotalMs.Add(latencyMs)
	metrics.PlaceOrderLatencySamples.Add(1)
	// 简单 max 统计（非严格原子，但 expvar.Int 内部已加锁；这里读写是串行的）
	if latencyMs > metrics.PlaceOrderLatencyMaxMs.Value() {
		metrics.PlaceOrderLatencyMaxMs.Set(latencyMs)
	}

	// 指标 + 风控：错误计数
	if err != nil {
		metrics.PlaceOrderErrors.Add(1)
		if s.circuitBreaker != nil {
			s.circuitBreaker.OnError()
		}
		return created, err
	}
	if s.circuitBreaker != nil {
		s.circuitBreaker.OnSuccess()
	}

	return created, nil
}

// adjustOrderSize 调整订单大小（确保满足最小要求）
func (o *OrdersService) adjustOrderSize(order *domain.Order) *domain.Order {
	s := o.s
	// 创建订单副本
	adjustedOrder := *order

	// 计算订单所需金额（USDC）
	requiredAmount := adjustedOrder.Price.ToDecimal() * adjustedOrder.Size

	// 检查并调整最小订单金额和最小 share 数量
	minOrderSize := s.minOrderSize
	if minOrderSize <= 0 {
		minOrderSize = 1.1 // 默认值
	}

	// 限价单最小 share 数量（仅限价单 GTC 时应用）
	minShareSize := s.minShareSize
	if minShareSize <= 0 {
		minShareSize = 5.0 // 默认值
	}

	// 判断是否为限价单（GTC）
	isLimitOrder := adjustedOrder.OrderType == types.OrderTypeGTC

	// 卖单（SELL）不能为了满足"最小金额/最小 shares"而自动放大：
	// - 放大卖单很容易超过实际持仓 shares
	// - 交易所返回的 "not enough balance / allowance" 会误导为 USDC/授权问题
	// 因此：SELL 只记录提示，不做任何向上调整。
	if adjustedOrder.Side == types.SideSell {
		if isLimitOrder && adjustedOrder.Size < minShareSize {
			log.Warnf("⚠️ 限价卖单 share 数量 %.4f 小于最小值 %.0f；为避免超过持仓，本系统不会自动放大卖单，交易所可能拒单",
				adjustedOrder.Size, minShareSize)
		}
		if requiredAmount < minOrderSize {
			log.Warnf("⚠️ 卖单金额 %.2f USDC 小于最小要求 %.2f USDC；为避免超过持仓，本系统不会自动放大卖单，交易所可能拒单",
				requiredAmount, minOrderSize)
		}
		return &adjustedOrder
	}

	// 检查并调整订单大小
	originalSize := adjustedOrder.Size
	originalAmount := requiredAmount
	adjusted := false

	// 1. 首先检查 share 数量是否满足最小值（仅限价单 GTC）
	if isLimitOrder && adjustedOrder.Size < minShareSize {
		adjustedOrder.Size = minShareSize
		adjusted = true
		log.Infof("⚠️ 限价单 share 数量 %.4f 小于最小值 %.0f，自动调整: %.4f → %.4f shares",
			originalSize, minShareSize, originalSize, adjustedOrder.Size)
	}

	// 2. 重新计算金额（如果调整了 share 数量）
	requiredAmount = adjustedOrder.Price.ToDecimal() * adjustedOrder.Size

	// 3. 检查金额是否满足最小值
	if requiredAmount < minOrderSize {
		// 订单金额小于最小要求，自动调整 order.Size
		adjustedOrder.Size = minOrderSize / adjustedOrder.Price.ToDecimal()
		// 确保调整后的数量不小于最小 share 数量（仅限价单 GTC）
		if isLimitOrder && adjustedOrder.Size < minShareSize {
			adjustedOrder.Size = minShareSize
		}
		adjusted = true
		// 重新计算所需金额
		requiredAmount = adjustedOrder.Price.ToDecimal() * adjustedOrder.Size
		log.Infof("⚠️ 订单金额 %.2f USDC 小于最小要求 %.2f USDC，自动调整数量: %.4f → %.4f shares (金额: %.2f → %.2f USDC)",
			originalAmount, minOrderSize, originalSize, adjustedOrder.Size, originalAmount, requiredAmount)
	}

	if adjusted {
		log.Infof("✅ 订单大小已调整: 原始=%.4f shares (%.2f USDC), 调整后=%.4f shares (%.2f USDC)",
			originalSize, originalAmount, adjustedOrder.Size, requiredAmount)
	}

	return &adjustedOrder
}

// CancelOrder 取消订单（通过 OrderEngine）
func (o *OrdersService) CancelOrder(ctx context.Context, orderID string) error {
	s := o.s
	reply := make(chan error, 1)
	cmd := &CancelOrderCommand{
		id:      fmt.Sprintf("cancel_%d", time.Now().UnixNano()),
		Gen:     s.currentEngineGeneration(),
		OrderID: orderID,
		Reply:   reply,
		Context: ctx,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// GetBestPrice 获取订单簿的最佳买卖价格（买一价和卖一价）
func (o *OrdersService) GetBestPrice(ctx context.Context, assetID string) (bestBid float64, bestAsk float64, err error) {
	s := o.s

	// 快路径：优先读取 WS 推送的 AtomicBestBook（避免每次都打 REST orderbook）
	// - 仅当 bestBook 注入且新鲜（<=3s）且能映射到 YES/NO assetId 时使用
	if s != nil {
		book := s.getBestBook()
		market := s.getCurrentMarketInfo()
		if book != nil && market != nil && book.IsFresh(3*time.Second) {
			snap := book.Load()
			if assetID == market.YesAssetID {
				if snap.YesBidCents > 0 {
					bestBid = float64(snap.YesBidCents) / 100.0
				}
				if snap.YesAskCents > 0 {
					bestAsk = float64(snap.YesAskCents) / 100.0
				}
				// 如果能提供任何一个价格，就认为快路径成功
				if bestBid > 0 || bestAsk > 0 {
					return bestBid, bestAsk, nil
				}
			} else if assetID == market.NoAssetID {
				if snap.NoBidCents > 0 {
					bestBid = float64(snap.NoBidCents) / 100.0
				}
				if snap.NoAskCents > 0 {
					bestAsk = float64(snap.NoAskCents) / 100.0
				}
				if bestBid > 0 || bestAsk > 0 {
					return bestBid, bestAsk, nil
				}
			}
		}
	}

	// 获取订单簿
	book, err := s.clobClient.GetOrderBook(ctx, assetID, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("获取订单簿失败: %w", err)
	}

	// 获取最佳买一价（bids 中价格最高的）
	if len(book.Bids) > 0 {
		bestBid, err = strconv.ParseFloat(book.Bids[0].Price, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("解析买一价失败: %w", err)
		}
	}

	// 获取最佳卖一价（asks 中价格最低的）
	if len(book.Asks) > 0 {
		bestAsk, err = strconv.ParseFloat(book.Asks[0].Price, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("解析卖一价失败: %w", err)
		}
	}

	return bestBid, bestAsk, nil
}

// checkOrderBookLiquidity 检查订单簿是否有足够的流动性来匹配订单
// 返回: (是否有流动性, 实际可用价格)
func (o *OrdersService) checkOrderBookLiquidity(ctx context.Context, assetID string, side types.Side, price float64, size float64) (bool, float64) {
	s := o.s
	// 获取订单簿
	book, err := s.clobClient.GetOrderBook(ctx, assetID, nil)
	if err != nil {
		log.Debugf("⚠️ [订单簿检查] 获取订单簿失败，假设有流动性: %v", err)
		return true, price // 假设有流动性，使用原价格
	}

	// 根据订单方向检查对应的订单簿
	var levels []types.OrderSummary
	if side == types.SideBuy {
		// 买入订单：检查卖单（asks）
		levels = book.Asks
	} else {
		// 卖出订单：检查买单（bids）
		levels = book.Bids
	}

	if len(levels) == 0 {
		log.Debugf("⚠️ [订单簿检查] 订单簿为空，无流动性")
		return false, 0
	}

	// 检查是否有价格匹配的订单
	// 对于买入订单：asks 中的价格应该 <= 我们的价格
	// 对于卖出订单：bids 中的价格应该 >= 我们的价格
	matchedLevels := make([]types.OrderSummary, 0)
	totalSize := 0.0

	for _, level := range levels {
		levelPrice, err := strconv.ParseFloat(level.Price, 64)
		if err != nil {
			continue
		}

		levelSize, err := strconv.ParseFloat(level.Size, 64)
		if err != nil {
			continue
		}

		// 检查价格是否匹配
		if side == types.SideBuy {
			// 买入：asks 价格应该 <= 我们的价格
			if levelPrice <= price {
				matchedLevels = append(matchedLevels, level)
				totalSize += levelSize
			}
		} else {
			// 卖出：bids 价格应该 >= 我们的价格
			if levelPrice >= price {
				matchedLevels = append(matchedLevels, level)
				totalSize += levelSize
			}
		}

		// 如果已经累积足够的数量，停止检查
		if totalSize >= size {
			break
		}
	}

	// 检查是否有足够的流动性
	if len(matchedLevels) == 0 {
		log.Debugf("⚠️ [订单簿检查] 无价格匹配的订单: 订单价格=%.4f, 订单簿价格范围=[%.4f, %.4f]",
			price, getFirstPrice(levels), getLastPrice(levels))
		return false, 0
	}

	if totalSize < size {
		log.Debugf("⚠️ [订单簿检查] 流动性不足: 需要=%.4f, 可用=%.4f", size, totalSize)
		// 即使流动性不足，也返回 true，让 FAK 订单尝试部分成交
		// 但返回实际可用价格
		if len(matchedLevels) > 0 {
			actualPrice, _ := strconv.ParseFloat(matchedLevels[0].Price, 64)
			return true, actualPrice
		}
		return false, 0
	}

	// 有足够的流动性，返回最佳价格
	if len(matchedLevels) > 0 {
		actualPrice, _ := strconv.ParseFloat(matchedLevels[0].Price, 64)
		return true, actualPrice
	}

	return true, price
}

// getFirstPrice 获取订单簿第一个价格
func getFirstPrice(levels []types.OrderSummary) float64 {
	if len(levels) == 0 {
		return 0
	}
	price, _ := strconv.ParseFloat(levels[0].Price, 64)
	return price
}

// getLastPrice 获取订单簿最后一个价格
func getLastPrice(levels []types.OrderSummary) float64 {
	if len(levels) == 0 {
		return 0
	}
	price, _ := strconv.ParseFloat(levels[len(levels)-1].Price, 64)
	return price
}
