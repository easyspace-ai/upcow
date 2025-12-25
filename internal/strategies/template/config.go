package template

import "fmt"

const ID = "template"

// Config 策略配置（BBGO 风格）
//
// 设计原则：
// - 新版不再需要 enabled 字段；是否启用由 exchangeStrategies 是否包含该策略决定
// - 字段使用 camelCase 的 yaml/json tag，避免 snake_case 的历史包袱
// - 所有配置字段都应该有合理的默认值和验证逻辑
type Config struct {
	// OrderSize 订单大小（shares）
	// 注意：实际下单时会根据 minOrderSize 自动调整
	OrderSize float64 `yaml:"orderSize" json:"orderSize"`

	// 示例：可以添加更多配置字段
	// CooldownMs int `yaml:"cooldownMs" json:"cooldownMs"` // 冷却时间（毫秒）
	// MaxTradesPerCycle int `yaml:"maxTradesPerCycle" json:"maxTradesPerCycle"` // 每周期最大交易次数
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}
	if c.OrderSize <= 0 {
		return fmt.Errorf("orderSize 必须 > 0")
	}
	// 示例：可以添加更多验证逻辑
	// if c.CooldownMs < 0 {
	// 	return fmt.Errorf("cooldownMs 不能为负数")
	// }
	return nil
}
