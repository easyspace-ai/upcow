package winbet

import (
	"fmt"

	"github.com/betbot/gobet/internal/common"
)

const ID = "winbet"

// Config WinBet 策略配置（全新模块化实现）
type Config struct {
	// ====== 基础下单参数 ======
	OrderSize      float64 `yaml:"orderSize" json:"orderSize"`           // Entry shares 数量
	HedgeOrderSize float64 `yaml:"hedgeOrderSize" json:"hedgeOrderSize"` // Hedge shares 数量（0=跟随 orderSize）

	// ====== 信号（动量/速度） ======
	WindowSeconds          int     `yaml:"windowSeconds" json:"windowSeconds"`                   // 速度窗口（秒）
	MinMoveCents           int     `yaml:"minMoveCents" json:"minMoveCents"`                    // 窗口内最小位移（分）
	MinVelocityCentsPerSec float64 `yaml:"minVelocityCentsPerSec" json:"minVelocityCentsPerSec"` // 最小速度（分/秒）

	// ====== 交易节律 ======
	CooldownMs        int `yaml:"cooldownMs" json:"cooldownMs"`               // 触发冷却（毫秒）
	WarmupMs          int `yaml:"warmupMs" json:"warmupMs"`                   // 周期切换后的预热（毫秒）
	MaxTradesPerCycle int `yaml:"maxTradesPerCycle" json:"maxTradesPerCycle"` // 每周期最大交易次数（0=不设限）

	// ====== 盘口/价格过滤 ======
	MinEntryPriceCents int `yaml:"minEntryPriceCents" json:"minEntryPriceCents"` // Entry 价格下限（分），0=不设下限
	MaxEntryPriceCents int `yaml:"maxEntryPriceCents" json:"maxEntryPriceCents"` // Entry 价格上限（分）
	MaxSpreadCents     int `yaml:"maxSpreadCents" json:"maxSpreadCents"`         // 一档价差上限（分），0=不设限

	// ====== 对冲定价 ======
	HedgeOffsetCents int `yaml:"hedgeOffsetCents" json:"hedgeOffsetCents"` // Hedge 价格偏移（分）

	// ====== 周期保护 ======
	CycleEndProtectionMinutes int `yaml:"cycleEndProtectionMinutes" json:"cycleEndProtectionMinutes"` // 周期结束前保护（分钟）

	// ====== 订单执行（顺序） ======
	EntryFillMaxWaitMs       int `yaml:"entryFillMaxWaitMs" json:"entryFillMaxWaitMs"`             // Entry 成交最大等待（毫秒）
	EntryFillCheckIntervalMs int `yaml:"entryFillCheckIntervalMs" json:"entryFillCheckIntervalMs"` // Entry 成交检查间隔（毫秒）

	// ====== 风控：未对冲敞口管理 ======
	RiskCheckIntervalMs          int  `yaml:"riskCheckIntervalMs" json:"riskCheckIntervalMs"`                   // 风控检查间隔（毫秒）
	HedgeReorderTimeoutSeconds   int  `yaml:"hedgeReorderTimeoutSeconds" json:"hedgeReorderTimeoutSeconds"`     // Hedge 未成交多久触发调价（秒）
	AggressiveHedgeTimeoutSeconds int `yaml:"aggressiveHedgeTimeoutSeconds" json:"aggressiveHedgeTimeoutSeconds"` // Hedge 未成交多久触发激进对冲（秒）

	AllowNegativeProfitOnHedge bool `yaml:"allowNegativeProfitOnHedge" json:"allowNegativeProfitOnHedge"` // 是否允许负收益（总成本>100c）
	MaxNegativeProfitCents     int  `yaml:"maxNegativeProfitCents" json:"maxNegativeProfitCents"`         // 最大允许负收益（分）

	// ====== Dashboard UI ======
	DashboardEnabled       bool `yaml:"dashboardEnabled" json:"dashboardEnabled"`
	DashboardUseNativeTUI  bool `yaml:"dashboardUseNativeTUI" json:"dashboardUseNativeTUI"` // 默认 false（Bubble Tea）
	DashboardRefreshMs     int  `yaml:"dashboardRefreshMs" json:"dashboardRefreshMs"`

	// ====== 自动合并 complete sets ======
	AutoMerge common.AutoMergeConfig `yaml:"autoMerge" json:"autoMerge"`
}

func (c *Config) Defaults() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}

	if c.OrderSize <= 0 {
		c.OrderSize = 1.0
	}
	if c.HedgeOrderSize < 0 {
		c.HedgeOrderSize = 0
	}

	if c.WindowSeconds <= 0 {
		c.WindowSeconds = 10
	}
	if c.MinMoveCents <= 0 {
		c.MinMoveCents = 3
	}
	if c.MinVelocityCentsPerSec <= 0 {
		c.MinVelocityCentsPerSec = float64(c.MinMoveCents) / float64(c.WindowSeconds)
	}

	if c.CooldownMs <= 0 {
		c.CooldownMs = 1500
	}
	if c.WarmupMs < 0 {
		c.WarmupMs = 800
	}
	if c.MaxTradesPerCycle < 0 {
		c.MaxTradesPerCycle = 6
	}

	if c.MaxEntryPriceCents <= 0 {
		c.MaxEntryPriceCents = 89
	}
	if c.MaxSpreadCents < 0 {
		c.MaxSpreadCents = 2
	}
	if c.HedgeOffsetCents <= 0 {
		c.HedgeOffsetCents = 1
	}

	if c.CycleEndProtectionMinutes <= 0 {
		c.CycleEndProtectionMinutes = 3
	}

	if c.EntryFillMaxWaitMs <= 0 {
		c.EntryFillMaxWaitMs = 2000
	}
	if c.EntryFillCheckIntervalMs <= 0 {
		c.EntryFillCheckIntervalMs = 20
	}

	if c.RiskCheckIntervalMs <= 0 {
		c.RiskCheckIntervalMs = 1000
	}
	if c.HedgeReorderTimeoutSeconds <= 0 {
		c.HedgeReorderTimeoutSeconds = 15
	}
	if c.AggressiveHedgeTimeoutSeconds <= 0 {
		c.AggressiveHedgeTimeoutSeconds = 60
	}
	if c.MaxNegativeProfitCents <= 0 {
		c.MaxNegativeProfitCents = 5
	}

	// 默认使用 Bubble Tea（更稳定）
	if c.DashboardEnabled {
		c.DashboardUseNativeTUI = false
	}
	if c.DashboardRefreshMs <= 0 {
		c.DashboardRefreshMs = 200
	}

	if !c.AutoMerge.Enabled {
		c.AutoMerge.Enabled = true
	}
	c.AutoMerge.Normalize()
	return nil
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}
	if c.OrderSize <= 0 {
		return fmt.Errorf("orderSize 必须 > 0")
	}
	if c.WindowSeconds <= 0 {
		return fmt.Errorf("windowSeconds 必须 > 0")
	}
	if c.MinMoveCents <= 0 {
		return fmt.Errorf("minMoveCents 必须 > 0")
	}
	if c.MinVelocityCentsPerSec <= 0 {
		return fmt.Errorf("minVelocityCentsPerSec 必须 > 0")
	}
	if c.MaxEntryPriceCents <= 0 || c.MaxEntryPriceCents >= 100 {
		return fmt.Errorf("maxEntryPriceCents 必须在 (0,100) 范围内")
	}
	if c.HedgeOffsetCents < 0 || c.HedgeOffsetCents >= 100 {
		return fmt.Errorf("hedgeOffsetCents 不合法")
	}
	c.AutoMerge.Normalize()
	return nil
}

