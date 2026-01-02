package services

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/metrics"
)

func cloneDomainOrder(o *domain.Order) *domain.Order {
	if o == nil {
		return nil
	}
	cp := *o
	// deep copy pointer fields
	if o.FilledPrice != nil {
		fp := *o.FilledPrice
		cp.FilledPrice = &fp
	}
	if o.FilledAt != nil {
		t := *o.FilledAt
		cp.FilledAt = &t
	}
	if o.CanceledAt != nil {
		t := *o.CanceledAt
		cp.CanceledAt = &t
	}
	if o.HedgeOrderID != nil {
		id := *o.HedgeOrderID
		cp.HedgeOrderID = &id
	}
	if o.PairOrderID != nil {
		id := *o.PairOrderID
		cp.PairOrderID = &id
	}
	if o.NegRisk != nil {
		b := *o.NegRisk
		cp.NegRisk = &b
	}
	return &cp
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// findSimilarOpenOrder implements "less-action principle" for GTC quotes:
// if there is already an open/pending order with same market+asset+side and
// price/size are within small tolerance, we should NOT cancel/replace or place a duplicate.
func (o *OrdersService) findSimilarOpenOrder(target *domain.Order) *domain.Order {
	if o == nil || o.s == nil || target == nil {
		return nil
	}
	if target.MarketSlug == "" || target.AssetID == "" || target.Side == "" {
		return nil
	}
	// Only for maker-style GTC (avoid interfering with FAK/FOK "must execute now" orders).
	ot := target.OrderType
	if ot == "" {
		ot = types.OrderTypeGTC
	}
	if ot != types.OrderTypeGTC {
		return nil
	}

	// Tolerances (conservative defaults; can be made configurable later)
	const priceTolCents = 1
	const sizeRelTol = 0.02 // 2%
	const sizeAbsTol = 0.5  // 0.5 shares

	openOrders := o.s.GetActiveOrders()
	for _, ex := range openOrders {
		if ex == nil || ex.OrderID == "" {
			continue
		}
		if ex.MarketSlug != target.MarketSlug || ex.AssetID != target.AssetID || ex.Side != target.Side {
			continue
		}
		// Don't match canceling/final orders.
		if ex.Status == domain.OrderStatusCanceling || ex.IsFinalStatus() {
			continue
		}
		exType := ex.OrderType
		if exType == "" {
			exType = types.OrderTypeGTC
		}
		if exType != types.OrderTypeGTC {
			continue
		}
		if absInt(ex.Price.ToCents()-target.Price.ToCents()) > priceTolCents {
			continue
		}
		// size tolerance: accept either absolute or relative close
		ds := absFloat(ex.Size - target.Size)
		if ds <= sizeAbsTol {
			return ex
		}
		den := math.Max(1e-9, math.Max(ex.Size, target.Size))
		if ds/den <= sizeRelTol {
			return ex
		}
	}
	return nil
}

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

	// 系统级硬防线：暂停 / risk-off / 熔断（fail-safe）
	if s == nil {
		metrics.PlaceOrderBlockedInvalidInput.Add(1)
		return nil, fmt.Errorf("trading service not initialized")
	}
	if e := s.allowPlaceOrder(order); e != nil {
		metrics.PlaceOrderBlockedCircuit.Add(1)
		return nil, e
	}
	// 只管理本周期：强制要求所有策略下单都带 MarketSlug
	// 否则订单更新无法可靠过滤，容易跨周期串单
	if order.MarketSlug == "" {
		metrics.PlaceOrderBlockedInvalidInput.Add(1)
		return nil, fmt.Errorf("order.MarketSlug 不能为空（只管理本周期）")
	}
	// 系统级硬防线：只允许对“当前市场”下单。否则一律拒绝，避免跨周期串单。
	cur := s.GetCurrentMarket()
	if cur == "" || cur != order.MarketSlug {
		metrics.PlaceOrderBlockedInvalidInput.Add(1)
		return nil, fmt.Errorf("order market mismatch (refuse to trade): current=%s order=%s", cur, order.MarketSlug)
	}

	// 调整/归一化订单（在去重 key 前）
	// - 关键：OrderType 为空时默认按 GTC 处理，否则会导致最小 share 调整不生效
	// - 去重 key 也应基于“实际会发送”的订单参数
	order = o.adjustOrderSize(order)

	// “少动原则”：若已有相似挂单，直接 no-op（避免重复挂单/撤挂抖动）
	if ex := o.findSimilarOpenOrder(order); ex != nil {
		// 返回副本，避免调用方误修改引擎内指针
		return cloneDomainOrder(ex), nil
	}

	// 执行层去重：同一订单 key 的短窗口去重（避免重复下单/重复 IO）
	dedupKey := fmt.Sprintf(
		"%s|%s|%s|%dc|%.4f|%s",
		order.MarketSlug,
		order.AssetID,
		order.Side,
		order.Price.ToCents(),
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
		// 分类：以下错误属于“预期/本地门控/风控拒绝”，不应触发 circuit breaker / risk-off：
		// - duplicate in-flight（本地去重）
		// - 余额不足（本地余额未同步/或确实不足，属于“业务拒绝”）
		// - risk-off/paused/market mismatch/stale cycle（系统门控）
		msgLower := strings.ToLower(err.Error())
		isDedup := errors.Is(err, execution.ErrDuplicateInFlight)
		// 余额不足的错误（中英文都要识别）
		isBalance := strings.Contains(err.Error(), "余额不足") ||
			strings.Contains(msgLower, "not enough balance") ||
			strings.Contains(msgLower, "insufficient balance") ||
			strings.Contains(msgLower, "balance / allowance")
		isGate := strings.Contains(msgLower, "risk-off active") ||
			strings.Contains(msgLower, "trading paused") ||
			strings.Contains(msgLower, "order market mismatch") ||
			strings.Contains(msgLower, "stale cycle command")
		// Circuit Breaker 打开的错误不应该再次触发 OnError()，避免循环
		isCircuitBreaker := strings.Contains(msgLower, "circuit breaker open")

		if !isDedup && !isBalance && !isGate && !isCircuitBreaker {
			if s.circuitBreaker != nil {
				s.circuitBreaker.OnError()
			}
			// risk-off：错误后短暂冷静，避免持续重试放大损失/限流
			// - 保守：默认 2s；若疑似限流/网络抖动，提高到 5s
			d := 2 * time.Second
			if strings.Contains(msgLower, "429") || strings.Contains(msgLower, "rate") || strings.Contains(msgLower, "timeout") ||
				strings.Contains(msgLower, "temporarily") || strings.Contains(msgLower, "too many") {
				d = 5 * time.Second
			}
			s.TriggerRiskOff(d, "place_order_error")
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

	// 归一化 OrderType：上层常传空字符串表示“默认”
	// IOExecutor 会将空 OrderType 当作 GTC；这里必须一致，否则最小 share 调整逻辑会被绕过。
	if adjustedOrder.OrderType == "" {
		adjustedOrder.OrderType = types.OrderTypeGTC
	}
	// 严格模式：不做任何自动放大（用于“一对一对冲”）
	if adjustedOrder.DisableSizeAdjust {
		return &adjustedOrder
	}

	// 计算订单所需金额（USDC）
	requiredAmount := adjustedOrder.Price.ToDecimal() * adjustedOrder.Size

	// 检查并调整最小订单金额和最小 share 数量
	minOrderSize := s.minOrderSize
	if minOrderSize <= 0 {
		minOrderSize = 0.1 // 默认值
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
	if s == nil || orderID == "" {
		return fmt.Errorf("invalid cancel: orderID empty")
	}

	// 幂等：已终态/取消中 => 直接认为成功（避免策略层反复 cancel 放大噪音）
	if ord, ok := s.GetOrder(orderID); ok && ord != nil {
		if ord.IsFinalStatus() || ord.Status == domain.OrderStatusCanceling {
			return nil
		}
	}

	// cancel 去重：同一 orderID 的短窗口重复撤单直接吞掉
	cancelKey := "cancel|" + orderID
	if s.inFlightDeduper != nil {
		if e := s.inFlightDeduper.TryAcquire(cancelKey); e != nil {
			if e == execution.ErrDuplicateInFlight {
				return nil
			}
			return e
		}
		defer func() {
			// 撤单失败允许尽快重试；成功则靠 TTL 过期
			// 这里无法拿到真实失败与否（异步），因此仅在 ctx 取消时释放即可（保守不释放）
			if ctx != nil && ctx.Err() != nil {
				s.inFlightDeduper.Release(cancelKey)
			}
		}()
	}

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
	// - 放宽新鲜度要求：从 3 秒增加到 30 秒，优先使用 WebSocket 数据
	// - 放宽价差检查：从 10 分增加到 30 分，减少回退到 REST API
	wsFallbackReason := ""
	if s != nil {
		book := s.getBestBook()
		market := s.getCurrentMarketInfo()
		if book == nil {
			wsFallbackReason = "WebSocket book为nil"
		} else if market == nil {
			wsFallbackReason = "市场信息为nil"
		} else if !book.IsFresh(30 * time.Second) {
			snap := book.Load()
			age := time.Since(snap.UpdatedAt)
			wsFallbackReason = fmt.Sprintf("数据过期: age=%.1fs (要求<30s)", age.Seconds())
		} else {
			snap := book.Load()
			if assetID == market.YesAssetID {
				if snap.YesBidPips > 0 {
					bestBid = float64(snap.YesBidPips) / 10000.0
				}
				if snap.YesAskPips > 0 {
					bestAsk = float64(snap.YesAskPips) / 10000.0
				}
				// 架构层数据质量 gate：必须双边价格都存在，避免 ask-only=0.99 误触发策略
				if bestBid <= 0 || bestAsk <= 0 {
					wsFallbackReason = fmt.Sprintf("YES数据不完整: bid=%.4f ask=%.4f", bestBid, bestAsk)
				} else {
					spreadCents := int(snap.YesAskPips/100) - int(snap.YesBidPips/100)
					if spreadCents < 0 {
						spreadCents = -spreadCents
					}
					// 放宽价差检查：从 10 分增加到 30 分，优先使用 WebSocket 数据
					if spreadCents > 30 {
						wsFallbackReason = fmt.Sprintf("YES价差过大: %dc (要求<=30c)", spreadCents)
					} else {
						return bestBid, bestAsk, nil
					}
				}
			} else if assetID == market.NoAssetID {
				if snap.NoBidPips > 0 {
					bestBid = float64(snap.NoBidPips) / 10000.0
				}
				if snap.NoAskPips > 0 {
					bestAsk = float64(snap.NoAskPips) / 10000.0
				}
				if bestBid <= 0 || bestAsk <= 0 {
					wsFallbackReason = fmt.Sprintf("NO数据不完整: bid=%.4f ask=%.4f", bestBid, bestAsk)
				} else {
					spreadCents := int(snap.NoAskPips/100) - int(snap.NoBidPips/100)
					if spreadCents < 0 {
						spreadCents = -spreadCents
					}
					// 放宽价差检查：从 10 分增加到 30 分，优先使用 WebSocket 数据
					if spreadCents > 30 {
						wsFallbackReason = fmt.Sprintf("NO价差过大: %dc (要求<=30c)", spreadCents)
					} else {
						return bestBid, bestAsk, nil
					}
				}
			} else {
				wsFallbackReason = fmt.Sprintf("assetID不匹配: assetID=%s YES=%s NO=%s", assetID, market.YesAssetID, market.NoAssetID)
			}
		}
	} else {
		wsFallbackReason = "Session为nil"
	}

	// 记录回退原因，方便排查
	if wsFallbackReason != "" {
		log.Debugf("⚠️ [GetBestPrice] 回退到REST API: assetID=%s reason=%s", assetID, wsFallbackReason)
	}

	// 获取订单簿（REST）
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

	// REST 也做同样的双边 gate（避免 ask-only/断档）
	if bestBid <= 0 || bestAsk <= 0 {
		return 0, 0, fmt.Errorf("订单簿盘口不完整: bestBid=%.6f bestAsk=%.6f", bestBid, bestAsk)
	}
	spreadCents := int(bestAsk*100+0.5) - int(bestBid*100+0.5)
	if spreadCents < 0 {
		spreadCents = -spreadCents
	}
	if spreadCents > 10 {
		return 0, 0, fmt.Errorf("订单簿价差过大: bestBid=%.6f bestAsk=%.6f spread=%dc", bestBid, bestAsk, spreadCents)
	}

	return bestBid, bestAsk, nil
}

// GetTopOfBook 返回当前 market 的 YES/NO 一档 bid/ask（优先 WS bestBook，必要时回退 REST）。
//
// 返回值为 domain.Price（1e-4 精度），可直接用于策略/执行层做“有效价格/套利”计算。
func (o *OrdersService) GetTopOfBook(ctx context.Context, market *domain.Market) (yesBid, yesAsk, noBid, noAsk domain.Price, source string, err error) {
	s := o.s
	if market == nil || market.YesAssetID == "" || market.NoAssetID == "" {
		return domain.Price{}, domain.Price{}, domain.Price{}, domain.Price{}, "", fmt.Errorf("market invalid")
	}

	// 1) WS 快路径：当前 market 且 bestBook 新鲜（放宽新鲜度要求，从 10 秒增加到 60 秒）
	// 这样可以减少 REST API 调用，降低超时风险，优先使用 WebSocket 数据
	wsFallbackReason := ""
	if s != nil {
		book := s.getBestBook()
		cur := s.getCurrentMarketInfo()
		if book == nil {
			wsFallbackReason = "WebSocket book为nil"
		} else if cur == nil {
			wsFallbackReason = "当前市场信息为nil"
		} else if cur.Slug != market.Slug {
			wsFallbackReason = fmt.Sprintf("市场不匹配: cur=%s expected=%s", cur.Slug, market.Slug)
		} else if !book.IsFresh(60 * time.Second) {
			snap := book.Load()
			age := time.Since(snap.UpdatedAt)
			wsFallbackReason = fmt.Sprintf("数据过期: age=%.1fs (要求<60s)", age.Seconds())
		} else {
			snap := book.Load()
			// 放宽数据完整性要求：允许部分数据（只要有一边有bid和ask即可）
			hasYesData := snap.YesBidPips > 0 && snap.YesAskPips > 0
			hasNoData := snap.NoBidPips > 0 && snap.NoAskPips > 0
			if !hasYesData {
				wsFallbackReason = fmt.Sprintf("YES数据不完整: bid=%d ask=%d", snap.YesBidPips, snap.YesAskPips)
			} else if !hasNoData {
				wsFallbackReason = fmt.Sprintf("NO数据不完整: bid=%d ask=%d", snap.NoBidPips, snap.NoAskPips)
			} else {
				return domain.Price{Pips: int(snap.YesBidPips)},
					domain.Price{Pips: int(snap.YesAskPips)},
					domain.Price{Pips: int(snap.NoBidPips)},
					domain.Price{Pips: int(snap.NoAskPips)},
					"ws.bestbook",
					nil
			}
		}
	} else {
		wsFallbackReason = "Session为nil"
	}

	// 2) REST 回退：分别拉 YES/NO 的 orderbook 一档（添加重试机制）
	// 记录回退原因，方便排查
	if wsFallbackReason != "" {
		log.Debugf("⚠️ [GetTopOfBook] 回退到REST API: market=%s reason=%s", market.Slug, wsFallbackReason)
	}
	maxRetries := 2
	var yesBook, noBook *types.OrderBookSummary
	var restErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// 重试前等待一小段时间
			select {
			case <-ctx.Done():
				return domain.Price{}, domain.Price{}, domain.Price{}, domain.Price{}, "", ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}

		yesBook, restErr = s.clobClient.GetOrderBook(ctx, market.YesAssetID, nil)
		if restErr != nil {
			if attempt < maxRetries-1 {
				continue // 重试
			}
			return domain.Price{}, domain.Price{}, domain.Price{}, domain.Price{}, "", fmt.Errorf("get yes orderbook (after %d attempts): %w", maxRetries, restErr)
		}

		noBook, restErr = s.clobClient.GetOrderBook(ctx, market.NoAssetID, nil)
		if restErr != nil {
			if attempt < maxRetries-1 {
				continue // 重试
			}
			return domain.Price{}, domain.Price{}, domain.Price{}, domain.Price{}, "", fmt.Errorf("get no orderbook (after %d attempts): %w", maxRetries, restErr)
		}

		// 成功获取两个订单簿
		break
	}

	parseTop := func(book *types.OrderBookSummary) (bid, ask domain.Price, e error) {
		var bestBid, bestAsk float64
		if book != nil && len(book.Bids) > 0 {
			bestBid, _ = strconv.ParseFloat(book.Bids[0].Price, 64)
		}
		if book != nil && len(book.Asks) > 0 {
			bestAsk, _ = strconv.ParseFloat(book.Asks[0].Price, 64)
		}
		if bestBid <= 0 || bestAsk <= 0 {
			return domain.Price{}, domain.Price{}, fmt.Errorf("incomplete book: bid=%.6f ask=%.6f", bestBid, bestAsk)
		}
		return domain.PriceFromDecimal(bestBid), domain.PriceFromDecimal(bestAsk), nil
	}

	yb, ya, err := parseTop(yesBook)
	if err != nil {
		return domain.Price{}, domain.Price{}, domain.Price{}, domain.Price{}, "", fmt.Errorf("parse yes book: %w", err)
	}
	nb, na, err := parseTop(noBook)
	if err != nil {
		return domain.Price{}, domain.Price{}, domain.Price{}, domain.Price{}, "", fmt.Errorf("parse no book: %w", err)
	}
	return yb, ya, nb, na, "rest.orderbook", nil
}

// CheckOrderBookLiquidity 检查订单簿是否有足够的流动性来匹配订单
// 返回: (是否有流动性, 实际可用价格, 可用数量)
func (o *OrdersService) CheckOrderBookLiquidity(ctx context.Context, assetID string, side types.Side, price float64, size float64) (bool, float64, float64) {
	s := o.s
	// 获取订单簿
	book, err := s.clobClient.GetOrderBook(ctx, assetID, nil)
	if err != nil {
		log.Debugf("⚠️ [订单簿检查] 获取订单簿失败，假设有流动性: %v", err)
		return true, price, size // 假设有流动性，使用原价格和数量
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
		return false, 0, 0
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
		return false, 0, 0
	}

	actualPrice, _ := strconv.ParseFloat(matchedLevels[0].Price, 64)

	if totalSize < size {
		log.Debugf("⚠️ [订单簿检查] 流动性不足: 需要=%.4f, 可用=%.4f", size, totalSize)
		// 即使流动性不足，也返回 true，让 FAK 订单尝试部分成交
		// 但返回实际可用价格和可用数量
		return true, actualPrice, totalSize
	}

	// 有足够的流动性，返回最佳价格和可用数量
	return true, actualPrice, totalSize
}

// GetSecondLevelPrice 获取订单簿的第二档价格（卖二价或买二价）
// 对于买入订单：返回卖二价（asks[1]），如果不存在则返回卖一价（asks[0]）
// 对于卖出订单：返回买二价（bids[1]），如果不存在则返回买一价（bids[0]）
// 返回: (价格, 是否存在第二档)
func (o *OrdersService) GetSecondLevelPrice(ctx context.Context, assetID string, side types.Side) (float64, bool) {
	s := o.s
	// 获取订单簿
	book, err := s.clobClient.GetOrderBook(ctx, assetID, nil)
	if err != nil {
		log.Debugf("⚠️ [订单簿检查] 获取订单簿失败: %v", err)
		return 0, false
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
		return 0, false
	}

	// 如果有第二档，返回第二档价格
	if len(levels) >= 2 {
		secondPrice, err := strconv.ParseFloat(levels[1].Price, 64)
		if err == nil && secondPrice > 0 {
			return secondPrice, true
		}
	}

	// 如果没有第二档，返回第一档价格
	if len(levels) >= 1 {
		firstPrice, err := strconv.ParseFloat(levels[0].Price, 64)
		if err == nil && firstPrice > 0 {
			return firstPrice, false
		}
	}

	return 0, false
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
