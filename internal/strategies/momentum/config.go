package momentum

import (
	"fmt"
	"strings"
)

// MomentumStrategyConfig 动量策略配置（来自 pkg/config.MomentumConfig 的适配结果）。
type MomentumStrategyConfig struct {
	Asset          string
	SizeUSDC       float64
	ThresholdBps   int
	WindowSecs     int
	MinEdgeCents   int
	CooldownSecs   int
	UsePolygonFeed bool
}

// GetName 实现 StrategyConfig 接口
func (c *MomentumStrategyConfig) GetName() string {
	return ID
}

func (c *MomentumStrategyConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("配置为空")
	}
	if strings.TrimSpace(c.Asset) == "" {
		return fmt.Errorf("asset 不能为空")
	}
	if c.SizeUSDC <= 0 {
		return fmt.Errorf("size_usdc 必须大于 0")
	}
	if c.ThresholdBps <= 0 {
		return fmt.Errorf("threshold_bps 必须大于 0")
	}
	if c.WindowSecs <= 0 {
		return fmt.Errorf("window_secs 必须大于 0")
	}
	if c.MinEdgeCents < 0 {
		return fmt.Errorf("min_edge_cents 不能为负数")
	}
	if c.CooldownSecs < 0 {
		return fmt.Errorf("cooldown_secs 不能为负数")
	}
	return nil
}

