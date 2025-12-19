package arbitrage

import (
	"fmt"
	"time"

	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
)

// ArbitrageConfigAdapter 套利策略配置适配器
type ArbitrageConfigAdapter struct{}

// AdaptConfig 从通用配置适配为套利策略配置
func (a *ArbitrageConfigAdapter) AdaptConfig(strategyConfig interface{}, proxyConfig interface{}) (interface{}, error) {
	cfg, ok := strategyConfig.(config.StrategyConfig)
	if !ok {
		return nil, fmt.Errorf("无效的策略配置类型: %T", strategyConfig)
	}

	if cfg.Arbitrage == nil {
		return nil, fmt.Errorf("套利策略已启用但配置为空")
	}

	return &ArbitrageStrategyConfig{
		CycleDuration:           15 * time.Minute,
		LockStart:               time.Duration(cfg.Arbitrage.LockStartMinutes) * time.Minute,
		EarlyLockPriceThreshold: cfg.Arbitrage.EarlyLockPriceThreshold,
		TargetUpBase:            cfg.Arbitrage.TargetUpBase,
		TargetDownBase:          cfg.Arbitrage.TargetDownBase,
		BaseTarget:              cfg.Arbitrage.BaseTarget,
		BuildLotSize:            cfg.Arbitrage.BuildLotSize,
		MaxUpIncrement:          cfg.Arbitrage.MaxUpIncrement,
		MaxDownIncrement:        cfg.Arbitrage.MaxDownIncrement,
		SmallIncrement:          cfg.Arbitrage.SmallIncrement,
		MinOrderSize:            cfg.Arbitrage.MinOrderSize,
	}, nil
}

// 确保 ArbitrageConfigAdapter 实现了 ConfigAdapter 接口
var _ bbgo.ConfigAdapter = (*ArbitrageConfigAdapter)(nil)

