package updown

import (
	"fmt"

	"github.com/betbot/gobet/internal/strategies/common"
)

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
	// 用指针是为了支持“默认 true”同时允许用户显式关闭
	OncePerCycle *bool `yaml:"oncePerCycle" json:"oncePerCycle"`

	MinOrderSize        float64 `yaml:"minOrderSize" json:"minOrderSize"`
	MaxBuySlippageCents int     `yaml:"maxBuySlippageCents" json:"maxBuySlippageCents"`

	// MaxBuyPriceCents: 买入价硬上限（分）。默认 80（禁止一启动就 99c 追买）。
	MaxBuyPriceCents int `yaml:"maxBuyPriceCents" json:"maxBuyPriceCents"`
	// MaxSpreadCents: 盘口价差上限（ask-bid，分）。默认 5（过滤“bid=0 ask=99”这类假盘口）。
	MaxSpreadCents int `yaml:"maxSpreadCents" json:"maxSpreadCents"`
	// WarmupMs: 启动/周期切换后的预热期（毫秒），预热期内不下单，避免刚连上 WS 时的脏快照误触发。
	WarmupMs int `yaml:"warmupMs" json:"warmupMs"`

	AutoMerge common.AutoMergeConfig `yaml:"autoMerge" json:"autoMerge"`
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}
	c.AutoMerge.Normalize()
	if c.OrderSize <= 0 {
		return fmt.Errorf("orderSize 必须 > 0")
	}
	if c.MinOrderSize <= 0 {
		c.MinOrderSize = 1.1
	}
	if c.MaxBuySlippageCents < 0 {
		return fmt.Errorf("maxBuySlippageCents 不能为负数")
	}
	if c.OncePerCycle == nil {
		def := true
		c.OncePerCycle = &def
	}
	if c.MaxBuyPriceCents <= 0 {
		c.MaxBuyPriceCents = 80
	}
	if c.MaxBuyPriceCents > 99 {
		c.MaxBuyPriceCents = 99
	}
	if c.MaxSpreadCents <= 0 {
		c.MaxSpreadCents = 5
	}
	if c.WarmupMs <= 0 {
		c.WarmupMs = 1200
	}
	return nil
}
