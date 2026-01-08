package velocitypairlock

import (
	"fmt"
	"time"

	"github.com/betbot/gobet/internal/common"
)

// Config 速度触发“双向限价对冲 + 成交后自动 merge 释放资金”的最小配置集。
//
// 设计目标：
// - 参数尽量少，但覆盖你描述的核心逻辑（速度触发、两边挂单、锁定利润、成交后 merge、释放后可继续开单）
// - 其余复杂能力（盘口质量、止盈止损、重下/FAK 等）作为后续可插拔模块再引入，避免一开始把策略做成“巨无霸”
type Config struct {
	Enabled bool `json:"enabled" yaml:"enabled"`

	// ===== 信号：价格速度 =====
	WindowSeconds          int     `json:"windowSeconds" yaml:"windowSeconds"`                   // 速度计算窗口（秒）
	MinMoveCents           int     `json:"minMoveCents" yaml:"minMoveCents"`                     // 窗口内最小位移（分）
	MinVelocityCentsPerSec float64 `json:"minVelocityCentsPerSec" yaml:"minVelocityCentsPerSec"` // 最小速度阈值（分/秒）
	CooldownMs             int     `json:"cooldownMs" yaml:"cooldownMs"`                         // 触发后冷却（毫秒）

	// ===== 挂单：锁定利润 =====
	// 利润（分）：两边最终成交价之和必须 <= 100 - ProfitCents
	// 示例：Up=70，则 Down=100-3-70=27（锁 3 个点利润）
	ProfitCents int `json:"profitCents" yaml:"profitCents"`

	// 订单数量（shares）
	OrderSize float64 `json:"orderSize" yaml:"orderSize"`

	// 安全边界：价格范围（分）
	MinEntryPriceCents int `json:"minEntryPriceCents" yaml:"minEntryPriceCents"`
	MaxEntryPriceCents int `json:"maxEntryPriceCents" yaml:"maxEntryPriceCents"`

	// 最小订单金额（USDC），Polymarket 通常要求 >= $1（建议 1.01 留余量）
	MinOrderUSDC float64 `json:"minOrderUSDC" yaml:"minOrderUSDC"`

	// 周期尾部保护：周期结束前 N 分钟不再开新对（避免来不及双边成交/合并）
	CycleEndProtectionMinutes int `json:"cycleEndProtectionMinutes" yaml:"cycleEndProtectionMinutes"`

	// 每周期最多开几对（0=不限制）
	MaxTradesPerCycle int `json:"maxTradesPerCycle" yaml:"maxTradesPerCycle"`

	// 成交后自动 merge complete sets（YES+NO -> USDC），用于释放资金继续开单
	AutoMerge common.AutoMergeConfig `json:"autoMerge" yaml:"autoMerge"`
}

func (c *Config) Defaults() {
	if c == nil {
		return
	}
	if c.WindowSeconds <= 0 {
		c.WindowSeconds = 10
	}
	if c.MinMoveCents <= 0 {
		c.MinMoveCents = 3
	}
	if c.MinVelocityCentsPerSec <= 0 {
		c.MinVelocityCentsPerSec = 0.3
	}
	if c.CooldownMs <= 0 {
		c.CooldownMs = 3000
	}
	if c.ProfitCents <= 0 {
		c.ProfitCents = 3
	}
	if c.OrderSize <= 0 {
		c.OrderSize = 5
	}
	if c.MinEntryPriceCents <= 0 {
		c.MinEntryPriceCents = 5
	}
	if c.MaxEntryPriceCents <= 0 {
		c.MaxEntryPriceCents = 95
	}
	if c.MinOrderUSDC <= 0 {
		c.MinOrderUSDC = 1.01
	}
	if c.CycleEndProtectionMinutes < 0 {
		c.CycleEndProtectionMinutes = 1
	}
	if c.MaxTradesPerCycle < 0 {
		c.MaxTradesPerCycle = 0
	}
	c.AutoMerge.Normalize()
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}
	if !c.Enabled {
		return nil
	}
	if c.WindowSeconds <= 0 {
		return fmt.Errorf("windowSeconds must be > 0")
	}
	if c.MinMoveCents < 0 {
		return fmt.Errorf("minMoveCents must be >= 0")
	}
	if c.MinVelocityCentsPerSec <= 0 {
		return fmt.Errorf("minVelocityCentsPerSec must be > 0")
	}
	if c.CooldownMs < 0 {
		return fmt.Errorf("cooldownMs must be >= 0")
	}
	if c.ProfitCents <= 0 || c.ProfitCents >= 100 {
		return fmt.Errorf("profitCents must be within (0,100)")
	}
	if c.OrderSize <= 0 {
		return fmt.Errorf("orderSize must be > 0")
	}
	if c.MinEntryPriceCents <= 0 || c.MinEntryPriceCents >= 100 {
		return fmt.Errorf("minEntryPriceCents must be within (0,100)")
	}
	if c.MaxEntryPriceCents <= 0 || c.MaxEntryPriceCents >= 100 {
		return fmt.Errorf("maxEntryPriceCents must be within (0,100)")
	}
	if c.MinEntryPriceCents > c.MaxEntryPriceCents {
		return fmt.Errorf("minEntryPriceCents must be <= maxEntryPriceCents")
	}
	if c.MinOrderUSDC <= 0 {
		return fmt.Errorf("minOrderUSDC must be > 0")
	}
	if c.CycleEndProtectionMinutes < 0 {
		return fmt.Errorf("cycleEndProtectionMinutes must be >= 0")
	}
	if c.MaxTradesPerCycle < 0 {
		return fmt.Errorf("maxTradesPerCycle must be >= 0")
	}
	return nil
}

func (c *Config) CooldownDuration() time.Duration {
	if c == nil || c.CooldownMs <= 0 {
		return 0
	}
	return time.Duration(c.CooldownMs) * time.Millisecond
}

