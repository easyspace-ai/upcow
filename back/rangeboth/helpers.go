package rangeboth

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)

type priceSample struct {
	ts         time.Time
	priceCents int
}

func isFailSafeRefusal(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "trading paused") ||
		strings.Contains(s, "market mismatch") ||
		strings.Contains(s, "refuse to trade")
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

// adjustSizeForMakerAmountPrecision 调整 size 使得 maker amount = size × price 是 2 位小数。
// - Polymarket BUY 的 maker amount 是 USDC 金额，需要 <= 2 位小数
// - size（taker amount）通常要求 <= 4 位小数
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

// MarketPrecisionInfo 市场精度信息（从配置文件加载）。
type MarketPrecisionInfo struct {
	TickSize     string
	MinOrderSize string
	NegRisk      bool
}

func ParseTickSize(tickSizeStr string) (types.TickSize, error) {
	switch tickSizeStr {
	case "0.1":
		return types.TickSize01, nil
	case "0.01":
		return types.TickSize001, nil
	case "0.001":
		return types.TickSize0001, nil
	case "0.0001":
		return types.TickSize00001, nil
	default:
		return "", fmt.Errorf("不支持的 tick size: %s", tickSizeStr)
	}
}

func boolPtr(b bool) *bool { return &b }

func pruneSamples(in []priceSample, cutoff time.Time) []priceSample {
	if len(in) == 0 {
		return in
	}
	// 找到第一个 >= cutoff 的位置
	i := 0
	for i < len(in) && in[i].ts.Before(cutoff) {
		i++
	}
	if i <= 0 {
		return in
	}
	out := make([]priceSample, 0, len(in)-i)
	out = append(out, in[i:]...)
	return out
}

func rangeCents(in []priceSample) (min int, max int, ok bool) {
	if len(in) < 2 {
		return 0, 0, false
	}
	min = int(^uint(0) >> 1) // MaxInt
	max = -min - 1           // MinInt
	for _, s := range in {
		if s.priceCents <= 0 {
			continue
		}
		if s.priceCents < min {
			min = s.priceCents
		}
		if s.priceCents > max {
			max = s.priceCents
		}
	}
	if max < min {
		return 0, 0, false
	}
	return min, max, true
}

// chooseLimitBuyPrice 选择限价买单价格
// bidCents: bestBid（整数分）
// askCents: bestAsk（整数分）
// offsetCents: 价格偏移（美分，支持小数，如 0.1）
// 返回：计算后的价格（美分，支持小数）和是否成功
func chooseLimitBuyPrice(bidCents int, askCents int, offsetCents float64) (float64, bool) {
	if bidCents <= 0 || askCents <= 0 || bidCents >= 100 || askCents >= 100 {
		return 0, false
	}
	price := float64(bidCents) + offsetCents
	if price >= float64(askCents) {
		// 如果价格 >= ask，降低到 ask - 0.1（避免成为 taker）
		price = float64(askCents) - 0.1
	}
	if price <= 0 || price >= 100 {
		return 0, false
	}
	return price, true
}

func hasActiveBuyOrder(orders []*domain.Order, marketSlug string, assetID string) bool {
	if marketSlug == "" || assetID == "" {
		return false
	}
	for _, o := range orders {
		if o == nil {
			continue
		}
		if o.MarketSlug != marketSlug {
			continue
		}
		if o.AssetID != assetID {
			continue
		}
		if o.Side != types.SideBuy {
			continue
		}
		// 只要不是最终态，就认为“活跃”（pending/open/partial/canceling）
		if !o.IsFinalStatus() {
			return true
		}
	}
	return false
}
