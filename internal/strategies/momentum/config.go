package momentum

import (
	"fmt"
	"strings"

	"github.com/betbot/gobet/internal/strategies/common"
)

// MomentumStrategyConfig 动量策略配置（来自 pkg/config.MomentumConfig 的适配结果）。
type MomentumStrategyConfig struct {
	Asset          string  `json:"asset" yaml:"asset"`
	SizeUSDC       float64 `json:"sizeUSDC" yaml:"sizeUSDC"`
	ThresholdBps   int     `json:"thresholdBps" yaml:"thresholdBps"`
	WindowSecs     int     `json:"windowSecs" yaml:"windowSecs"`
	MinEdgeCents   int     `json:"minEdgeCents" yaml:"minEdgeCents"`
	CooldownSecs   int     `json:"cooldownSecs" yaml:"cooldownSecs"`
	UsePolygonFeed bool    `json:"usePolygonFeed" yaml:"usePolygonFeed"`

	AutoMerge common.AutoMergeConfig `yaml:"autoMerge" json:"autoMerge"`
}

// GetName 实现 StrategyConfig 接口
func (c *MomentumStrategyConfig) GetName() string {
	return ID
}

func (c *MomentumStrategyConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("配置为空")
	}
	c.AutoMerge.Normalize()
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

