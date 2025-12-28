package threshold

import (
	"fmt"

	"github.com/betbot/gobet/internal/strategies/common"
)

// ThresholdStrategyConfig 价格阈值策略配置
type ThresholdStrategyConfig struct {
	BuyThreshold         int     `json:"buyThreshold" yaml:"buyThreshold"`         // 买入阈值（美分，例如 62 表示 62 cents = $0.62）
	MaxBuyPrice          int     `json:"maxBuyPrice" yaml:"maxBuyPrice"`         // 最大买入价格（美分，例如 80 表示 80 cents，超过此价格不买入）
	SellThreshold       int     `json:"sellThreshold" yaml:"sellThreshold"`       // 卖出阈值（美分，0 表示不使用）
	OrderSize           float64 `json:"orderSize" yaml:"orderSize"`
	TokenType           string  `json:"tokenType" yaml:"tokenType"`
	ProfitTargetCents   int     `json:"profitTargetCents" yaml:"profitTargetCents"`   // 止盈目标（美分，价格差）
	StopLossCents       int     `json:"stopLossCents" yaml:"stopLossCents"`            // 止损目标（美分，价格差）
	MaxBuySlippageCents int     `json:"maxBuySlippageCents" yaml:"maxBuySlippageCents"`
	MaxSellSlippageCents int     `json:"maxSellSlippageCents" yaml:"maxSellSlippageCents"`

	AutoMerge common.AutoMergeConfig `yaml:"autoMerge" json:"autoMerge"`
}

// GetName 实现 StrategyConfig 接口
func (c *ThresholdStrategyConfig) GetName() string {
	return "threshold"
}

// Validate 验证配置
func (c *ThresholdStrategyConfig) Validate() error {
	c.AutoMerge.Normalize()
	if c.BuyThreshold <= 0 || c.BuyThreshold > 100 {
		return fmt.Errorf("买入阈值必须在 1-100 美分之间（例如 62 表示 62 cents = $0.62）")
	}
	if c.MaxBuyPrice > 0 && (c.MaxBuyPrice < c.BuyThreshold || c.MaxBuyPrice > 100) {
		return fmt.Errorf("最大买入价格必须在买入阈值到 100 美分之间（例如 80 表示 80 cents）")
	}
	if c.SellThreshold < 0 || c.SellThreshold > 100 {
		return fmt.Errorf("卖出阈值必须在 0-100 美分之间（0 表示不使用）")
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
