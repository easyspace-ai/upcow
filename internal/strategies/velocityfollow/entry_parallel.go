package velocityfollow

import (
	"context"
	"strings"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/execution"
)

// executeParallel 并发下单模式（新架构特性）
//
// 执行流程：
// 1. 同时提交 Entry 和 Hedge 订单（使用 ExecuteMultiLeg）
// 2. 等待两个订单都返回结果
//
// 优势：
// - 速度快：减少下单延迟（~100-200ms）
// - 适合高频交易：减少跨腿时差
//
// 风险：
// - Entry 订单失败时，Hedge 订单可能已提交（通过 OnOrderUpdate 自动取消）
func (s *Strategy) executeParallel(ctx context.Context, market *domain.Market, winner domain.TokenType,
	entryAsset, hedgeAsset string, entryPrice, hedgePrice domain.Price, entryShares, hedgeShares float64,
	entryAskCents, hedgeAskCents int, winMet metrics, biasTok, biasReason string) error {
	// 使用更短的超时时间（10秒），快速失败，避免阻塞策略
	orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// ⚠️ 重要：在创建订单前，最后进行一次精度调整
	// 确保 maker amount = size × price 是 2 位小数，taker amount (size) 是 4 位小数
	entryPriceDecFinal := entryPrice.ToDecimal()
	entrySharesFinal := adjustSizeForMakerAmountPrecision(entryShares, entryPriceDecFinal)
	if entrySharesFinal != entryShares {
		log.Infof("🔧 [%s] Entry size 最终精度调整（并发模式，创建订单前）: %.4f -> %.4f (maker amount: %.2f -> %.2f, price=%.4f)",
			ID, entryShares, entrySharesFinal, entryShares*entryPriceDecFinal, entrySharesFinal*entryPriceDecFinal, entryPriceDecFinal)
		entryShares = entrySharesFinal
	}

	hedgePriceDecFinal := hedgePrice.ToDecimal()
	hedgeSharesFinal := adjustSizeForMakerAmountPrecision(hedgeShares, hedgePriceDecFinal)
	if hedgeSharesFinal != hedgeShares {
		log.Infof("🔧 [%s] Hedge size 最终精度调整（并发模式，创建订单前）: %.4f -> %.4f (maker amount: %.2f -> %.2f, price=%.4f)",
			ID, hedgeShares, hedgeSharesFinal, hedgeShares*hedgePriceDecFinal, hedgeSharesFinal*hedgePriceDecFinal, hedgePriceDecFinal)
		hedgeShares = hedgeSharesFinal
	}

	// ===== 并发下单：使用 ExecuteMultiLeg 同时提交 Entry 和 Hedge 订单 =====
	req := execution.MultiLegRequest{
		Name:       "velocityfollow",
		MarketSlug: market.Slug,
		Legs: []execution.LegIntent{
			{
				Name:      "taker_buy_winner",
				AssetID:   entryAsset,
				TokenType: winner,
				Side:      types.SideBuy,
				Price:     entryPrice,
				Size:      entryShares,
				OrderType: types.OrderTypeFAK,
			},
			{
				Name:      "maker_buy_hedge",
				AssetID:   hedgeAsset,
				TokenType: opposite(winner),
				Side:      types.SideBuy,
				Price:     hedgePrice,
				Size:      hedgeShares,
				OrderType: types.OrderTypeGTC,
			},
		},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}

	createdOrders, execErr := s.TradingService.ExecuteMultiLeg(orderCtx, req)

	// 检测余额不足错误，刷新余额
	if execErr != nil {
		errStr := execErr.Error()
		if strings.Contains(errStr, "余额不足") || strings.Contains(errStr, "insufficient") || strings.Contains(errStr, "balance") {
			log.Warnf("⚠️ [%s] 并发下单失败（余额不足），尝试刷新余额: err=%v side=%s market=%s", ID, execErr, winner, market.Slug)
			// 使用独立的上下文刷新余额，避免阻塞
			refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer refreshCancel()
			if refreshErr := s.TradingService.RefreshBalance(refreshCtx); refreshErr != nil {
				log.Warnf("⚠️ [%s] 刷新余额失败: err=%v", ID, refreshErr)
			} else {
				log.Infof("✅ [%s] 已刷新余额，请稍后重试", ID)
			}
		}
	}

	var tradesCount int
	if execErr == nil && len(createdOrders) > 0 {
		now := time.Now()
		// 只在更新共享状态时持锁（性能关键）
		s.mu.Lock()
		s.lastTriggerAt = now
		s.lastTriggerSide = winner
		s.lastTriggerSideAt = now
		s.tradedThisCycle = true
		s.tradesCountThisCycle++ // 增加交易计数

		// 更新订单跟踪状态，并识别 Entry 和 Hedge 订单
		var entryOrderID, hedgeOrderID string
		var entryFilled bool
		entryFilledTime := now
		entryFilledSize := entryShares

		for _, order := range createdOrders {
			if order == nil || order.OrderID == "" {
				continue
			}
			if order.TokenType == winner {
				s.lastEntryOrderID = order.OrderID
				s.lastEntryOrderStatus = order.Status
				entryOrderID = order.OrderID
				if order.Status == domain.OrderStatusFilled {
					entryFilled = true
					if order.FilledSize > 0 {
						entryFilledSize = order.FilledSize
					}
				}
			} else if order.TokenType == opposite(winner) {
				s.lastHedgeOrderID = order.OrderID
				hedgeOrderID = order.OrderID
			}
		}
		tradesCount = s.tradesCountThisCycle
		s.mu.Unlock()

		// 如果 Entry 订单已成交，启动对冲单监控（支持 hedgeTimeoutFakSeconds）
		if entryFilled && hedgeOrderID != "" {
			// 记录未完成的对冲单：Entry已成交但Hedge未成交，确保对冲单成交后才能开启下一轮交易
			s.mu.Lock()
			if s.pendingHedges == nil {
				s.pendingHedges = make(map[string]string)
			}
			s.pendingHedges[entryOrderID] = hedgeOrderID
			log.Infof("📝 [%s] 记录未完成的对冲单，等待对冲单成交后才能开启下一轮交易: entryOrderID=%s hedgeOrderID=%s",
				ID, entryOrderID, hedgeOrderID)
			s.mu.Unlock()

			if s.HedgeReorderTimeoutSeconds > 0 || s.HedgeTimeoutFakSeconds > 0 {
				log.Infof("✅ [%s] Entry 订单已成交，启动对冲单监控: entryOrderID=%s hedgeOrderID=%s",
					ID, entryOrderID, hedgeOrderID)
				go s.monitorAndReorderHedge(ctx, market, entryOrderID, hedgeOrderID, hedgeAsset, hedgePrice, hedgeShares, entryFilledTime, entryFilledSize, entryAskCents, winner)
			}
		}

		log.Infof("⚡ [%s] 触发(并发): side=%s ask=%dc hedge=%dc vel=%.3f(c/s) move=%dc/%0.1fs bias=%s(%s) market=%s trades=%d/%d orders=%d",
			ID, winner, entryAskCents, hedgeAskCents, winMet.velocity, winMet.delta, winMet.seconds, biasTok, biasReason, market.Slug, tradesCount, s.MaxTradesPerCycle, len(createdOrders))
		if biasTok != "" || biasReason != "" {
			log.Infof("🧭 [%s] bias: token=%s reason=%s cycleStartMs=%d", ID, biasTok, biasReason, s.cycleStartMs)
		}

		// 额外：打印 Binance 1s/1m 最新 K 线（用于你观察"开盘 1 分钟"关系）
		if s.BinanceFuturesKlines != nil {
			if k1m, ok := s.BinanceFuturesKlines.Latest("1m"); ok {
				log.Infof("📊 [%s] Binance 1m kline: sym=%s o=%.2f c=%.2f h=%.2f l=%.2f closed=%v startMs=%d",
					ID, k1m.Symbol, k1m.Open, k1m.Close, k1m.High, k1m.Low, k1m.IsClosed, k1m.StartTimeMs)
			}
			if k1s, ok := s.BinanceFuturesKlines.Latest("1s"); ok {
				log.Infof("📊 [%s] Binance 1s kline: sym=%s o=%.2f c=%.2f closed=%v startMs=%d",
					ID, k1s.Symbol, k1s.Open, k1s.Close, k1s.IsClosed, k1s.StartTimeMs)
			}
		}
	} else {
		if isFailSafeRefusal(execErr) {
			log.Warnf("⏸️ [%s] 系统拒绝下单（fail-safe，预期行为）：err=%v market=%s", ID, execErr, market.Slug)
			return nil
		}
		log.Warnf("⚠️ [%s] 下单失败: err=%v side=%s market=%s", ID, execErr, winner, market.Slug)
	}
	return nil
}
