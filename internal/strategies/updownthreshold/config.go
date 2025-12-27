package updownthreshold

import "fmt"

const ID = "updownthreshold"

// Config 监控 UP/DOWN 两方向代币：
// - 任一方向价格从 < entryCents 跨越到 >= entryCents：买入 orderSize
// - 买入后若价格跌到 <= stopLossCents：止损卖出
type Config struct {
	// OrderSize: shares
	OrderSize float64 `yaml:"orderSize" json:"orderSize"`

	// TokenType: ""=同时监控 up/down；"up" 或 "down"=只监控单方向
	TokenType string `yaml:"tokenType" json:"tokenType"`

	// EntryCents: 突破买入阈值（分），默认 70
	EntryCents int `yaml:"entryCents" json:"entryCents"`
	// StopLossCents: 止损阈值（分），默认 30
	StopLossCents int `yaml:"stopLossCents" json:"stopLossCents"`

	// OncePerCycle: 默认 true（每个 market 周期只允许入场一次；止损不受影响）
	OncePerCycle *bool `yaml:"oncePerCycle" json:"oncePerCycle"`

	// MaxBuyPriceCents: 买入价硬上限（分），默认 99（不限制）
	MaxBuyPriceCents int `yaml:"maxBuyPriceCents" json:"maxBuyPriceCents"`
	// MaxSpreadCents: 盘口价差上限（ask-bid，分），默认 5
	MaxSpreadCents int `yaml:"maxSpreadCents" json:"maxSpreadCents"`
	// WarmupMs: 启动/周期切换后的预热期（毫秒），默认 1200
	WarmupMs int `yaml:"warmupMs" json:"warmupMs"`
	// DelayedEntryMinutes: 周期开始后延迟多少分钟才开始交易，默认 8
	// 如果设置了此值，在延迟期间后，只要价格 >= EntryCents 就买入（不需要"越过"逻辑）
	DelayedEntryMinutes int `yaml:"delayedEntryMinutes" json:"delayedEntryMinutes"`
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}
	if c.OrderSize <= 0 {
		return fmt.Errorf("orderSize 必须 > 0")
	}
	if c.TokenType != "" && c.TokenType != "up" && c.TokenType != "down" && c.TokenType != "yes" && c.TokenType != "no" {
		return fmt.Errorf("tokenType 必须是 up/down/yes/no 或空字符串")
	}
	if c.EntryCents <= 0 {
		c.EntryCents = 70
	}
	if c.EntryCents > 99 {
		c.EntryCents = 99
	}
	if c.StopLossCents <= 0 {
		c.StopLossCents = 30
	}
	if c.StopLossCents > 99 {
		c.StopLossCents = 99
	}
	if c.StopLossCents >= c.EntryCents {
		return fmt.Errorf("stopLossCents 必须 < entryCents（当前 stopLossCents=%d entryCents=%d）", c.StopLossCents, c.EntryCents)
	}
	if c.OncePerCycle == nil {
		def := true
		c.OncePerCycle = &def
	}
	if c.MaxBuyPriceCents <= 0 {
		c.MaxBuyPriceCents = 99
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
	if c.DelayedEntryMinutes <= 0 {
		c.DelayedEntryMinutes = 8 // 默认 8 分钟
	}
	return nil
}
