package velocityfollow

import "fmt"

const ID = "velocityfollow"

// Config：监控价格变化速度，触发“强势侧吃单 + 弱势侧挂互补限价单”。
//
// 例：UP 迅速拉升到 70c，触发：
// - 吃单买 UP @ 70c
// - 同时挂 DOWN 买单 @ (100-70-3)=27c
type Config struct {
	// 交易参数
	OrderSize      float64 `yaml:"orderSize" json:"orderSize"`           // 吃单买入 shares
	HedgeOrderSize float64 `yaml:"hedgeOrderSize" json:"hedgeOrderSize"` // 对侧挂单 shares（0 表示跟随 orderSize）

	// “速度快”判定参数（建议从保守开始）
	WindowSeconds           int     `yaml:"windowSeconds" json:"windowSeconds"`                     // 速度计算窗口（秒）
	MinMoveCents            int     `yaml:"minMoveCents" json:"minMoveCents"`                       // 窗口内最小上行位移（分）
	MinVelocityCentsPerSec  float64 `yaml:"minVelocityCentsPerSec" json:"minVelocityCentsPerSec"`   // 最小速度（分/秒）
	CooldownMs              int     `yaml:"cooldownMs" json:"cooldownMs"`                           // 触发冷却（毫秒）
	OncePerCycle            bool    `yaml:"oncePerCycle" json:"oncePerCycle"`                       // 每周期最多触发一次
	WarmupMs                int     `yaml:"warmupMs" json:"warmupMs"`                               // 启动/换周期后的预热窗口（毫秒）

	// 下单安全参数
	HedgeOffsetCents int `yaml:"hedgeOffsetCents" json:"hedgeOffsetCents"` // 对侧挂单 = (100 - entryAskCents - offset)
	MaxEntryPriceCents int `yaml:"maxEntryPriceCents" json:"maxEntryPriceCents"` // 吃单价上限（分），避免 99/100 假盘口
	MaxSpreadCents     int `yaml:"maxSpreadCents" json:"maxSpreadCents"`         // 盘口价差上限（分），避免极差盘口误触发
}

func (c *Config) Validate() error {
	if c.OrderSize <= 0 {
		return fmt.Errorf("orderSize 必须 > 0")
	}
	if c.WindowSeconds <= 0 {
		c.WindowSeconds = 10
	}
	if c.MinMoveCents <= 0 {
		c.MinMoveCents = 3
	}
	if c.MinVelocityCentsPerSec <= 0 {
		// 3c/10s = 0.3c/s
		c.MinVelocityCentsPerSec = float64(c.MinMoveCents) / float64(c.WindowSeconds)
	}
	if c.CooldownMs <= 0 {
		c.CooldownMs = 1500
	}
	if c.WarmupMs < 0 {
		c.WarmupMs = 0
	}
	if c.HedgeOffsetCents <= 0 {
		c.HedgeOffsetCents = 3
	}
	if c.MaxEntryPriceCents <= 0 {
		c.MaxEntryPriceCents = 95
	}
	if c.MaxSpreadCents < 0 {
		c.MaxSpreadCents = 0
	}
	if c.HedgeOrderSize < 0 {
		c.HedgeOrderSize = 0
	}
	return nil
}

