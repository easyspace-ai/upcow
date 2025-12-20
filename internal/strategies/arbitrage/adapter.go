package arbitrage

import (
	"time"

	"github.com/betbot/gobet/internal/strategies/configadapter"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
)

// ArbitrageConfigAdapter 套利策略配置适配器
type ArbitrageConfigAdapter struct{}

// AdaptConfig 从通用配置适配为套利策略配置
func (a *ArbitrageConfigAdapter) AdaptConfig(strategyConfig interface{}, proxyConfig interface{}) (interface{}, error) {
	return configadapter.AdaptRequired[config.ArbitrageConfig, ArbitrageStrategyConfig](
		strategyConfig,
		ID,
		func(cfg config.StrategyConfig) *config.ArbitrageConfig { return cfg.Arbitrage },
		func(c *config.ArbitrageConfig) (*ArbitrageStrategyConfig, error) {
			return &ArbitrageStrategyConfig{
				CycleDuration:           15 * time.Minute,
				LockStart:               time.Duration(c.LockStartMinutes) * time.Minute,
				EarlyLockPriceThreshold: c.EarlyLockPriceThreshold,
				TargetUpBase:            c.TargetUpBase,
				TargetDownBase:          c.TargetDownBase,
				BaseTarget:              c.BaseTarget,
				BuildLotSize:            c.BuildLotSize,
				MaxUpIncrement:          c.MaxUpIncrement,
				MaxDownIncrement:        c.MaxDownIncrement,
				SmallIncrement:          c.SmallIncrement,
				MinOrderSize:            c.MinOrderSize,
				MaxBuySlippageCents:     c.MaxBuySlippageCents,
			}, nil
		},
	)
}

// 确保 ArbitrageConfigAdapter 实现了 ConfigAdapter 接口
var _ bbgo.ConfigAdapter = (*ArbitrageConfigAdapter)(nil)

