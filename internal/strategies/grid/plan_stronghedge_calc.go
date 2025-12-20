package grid

import (
	"math"

	"github.com/betbot/gobet/internal/domain"
)

// calcStrongHedgeOrderParams computes the supplement (strong hedge) order parameters.
// It keeps the original semantics in planStrongHedge, but centralizes calculations.
func (s *GridStrategy) calcStrongHedgeOrderParams(target, upWin, downWin float64) (tokenType domain.TokenType, assetID string, price domain.Price, dQ float64, maxBuy int, ok bool) {
	if s == nil || s.config == nil || s.currentMarket == nil {
		return "", "", domain.Price{}, 0, 0, false
	}

	needUp := math.Max(0, target-upWin)
	needDown := math.Max(0, target-downWin)

	var priceCents int
	var needed float64
	if needUp >= needDown {
		tokenType = domain.TokenTypeUp
		assetID = s.currentMarket.YesAssetID
		priceCents = s.currentPriceUp
		needed = needUp
	} else {
		tokenType = domain.TokenTypeDown
		assetID = s.currentMarket.NoAssetID
		priceCents = s.currentPriceDown
		needed = needDown
	}

	price = domain.Price{Cents: priceCents}
	priceDec := price.ToDecimal()
	if priceDec <= 0 || priceDec >= 1 {
		return "", "", domain.Price{}, 0, 0, false
	}

	dQ = needed / (1.0 - priceDec)
	if dQ <= 0 || math.IsNaN(dQ) || math.IsInf(dQ, 0) {
		return "", "", domain.Price{}, 0, 0, false
	}

	minOrderUSDC := s.config.MinOrderSize
	if minOrderUSDC <= 0 {
		minOrderUSDC = 1.1
	}
	if dQ*priceDec < minOrderUSDC {
		dQ = minOrderUSDC / priceDec
	}

	// 限制单次补仓
	maxDQ := 50.0
	if s.isInHedgeLockWindow(s.currentMarket) {
		maxDQ = math.Max(50.0, dQ)
	}
	if dQ > maxDQ {
		dQ = maxDQ
	}

	// 取 bestAsk 的滑点上限（相对当前观测价）
	maxBuy = 0
	if s.config.SupplementMaxBuySlippageCents > 0 {
		maxBuy = price.Cents + s.config.SupplementMaxBuySlippageCents
	}

	return tokenType, assetID, price, dQ, maxBuy, true
}
