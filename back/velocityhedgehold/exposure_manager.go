package velocityhedgehold

import (
	"context"
	"math"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)

// manageExistingExposure returns true when we handled an existing exposure and
// caller should skip entry logic for this tick.
func (s *Strategy) manageExistingExposure(now time.Time, market *domain.Market) bool {
	if s == nil || s.TradingService == nil || market == nil {
		return false
	}
	positions := s.TradingService.GetOpenPositionsForMarket(market.Slug)
	if !hasAnyOpenPosition(positions) {
		return false
	}

	upPos, downPos := splitPositions(positions)
	upSize, downSize := 0.0, 0.0
	if upPos != nil {
		upSize = upPos.Size
	}
	if downPos != nil {
		downSize = downPos.Size
	}

	target := math.Max(upSize, downSize)
	if target <= 0 {
		log.Debugf("🔍 [%s] manageExistingExposure: 返回 true (target<=0), upSize=%.4f downSize=%.4f market=%s", ID, upSize, downSize, market.Slug)
		return true
	}

	// 1) 已对冲：两边数量几乎相等 -> 清理残留挂单，避免额外被动成交
	// 注意：如果 RequireFullyHedgedBeforeNewEntry=true，需要检查是否有未成交的对冲订单
	if upSize > 0 && downSize > 0 && nearlyEqualShares(upSize, downSize) {
		// 如果要求完全对冲后才能开新单，检查是否有未成交的对冲订单
		if s.RequireFullyHedgedBeforeNewEntry {
			orders := s.TradingService.GetActiveOrders()
			hasPendingHedgeOrder := false
			for _, o := range orders {
				if o == nil || o.OrderID == "" {
					continue
				}
				if o.MarketSlug != market.Slug {
					continue
				}
				if o.Side != types.SideBuy {
					continue
				}
				if o.OrderType != types.OrderTypeGTC {
					continue
				}
				// 检查是否有未成交的对冲订单（Open、Pending、Partial 状态）
				if !o.IsFinalStatus() && o.Status != domain.OrderStatusCanceling {
					hasPendingHedgeOrder = true
					log.Debugf("🔍 [%s] manageExistingExposure: 发现未成交的对冲订单: orderID=%s status=%s market=%s", ID, o.OrderID, o.Status, market.Slug)
					break
				}
			}
			if hasPendingHedgeOrder {
				log.Infof("🚫 [%s] manageExistingExposure: 有未成交的对冲订单且 RequireFullyHedgedBeforeNewEntry=true，禁止开新单: upSize=%.4f downSize=%.4f market=%s", ID, upSize, downSize, market.Slug)
				return true
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.TradingService.CancelOrdersForMarket(ctx, market.Slug)
		// 返回 false，允许 maxTradesPerCycle 控制是否继续开新仓
		log.Debugf("🔍 [%s] manageExistingExposure: 返回 false (已对冲), upSize=%.4f downSize=%.4f market=%s", ID, upSize, downSize, market.Slug)
		return false
	}

	// 2) 未对冲：确定 entry/hedge 方向与剩余量
	entryTok := domain.TokenTypeUp
	entryPos := upPos
	hedgeTok := domain.TokenTypeDown
	hedgedSoFar := downSize
	if downSize > upSize {
		entryTok = domain.TokenTypeDown
		entryPos = downPos
		hedgeTok = domain.TokenTypeUp
		hedgedSoFar = upSize
	}
	remaining := target - hedgedSoFar
	if remaining <= 0 {
		// 已完全对冲：检查是否有未成交的对冲订单
		if s.RequireFullyHedgedBeforeNewEntry {
			orders := s.TradingService.GetActiveOrders()
			hasPendingHedgeOrder := false
			for _, o := range orders {
				if o == nil || o.OrderID == "" {
					continue
				}
				if o.MarketSlug != market.Slug {
					continue
				}
				if o.Side != types.SideBuy {
					continue
				}
				if o.TokenType != hedgeTok {
					continue
				}
				if o.OrderType != types.OrderTypeGTC {
					continue
				}
				// 检查是否有未成交的对冲订单（Open、Pending、Partial 状态）
				if !o.IsFinalStatus() && o.Status != domain.OrderStatusCanceling {
					hasPendingHedgeOrder = true
					log.Debugf("🔍 [%s] manageExistingExposure: 发现未成交的对冲订单: orderID=%s status=%s market=%s", ID, o.OrderID, o.Status, market.Slug)
					break
				}
			}
			if hasPendingHedgeOrder {
				log.Infof("🚫 [%s] manageExistingExposure: 有未成交的对冲订单且 RequireFullyHedgedBeforeNewEntry=true，禁止开新单: entryTok=%s remaining=%.4f market=%s", ID, entryTok, remaining, market.Slug)
				return true
			}
		}
		// 已完全对冲：返回 false，让 maxTradesPerCycle 控制是否继续开新仓
		log.Debugf("🔍 [%s] manageExistingExposure: 返回 false (已完全对冲), entryTok=%s remaining=%.4f market=%s", ID, entryTok, remaining, market.Slug)
		return false
	}

	// Entry time / price（用于超时与互补价上界）
	entryAt := now
	entryPriceCents := 0
	if entryPos != nil {
		if !entryPos.EntryTime.IsZero() {
			entryAt = entryPos.EntryTime
		}
		if entryPos.AvgPrice > 0 {
			entryPriceCents = int(entryPos.AvgPrice*100 + 0.5)
		} else if entryPos.EntryPrice.Pips > 0 {
			entryPriceCents = entryPos.EntryPrice.ToCents()
		}
	}
	if entryPriceCents <= 0 || entryPriceCents >= 100 {
		// 无法推导 entry 价格：无法计算互补价上界，保守地只做“观察”，等待后续持仓/价格信息补齐
		log.Warnf("⚠️ [%s] 恢复场景无法获取 entryPriceCents，暂不重挂对冲单：entryTok=%s remaining=%.4f market=%s", ID, entryTok, remaining, market.Slug)
		return true
	}

	// 找到现存 hedge 买单（若存在多个，全部取消，重新挂单）
	// 注意：这里不保留旧订单，因为价格可能已经变化，统一重新挂单更安全
	hedgeOrderID := ""
	orders := s.TradingService.GetActiveOrders()
	canceledCount := 0
	for _, o := range orders {
		if o == nil || o.OrderID == "" {
			continue
		}
		if o.MarketSlug != market.Slug {
			continue
		}
		if o.Side != types.SideBuy {
			continue
		}
		if o.TokenType != hedgeTok {
			continue
		}
		if o.OrderType != types.OrderTypeGTC {
			continue
		}
		// 只取消可取消状态的订单
		if o.IsFinalStatus() || o.Status == domain.OrderStatusCanceling {
			continue
		}
		// 同步取消订单，确保取消完成
		cancelCtx, cancelCancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := s.TradingService.CancelOrder(cancelCtx, o.OrderID); err != nil {
			log.Debugf("🔍 [%s] manageExistingExposure: 取消旧 hedge 订单失败: orderID=%s err=%v market=%s", ID, o.OrderID, err, market.Slug)
		} else {
			canceledCount++
			log.Debugf("🔍 [%s] manageExistingExposure: 已取消旧 hedge 订单: orderID=%s price=%dc market=%s", ID, o.OrderID, o.Price.ToCents(), market.Slug)
		}
		cancelCancel()
	}
	// 如果取消了订单，等待状态更新
	if canceledCount > 0 {
		time.Sleep(300 * time.Millisecond)
	}

	hedgeAsset := market.GetAssetID(hedgeTok)

	// 重新检查，确保没有残留的 hedge 订单（防止重复挂单）
	// 注意：即使之前找到了 hedgeOrderID，我们也取消它并重新挂单，确保价格是最新的
	verifyOrders := s.TradingService.GetActiveOrders()
	for _, o := range verifyOrders {
		if o == nil || o.OrderID == "" {
			continue
		}
		if o.MarketSlug != market.Slug {
			continue
		}
		if o.Side != types.SideBuy {
			continue
		}
		if o.TokenType != hedgeTok {
			continue
		}
		if o.OrderType != types.OrderTypeGTC {
			continue
		}
		if !o.IsFinalStatus() && o.Status != domain.OrderStatusCanceling {
			// 仍有未取消的订单，强制取消
			cancelCtx, cancelCancel := context.WithTimeout(context.Background(), 2*time.Second)
			s.TradingService.CancelOrder(cancelCtx, o.OrderID)
			cancelCancel()
			log.Warnf("⚠️ [%s] manageExistingExposure: 发现残留 hedge 订单，已强制取消: orderID=%s market=%s", ID, o.OrderID, market.Slug)
		}
	}
	// 再等待一下，确保取消完成
	if canceledCount > 0 {
		time.Sleep(200 * time.Millisecond)
	}

	// 统一重新挂单（确保价格是最新的，避免重复挂单）
	// 需要对侧 ask（防穿价）
	{
		bookCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, yesAsk, _, noAsk, _, err := s.TradingService.GetTopOfBook(bookCtx, market)
		if err != nil {
			log.Debugf("🔍 [%s] manageExistingExposure: 返回 true (获取盘口失败), err=%v market=%s", ID, err, market.Slug)
			return true
		}
		oppAskCents := yesAsk.ToCents()
		if hedgeTok == domain.TokenTypeDown {
			oppAskCents = noAsk.ToCents()
		}

		maxHedgeCents := 0
		if entryPriceCents > 0 {
			maxHedgeCents = 100 - entryPriceCents - s.HedgeOffsetCents
		}
		if maxHedgeCents <= 0 {
			log.Debugf("🔍 [%s] manageExistingExposure: 返回 true (maxHedgeCents<=0), entryPriceCents=%d hedgeOffset=%d market=%s", ID, entryPriceCents, s.HedgeOffsetCents, market.Slug)
			return true
		}
		limitCents := maxHedgeCents
		if oppAskCents > 0 && limitCents >= oppAskCents {
			limitCents = oppAskCents - 1
		}
		if limitCents <= 0 || limitCents >= 100 {
			log.Debugf("🔍 [%s] manageExistingExposure: 返回 true (limitCents无效), limitCents=%d maxHedgeCents=%d oppAskCents=%d market=%s", ID, limitCents, maxHedgeCents, oppAskCents, market.Slug)
			return true
		}
		price := domain.Price{Pips: limitCents * 100}
		px := price.ToDecimal()
		remaining = adjustSizeForMakerAmountPrecision(remaining, px)
		// 若无法以 maker(GTC) 完成对冲（shares 或金额不足），则不止损；改为尝试 taker(FAK) 对冲或等待后续条件满足。
		if remaining*px < s.minOrderSize || remaining < s.minShareSize {
			takerAsk := yesAsk
			if hedgeTok == domain.TokenTypeDown {
				takerAsk = noAsk
			}
			if takerAsk.Pips > 0 && remaining*takerAsk.ToDecimal() >= s.minOrderSize {
				fak := &domain.Order{
					MarketSlug:        market.Slug,
					AssetID:           hedgeAsset,
					TokenType:         hedgeTok,
					Side:              types.SideBuy,
					Price:             takerAsk,
					Size:              remaining,
					OrderType:         types.OrderTypeFAK,
					BypassRiskOff:     true,
					SkipBalanceCheck:  s.SkipBalanceCheck,
					DisableSizeAdjust: (s.StrictOneToOneHedge == nil || *s.StrictOneToOneHedge),
					Status:            domain.OrderStatusPending,
					CreatedAt:         time.Now(),
				}
				s.attachMarketPrecision(fak)
				if placed, e := s.TradingService.PlaceOrder(context.Background(), fak); e == nil && placed != nil && placed.OrderID != "" {
					hedgeOrderID = placed.OrderID
					log.Infof("✅ [%s] manageExistingExposure: 已创建 FAK hedge 订单: orderID=%s price=%dc size=%.4f market=%s", ID, placed.OrderID, takerAsk.ToCents(), remaining, market.Slug)
				} else {
					log.Warnf("⚠️ [%s] manageExistingExposure: 创建 FAK hedge 订单失败: err=%v size=%.4f market=%s", ID, e, remaining, market.Slug)
					// 如果要求完全对冲后才能开新单，且 FAK 对冲订单创建失败，禁止开新单
					if s.RequireFullyHedgedBeforeNewEntry {
						log.Warnf("🚫 [%s] manageExistingExposure: FAK 对冲订单创建失败，且 RequireFullyHedgedBeforeNewEntry=true，禁止开新单: remaining=%.4f market=%s", ID, remaining, market.Slug)
						return true
					}
				}
			}
			// 如果要求完全对冲后才能开新单，且有未对冲持仓，禁止开新单
			// 但是，如果 remaining 非常小（小于容差阈值），可以认为已经基本对冲完成
			// 容差阈值：remaining < 0.1 shares 或 remaining < target * 0.01 (1%)
			remainingTolerance := math.Max(0.1, target*0.01)
			if s.RequireFullyHedgedBeforeNewEntry && remaining > remainingTolerance && hedgeOrderID == "" {
				log.Warnf("🚫 [%s] manageExistingExposure: 无法用maker完成对冲且 RequireFullyHedgedBeforeNewEntry=true，禁止开新单: remaining=%.4f tolerance=%.4f market=%s", ID, remaining, remainingTolerance, market.Slug)
				return true
			}
			if s.RequireFullyHedgedBeforeNewEntry && remaining > 0 && remaining <= remainingTolerance && hedgeOrderID == "" {
				log.Infof("✅ [%s] manageExistingExposure: remaining=%.4f 小于容差阈值 %.4f，视为已基本对冲完成，允许开新单: market=%s", ID, remaining, remainingTolerance, market.Slug)
				// 不返回 true，继续执行后续逻辑，允许开新单
			}
			// 无论是否成功，都不止损；交给后续 tick/监控继续尝试
			log.Debugf("🔍 [%s] manageExistingExposure: 返回 true (无法用maker完成对冲), remaining=%.4f minOrderSize=%.2f minShareSize=%.2f market=%s", ID, remaining, s.minOrderSize, s.minShareSize, market.Slug)
			return true
		}

		o := &domain.Order{
			MarketSlug:        market.Slug,
			AssetID:           hedgeAsset,
			TokenType:         hedgeTok,
			Side:              types.SideBuy,
			Price:             price,
			Size:              remaining,
			OrderType:         types.OrderTypeGTC,
			BypassRiskOff:     true,
			SkipBalanceCheck:  s.SkipBalanceCheck,
			DisableSizeAdjust: (s.StrictOneToOneHedge == nil || *s.StrictOneToOneHedge),
			Status:            domain.OrderStatusPending,
			CreatedAt:         time.Now(),
		}
		s.attachMarketPrecision(o)
		placed, err := s.TradingService.PlaceOrder(context.Background(), o)
		if err == nil && placed != nil && placed.OrderID != "" {
			hedgeOrderID = placed.OrderID
			log.Infof("✅ [%s] manageExistingExposure: 已创建新 hedge 订单: orderID=%s price=%dc size=%.4f market=%s", ID, placed.OrderID, limitCents, remaining, market.Slug)
		} else if err != nil {
			log.Warnf("⚠️ [%s] manageExistingExposure: 创建 hedge 订单失败: err=%v price=%dc size=%.4f market=%s", ID, err, limitCents, remaining, market.Slug)
			// 如果要求完全对冲后才能开新单，且对冲订单创建失败，禁止开新单
			if s.RequireFullyHedgedBeforeNewEntry {
				log.Warnf("🚫 [%s] manageExistingExposure: 对冲订单创建失败，且 RequireFullyHedgedBeforeNewEntry=true，禁止开新单: remaining=%.4f market=%s", ID, remaining, market.Slug)
				return true
			}
		}
	}

	// 启动监控（重启恢复）：用 position 的 entryAt 作为计时基准
	if hedgeOrderID != "" && entryPriceCents > 0 {
		s.startMonitorIfNeeded(market.Slug, func() {
			s.monitorHedge(context.Background(), market, entryTok, "", entryPriceCents, target, entryAt, hedgeOrderID, hedgeAsset, s.HedgeReorderTimeoutSeconds)
		})
	}

	// 如果要求完全对冲后才能开新单，且有未对冲持仓，禁止开新单
	// 但是，如果 remaining 非常小（小于容差阈值），可以认为已经基本对冲完成
	// 容差阈值：remaining < 0.1 shares 或 remaining < target * 0.01 (1%)
	remainingTolerance := math.Max(0.1, target*0.01)
	if s.RequireFullyHedgedBeforeNewEntry && remaining > remainingTolerance {
		log.Infof("🚫 [%s] manageExistingExposure: 有未对冲持仓且 RequireFullyHedgedBeforeNewEntry=true，禁止开新单: entryTok=%s remaining=%.4f tolerance=%.4f hedgeOrderID=%s market=%s", ID, entryTok, remaining, remainingTolerance, hedgeOrderID, market.Slug)
		return true
	}
	if s.RequireFullyHedgedBeforeNewEntry && remaining > 0 && remaining <= remainingTolerance {
		log.Infof("✅ [%s] manageExistingExposure: remaining=%.4f 小于容差阈值 %.4f，视为已基本对冲完成，允许开新单: entryTok=%s hedgeOrderID=%s market=%s", ID, remaining, remainingTolerance, entryTok, hedgeOrderID, market.Slug)
		// 不返回 true，继续执行后续逻辑，允许开新单
	}

	// 返回 false，让 maxTradesPerCycle 控制是否继续开新仓
	// 即使有未对冲持仓，只要 tradesCount < maxTradesPerCycle，仍可以继续开新仓
	log.Debugf("🔍 [%s] manageExistingExposure: 返回 false (已处理未对冲持仓), entryTok=%s remaining=%.4f hedgeOrderID=%s market=%s", ID, entryTok, remaining, hedgeOrderID, market.Slug)
	return false
}

func splitPositions(positions []*domain.Position) (up *domain.Position, down *domain.Position) {
	for _, p := range positions {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}
		if p.TokenType == domain.TokenTypeUp {
			up = p
		} else if p.TokenType == domain.TokenTypeDown {
			down = p
		}
	}
	return
}

func nearlyEqualShares(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	// 容错：至少 1e-4，并随规模略放大
	eps := math.Max(1e-4, 0.001*math.Max(a, b))
	return d <= eps
}
