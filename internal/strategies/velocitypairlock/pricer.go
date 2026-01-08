package velocitypairlock

import "fmt"

type PairPrices struct {
	PrimaryCents int
	HedgeCents   int
}

// PricePairLock 计算“锁定利润”的双边价格。
//
// 约束：
// - primary + hedge = 100 - profit
// - price 必须落在 [minCents, maxCents]
func PricePairLock(primaryCents int, profitCents int, minCents int, maxCents int) (PairPrices, error) {
	if profitCents <= 0 || profitCents >= 100 {
		return PairPrices{}, fmt.Errorf("profitCents invalid: %d", profitCents)
	}
	if minCents <= 0 {
		minCents = 1
	}
	if maxCents <= 0 {
		maxCents = 99
	}
	if minCents > maxCents {
		return PairPrices{}, fmt.Errorf("minCents > maxCents: %d > %d", minCents, maxCents)
	}
	if primaryCents < minCents || primaryCents > maxCents {
		return PairPrices{}, fmt.Errorf("primary price out of range: %d not in [%d,%d]", primaryCents, minCents, maxCents)
	}
	hedge := 100 - profitCents - primaryCents
	if hedge < minCents || hedge > maxCents {
		return PairPrices{}, fmt.Errorf("hedge price out of range: %d not in [%d,%d] (primary=%d profit=%d)", hedge, minCents, maxCents, primaryCents, profitCents)
	}
	return PairPrices{PrimaryCents: primaryCents, HedgeCents: hedge}, nil
}

