package websocket

import (
	"fmt"
	"github.com/betbot/gobet/internal/domain"
)

// parsePriceString 解析价格字符串（共享工具函数）
func parsePriceString(priceStr string) (domain.Price, error) {
	var price float64
	if _, err := fmt.Sscanf(priceStr, "%f", &price); err != nil {
		return domain.Price{}, fmt.Errorf("解析价格失败: %w", err)
	}
	return domain.PriceFromDecimal(price), nil
}

