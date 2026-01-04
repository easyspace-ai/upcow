package velocityfollow

import (
	"context"
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
	var tradesCount int
	var pendingCount int
	if execErr == nil && len(createdOrders) > 0 {
		now := time.Now()
		// ✅ 修复竞态条件：立即更新 lastEntryOrderID，防止第二次交易在订单提交后、状态更新前触发
		// 先找到 Entry 订单并立即更新状态
		var entryOrderID string
		for _, order := range createdOrders {
			if order == nil || order.OrderID == "" {
				continue
			}
			if order.TokenType == winner {
				entryOrderID = order.OrderID
				break
			}
		}
		
		// 只在更新共享状态时持锁（性能关键）
		s.mu.Lock()
		s.lastTriggerAt = now
		s.lastTriggerSide = winner
		s.lastTriggerSideAt = now
		s.tradedThisCycle = true
		// ⚠️ 重要：不再在这里增加交易计数，只有 Entry + Hedge 都成交后才算完成一次交易
		// 交易计数会在 OnOrderUpdate 回调中，当 Hedge 订单成交时增加
		// s.tradesCountThisCycle++ // 已移除：只有 Hedge 成交后才增加计数

		// 更新订单跟踪状态
		if entryOrderID != "" {
			s.lastEntryOrderID = entryOrderID
		}
		for _, order := range createdOrders {
			if order == nil || order.OrderID == "" {
				continue
			}
			if order.TokenType == winner {
				if entryOrderID == "" {
					s.lastEntryOrderID = order.OrderID
				}
				s.lastEntryOrderStatus = order.Status
			} else if order.TokenType == opposite(winner) {
				s.lastHedgeOrderID = order.OrderID
			}
		}
		tradesCount = s.tradesCountThisCycle
		if s.pendingTrades != nil {
			pendingCount = len(s.pendingTrades)
		}
		s.mu.Unlock()

		log.Infof("⚡ [%s] 触发(并发): side=%s ask=%dc hedge=%dc vel=%.3f(c/s) move=%dc/%0.1fs bias=%s(%s) market=%s trades=%d(已完成)+%d(进行中)/%d orders=%d",
			ID, winner, entryAskCents, hedgeAskCents, winMet.velocity, winMet.delta, winMet.seconds, biasTok, biasReason, market.Slug, tradesCount, pendingCount, s.MaxTradesPerCycle, len(createdOrders))
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
