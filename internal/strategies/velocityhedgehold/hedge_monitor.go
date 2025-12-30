package velocityhedgehold

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
)

// monitorHedgeAndStoploss：
// - 周期内等待 Hedge 成交到与 entryFilledSize 等量（或更高一点点容错）。
// - 超时/价格止损触发：撤销挂单并 SELL FAK 平掉当前 market 的所有持仓（清敞口）。
// - 若 Hedge 长时间未成交：按互补价上界重挂（不追价、不穿价）。
func (s *Strategy) monitorHedgeAndStoploss(
	ctx context.Context,
	market *domain.Market,
	entryToken domain.TokenType,
	entryOrderID string,
	entryPriceCents int,
	entryFilledSize float64,
	entryFilledAt time.Time,
	hedgeOrderID string,
	hedgeAsset string,
	reorderTimeoutSeconds int,
	unhedgedMaxSeconds int,
	unhedgedStopLossCents int,
) {
	if s == nil || s.TradingService == nil || market == nil {
		return
	}
	if entryFilledSize <= 0 {
		return
	}
	start := entryFilledAt
	if start.IsZero() {
		start = time.Now()
	}
	deadline := start.Add(time.Duration(unhedgedMaxSeconds) * time.Second)

	reorderEvery := time.Duration(reorderTimeoutSeconds) * time.Second
	if reorderEvery <= 0 {
		reorderEvery = 30 * time.Second
	}
	nextReorder := start.Add(reorderEvery)
	if time.Now().After(nextReorder) {
		nextReorder = time.Now() // 重启后已过重挂周期：立即允许重挂
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	target := entryFilledSize
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()

			// 如果持仓已不存在（例如外部手动处理），停止并清掉 hedge 挂单
			positions := s.TradingService.GetOpenPositionsForMarket(market.Slug)
			if !hasAnyOpenPosition(positions) {
				_ = s.TradingService.CancelOrder(context.Background(), hedgeOrderID)
				return
			}

			// 若当前仓位已对冲（双边数量几乎相等），停止监控并清理挂单
			upPos, downPos := splitPositions(positions)
			upSize, downSize := 0.0, 0.0
			if upPos != nil {
				upSize = upPos.Size
			}
			if downPos != nil {
				downSize = downPos.Size
			}
			if upSize > 0 && downSize > 0 && nearlyEqualShares(upSize, downSize) {
				s.TradingService.CancelOrdersForMarket(context.Background(), market.Slug)
				log.Infof("✅ [%s] 监控结束：仓位已对冲（按持仓判断） up=%.4f down=%.4f market=%s", ID, upSize, downSize, market.Slug)
				return
			}

			// 判断对冲是否完成：以 hedge order 的 filledSize 为准（最直接）
			hedgeFilled := 0.0
			if ord, ok := s.TradingService.GetOrder(hedgeOrderID); ok && ord != nil {
				hedgeFilled = ord.FilledSize
				if ord.Status == domain.OrderStatusFilled && ord.FilledSize <= 0 {
					// 兜底：有些路径 filledSize 可能缺失
					hedgeFilled = ord.Size
				}
			}

			// 允许一个很小的容错（浮点/精度）
			if hedgeFilled > 0 && hedgeFilled >= target*0.999 {
				log.Infof("✅ [%s] Hedge 已完成：entryFilled=%.4f hedgeFilled=%.4f entryOrderID=%s hedgeOrderID=%s market=%s",
					ID, target, hedgeFilled, entryOrderID, hedgeOrderID, market.Slug)
				return
			}

			// 价格止损：未对冲期间，若 entry 方向浮亏过大，立即止损
			if unhedgedStopLossCents > 0 {
				if hit, diff := s.unhedgedStopLossHit(market, entryToken, unhedgedStopLossCents); hit {
					log.Warnf("🚨 [%s] 未对冲止损触发（价格）：diff=%dc sl=%dc entryOrderID=%s hedgeOrderID=%s market=%s",
						ID, diff, unhedgedStopLossCents, entryOrderID, hedgeOrderID, market.Slug)
					s.forceStoploss(context.Background(), market, "unhedged_price_stoploss", entryOrderID, hedgeOrderID)
					return
				}
			}

			// 超时止损：仍未完成对冲
			if now.After(deadline) {
				log.Warnf("🚨 [%s] 未对冲止损触发（超时）：wait=%.1fs max=%ds entryOrderID=%s hedgeOrderID=%s market=%s",
					ID, now.Sub(start).Seconds(), unhedgedMaxSeconds, entryOrderID, hedgeOrderID, market.Slug)
				s.forceStoploss(context.Background(), market, "unhedged_timeout_stoploss", entryOrderID, hedgeOrderID)
				return
			}

			// 到点重挂：撤旧单，按互补价上界 + 不穿价，挂“剩余未对冲量”
			if now.After(nextReorder) {
				nextReorder = now.Add(reorderEvery)

				remaining := target - hedgeFilled
				if remaining <= 0 {
					continue
				}
				if remaining < s.minShareSize {
					// 剩余量小于 GTC 最小 shares：无法完成对冲 => 走止损
					log.Warnf("🚨 [%s] 未对冲剩余量过小，无法继续挂单完成对冲：remaining=%.4f minShareSize=%.4f entryOrderID=%s hedgeOrderID=%s",
						ID, remaining, s.minShareSize, entryOrderID, hedgeOrderID)
					s.forceStoploss(context.Background(), market, "unhedged_remaining_too_small", entryOrderID, hedgeOrderID)
					return
				}

				// 取消旧 hedge 单（best effort）
				_ = s.TradingService.CancelOrder(context.Background(), hedgeOrderID)

				reorderCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, yesAsk, _, noAsk, source, err := s.TradingService.GetTopOfBook(reorderCtx, market)
				cancel()
				if err != nil {
					// 获取失败：本次不重挂，等待下一次 tick
					continue
				}

				// 当前对侧 ask（用于防穿价）
				oppAskCents := yesAsk.ToCents()
				if entryToken == domain.TokenTypeUp {
					// entry=UP => hedge=DOWN => 对侧 ask=NO ask
					oppAskCents = noAsk.ToCents()
				}

				maxHedgeCents := 100 - entryPriceCents - s.HedgeOffsetCents
				newLimitCents := maxHedgeCents
				if oppAskCents > 0 && newLimitCents >= oppAskCents {
					newLimitCents = oppAskCents - 1
				}
				if newLimitCents <= 0 || newLimitCents >= 100 {
					log.Warnf("🚨 [%s] 对冲重挂失败：互补价无效 max=%dc oppAsk=%dc (source=%s) entryPrice=%dc offset=%dc",
						ID, maxHedgeCents, oppAskCents, source, entryPriceCents, s.HedgeOffsetCents)
					continue
				}
				hedgePrice := domain.Price{Pips: newLimitCents * 100}
				hedgePriceDec := hedgePrice.ToDecimal()
				if hedgePriceDec <= 0 {
					continue
				}

				// 金额约束：remaining 必须满足最小金额，否则无法下单；这种情况直接止损（避免拖到最后）
				if remaining*hedgePriceDec < s.minOrderSize {
					log.Warnf("🚨 [%s] 对冲重挂剩余金额不足：remaining=%.4f price=%dc notional=%.2f < minOrderSize=%.2f，触发止损",
						ID, remaining, newLimitCents, remaining*hedgePriceDec, s.minOrderSize)
					s.forceStoploss(context.Background(), market, "unhedged_remaining_notional_too_small", entryOrderID, hedgeOrderID)
					return
				}

				remaining = adjustSizeForMakerAmountPrecision(remaining, hedgePriceDec)
				if remaining < s.minShareSize {
					s.forceStoploss(context.Background(), market, "unhedged_remaining_precision_too_small", entryOrderID, hedgeOrderID)
					return
				}

				newHedge := &domain.Order{
					MarketSlug:   market.Slug,
					AssetID:      hedgeAsset,
					TokenType:    opposite(entryToken),
					Side:         types.SideBuy,
					Price:        hedgePrice,
					Size:         remaining,
					OrderType:    types.OrderTypeGTC,
					IsEntryOrder: false,
					HedgeOrderID: &entryOrderID,
					Status:       domain.OrderStatusPending,
					CreatedAt:    time.Now(),
				}
				s.attachMarketPrecision(newHedge)
				placed, err := s.TradingService.PlaceOrder(context.Background(), newHedge)
				if err != nil {
					if isFailSafeRefusal(err) {
						s.forceStoploss(context.Background(), market, "hedge_reorder_refused_by_failsafe", entryOrderID, hedgeOrderID)
						return
					}
					continue
				}
				if placed == nil || placed.OrderID == "" {
					continue
				}
				log.Infof("🔄 [%s] Hedge 重挂：old=%s new=%s remaining=%.4f limit=%dc (max=%dc oppAsk=%dc source=%s)",
					ID, hedgeOrderID, placed.OrderID, remaining, newLimitCents, maxHedgeCents, oppAskCents, source)
				hedgeOrderID = placed.OrderID
			}
		}
	}
}

func (s *Strategy) unhedgedStopLossHit(market *domain.Market, entryToken domain.TokenType, slCents int) (bool, int) {
	if s == nil || s.TradingService == nil || market == nil || slCents <= 0 {
		return false, 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	yesBid, _, noBid, _, _, err := s.TradingService.GetTopOfBook(ctx, market)
	if err != nil {
		return false, 0
	}

	positions := s.TradingService.GetOpenPositionsForMarket(market.Slug)
	var entryPos *domain.Position
	for _, p := range positions {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}
		if p.TokenType == entryToken {
			entryPos = p
			break
		}
	}
	if entryPos == nil {
		return false, 0
	}

	bid := yesBid
	if entryToken == domain.TokenTypeDown {
		bid = noBid
	}
	curC := bid.ToCents()
	avgC := entryPos.EntryPrice.ToCents()
	if entryPos.AvgPrice > 0 {
		avgC = int(entryPos.AvgPrice*100 + 0.5)
	}
	diff := curC - avgC
	if diff <= -slCents {
		return true, diff
	}
	return false, diff
}

func (s *Strategy) forceStoploss(ctx context.Context, market *domain.Market, reason string, entryOrderID string, hedgeOrderID string) {
	if s == nil || s.TradingService == nil || market == nil {
		return
	}
	stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// 记录止损触发时的详细上下文信息
	positions := s.TradingService.GetOpenPositionsForMarket(market.Slug)
	activeOrders := s.TradingService.GetActiveOrders()
	
	var upPos *domain.Position
	var upSize, downSize float64
	for _, p := range positions {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}
		if p.TokenType == domain.TokenTypeUp {
			upPos = p
			upSize = p.Size
		} else if p.TokenType == domain.TokenTypeDown {
			downSize = p.Size
		}
	}
	
	var activeOrderIDs []string
	var hedgeOrderStatus string
	var entryOrderStatus string
	for _, o := range activeOrders {
		if o == nil || o.OrderID == "" {
			continue
		}
		if o.MarketSlug != market.Slug {
			continue
		}
		activeOrderIDs = append(activeOrderIDs, o.OrderID)
		if o.OrderID == hedgeOrderID {
			hedgeOrderStatus = string(o.Status)
		}
		if o.OrderID == entryOrderID {
			entryOrderStatus = string(o.Status)
		}
	}
	
	// 如果 entryOrderID 或 hedgeOrderID 为空，尝试从订单中查找
	if entryOrderID == "" && upPos != nil {
		// 尝试查找 entry 订单
		for _, o := range activeOrders {
			if o != nil && o.MarketSlug == market.Slug && o.TokenType == domain.TokenTypeUp && o.Side == types.SideBuy {
				entryOrderID = o.OrderID
				entryOrderStatus = string(o.Status)
				break
			}
		}
	}
	if hedgeOrderID == "" {
		// 尝试查找 hedge 订单
		for _, o := range activeOrders {
			if o != nil && o.MarketSlug == market.Slug && o.Side == types.SideBuy && o.OrderType == types.OrderTypeGTC {
				if (upSize > downSize && o.TokenType == domain.TokenTypeDown) || (downSize > upSize && o.TokenType == domain.TokenTypeUp) {
					hedgeOrderID = o.OrderID
					hedgeOrderStatus = string(o.Status)
					break
				}
			}
		}
	}
	
	log.Warnf("🚨 [%s] 止损触发详情：reason=%s entryOrderID=%s entryOrderStatus=%s hedgeOrderID=%s hedgeOrderStatus=%s upSize=%.4f downSize=%.4f activeOrders=%d market=%s",
		ID, reason, entryOrderID, entryOrderStatus, hedgeOrderID, hedgeOrderStatus, upSize, downSize, len(activeOrderIDs), market.Slug)
	
	if len(activeOrderIDs) > 0 {
		log.Debugf("📋 [%s] 止损时的活跃订单：orderIDs=%v market=%s", ID, activeOrderIDs, market.Slug)
	}

	// 1) 先取消本市场所有挂单，避免平仓过程中被动成交造成反向敞口
	s.TradingService.CancelOrdersForMarket(stopCtx, market.Slug)

	// 2) 拉 bid 并平掉所有持仓（UP/DOWN 都平，确保净敞口=0）
	yesBid, _, noBid, _, _, err := s.TradingService.GetTopOfBook(stopCtx, market)
	if err != nil {
		log.Warnf("⚠️ [%s] 止损获取盘口失败：reason=%s err=%v entryOrderID=%s hedgeOrderID=%s upSize=%.4f downSize=%.4f market=%s",
			ID, reason, err, entryOrderID, hedgeOrderID, upSize, downSize, market.Slug)
		return
	}
	
	log.Debugf("📊 [%s] 止损时盘口价格：yesBid=%dc noBid=%dc reason=%s market=%s",
		ID, yesBid.ToCents(), noBid.ToCents(), reason, market.Slug)

	positions = s.TradingService.GetOpenPositionsForMarket(market.Slug)
	flattenedCount := 0
	for _, p := range positions {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}
		exitPrice := yesBid
		exitAsset := market.YesAssetID
		if p.TokenType == domain.TokenTypeDown {
			exitPrice = noBid
			exitAsset = market.NoAssetID
		}
		if exitPrice.Pips <= 0 {
			log.Warnf("⚠️ [%s] 止损平仓跳过：token=%s size=%.4f exitPrice=0 reason=%s market=%s",
				ID, p.TokenType, p.Size, reason, market.Slug)
			continue
		}
		
		// 记录持仓详情
		entryPriceInfo := ""
		if p.AvgPrice > 0 {
			entryPriceInfo = fmt.Sprintf("avgPrice=%.4f", p.AvgPrice)
		} else if p.EntryPrice.Pips > 0 {
			entryPriceInfo = fmt.Sprintf("entryPrice=%dc", p.EntryPrice.ToCents())
		}
		entryTimeInfo := ""
		if !p.EntryTime.IsZero() {
			entryTimeInfo = fmt.Sprintf("entryTime=%s elapsed=%.1fs", p.EntryTime.Format(time.RFC3339), time.Since(p.EntryTime).Seconds())
		}
		
		exit := &domain.Order{
			MarketSlug: market.Slug,
			AssetID:    exitAsset,
			TokenType:  p.TokenType,
			Side:       types.SideSell,
			Price:      exitPrice,
			Size:       p.Size,
			OrderType:  types.OrderTypeFAK,
			Status:     domain.OrderStatusPending,
			CreatedAt:  time.Now(),
		}
		s.attachMarketPrecision(exit)
		if _, err := s.TradingService.PlaceOrder(stopCtx, exit); err != nil {
			log.Warnf("❌ [%s] 止损平仓失败：token=%s size=%.4f bid=%dc %s %s err=%v reason=%s entryOrderID=%s hedgeOrderID=%s market=%s",
				ID, p.TokenType, p.Size, exitPrice.ToCents(), entryPriceInfo, entryTimeInfo, err, reason, entryOrderID, hedgeOrderID, market.Slug)
		} else {
			flattenedCount++
			log.Warnf("✅ [%s] 止损平仓：token=%s size=%.4f bid=%dc %s %s reason=%s entryOrderID=%s hedgeOrderID=%s market=%s",
				ID, p.TokenType, p.Size, exitPrice.ToCents(), entryPriceInfo, entryTimeInfo, reason, entryOrderID, hedgeOrderID, market.Slug)
		}
	}
	
	if flattenedCount == 0 && (upSize > 0 || downSize > 0) {
		log.Warnf("⚠️ [%s] 止损平仓警告：检测到持仓但未成功平仓任何仓位 upSize=%.4f downSize=%.4f reason=%s market=%s",
			ID, upSize, downSize, reason, market.Slug)
	}
}

// candleStatsBps：复制自 velocityfollow（用于 open1m bias）。
func candleStatsBps(k services.Kline, upTok domain.TokenType, downTok domain.TokenType) (bodyBps int, wickBps int, dirTok domain.TokenType) {
	body := math.Abs(k.Close-k.Open) / k.Open * 10000
	bodyBps = int(body + 0.5)

	hi := k.High
	lo := k.Low
	o := k.Open
	c := k.Close
	maxOC := math.Max(o, c)
	minOC := math.Min(o, c)
	upperWick := (hi - maxOC) / o * 10000
	lowerWick := (minOC - lo) / o * 10000
	w := math.Max(upperWick, lowerWick)
	if w < 0 {
		w = 0
	}
	wickBps = int(w + 0.5)

	dirTok = downTok
	if c >= o {
		dirTok = upTok
	}
	return
}
