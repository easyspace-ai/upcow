package velocityhedgehold

import (
	"context"
	"math"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
)

// monitorHedge：
// - 周期内等待 Hedge 成交到与 entryFilledSize 等量（或更高一点点容错）。
// - 若 Hedge 长时间未成交：按互补价上界重挂（不追价、不穿价）。
//
// 注意：按用户要求，本策略不允许止损/平仓，因此该监控只做“持续尝试对冲”，绝不下 SELL。
func (s *Strategy) monitorHedge(
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

	reorderEvery := time.Duration(reorderTimeoutSeconds) * time.Second
	if reorderEvery <= 0 {
		reorderEvery = 30 * time.Second
	}
	nextReorder := start.Add(reorderEvery)
	if time.Now().After(nextReorder) {
		nextReorder = time.Now() // 重启后已过重挂周期：立即允许重挂
	}

	interval := time.Duration(s.HedgeMonitorIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = 1 * time.Second
	}
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
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
				if hedgeOrderID != "" {
					_ = s.TradingService.CancelOrder(context.Background(), hedgeOrderID)
				}
				return
			}

			// 若当前仓位已对冲（双边数量几乎相等），停止监控并清理挂单
			// 清除未对冲记录（如果存在）
			if entryOrderID != "" {
				s.unhedgedEntriesMu.Lock()
				delete(s.unhedgedEntries, entryOrderID)
				s.unhedgedEntriesMu.Unlock()
			}
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
				// 清除未对冲记录（如果存在）
				if entryOrderID != "" {
					s.unhedgedEntriesMu.Lock()
					delete(s.unhedgedEntries, entryOrderID)
					s.unhedgedEntriesMu.Unlock()
				}
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
				// 清除未对冲记录（如果存在）
				if entryOrderID != "" {
					s.unhedgedEntriesMu.Lock()
					delete(s.unhedgedEntries, entryOrderID)
					s.unhedgedEntriesMu.Unlock()
				}
				log.Infof("✅ [%s] Hedge 已完成：entryFilled=%.4f hedgeFilled=%.4f entryOrderID=%s hedgeOrderID=%s market=%s",
					ID, target, hedgeFilled, entryOrderID, hedgeOrderID, market.Slug)
				return
			}

			// 到点重挂：撤旧单，按互补价上界 + 不穿价，挂“剩余未对冲量”
			if now.After(nextReorder) {
				nextReorder = now.Add(reorderEvery)

				remaining := target - hedgeFilled
				if remaining <= 0 {
					continue
				}

				// 取消所有旧的 hedge 挂单（避免重复挂单）
				// 查找所有相同方向的 GTC 挂单并取消
				hedgeTok := opposite(entryToken)
				allOrders := s.TradingService.GetActiveOrders()
				oldPrices := make(map[string]int) // 记录旧订单的价格，用于判断是否需要改价
				canceledCount := 0
				for _, o := range allOrders {
					if o == nil || o.OrderID == "" {
						continue
					}
					if o.MarketSlug != market.Slug {
						continue
					}
					if o.TokenType != hedgeTok {
						continue
					}
					if o.Side != types.SideBuy {
						continue
					}
					if o.OrderType != types.OrderTypeGTC {
						continue
					}
					// 只取消可取消状态的订单（Open、Pending、Partial）
					// 跳过已终态订单（Filled、Canceled、Failed）和正在取消的订单（Canceling）
					if o.IsFinalStatus() || o.Status == domain.OrderStatusCanceling {
						continue
					}
					// 记录旧订单价格
					if o.Price.Pips > 0 {
						oldPrices[o.OrderID] = o.Price.ToCents()
					}
					// 取消订单（同步等待，确保取消完成）
					cancelCtx, cancelCancel := context.WithTimeout(context.Background(), 3*time.Second)
					if err := s.TradingService.CancelOrder(cancelCtx, o.OrderID); err != nil {
						log.Warnf("⚠️ [%s] 取消旧 hedge 订单失败: orderID=%s status=%s err=%v market=%s", ID, o.OrderID, o.Status, err, market.Slug)
					} else {
						canceledCount++
						log.Infof("✅ [%s] 已取消旧 hedge 订单: orderID=%s price=%dc status=%s market=%s", ID, o.OrderID, o.Price.ToCents(), o.Status, market.Slug)
					}
					cancelCancel()
				}
				// 如果取消了订单，等待一小段时间让订单状态更新
				if canceledCount > 0 {
					time.Sleep(300 * time.Millisecond)
					// 再次检查，确保订单真的被取消了（防止重复挂单）
					verifyOrders := s.TradingService.GetActiveOrders()
					stillOpenOrders := make([]string, 0)
					for _, o := range verifyOrders {
						if o == nil || o.OrderID == "" {
							continue
						}
						if o.MarketSlug != market.Slug {
							continue
						}
						if o.TokenType != hedgeTok {
							continue
						}
						if o.Side != types.SideBuy {
							continue
						}
						if o.OrderType != types.OrderTypeGTC {
							continue
						}
						if !o.IsFinalStatus() && o.Status != domain.OrderStatusCanceling {
							stillOpenOrders = append(stillOpenOrders, o.OrderID)
							log.Warnf("⚠️ [%s] 对冲重挂前仍有未取消的订单: orderID=%s status=%s market=%s", ID, o.OrderID, o.Status, market.Slug)
						}
					}
					if len(stillOpenOrders) > 0 {
						// 如果仍有未取消的订单，尝试使用 CancelOrdersForMarket 强制取消
						log.Warnf("⚠️ [%s] 对冲重挂前仍有 %d 个未取消的订单，尝试强制取消所有市场订单: orderIDs=%v market=%s", ID, len(stillOpenOrders), stillOpenOrders, market.Slug)
						forceCancelCtx, forceCancelCancel := context.WithTimeout(context.Background(), 2*time.Second)
						s.TradingService.CancelOrdersForMarket(forceCancelCtx, market.Slug)
						forceCancelCancel()
						// 再等待一下
						time.Sleep(200 * time.Millisecond)
						// 最后检查一次
						finalOrders := s.TradingService.GetActiveOrders()
						finalOpenCount := 0
						for _, o := range finalOrders {
							if o == nil || o.OrderID == "" {
								continue
							}
							if o.MarketSlug != market.Slug {
								continue
							}
							if o.TokenType != hedgeTok {
								continue
							}
							if o.Side != types.SideBuy {
								continue
							}
							if o.OrderType != types.OrderTypeGTC {
								continue
							}
							if !o.IsFinalStatus() && o.Status != domain.OrderStatusCanceling {
								finalOpenCount++
							}
						}
						if finalOpenCount > 0 {
							log.Errorf("🚨 [%s] 强制取消后仍有 %d 个未取消的订单，跳过本次重挂以避免重复挂单 market=%s", ID, finalOpenCount, market.Slug)
							continue // 跳过本次重挂，等待下一次
						}
					}
				}

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

				// 检查价格是否有变化：如果所有旧订单的价格都和新区间价相同，记录日志
				priceChanged := true
				if len(oldPrices) > 0 {
					allSamePrice := true
					for _, oldPrice := range oldPrices {
						if oldPrice != newLimitCents {
							allSamePrice = false
							break
						}
					}
					if allSamePrice {
						priceChanged = false
						log.Debugf("🔍 [%s] 对冲重挂：价格未变化 newLimitCents=%dc，但仍重挂以确保订单状态正确 market=%s", ID, newLimitCents, market.Slug)
					}
				}

				hedgePrice := domain.Price{Pips: newLimitCents * 100}
				hedgePriceDec := hedgePrice.ToDecimal()
				if hedgePriceDec <= 0 {
					continue
				}

				remaining = adjustSizeForMakerAmountPrecision(remaining, hedgePriceDec)
				// 若 maker 挂单金额不足，OrdersService 会自动放大 BUY size（破坏“一对一对冲”）。
				// 因此这里不下 maker 单，优先尝试用 taker(FOK/FAK) 对冲；若仍不满足最小金额则等待。
				enableTaker := s.EnableHedgeTakerFallback == nil || *s.EnableHedgeTakerFallback
				if remaining*hedgePriceDec < s.minOrderSize {
					if !enableTaker {
						continue
					}
					takerAsk := yesAsk
					if entryToken == domain.TokenTypeUp {
						takerAsk = noAsk
					}
					if takerAsk.Pips <= 0 {
						continue
					}
					if remaining*takerAsk.ToDecimal() < s.minOrderSize {
						log.Warnf("⚠️ [%s] 对冲金额不足：remaining=%.4f maker=%dc ask=%dc notional=%.2f < minOrderSize=%.2f entryOrderID=%s market=%s",
							ID, remaining, hedgePrice.ToCents(), takerAsk.ToCents(), remaining*takerAsk.ToDecimal(), s.minOrderSize, entryOrderID, market.Slug)
						continue
					}
					fak := &domain.Order{
						MarketSlug:        market.Slug,
						AssetID:           hedgeAsset,
						TokenType:         opposite(entryToken),
						Side:              types.SideBuy,
						Price:             takerAsk,
						Size:              remaining,
						OrderType:         types.OrderTypeFAK,
						IsEntryOrder:      false,
						HedgeOrderID:      &entryOrderID,
						BypassRiskOff:     true,
						SkipBalanceCheck:  s.SkipBalanceCheck,
						DisableSizeAdjust: (s.StrictOneToOneHedge == nil || *s.StrictOneToOneHedge),
						Status:            domain.OrderStatusPending,
						CreatedAt:         time.Now(),
					}
					s.attachMarketPrecision(fak)
					if placed, e := s.TradingService.PlaceOrder(context.Background(), fak); e == nil && placed != nil && placed.OrderID != "" {
						log.Infof("✅ [%s] 对冲 FAK（金额兜底）：orderID=%s remaining=%.4f ask=%dc entryOrderID=%s market=%s",
							ID, placed.OrderID, remaining, takerAsk.ToCents(), entryOrderID, market.Slug)
						hedgeOrderID = placed.OrderID
					}
					continue
				}
				// 若剩余量太小导致无法用 GTC 完成对冲，尝试用 FAK 对冲（不受 minShareSize 限制）。
				if remaining < s.minShareSize {
					if !enableTaker {
						continue
					}
					// taker price：对冲侧 ask
					takerAsk := yesAsk
					if entryToken == domain.TokenTypeUp {
						takerAsk = noAsk
					}
					// 若 takerAsk 无效则跳过
					if takerAsk.Pips <= 0 {
						continue
					}
					if remaining*takerAsk.ToDecimal() < s.minOrderSize {
						// 金额仍不足：等待后续价格变化/更多成交后再尝试
						log.Warnf("⚠️ [%s] 对冲剩余量过小且金额不足，无法 FAK 对冲：remaining=%.4f ask=%dc notional=%.2f < minOrderSize=%.2f entryOrderID=%s market=%s",
							ID, remaining, takerAsk.ToCents(), remaining*takerAsk.ToDecimal(), s.minOrderSize, entryOrderID, market.Slug)
						continue
					}
					fak := &domain.Order{
						MarketSlug:        market.Slug,
						AssetID:           hedgeAsset,
						TokenType:         opposite(entryToken),
						Side:              types.SideBuy,
						Price:             takerAsk,
						Size:              remaining,
						OrderType:         types.OrderTypeFAK,
						IsEntryOrder:      false,
						HedgeOrderID:      &entryOrderID,
						BypassRiskOff:     true,
						SkipBalanceCheck:  s.SkipBalanceCheck,
						DisableSizeAdjust: (s.StrictOneToOneHedge == nil || *s.StrictOneToOneHedge),
						Status:            domain.OrderStatusPending,
						CreatedAt:         time.Now(),
					}
					s.attachMarketPrecision(fak)
					if placed, e := s.TradingService.PlaceOrder(context.Background(), fak); e == nil && placed != nil && placed.OrderID != "" {
						log.Infof("✅ [%s] 对冲 FAK（小额兜底）：orderID=%s remaining=%.4f ask=%dc entryOrderID=%s market=%s",
							ID, placed.OrderID, remaining, takerAsk.ToCents(), entryOrderID, market.Slug)
						hedgeOrderID = placed.OrderID
					}
					continue
				}

				newHedge := &domain.Order{
					MarketSlug:        market.Slug,
					AssetID:           hedgeAsset,
					TokenType:         opposite(entryToken),
					Side:              types.SideBuy,
					Price:             hedgePrice,
					Size:              remaining,
					OrderType:         types.OrderTypeGTC,
					IsEntryOrder:      false,
					HedgeOrderID:      &entryOrderID,
					BypassRiskOff:     true,
					SkipBalanceCheck:  s.SkipBalanceCheck,
					DisableSizeAdjust: (s.StrictOneToOneHedge == nil || *s.StrictOneToOneHedge),
					Status:            domain.OrderStatusPending,
					CreatedAt:         time.Now(),
				}
				s.attachMarketPrecision(newHedge)
				placed, err := s.TradingService.PlaceOrder(context.Background(), newHedge)
				if err != nil {
					if isFailSafeRefusal(err) {
						// 系统拒绝：不做止损，等待下一轮重试
						continue
					}
					log.Warnf("⚠️ [%s] Hedge 重挂下单失败: err=%v remaining=%.4f limit=%dc market=%s", ID, err, remaining, newLimitCents, market.Slug)
					continue
				}
				if placed == nil || placed.OrderID == "" {
					log.Warnf("⚠️ [%s] Hedge 重挂下单返回空: remaining=%.4f limit=%dc market=%s", ID, remaining, newLimitCents, market.Slug)
					continue
				}
				priceChangeStr := "价格未变化"
				if priceChanged {
					priceChangeStr = "价格已更新"
				}
				log.Infof("🔄 [%s] Hedge 重挂：old=%s new=%s remaining=%.4f limit=%dc (max=%dc oppAsk=%dc source=%s) %s",
					ID, hedgeOrderID, placed.OrderID, remaining, newLimitCents, maxHedgeCents, oppAskCents, source, priceChangeStr)
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
	// 按用户要求：不允许止损/不允许 SELL 平仓。
	// 保留函数签名仅用于向后兼容旧调用点；任何调用只记录日志并返回。
	_ = ctx
	_ = entryOrderID
	_ = hedgeOrderID
	if market == nil {
		return
	}
	log.Warnf("⛔ [%s] stoploss 已禁用：忽略止损请求 reason=%s market=%s", ID, reason, market.Slug)
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
