package paircostarb

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/betbot/gobet/clob/types"
)

// estimateBuyCostEff 使用订单簿 asks 深度估算 buy VWAP，并叠加 slippagePad 与 feeRate，返回有效 VWAP 与有效成本。
func estimateBuyCostEff(ctx context.Context, getBook func(context.Context, string) (*types.OrderBookSummary, error), assetID string, qty float64, feeRate float64, slippagePad float64) (vwapEff float64, costEff float64, ok bool) {
	if getBook == nil || assetID == "" || qty <= 0 {
		return 0, 0, false
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	book, err := getBook(cctx, assetID)
	if err != nil || book == nil {
		return 0, 0, false
	}
	vwap, ok := estimateBuyVWAP(book.Asks, qty)
	if !ok || vwap <= 0 {
		return 0, 0, false
	}
	vwapEff = vwap + slippagePad
	if vwapEff <= 0 {
		return 0, 0, false
	}
	costEff = vwapEff * qty * (1.0 + feeRate)
	return vwapEff, costEff, true
}

func estimateBuyVWAP(asks []types.OrderSummary, qty float64) (vwap float64, ok bool) {
	if qty <= 0 {
		return 0, false
	}
	filled := 0.0
	cost := 0.0
	for _, lv := range asks {
		if filled >= qty {
			break
		}
		p, err := strconv.ParseFloat(lv.Price, 64)
		if err != nil || p <= 0 {
			continue
		}
		sz, err := strconv.ParseFloat(lv.Size, 64)
		if err != nil || sz <= 0 {
			continue
		}
		take := math.Min(sz, qty-filled)
		if take <= 0 {
			continue
		}
		cost += p * take
		filled += take
	}
	if filled+1e-9 < qty {
		return 0, false
	}
	return cost / qty, true
}
