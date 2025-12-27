package volmm

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)

type quoteSide string

const (
	sideBuy  quoteSide = "buy"
	sideSell quoteSide = "sell"
)

type quoteKey struct {
	token domain.TokenType
	side  quoteSide
}

type desiredQuote struct {
	key       quoteKey
	pricePips int
	size      float64
}

func tickPipsFromTickSizeStr(tickSize string) (int, error) {
	s := strings.TrimSpace(tickSize)
	if s == "" {
		return 0, fmt.Errorf("tickSize 为空")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("无效 tickSize=%q", tickSize)
	}
	pips := int(math.Round(f * 10000))
	if pips <= 0 {
		return 0, fmt.Errorf("tickSize=%q 转换 pips 失败", tickSize)
	}
	return pips, nil
}

func clampPricePips(pips int, tickPips int) int {
	if tickPips <= 0 {
		tickPips = 1
	}
	if pips < tickPips {
		return tickPips
	}
	max := 10000 - tickPips
	if pips > max {
		return max
	}
	return pips
}

func roundDownToTick(pips int, tickPips int) int {
	if tickPips <= 0 {
		return pips
	}
	return (pips / tickPips) * tickPips
}

func roundUpToTick(pips int, tickPips int) int {
	if tickPips <= 0 {
		return pips
	}
	return ((pips + tickPips - 1) / tickPips) * tickPips
}

func parseTickSizeForOrder(tickSizeStr string) (types.TickSize, error) {
	switch strings.TrimSpace(tickSizeStr) {
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

