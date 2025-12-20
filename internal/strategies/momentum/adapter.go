package momentum

import (
	"strings"

	"github.com/betbot/gobet/internal/strategies/configadapter"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
)

// MomentumConfigAdapter 动量策略配置适配器
type MomentumConfigAdapter struct{}

func (a *MomentumConfigAdapter) AdaptConfig(strategyConfig interface{}, proxyConfig interface{}) (interface{}, error) {
	return configadapter.AdaptRequired[config.MomentumConfig, MomentumStrategyConfig](
		strategyConfig,
		ID,
		func(cfg config.StrategyConfig) *config.MomentumConfig { return cfg.Momentum },
		func(c *config.MomentumConfig) (*MomentumStrategyConfig, error) {
			out := &MomentumStrategyConfig{
				Asset:          strings.ToUpper(strings.TrimSpace(c.Asset)),
				SizeUSDC:       c.SizeUSDC,
				ThresholdBps:   c.ThresholdBps,
				WindowSecs:     c.WindowSecs,
				MinEdgeCents:   c.MinEdgeCents,
				CooldownSecs:   c.CooldownSecs,
				UsePolygonFeed: c.UsePolygonFeed,
			}
			if err := out.Validate(); err != nil {
				return nil, err
			}
			return out, nil
		},
	)
}

var _ bbgo.ConfigAdapter = (*MomentumConfigAdapter)(nil)

