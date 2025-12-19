package grid

import (
	"fmt"

	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
)

// GridConfigAdapter 网格策略配置适配器
type GridConfigAdapter struct{}

// AdaptConfig 从通用配置适配为网格策略配置
func (a *GridConfigAdapter) AdaptConfig(strategyConfig interface{}, proxyConfig interface{}) (interface{}, error) {
	cfg, ok := strategyConfig.(config.StrategyConfig)
	if !ok {
		return nil, fmt.Errorf("无效的策略配置类型: %T", strategyConfig)
	}

	if cfg.Grid == nil {
		return nil, fmt.Errorf("网格策略已启用但配置为空")
	}

	return &GridStrategyConfig{
		GridLevels:         cfg.Grid.GridLevels,
		OrderSize:          cfg.Grid.OrderSize,
		MinOrderSize:       cfg.Grid.MinOrderSize,
		EnableRebuy:        cfg.Grid.EnableRebuy,
		EnableDoubleSide:   cfg.Grid.EnableDoubleSide,
		ProfitTarget:       cfg.Grid.ProfitTarget,
		MaxUnhedgedLoss:    cfg.Grid.MaxUnhedgedLoss,
		HardStopPrice:      cfg.Grid.HardStopPrice,
		ElasticStopPrice:   cfg.Grid.ElasticStopPrice,
		MaxRoundsPerPeriod: cfg.Grid.MaxRoundsPerPeriod,
	}, nil
}

// 确保 GridConfigAdapter 实现了 ConfigAdapter 接口
var _ bbgo.ConfigAdapter = (*GridConfigAdapter)(nil)

