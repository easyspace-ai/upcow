package orderutil

import (
	"context"
	"fmt"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)

type BestPriceGetter interface {
	// GetBestPrice 返回 bestBid, bestAsk（小数价格）
	GetBestPrice(ctx context.Context, assetID string) (bestBid float64, bestAsk float64, err error)
}

// QuoteBuyPrice 返回买入时使用的价格（默认取 bestAsk），可选做上限保护（maxCents>0）。
func QuoteBuyPrice(ctx context.Context, g BestPriceGetter, assetID string, maxCents int) (domain.Price, error) {
	if g == nil {
		return domain.Price{}, fmt.Errorf("BestPriceGetter 为空")
	}
	_, bestAsk, err := g.GetBestPrice(ctx, assetID)
	if err != nil {
		return domain.Price{}, err
	}
	if bestAsk <= 0 {
		return domain.Price{}, fmt.Errorf("订单簿 bestAsk 无效: %.6f", bestAsk)
	}
	p := domain.PriceFromDecimal(bestAsk)
	if maxCents > 0 && p.Cents > maxCents {
		return domain.Price{}, fmt.Errorf("买入滑点保护触发: bestAsk=%dc > max=%dc", p.Cents, maxCents)
	}
	return p, nil
}

// QuoteBuyPriceOr is a convenience wrapper that falls back to fallback price when QuoteBuyPrice fails.
// It returns the chosen price and the original error (if any).
func QuoteBuyPriceOr(ctx context.Context, g BestPriceGetter, assetID string, maxCents int, fallback domain.Price) (domain.Price, error) {
	p, err := QuoteBuyPrice(ctx, g, assetID, maxCents)
	if err != nil {
		return fallback, err
	}
	return p, nil
}

// QuoteSellPrice 返回卖出时使用的价格（默认取 bestBid），可选做下限保护（minCents>0）。
func QuoteSellPrice(ctx context.Context, g BestPriceGetter, assetID string, minCents int) (domain.Price, error) {
	if g == nil {
		return domain.Price{}, fmt.Errorf("BestPriceGetter 为空")
	}
	bestBid, _, err := g.GetBestPrice(ctx, assetID)
	if err != nil {
		return domain.Price{}, err
	}
	if bestBid <= 0 {
		return domain.Price{}, fmt.Errorf("订单簿 bestBid 无效: %.6f", bestBid)
	}
	p := domain.PriceFromDecimal(bestBid)
	if minCents > 0 && p.Cents < minCents {
		return domain.Price{}, fmt.Errorf("卖出滑点保护触发: bestBid=%dc < min=%dc", p.Cents, minCents)
	}
	return p, nil
}

// NewOrder 统一构建订单（自动填 MarketSlug/Status/CreatedAt/OrderType）。
func NewOrder(marketSlug string, assetID string, side types.Side, price domain.Price, size float64, tokenType domain.TokenType, isEntry bool, orderType types.OrderType) *domain.Order {
	return &domain.Order{
		MarketSlug:   marketSlug,
		AssetID:      assetID,
		Side:         side,
		Price:        price,
		Size:         size,
		TokenType:    tokenType,
		IsEntryOrder: isEntry,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
		OrderType:    orderType,
	}
}
