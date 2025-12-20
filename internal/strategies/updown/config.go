package updown

import "fmt"

const ID = "updown"

// Config is a standard strategy config:
// - implements strategies.StrategyConfig (GetName/Validate)
// - lives in internal/strategies/<your_strategy>/config.go
type Config struct {
	Enabled bool `yaml:"enabled" json:"enabled"`

	MarketSlug string  `yaml:"market" json:"market"`
	OrderSize  float64 `yaml:"order_size" json:"order_size"`

	MinOrderSize        float64 `yaml:"min_order_size" json:"min_order_size"`
	MaxBuySlippageCents int     `yaml:"max_buy_slippage_cents" json:"max_buy_slippage_cents"`
	AutoAdjustSize      bool    `yaml:"auto_adjust_size" json:"auto_adjust_size"`
	MaxSizeAdjustRatio  float64 `yaml:"max_size_adjust_ratio" json:"max_size_adjust_ratio"`
}

func (c *Config) GetName() string { return ID }

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}
	if !c.Enabled {
		return nil
	}
	if c.MarketSlug == "" {
		return fmt.Errorf("market 不能为空")
	}
	if c.OrderSize <= 0 {
		return fmt.Errorf("order_size 必须 > 0")
	}
	if c.MinOrderSize <= 0 {
		c.MinOrderSize = 1.1
	}
	if c.MaxSizeAdjustRatio < 0 {
		return fmt.Errorf("max_size_adjust_ratio 必须 >= 0")
	}
	return nil
}
