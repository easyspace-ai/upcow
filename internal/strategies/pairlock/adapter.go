package pairlock

import (
	"github.com/betbot/gobet/internal/strategies/configadapter"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
)

// PairLockConfigAdapter 将通用配置适配为 PairLockStrategyConfig
type PairLockConfigAdapter struct{}

func (a *PairLockConfigAdapter) AdaptConfig(strategyConfig interface{}, proxyConfig interface{}) (interface{}, error) {
	return configadapter.AdaptRequired[config.PairLockConfig, PairLockStrategyConfig](
		strategyConfig,
		ID,
		func(cfg config.StrategyConfig) *config.PairLockConfig { return cfg.PairLock },
		func(c *config.PairLockConfig) (*PairLockStrategyConfig, error) {
			return &PairLockStrategyConfig{
				OrderSize:                c.OrderSize,
				MinOrderSize:             c.MinOrderSize,
				ProfitTargetCents:        c.ProfitTargetCents,
				MaxRoundsPerPeriod:       c.MaxRoundsPerPeriod,
				EnableParallel:           c.EnableParallel,
				MaxConcurrentPlans:       c.MaxConcurrentPlans,
				MaxTotalUnhedgedShares:   c.MaxTotalUnhedgedShares,
				MaxPlanAgeSeconds:        c.MaxPlanAgeSeconds,
				OnFailAction:             c.OnFailAction,
				FailMaxSellSlippageCents: c.FailMaxSellSlippageCents,
				FailFlattenMinShares:     c.FailFlattenMinShares,
				CooldownMs:               c.CooldownMs,
				MaxSupplementAttempts:    c.MaxSupplementAttempts,
				EntryMaxBuySlippageCents: c.EntryMaxBuySlippageCents,
			}, nil
		},
	)
}

var _ bbgo.ConfigAdapter = (*PairLockConfigAdapter)(nil)

