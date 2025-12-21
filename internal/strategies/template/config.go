package template

import "fmt"

const ID = "template"

// Config is a standard strategy config (bbgo main 风格)：
// - 新版不再需要 enabled 字段；是否启用由 exchangeStrategies 是否包含该策略决定
// - 字段使用 camelCase 的 yaml/json tag，避免 snake_case 的历史包袱
type Config struct {
	OrderSize  float64 `yaml:"orderSize" json:"orderSize"`
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}
	if c.OrderSize <= 0 {
		return fmt.Errorf("orderSize 必须 > 0")
	}
	return nil
}
