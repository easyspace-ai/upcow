package velocityhedgehold

import (
	"fmt"

	"github.com/betbot/gobet/clob/types"
)

// MarketPrecisionInfo 市场精度信息（从配置文件加载）
type MarketPrecisionInfo struct {
	TickSize     string // 价格精度（如 "0.01", "0.001"）
	MinOrderSize string // 最小订单大小（如 "0.1", "5"）
	NegRisk      bool   // 是否为负风险市场
}

// ParseTickSize 解析 tick size 字符串为 TickSize 类型
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
