package paircostarb

import (
	"math"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)

func orderTypeFromConfig(cfg Config) types.OrderType {
	if cfg.OrderType == "taker" {
		return types.OrderTypeFAK
	}
	return types.OrderTypeGTC
}

func clampPrice(p float64) float64 {
	if p <= 0 {
		return 0
	}
	if p > 0.9999 {
		return 0.9999
	}
	return p
}

func makeBuyOrder(market *domain.Market, assetID string, token domain.TokenType, qty float64, limitPrice float64, ot types.OrderType, bypassRiskOff bool) domain.Order {
	return domain.Order{
		MarketSlug:        market.Slug,
		AssetID:           assetID,
		Side:              types.SideBuy,
		Price:             domain.PriceFromDecimal(limitPrice),
		Size:              qty,
		TokenType:         token,
		IsEntryOrder:      true,
		Status:            domain.OrderStatusPending,
		CreatedAt:         time.Now(),
		OrderType:         ot,
		BypassRiskOff:     bypassRiskOff,
		DisableSizeAdjust: true, // 严格 share 数量，避免系统放大导致失衡
	}
}

func applyPad(baseCents, extraCents int) float64 {
	return float64(baseCents+extraCents) / 100.0
}

func safeMin(a, b float64) float64 {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	return math.Min(a, b)
}
