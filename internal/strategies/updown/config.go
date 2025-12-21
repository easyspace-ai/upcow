package updown

import "fmt"

const ID = "updown"

// Config is a standard strategy config (bbgo main 风格)：
// - 新版不再需要 enabled 字段；是否启用由 exchangeStrategies 是否包含该策略决定
// - 字段使用 camelCase 的 yaml/json tag，避免 snake_case 的历史包袱
type Config struct {
	// TokenType: "up" 或 "down"；为空默认 up
	TokenType string `yaml:"tokenType" json:"tokenType"`
	// OrderSize: shares
	OrderSize float64 `yaml:"orderSize" json:"orderSize"`
	// OncePerCycle: 默认 true（每个 market 周期只下 1 次单，避免信号风暴）
	OncePerCycle bool `yaml:"oncePerCycle" json:"oncePerCycle"`

	MinOrderSize        float64 `yaml:"minOrderSize" json:"minOrderSize"`
	MaxBuySlippageCents int     `yaml:"maxBuySlippageCents" json:"maxBuySlippageCents"`
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}
	if c.OrderSize <= 0 {
		return fmt.Errorf("orderSize 必须 > 0")
	}
	if c.MinOrderSize <= 0 {
		c.MinOrderSize = 1.1
	}
	if c.MaxBuySlippageCents < 0 {
		return fmt.Errorf("maxBuySlippageCents 不能为负数")
	}
	// default
	if !c.OncePerCycle {
		// allow explicitly false
	}
	return nil
}
