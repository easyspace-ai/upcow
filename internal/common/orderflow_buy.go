package common

import (
	"context"
	"fmt"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/orderutil"
	"github.com/betbot/gobet/internal/ports"
)

// QuoteAndAdjustBuy shares common logic used by multiple strategies:
// 1) Quote bestAsk (with optional maxCents slippage cap)
// 2) Adjust/skip the order size to satisfy minOrderUSDC and auto-adjust policy
func QuoteAndAdjustBuy(
	ctx context.Context,
	ts ports.BestPriceGetter,
	assetID string,
	maxCents int,
	size float64,
	minOrderUSDC float64,
	autoAdjust bool,
	maxSizeAdjustRatio float64,
) (bestAskPrice domain.Price, adjustedSize float64, skipped bool, adjusted bool, adjustRatio float64, orderAmount float64, newOrderAmount float64, err error) {
	bestAskPrice, err = orderutil.QuoteBuyPrice(ctx, ts, assetID, maxCents)
	if err != nil {
		return domain.Price{}, 0, false, false, 0, 0, 0, fmt.Errorf("获取订单簿失败: %w", err)
	}

	adjustedSize, skipped, adjusted, adjustRatio, orderAmount, newOrderAmount = AdjustSizeForMinOrderUSDC(
		size,
		bestAskPrice,
		minOrderUSDC,
		autoAdjust,
		maxSizeAdjustRatio,
	)

	return bestAskPrice, adjustedSize, skipped, adjusted, adjustRatio, orderAmount, newOrderAmount, nil
}
