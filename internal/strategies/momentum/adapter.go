package momentum

import (
	"fmt"
	"strings"

	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
)

// MomentumConfigAdapter 动量策略配置适配器
type MomentumConfigAdapter struct{}

func (a *MomentumConfigAdapter) AdaptConfig(strategyConfig interface{}, proxyConfig interface{}) (interface{}, error) {
	cfg, ok := strategyConfig.(config.StrategyConfig)
	if !ok {
		return nil, fmt.Errorf("无效的策略配置类型: %T", strategyConfig)
	}
	if cfg.Momentum == nil {
		return nil, fmt.Errorf("动量策略已启用但配置为空")
	}
	out := &MomentumStrategyConfig{
		Asset:          strings.ToUpper(strings.TrimSpace(cfg.Momentum.Asset)),
		SizeUSDC:       cfg.Momentum.SizeUSDC,
		ThresholdBps:   cfg.Momentum.ThresholdBps,
		WindowSecs:     cfg.Momentum.WindowSecs,
		MinEdgeCents:   cfg.Momentum.MinEdgeCents,
		CooldownSecs:   cfg.Momentum.CooldownSecs,
		UsePolygonFeed: cfg.Momentum.UsePolygonFeed,
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

var _ bbgo.ConfigAdapter = (*MomentumConfigAdapter)(nil)

