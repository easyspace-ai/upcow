package orderlistener

import "fmt"

const ID = "orderlistener"

// Config 订单监听策略配置
type Config struct {
	// 止盈利润（分）：当监听到订单成交时，加多少分利润挂止盈单
	ProfitTargetCents int `yaml:"profitTargetCents" json:"profitTargetCents"`
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}
	if c.ProfitTargetCents <= 0 {
		c.ProfitTargetCents = 3 // 默认3分
	}
	return nil
}

