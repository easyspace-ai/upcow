package orderlistener

import (
	"fmt"

	"github.com/betbot/gobet/internal/strategies/common"
)

const ID = "orderlistener"

// Config 订单监听策略配置
type Config struct {
	// 止盈利润（分）：当监听到订单成交时，加多少分利润挂止盈单
	ProfitTargetCents int `yaml:"profitTargetCents" json:"profitTargetCents"`

	AutoMerge common.AutoMergeConfig `yaml:"autoMerge" json:"autoMerge"`
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}
	c.AutoMerge.Normalize()
	if c.ProfitTargetCents <= 0 {
		c.ProfitTargetCents = 3 // 默认3分
	}
	return nil
}

