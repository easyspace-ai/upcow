package arbitrage

import (
	"fmt"

	"github.com/betbot/gobet/internal/strategies/common"
)

// Config（新架构简化版）：complete-set（买 YES+NO）策略配置
type Config struct {
	OrderSize        float64 `json:"orderSize" yaml:"orderSize"`
	MinOrderSize     float64 `json:"minOrderSize" yaml:"minOrderSize"`
	ProfitTargetCents int    `json:"profitTargetCents" yaml:"profitTargetCents"`
	MaxRoundsPerPeriod int   `json:"maxRoundsPerPeriod" yaml:"maxRoundsPerPeriod"`
	CooldownMs       int     `json:"cooldownMs" yaml:"cooldownMs"`

	AutoMerge common.AutoMergeConfig `yaml:"autoMerge" json:"autoMerge"`
}

func (c *Config) Validate() error {
	c.AutoMerge.Normalize()
	if c.OrderSize <= 0 {
		return fmt.Errorf("orderSize 必须 > 0")
	}
	if c.MinOrderSize <= 0 {
		c.MinOrderSize = 1.1
	}
	if c.ProfitTargetCents < 0 || c.ProfitTargetCents > 50 {
		return fmt.Errorf("profitTargetCents 建议在 [0,50] 范围内")
	}
	if c.MaxRoundsPerPeriod <= 0 {
		c.MaxRoundsPerPeriod = 1
	}
	if c.CooldownMs <= 0 {
		c.CooldownMs = 250
	}
	return nil
}
