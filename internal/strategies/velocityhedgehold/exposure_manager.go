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
		return true
	}

	// 1) 已对冲：两边数量几乎相等 -> 清理残留挂单，避免额外被动成交
	// 注意：返回 false，让 maxTradesPerCycle 来控制是否继续开新仓
	// 这样即使已对冲，只要 tradesCount < maxTradesPerCycle，仍可以继续开新仓
	if upSize > 0 && downSize > 0 && nearlyEqualShares(upSize, downSize) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.TradingService.CancelOrdersForMarket(ctx, market.Slug)
		// 返回 false，允许 maxTradesPerCycle 控制是否继续开新仓
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
		// 已完全对冲：返回 false，让 maxTradesPerCycle 控制是否继续开新仓
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

	// 找到现存 hedge 买单（若存在多个，保留一个，其他撤掉）
	hedgeOrderID := ""
	orders := s.TradingService.GetActiveOrders()
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
		if hedgeOrderID == "" {
			hedgeOrderID = o.OrderID
			continue
		}
		// 多余挂单撤掉，避免意外加仓
		go func(id string) { _ = s.TradingService.CancelOrder(context.Background(), id) }(o.OrderID)
	}

	hedgeAsset := market.GetAssetID(hedgeTok)

	// 若没有 hedge 单，则立即挂一张（不依赖 goroutine）
	if hedgeOrderID == "" {
		// 需要对侧 ask（防穿价）
		bookCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, yesAsk, _, noAsk, _, err := s.TradingService.GetTopOfBook(bookCtx, market)
		if err != nil {
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
			return true
		}
		limitCents := maxHedgeCents
		if oppAskCents > 0 && limitCents >= oppAskCents {
			limitCents = oppAskCents - 1
		}
		if limitCents <= 0 || limitCents >= 100 {
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
					MarketSlug:       market.Slug,
					AssetID:          hedgeAsset,
					TokenType:        hedgeTok,
					Side:             types.SideBuy,
					Price:            takerAsk,
					Size:             remaining,
					OrderType:        types.OrderTypeFAK,
					BypassRiskOff:    true,
					SkipBalanceCheck: s.SkipBalanceCheck,
					Status:           domain.OrderStatusPending,
					CreatedAt:        time.Now(),
				}
				s.attachMarketPrecision(fak)
				if placed, e := s.TradingService.PlaceOrder(context.Background(), fak); e == nil && placed != nil && placed.OrderID != "" {
					hedgeOrderID = placed.OrderID
				}
			}
			// 无论是否成功，都不止损；交给后续 tick/监控继续尝试
			return true
		}

		o := &domain.Order{
			MarketSlug:       market.Slug,
			AssetID:          hedgeAsset,
			TokenType:        hedgeTok,
			Side:             types.SideBuy,
			Price:            price,
			Size:             remaining,
			OrderType:        types.OrderTypeGTC,
			BypassRiskOff:    true,
			SkipBalanceCheck: s.SkipBalanceCheck,
			Status:           domain.OrderStatusPending,
			CreatedAt:        time.Now(),
		}
		s.attachMarketPrecision(o)
		placed, err := s.TradingService.PlaceOrder(context.Background(), o)
		if err == nil && placed != nil {
			hedgeOrderID = placed.OrderID
		}
	}

	// 启动监控（重启恢复）：用 position 的 entryAt 作为计时基准
	if hedgeOrderID != "" && entryPriceCents > 0 {
		s.startMonitorIfNeeded(market.Slug, func() {
			s.monitorHedge(context.Background(), market, entryTok, "", entryPriceCents, target, entryAt, hedgeOrderID, hedgeAsset, s.HedgeReorderTimeoutSeconds)
		})
	}

	// 返回 false，让 maxTradesPerCycle 控制是否继续开新仓
	// 即使有未对冲持仓，只要 tradesCount < maxTradesPerCycle，仍可以继续开新仓
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
