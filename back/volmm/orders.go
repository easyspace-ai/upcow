package volmm

import (
	"context"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
)

type trackedOrder struct {
	OrderID   string
	PricePips int
	Size      float64
}

// syncQuote ensures one GTC order exists for given quoteKey at target price.
// - If no existing order: place.
// - If price moved beyond threshold: cancel + replace.
func (s *Strategy) syncQuote(ctx context.Context, market *domain.Market, assetID string, q desiredQuote, bestBidPips, bestAskPips int) {
	if s == nil || s.TradingService == nil || market == nil || assetID == "" {
		return
	}
	if q.pricePips <= 0 || q.size <= 0 {
		return
	}

	// maker protection: avoid becoming taker
	pricePips := q.pricePips
	if q.key.side == sideBuy && bestAskPips > 0 {
		limit := bestAskPips - s.tickPips
		if limit <= 0 {
			return
		}
		if pricePips >= limit {
			pricePips = limit
		}
		pricePips = roundDownToTick(pricePips, s.tickPips)
	} else if q.key.side == sideSell && bestBidPips > 0 {
		limit := bestBidPips + s.tickPips
		if pricePips <= limit {
			pricePips = limit
		}
		pricePips = roundUpToTick(pricePips, s.tickPips)
	}
	pricePips = clampPricePips(pricePips, s.tickPips)
	if pricePips <= 0 {
		return
	}

	// min shares guard
	if q.size < s.minShareSize {
		return
	}

	key := q.key
	s.mu.Lock()
	cur := s.quoteOrders[key]
	replaceThreshold := s.tickPips * s.Config.ReplaceThresholdTicks
	needReplace := false
	if cur == nil || cur.OrderID == "" {
		needReplace = true
	} else if replaceThreshold > 0 && absInt(cur.PricePips-pricePips) >= replaceThreshold {
		needReplace = true
	}
	oldOrderID := ""
	if cur != nil {
		oldOrderID = cur.OrderID
	}
	s.mu.Unlock()
	if !needReplace {
		return
	}

	// cancel old
	if oldOrderID != "" {
		_ = s.TradingService.CancelOrder(ctx, oldOrderID)
	}

	side := types.SideBuy
	if key.side == sideSell {
		side = types.SideSell
	}

	order := &domain.Order{
		MarketSlug: market.Slug,
		AssetID:    assetID,
		TokenType:  key.token,
		Side:       side,
		Price:      domain.Price{Pips: pricePips},
		Size:       q.size,
		OrderType:  types.OrderTypeGTC,
		Status:     domain.OrderStatusPending,
		CreatedAt:  time.Now(),
		TickSize:   s.orderTickSize,
		NegRisk:    s.negRisk,
	}

	created, err := s.TradingService.PlaceOrder(ctx, order)
	if err != nil || created == nil || created.OrderID == "" {
		return
	}

	s.mu.Lock()
	s.quoteOrders[key] = &trackedOrder{
		OrderID:   created.OrderID,
		PricePips: pricePips,
		Size:      q.size,
	}
	s.mu.Unlock()
}

func (s *Strategy) cancelAllQuotes(ctx context.Context, marketSlug string) {
	if s == nil || s.TradingService == nil || marketSlug == "" {
		return
	}
	s.TradingService.CancelOrdersForMarket(ctx, marketSlug)
	s.mu.Lock()
	for k := range s.quoteOrders {
		delete(s.quoteOrders, k)
	}
	s.mu.Unlock()
}

func (s *Strategy) flattenIfNeeded(ctx context.Context, market *domain.Market, yesBid, noBid domain.Price, netDelta float64, inv inventorySnapshot) {
	if s == nil || s.TradingService == nil || market == nil {
		return
	}
	if s.Config.RiskOnlyAllowFlatten == nil || !*s.Config.RiskOnlyAllowFlatten {
		return
	}
	needReduce := mathAbs(netDelta) - s.Config.RiskOnlyMaxDeltaShares
	if needReduce <= 0 {
		return
	}
	// throttle
	s.mu.Lock()
	last := s.lastFlattenAt
	if !last.IsZero() && time.Since(last) < time.Duration(s.Config.FlattenIntervalMs)*time.Millisecond {
		s.mu.Unlock()
		return
	}
	s.lastFlattenAt = time.Now()
	s.mu.Unlock()

	// Decide which side to sell (reduce net exposure).
	var tok domain.TokenType
	var assetID string
	var bid domain.Price
	var available float64
	if netDelta > 0 {
		tok = domain.TokenTypeUp
		assetID = market.YesAssetID
		bid = yesBid
		available = inv.Up
	} else {
		tok = domain.TokenTypeDown
		assetID = market.NoAssetID
		bid = noBid
		available = inv.Down
	}
	if bid.Pips <= 0 || assetID == "" {
		return
	}

	sellSize := needReduce
	if sellSize > s.Config.FlattenMaxOrderSize && s.Config.FlattenMaxOrderSize > 0 {
		sellSize = s.Config.FlattenMaxOrderSize
	}
	if sellSize > available {
		sellSize = available
	}
	if sellSize < s.minShareSize {
		return
	}

	order := &domain.Order{
		MarketSlug: market.Slug,
		AssetID:    assetID,
		TokenType:  tok,
		Side:       types.SideSell,
		Price:      bid,
		Size:       sellSize,
		OrderType:  types.OrderTypeFAK,
		Status:     domain.OrderStatusPending,
		CreatedAt:  time.Now(),
		TickSize:   s.orderTickSize,
		NegRisk:    s.negRisk,
	}
	_, _ = s.TradingService.PlaceOrder(ctx, order)
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func mathAbs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// mkMQOptions builds MarketQualityOptions from config (nil if disabled).
func (s *Strategy) mkMQOptions() *services.MarketQualityOptions {
	if s == nil || s.Config.EnableMarketQualityGate == nil || !*s.Config.EnableMarketQualityGate {
		return nil
	}
	maxAge := time.Duration(s.Config.MarketQualityMaxBookAgeMs) * time.Millisecond
	if maxAge <= 0 {
		maxAge = 3 * time.Second
	}
	maxSpreadPips := s.Config.MarketQualityMaxSpreadCents * 100 // 1c=100 pips
	if maxSpreadPips <= 0 {
		maxSpreadPips = 10 * 100
	}
	return &services.MarketQualityOptions{
		MaxBookAge:     maxAge,
		MaxSpreadPips:  maxSpreadPips,
		PreferWS:       true,
		FallbackToREST: true,
		AllowPartialWS: true,
	}
}

