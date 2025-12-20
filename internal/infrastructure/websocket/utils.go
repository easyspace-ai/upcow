package websocket

import (
	"fmt"
	"os"
	"strings"

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

// getProxyFromEnv 从环境变量获取代理 URL（与 MarketStream/UserWebSocket 共用）
func getProxyFromEnv() string {
	proxyVars := []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"}
	for _, v := range proxyVars {
		if proxy := strings.TrimSpace(os.Getenv(v)); proxy != "" {
			return proxy
		}
	}
	return ""
}

