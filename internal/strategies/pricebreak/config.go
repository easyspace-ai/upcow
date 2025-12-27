package pricebreak

import "fmt"

const ID = "pricebreak"

// Config 价格突破策略配置
type Config struct {
	// BuyThreshold 买入阈值（美分），当价格越过此值时买入
	// 默认 70，表示当价格 >= 70 美分时买入
	BuyThreshold int `yaml:"buyThreshold" json:"buyThreshold"`

	// StopLossThreshold 止损阈值（美分），当价格跌到此值时止损卖出
	// 默认 30，表示当价格 <= 30 美分时止损
	StopLossThreshold int `yaml:"stopLossThreshold" json:"stopLossThreshold"`

	// OrderSize 订单大小（shares），买入时的数量
	OrderSize float64 `yaml:"orderSize" json:"orderSize"`

	// MaxBuyPriceCents 最大买入价格（美分），超过此价格不买入
	// 默认 99，防止极端价格买入
	MaxBuyPriceCents int `yaml:"maxBuyPriceCents" json:"maxBuyPriceCents"`

	// WarmupMs 预热期（毫秒），启动后等待一段时间再开始交易
	// 默认 1200ms，避免刚启动时的脏数据
	WarmupMs int `yaml:"warmupMs" json:"warmupMs"`
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}
	if c.BuyThreshold <= 0 || c.BuyThreshold > 100 {
		return fmt.Errorf("买入阈值必须在 1-100 美分之间（例如 70 表示 70 cents = $0.70）")
	}
	if c.StopLossThreshold <= 0 || c.StopLossThreshold > 100 {
		return fmt.Errorf("止损阈值必须在 1-100 美分之间（例如 30 表示 30 cents = $0.30）")
	}
	if c.BuyThreshold <= c.StopLossThreshold {
		return fmt.Errorf("买入阈值 (%d) 必须大于止损阈值 (%d)", c.BuyThreshold, c.StopLossThreshold)
	}
	if c.OrderSize <= 0 {
		return fmt.Errorf("订单大小必须大于 0")
	}
	if c.MaxBuyPriceCents <= 0 {
		c.MaxBuyPriceCents = 99
	}
	if c.MaxBuyPriceCents > 100 {
		c.MaxBuyPriceCents = 99
	}
	if c.WarmupMs <= 0 {
		c.WarmupMs = 1200
	}
	return nil
}
