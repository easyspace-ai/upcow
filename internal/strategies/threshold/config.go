package threshold

import (
	"fmt"
)

// ThresholdStrategyConfig 价格阈值策略配置
type ThresholdStrategyConfig struct {
	BuyThreshold      float64 // 买入阈值（小数，例如 0.62）
	SellThreshold     float64 // 卖出阈值（小数，可选，如果为 0 则不卖出）
	OrderSize         float64 // 订单大小
	TokenType         string  // Token 类型：YES 或 NO，空字符串表示两者都监控
	ProfitTargetCents int     // 止盈目标（分），例如 3 表示 +3 cents
	StopLossCents     int     // 止损目标（分），例如 10 表示 -10 cents
	MaxBuySlippageCents  int  // 买入最大滑点（分），相对触发价上限（0=关闭）
	MaxSellSlippageCents int  // 卖出最大滑点（分），相对触发价下限（0=关闭）
}

// GetName 实现 StrategyConfig 接口
func (c *ThresholdStrategyConfig) GetName() string {
	return "threshold"
}

// Validate 验证配置
func (c *ThresholdStrategyConfig) Validate() error {
	if c.BuyThreshold <= 0 || c.BuyThreshold >= 1 {
		return fmt.Errorf("买入阈值必须在 0 到 1 之间")
	}
	if c.OrderSize <= 0 {
		return fmt.Errorf("订单大小必须大于 0")
	}
	if c.TokenType != "" && c.TokenType != "YES" && c.TokenType != "NO" {
		return fmt.Errorf("Token 类型必须是 YES、NO 或空字符串")
	}
	if c.ProfitTargetCents < 0 {
		return fmt.Errorf("止盈目标不能为负数")
	}
	if c.StopLossCents < 0 {
		return fmt.Errorf("止损目标不能为负数")
	}
	if c.MaxBuySlippageCents < 0 || c.MaxSellSlippageCents < 0 {
		return fmt.Errorf("滑点配置不能为负数")
	}
	return nil
}
