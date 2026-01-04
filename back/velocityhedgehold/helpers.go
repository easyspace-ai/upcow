package velocityhedgehold

import (
	"math"
	"strings"

	"github.com/betbot/gobet/internal/domain"
)

func isFailSafeRefusal(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "trading paused") ||
		strings.Contains(s, "market mismatch") ||
		strings.Contains(s, "refuse to trade")
}

func opposite(t domain.TokenType) domain.TokenType {
	if t == domain.TokenTypeUp {
		return domain.TokenTypeDown
	}
	return domain.TokenTypeUp
}

func ensureMinOrderSize(desiredShares float64, price float64, minUSDC float64) float64 {
	if desiredShares <= 0 || price <= 0 {
		return desiredShares
	}
	if minUSDC <= 0 {
		minUSDC = 1.0
	}
	minShares := minUSDC / price
	if minShares > desiredShares {
		return minShares
	}
	return desiredShares
}

// adjustSizeForMakerAmountPrecision 调整 size 使 maker amount = size × price 为 2 位小数（向下取整）。
func adjustSizeForMakerAmountPrecision(size float64, price float64) float64 {
	if size <= 0 || price <= 0 {
		return size
	}
	makerAmount := size * price
	makerAmountRounded := math.Floor(makerAmount*100) / 100
	if makerAmountRounded <= 0 {
		makerAmountRounded = 0.01
	}
	newSize := makerAmountRounded / price
	newSize = math.Floor(newSize*10000) / 10000
	if newSize <= 0 {
		return size
	}
	return newSize
}
