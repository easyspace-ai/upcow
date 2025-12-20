package threshold

import (
	"github.com/betbot/gobet/internal/strategies/configadapter"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
)

// ThresholdConfigAdapter 价格阈值策略配置适配器
type ThresholdConfigAdapter struct{}

// AdaptConfig 从通用配置适配为价格阈值策略配置
func (a *ThresholdConfigAdapter) AdaptConfig(strategyConfig interface{}, proxyConfig interface{}) (interface{}, error) {
	return configadapter.AdaptRequired[config.ThresholdConfig, ThresholdStrategyConfig](
		strategyConfig,
		ID,
		func(cfg config.StrategyConfig) *config.ThresholdConfig { return cfg.Threshold },
		func(c *config.ThresholdConfig) (*ThresholdStrategyConfig, error) {
			return &ThresholdStrategyConfig{
				BuyThreshold:         c.BuyThreshold,
				SellThreshold:        c.SellThreshold,
				OrderSize:            c.OrderSize,
				TokenType:            c.TokenType,
				ProfitTargetCents:    c.ProfitTargetCents,
				StopLossCents:        c.StopLossCents,
				MaxBuySlippageCents:  c.MaxBuySlippageCents,
				MaxSellSlippageCents: c.MaxSellSlippageCents,
			}, nil
		},
	)
}

// 确保 ThresholdConfigAdapter 实现了 ConfigAdapter 接口
var _ bbgo.ConfigAdapter = (*ThresholdConfigAdapter)(nil)

