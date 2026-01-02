package ctfendgame

import (
	"fmt"
	"time"

	"github.com/betbot/gobet/internal/strategies/common"
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

	// MinStrongWeakDiffCents: 强弱差阈值（mid 的差），默认 10
	MinStrongWeakDiffCents int `yaml:"minStrongWeakDiffCents" json:"minStrongWeakDiffCents"`
	// MinStrongSideCents: 强方 mid 至少达到该值，默认 90
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

	// ===== CTF 自动编排（新周期开始立刻 split + 持仓校验）=====
	// EnableAutoSplitOnCycleStart: 新周期开始时是否自动 split 本周期代币
	EnableAutoSplitOnCycleStart bool `yaml:"enableAutoSplitOnCycleStart" json:"enableAutoSplitOnCycleStart"`
	// SplitAmount: split 的 USDC 数量（默认=orderSize）
	SplitAmount float64 `yaml:"splitAmount" json:"splitAmount"`

	// RPCURL: Polygon RPC（默认 https://polygon-rpc.com）
	RPCURL string `yaml:"rpcURL" json:"rpcURL"`
	// ChainID: 默认 137（Polygon 主网）
	ChainID int64 `yaml:"chainID" json:"chainID"`

	// HoldingsCheckOnCycleStart: 新周期开始时是否检查持仓（默认 true）
	HoldingsCheckOnCycleStart *bool `yaml:"holdingsCheckOnCycleStart" json:"holdingsCheckOnCycleStart"`
	// HoldingsExpectedMinRatio: 校验阈值（min(YES,NO) >= expected*ratio），默认 0.98
	HoldingsExpectedMinRatio float64 `yaml:"holdingsExpectedMinRatio" json:"holdingsExpectedMinRatio"`

	AutoMerge common.AutoMergeConfig `yaml:"autoMerge" json:"autoMerge"`

	// ===== 强方卖出配置（卖出弱方后立即挂强方卖单）=====
	// EnableStrongSellAfterWeak: 卖出弱方后是否立即挂强方卖单（默认 false）
	EnableStrongSellAfterWeak bool `yaml:"enableStrongSellAfterWeak" json:"enableStrongSellAfterWeak"`
	// StrongSellPrices: 强方卖出价格数组（cents），长度应与 sellSplits 一致，默认 [94, 95, 96, 97]
	StrongSellPrices []int `yaml:"strongSellPrices" json:"strongSellPrices"`
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}
	c.AutoMerge.Normalize()
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

	// ===== CTF 自动编排默认值 =====
	if c.SplitAmount <= 0 {
		c.SplitAmount = c.OrderSize
	}
	if c.RPCURL == "" {
		c.RPCURL = "https://polygon-rpc.com"
	}
	if c.ChainID <= 0 {
		c.ChainID = 137
	}
	if c.HoldingsCheckOnCycleStart == nil {
		def := true
		c.HoldingsCheckOnCycleStart = &def
	}
	if c.HoldingsExpectedMinRatio <= 0 {
		c.HoldingsExpectedMinRatio = 0.98
	}
	if c.HoldingsExpectedMinRatio > 1.0 {
		c.HoldingsExpectedMinRatio = 1.0
	}

	// ===== 强方卖出配置默认值 =====
	if len(c.StrongSellPrices) == 0 {
		c.StrongSellPrices = []int{94, 95, 96, 97}
	}
	// 验证强方卖出价格数组长度与 sellSplits 长度匹配
	if c.EnableStrongSellAfterWeak && len(c.StrongSellPrices) != len(c.SellSplits) {
		return fmt.Errorf("strongSellPrices 长度 (%d) 必须等于 sellSplits 长度 (%d)", len(c.StrongSellPrices), len(c.SellSplits))
	}

	return nil
}
