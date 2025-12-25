package websocket

import (
	"errors"
	"os"
	"strings"

	"github.com/betbot/gobet/internal/domain"
)

// parsePriceString 解析价格字符串（共享工具函数）
func parsePriceString(priceStr string) (domain.Price, error) {
	s := strings.TrimSpace(priceStr)
	if s == "" {
		return domain.Price{}, errors.New("empty price")
	}

	// 快速解析：将十进制字符串转换为 pips（1e-4）
	// 例： "0.39" => 3900, "0.3900" => 3900, "1" => 10000
	i := 0
	n := len(s)

	// integer part
	intPart := 0
	for i < n {
		c := s[i]
		if c < '0' || c > '9' {
			break
		}
		intPart = intPart*10 + int(c-'0')
		i++
	}

	frac := 0
	fracDigits := 0
	roundUp := false

	if i < n && s[i] == '.' {
		i++
		for i < n && fracDigits < 4 {
			c := s[i]
			if c < '0' || c > '9' {
				break
			}
			frac = frac*10 + int(c-'0')
			fracDigits++
			i++
		}
		// 第 5 位用于四舍五入
		if i < n {
			c := s[i]
			if c >= '0' && c <= '9' && c >= '5' {
				roundUp = true
			}
		}
	}

	// pad to 4 digits
	for fracDigits < 4 {
		frac *= 10
		fracDigits++
	}

	pips := intPart*10000 + frac
	if roundUp {
		pips++
	}
	if pips < 0 {
		return domain.Price{}, errors.New("invalid price")
	}
	return domain.Price{Pips: pips}, nil
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
