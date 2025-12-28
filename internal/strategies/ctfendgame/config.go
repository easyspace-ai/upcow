package ctfendgame

import (
	"fmt"
	"time"
)

const ID = "ctfendgame"

// Config: 尾盘卖弱策略（V0）
//
// 目标：
// - 默认不交易（不确定不动，等待 merge 兜底）
// - 仅在尾盘窗口内，且强弱明确时，卖出弱方（弱方价格 5–15）
//
// 注意：domain.Market 仅提供周期开始时间戳（Timestamp），本策略通过 timeframe 计算周期结束时间。
type Config struct {
	// Timeframe: 周期长度（必须显式配置），例如 "15m"
	Timeframe string `yaml:"timeframe" json:"timeframe"`

	// OrderSize: 每次周期准备卖出的弱方总份额（shares）
	// 通常应与 split 的规模匹配（例如 split 10 USDC，则对应 shares≈10）
	OrderSize float64 `yaml:"orderSize" json:"orderSize"`

	// WarmupMs: 周期切换/启动后的预热期（毫秒）
	WarmupMs int `yaml:"warmupMs" json:"warmupMs"`

	// EndgameWindowSecs: 尾盘窗口（秒），默认 180（<=3分钟）
	EndgameWindowSecs int `yaml:"endgameWindowSecs" json:"endgameWindowSecs"`

	// UncertainMinCents/UncertainMaxCents: 摇摆区（默认 40–60），两边都在此范围则不卖
	UncertainMinCents int `yaml:"uncertainMinCents" json:"uncertainMinCents"`
	UncertainMaxCents int `yaml:"uncertainMaxCents" json:"uncertainMaxCents"`

	// WeakSellMinCents/WeakSellMaxCents: 弱方卖出价区间（默认 5–15，使用 bestBidCents 判定）
	WeakSellMinCents int `yaml:"weakSellMinCents" json:"weakSellMinCents"`
	WeakSellMaxCents int `yaml:"weakSellMaxCents" json:"weakSellMaxCents"`

	// MinStrongWeakDiffCents: 强弱差阈值（mid 的差），默认 20
	MinStrongWeakDiffCents int `yaml:"minStrongWeakDiffCents" json:"minStrongWeakDiffCents"`
	// MinStrongSideCents: 强方 mid 至少达到该值，默认 80
	MinStrongSideCents int `yaml:"minStrongSideCents" json:"minStrongSideCents"`

	// MaxSpreadCents: 盘口价差上限（ask-bid），默认 5
	MaxSpreadCents int `yaml:"maxSpreadCents" json:"maxSpreadCents"`

	// MaxSellSequencesPerCycle: 每周期最多执行几次“卖弱序列”（默认 1）
	MaxSellSequencesPerCycle int `yaml:"maxSellSequencesPerCycle" json:"maxSellSequencesPerCycle"`
	// MaxAttemptsPerCycle: 本周期最多尝试多少次（包含失败），默认 6
	MaxAttemptsPerCycle int `yaml:"maxAttemptsPerCycle" json:"maxAttemptsPerCycle"`

	// SellSplits: 分批卖弱比例（默认 [0.4,0.3,0.3]），总和应接近 1
	SellSplits []float64 `yaml:"sellSplits" json:"sellSplits"`

	// CooldownMs: 每次尝试/批次间的最小冷却（毫秒），默认 250
	CooldownMs int `yaml:"cooldownMs" json:"cooldownMs"`
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}
	if c.Timeframe == "" {
		c.Timeframe = "15m"
	}
	if _, err := time.ParseDuration(c.Timeframe); err != nil {
		return fmt.Errorf("timeframe 无效: %q: %w", c.Timeframe, err)
	}
	if c.OrderSize <= 0 {
		return fmt.Errorf("orderSize 必须 > 0")
	}
	if c.WarmupMs <= 0 {
		c.WarmupMs = 1200
	}
	if c.EndgameWindowSecs <= 0 {
		c.EndgameWindowSecs = 180
	}
	if c.UncertainMinCents <= 0 {
		c.UncertainMinCents = 40
	}
	if c.UncertainMaxCents <= 0 {
		c.UncertainMaxCents = 60
	}
	if c.UncertainMaxCents < c.UncertainMinCents {
		c.UncertainMaxCents = c.UncertainMinCents
	}
	if c.WeakSellMinCents <= 0 {
		c.WeakSellMinCents = 5
	}
	if c.WeakSellMaxCents <= 0 {
		c.WeakSellMaxCents = 15
	}
	if c.WeakSellMaxCents < c.WeakSellMinCents {
		c.WeakSellMaxCents = c.WeakSellMinCents
	}
	if c.MinStrongWeakDiffCents <= 0 {
		c.MinStrongWeakDiffCents = 20
	}
	if c.MinStrongSideCents <= 0 {
		c.MinStrongSideCents = 80
	}
	if c.MaxSpreadCents <= 0 {
		c.MaxSpreadCents = 5
	}
	if c.MaxSellSequencesPerCycle <= 0 {
		c.MaxSellSequencesPerCycle = 1
	}
	if c.MaxAttemptsPerCycle <= 0 {
		c.MaxAttemptsPerCycle = 6
	}
	if c.CooldownMs <= 0 {
		c.CooldownMs = 250
	}
	if len(c.SellSplits) == 0 {
		c.SellSplits = []float64{0.4, 0.3, 0.3}
	}
	for i, v := range c.SellSplits {
		if v <= 0 {
			return fmt.Errorf("sellSplits[%d] 必须 > 0", i)
		}
	}
	return nil
}
