package common

import (
	"math"

	"github.com/betbot/gobet/internal/domain"
)

// AdjustSizeForMinOrderUSDC ensures order amount (size * price) meets minOrderUSDC.
//
// If autoAdjust is false:
// - returns skipped=true when amount < minOrderUSDC
//
// If autoAdjust is true:
// - increases size to meet minOrderUSDC
// - if required adjust ratio exceeds maxAdjustRatio (>0), returns skipped=true
//
// Returns:
// - adjustedSize: resulting size (may equal input size)
// - skipped: whether caller should skip placing the order
// - adjusted: whether size was changed
// - ratio: adjustedSize / size (0 if size<=0)
// - origAmount: size * price
// - newAmount: adjustedSize * price
func AdjustSizeForMinOrderUSDC(
	size float64,
	price domain.Price,
	minOrderUSDC float64,
	autoAdjust bool,
	maxAdjustRatio float64,
) (adjustedSize float64, skipped bool, adjusted bool, ratio float64, origAmount float64, newAmount float64) {
	if size <= 0 {
		return 0, true, false, 0, 0, 0
	}
	if minOrderUSDC <= 0 {
		// no constraint
		origAmount = size * price.ToDecimal()
		return size, false, false, 1, origAmount, origAmount
	}

	p := price.ToDecimal()
	if p <= 0 {
		return size, true, false, 0, 0, 0
	}

	origAmount = size * p
	if origAmount >= minOrderUSDC {
		return size, false, false, 1, origAmount, origAmount
	}

	if !autoAdjust {
		return size, true, false, 1, origAmount, origAmount
	}

	required := minOrderUSDC / p
	ratio = required / size
	if maxAdjustRatio > 0 && ratio > maxAdjustRatio {
		return size, true, false, ratio, origAmount, origAmount
	}

	adjustedSize = required
	newAmount = adjustedSize * p
	adjusted = true

	// ensure minimal rounding safety (avoid tiny float errors)
	if newAmount < minOrderUSDC {
		adjustedSize = adjustedSize * 1.01
		newAmount = adjustedSize * p
		ratio = adjustedSize / size
	}

	// prevent NaN/Inf propagation
	if math.IsNaN(adjustedSize) || math.IsInf(adjustedSize, 0) {
		return size, true, false, 0, origAmount, origAmount
	}

	return adjustedSize, false, adjusted, ratio, origAmount, newAmount
}
