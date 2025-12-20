package threshold

import (
	"fmt"
)

// ThresholdStrategyConfig 价格阈值策略配置
type ThresholdStrategyConfig struct {
	BuyThreshold         float64 `json:"buyThreshold" yaml:"buyThreshold"`
	SellThreshold        float64 `json:"sellThreshold" yaml:"sellThreshold"`
	OrderSize            float64 `json:"orderSize" yaml:"orderSize"`
	TokenType            string  `json:"tokenType" yaml:"tokenType"`
	ProfitTargetCents    int     `json:"profitTargetCents" yaml:"profitTargetCents"`
	StopLossCents        int     `json:"stopLossCents" yaml:"stopLossCents"`
	MaxBuySlippageCents  int     `json:"maxBuySlippageCents" yaml:"maxBuySlippageCents"`
	MaxSellSlippageCents int     `json:"maxSellSlippageCents" yaml:"maxSellSlippageCents"`
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
