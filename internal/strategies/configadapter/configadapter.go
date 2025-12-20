package configadapter

import (
	"fmt"

	"github.com/betbot/gobet/pkg/config"
)

// StrategyConfigFromAny casts the generic adapter input to the concrete config.StrategyConfig.
func StrategyConfigFromAny(strategyConfig interface{}) (config.StrategyConfig, error) {
	cfg, ok := strategyConfig.(config.StrategyConfig)
	if !ok {
		return config.StrategyConfig{}, fmt.Errorf("无效的策略配置类型: %T", strategyConfig)
	}
	return cfg, nil
}

// ProxyURLFromAny converts *config.ProxyConfig to a usable http proxy URL string.
func ProxyURLFromAny(proxyConfig interface{}) string {
	if proxyConfig == nil {
		return ""
	}
	if proxy, ok := proxyConfig.(*config.ProxyConfig); ok && proxy != nil {
		if proxy.Host != "" && proxy.Port > 0 {
			return fmt.Sprintf("http://%s:%d", proxy.Host, proxy.Port)
		}
	}
	return ""
}

// AdaptRequired is a generic helper for bbgo.ConfigAdapter implementations.
//
// It enforces:
// - strategyConfig must be config.StrategyConfig
// - the sub config returned by pick must be non-nil
// - build produces the final strategy-specific config
func AdaptRequired[T any, Out any](
	strategyConfig interface{},
	strategyName string,
	pick func(config.StrategyConfig) *T,
	build func(*T) (*Out, error),
) (interface{}, error) {
	cfg, err := StrategyConfigFromAny(strategyConfig)
	if err != nil {
		return nil, err
	}

	sub := pick(cfg)
	if sub == nil {
		if strategyName == "" {
			strategyName = "unknown"
		}
		return nil, fmt.Errorf("%s 策略已启用但配置为空", strategyName)
	}

	out, err := build(sub)
	if err != nil {
		return nil, err
	}
	return out, nil
}

