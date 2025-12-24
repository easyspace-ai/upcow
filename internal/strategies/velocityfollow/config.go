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
	OncePerCycle            bool    `yaml:"oncePerCycle" json:"oncePerCycle"`                       // 每周期最多触发一次（已废弃，使用 maxTradesPerCycle）
	WarmupMs                int     `yaml:"warmupMs" json:"warmupMs"`                               // 启动/换周期后的预热窗口（毫秒）
	MaxTradesPerCycle       int     `yaml:"maxTradesPerCycle" json:"maxTradesPerCycle"`             // 每周期最多交易次数（0=不设限）

	// 下单安全参数
	HedgeOffsetCents int `yaml:"hedgeOffsetCents" json:"hedgeOffsetCents"` // 对侧挂单 = (100 - entryAskCents - offset)
	MaxEntryPriceCents int `yaml:"maxEntryPriceCents" json:"maxEntryPriceCents"` // 吃单价上限（分），避免 99/100 假盘口
	MaxSpreadCents     int `yaml:"maxSpreadCents" json:"maxSpreadCents"`         // 盘口价差上限（分），避免极差盘口误触发

	// Binance K线融合（可选）：用“本周期开盘第 1 根 1m K 线阴阳”作为 bias/过滤器
	UseBinanceOpen1mBias bool   `yaml:"useBinanceOpen1mBias" json:"useBinanceOpen1mBias"`
	BiasMode             string `yaml:"biasMode" json:"biasMode"` // "hard" | "soft"
	Open1mMaxWaitSeconds int    `yaml:"open1mMaxWaitSeconds" json:"open1mMaxWaitSeconds"`
	Open1mMinBodyBps     int    `yaml:"open1mMinBodyBps" json:"open1mMinBodyBps"` // 实体最小阈值（bps，1bp=0.01%）
	Open1mMaxWickBps     int    `yaml:"open1mMaxWickBps" json:"open1mMaxWickBps"` // 影线最大阈值（bps）
	RequireBiasReady     bool   `yaml:"requireBiasReady" json:"requireBiasReady"` // 开启 bias 后，是否必须等开盘 1m 收线再允许交易

	// 当候选方向与开盘 1m bias 相反时，提高触发门槛（仅 BiasMode="soft" 时生效）
	OppositeBiasVelocityMultiplier float64 `yaml:"oppositeBiasVelocityMultiplier" json:"oppositeBiasVelocityMultiplier"`
	OppositeBiasMinMoveExtraCents  int     `yaml:"oppositeBiasMinMoveExtraCents" json:"oppositeBiasMinMoveExtraCents"`

	// 可选：用 Binance 1s 方向做确认（借鉴 momentum bot 的“底层硬动”过滤）
	UseBinanceMoveConfirm     bool `yaml:"useBinanceMoveConfirm" json:"useBinanceMoveConfirm"`
	MoveConfirmWindowSeconds  int  `yaml:"moveConfirmWindowSeconds" json:"moveConfirmWindowSeconds"`   // lookback 秒数
	MinUnderlyingMoveBps      int  `yaml:"minUnderlyingMoveBps" json:"minUnderlyingMoveBps"`         // 最小底层波动（bps）
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
	// maxTradesPerCycle: 0 表示不设限，>0 表示限制次数
	// 如果未设置且 oncePerCycle=true，则默认为 1（向后兼容）
	if c.MaxTradesPerCycle < 0 {
		c.MaxTradesPerCycle = 0
	}
	if c.OncePerCycle && c.MaxTradesPerCycle == 0 {
		c.MaxTradesPerCycle = 1
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

	// Binance bias defaults
	if c.BiasMode == "" {
		c.BiasMode = "hard"
	}
	if c.BiasMode != "hard" && c.BiasMode != "soft" {
		return fmt.Errorf("biasMode 必须是 hard 或 soft")
	}
	if c.Open1mMaxWaitSeconds <= 0 {
		c.Open1mMaxWaitSeconds = 120
	}
	if c.Open1mMinBodyBps <= 0 {
		c.Open1mMinBodyBps = 3 // 0.03%
	}
	if c.Open1mMaxWickBps <= 0 {
		c.Open1mMaxWickBps = 25 // 0.25%
	}
	// 默认：如果你显式开启 bias，我们就等 1m 收线再做（更贴合你说的“阴阳影响很大”）
	if c.UseBinanceOpen1mBias && !c.RequireBiasReady {
		c.RequireBiasReady = true
	}
	if c.OppositeBiasVelocityMultiplier <= 0 {
		c.OppositeBiasVelocityMultiplier = 1.5
	}
	if c.OppositeBiasMinMoveExtraCents < 0 {
		c.OppositeBiasMinMoveExtraCents = 0
	}

	// Binance move confirm defaults
	if c.MoveConfirmWindowSeconds <= 0 {
		c.MoveConfirmWindowSeconds = 60
	}
	if c.MinUnderlyingMoveBps <= 0 {
		c.MinUnderlyingMoveBps = 20 // 0.20%
	}
	return nil
}

