package grid

import (
	"github.com/betbot/gobet/internal/strategies/configadapter"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
)

// GridConfigAdapter 网格策略配置适配器
type GridConfigAdapter struct{}

// AdaptConfig 从通用配置适配为网格策略配置
func (a *GridConfigAdapter) AdaptConfig(strategyConfig interface{}, proxyConfig interface{}) (interface{}, error) {
	return configadapter.AdaptRequired[config.GridConfig, GridStrategyConfig](
		strategyConfig,
		ID,
		func(cfg config.StrategyConfig) *config.GridConfig { return cfg.Grid },
		func(c *config.GridConfig) (*GridStrategyConfig, error) {
			return &GridStrategyConfig{
				GridLevels:                    c.GridLevels,
				OrderSize:                     c.OrderSize,
				MinOrderSize:                  c.MinOrderSize,
				EnableRebuy:                   c.EnableRebuy,
				EnableDoubleSide:              c.EnableDoubleSide,
				ProfitTarget:                  c.ProfitTarget,
				MaxUnhedgedLoss:               c.MaxUnhedgedLoss,
				HardStopPrice:                 c.HardStopPrice,
				ElasticStopPrice:              c.ElasticStopPrice,
				MaxRoundsPerPeriod:            c.MaxRoundsPerPeriod,
				EntryMaxBuySlippageCents:      c.EntryMaxBuySlippageCents,
				SupplementMaxBuySlippageCents: c.SupplementMaxBuySlippageCents,
			}, nil
		},
	)
}

// 确保 GridConfigAdapter 实现了 ConfigAdapter 接口
var _ bbgo.ConfigAdapter = (*GridConfigAdapter)(nil)

