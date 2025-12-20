package pairlock

import (
	"fmt"

	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
)

// PairLockConfigAdapter 将通用配置适配为 PairLockStrategyConfig
type PairLockConfigAdapter struct{}

func (a *PairLockConfigAdapter) AdaptConfig(strategyConfig interface{}, proxyConfig interface{}) (interface{}, error) {
	cfg, ok := strategyConfig.(config.StrategyConfig)
	if !ok {
		return nil, fmt.Errorf("无效的策略配置类型: %T", strategyConfig)
	}

	if cfg.PairLock == nil {
		return nil, fmt.Errorf("pairlock 策略已启用但配置为空")
	}

	return &PairLockStrategyConfig{
		OrderSize:               cfg.PairLock.OrderSize,
		MinOrderSize:            cfg.PairLock.MinOrderSize,
		ProfitTargetCents:       cfg.PairLock.ProfitTargetCents,
		MaxRoundsPerPeriod:      cfg.PairLock.MaxRoundsPerPeriod,
		EnableParallel:          cfg.PairLock.EnableParallel,
		MaxConcurrentPlans:      cfg.PairLock.MaxConcurrentPlans,
		MaxTotalUnhedgedShares:  cfg.PairLock.MaxTotalUnhedgedShares,
		MaxPlanAgeSeconds:       cfg.PairLock.MaxPlanAgeSeconds,
		OnFailAction:            cfg.PairLock.OnFailAction,
		FailMaxSellSlippageCents: cfg.PairLock.FailMaxSellSlippageCents,
		FailFlattenMinShares:    cfg.PairLock.FailFlattenMinShares,
		CooldownMs:              cfg.PairLock.CooldownMs,
		MaxSupplementAttempts:   cfg.PairLock.MaxSupplementAttempts,
		EntryMaxBuySlippageCents: cfg.PairLock.EntryMaxBuySlippageCents,
	}, nil
}

var _ bbgo.ConfigAdapter = (*PairLockConfigAdapter)(nil)

