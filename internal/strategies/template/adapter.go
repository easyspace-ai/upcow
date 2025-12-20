package template

import (
	"fmt"

	"github.com/betbot/gobet/internal/strategies/configadapter"
	"github.com/betbot/gobet/pkg/config"
)

// ConfigAdapter shows the standard way to map config.StrategyConfig -> strategy config.
// It is NOT registered by default in this template package.
type ConfigAdapter struct{}

func (a *ConfigAdapter) AdaptConfig(strategyConfig interface{}, proxyConfig interface{}) (interface{}, error) {
	return configadapter.AdaptRequired(strategyConfig, ID,
		func(sc config.StrategyConfig) *config.StrategyConfig { return &sc },
		func(sc *config.StrategyConfig) (*Config, error) {
			// NOTE: this is only a demonstration. Most strategies pick a typed sub-config.
			// If you add `Template *Config` into config.StrategyConfig, change this pick/build accordingly.
			_ = proxyConfig // if you need proxy, use configadapter.ProxyURLFromAny(proxyConfig)
			if sc == nil {
				return nil, fmt.Errorf("strategy config 不能为空")
			}
			return &Config{Enabled: false}, nil
		},
	)
}
