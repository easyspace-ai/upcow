package velocityfollow

import (
	"math"
	"strings"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
)

func isFailSafeRefusal(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	// 这些错误都是系统级 gate 的“预期拒绝”，不能当作策略失败或污染状态
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

// adjustSizeForMakerAmountPrecision 调整 size 使得 maker amount = size × price 是 2 位小数
// 对于买入订单（FAK），maker amount 是 USDC 金额，必须 <= 2 位小数
// taker amount (size) 必须 <= 4 位小数
// 策略：先调整 maker amount 到 2 位小数，再重新计算 size 到 4 位小数
func adjustSizeForMakerAmountPrecision(size float64, price float64) float64 {
	if size <= 0 || price <= 0 {
		return size
	}

	// 计算 maker amount = size × price
	makerAmount := size * price

	// 将 maker amount 向下舍入到 2 位小数
	// 使用 math.Floor 确保向下舍入，避免浮点数精度问题
	makerAmountCents := int(math.Floor(makerAmount*100 + 0.0001)) // 添加小的epsilon避免浮点误差
	makerAmountRounded := float64(makerAmountCents) / 100.0

	// 如果舍入后为 0，使用最小有效值（0.01）
	if makerAmountRounded <= 0 {
		makerAmountRounded = 0.01
		makerAmountCents = 1
	}

	// 重新计算 size = maker amount / price
	newSize := makerAmountRounded / price

	// 将 size 向下舍入到 4 位小数（taker amount 要求）
	// 使用 math.Floor 确保向下舍入
	newSizeCents := int(math.Floor(newSize*10000 + 0.0001)) // 添加小的epsilon避免浮点误差
	newSize = float64(newSizeCents) / 10000.0

	// 确保 size 不为 0
	if newSize <= 0 {
		return size // 如果调整后为 0，返回原始值
	}

	// 验证：重新计算 maker amount，确保是 2 位小数
	verifyMakerAmount := newSize * price
	verifyMakerAmountCents := int(math.Floor(verifyMakerAmount*100 + 0.0001))
	verifyMakerAmountRounded := float64(verifyMakerAmountCents) / 100.0

	// 如果验证失败（maker amount 不是2位小数），再次调整
	if math.Abs(verifyMakerAmount-verifyMakerAmountRounded) > 0.0001 {
		// 使用验证后的 maker amount 重新计算 size
		newSize = verifyMakerAmountRounded / price
		newSizeCents = int(math.Floor(newSize*10000 + 0.0001))
		newSize = float64(newSizeCents) / 10000.0
	}

	return newSize
}

func candleStatsBps(k services.Kline, upTok domain.TokenType, downTok domain.TokenType) (bodyBps int, wickBps int, dirTok domain.TokenType) {
	// body: |c-o|/o
	body := math.Abs(k.Close-k.Open) / k.Open * 10000
	bodyBps = int(body + 0.5)

	hi := k.High
	lo := k.Low
	o := k.Open
	c := k.Close
	maxOC := math.Max(o, c)
	minOC := math.Min(o, c)
	upperWick := (hi - maxOC) / o * 10000
	lowerWick := (minOC - lo) / o * 10000
	w := math.Max(upperWick, lowerWick)
	if w < 0 {
		w = 0
	}
	wickBps = int(w + 0.5)

	dirTok = downTok
	if c >= o {
		dirTok = upTok
	}
	return
}
