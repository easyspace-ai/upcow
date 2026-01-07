package winbet

import (
	"fmt"

	"github.com/betbot/gobet/internal/common"
)

const ID = "winbet"

// Config WinBet 策略配置
// 目标：除“卖/出场”外，与 velocityfollow 功能对齐（字段口径尽量一致）。
type Config struct {
	// ====== 交易参数 ======
	OrderSize      float64 `yaml:"orderSize" json:"orderSize"`           // Entry shares 数量
	HedgeOrderSize float64 `yaml:"hedgeOrderSize" json:"hedgeOrderSize"` // Hedge shares 数量（0=跟随 orderSize）

	// ====== 速度判定参数 ======
	WindowSeconds          int     `yaml:"windowSeconds" json:"windowSeconds"`                   // 速度计算窗口（秒）
	MinMoveCents           int     `yaml:"minMoveCents" json:"minMoveCents"`                    // 窗口内最小位移（分）
	MinVelocityCentsPerSec float64 `yaml:"minVelocityCentsPerSec" json:"minVelocityCentsPerSec"` // 最小速度（分/秒）
	CooldownMs             int     `yaml:"cooldownMs" json:"cooldownMs"`                        // 触发冷却（毫秒）
	WarmupMs               int     `yaml:"warmupMs" json:"warmupMs"`                            // 周期切换后的预热（毫秒）
	MaxTradesPerCycle      int     `yaml:"maxTradesPerCycle" json:"maxTradesPerCycle"`          // 每周期最大交易次数（0=不设限）

	// ====== 速度快慢判断参数 ======
	FastVelocityThresholdCentsPerSec float64 `yaml:"fastVelocityThresholdCentsPerSec" json:"fastVelocityThresholdCentsPerSec"`
	VelocityHistoryWindowSeconds      int     `yaml:"velocityHistoryWindowSeconds" json:"velocityHistoryWindowSeconds"`
	VelocityComparisonMultiplier      float64 `yaml:"velocityComparisonMultiplier" json:"velocityComparisonMultiplier"`

	// ====== 慢速策略参数 ======
	SlowStrategyMaxSpreadCents      int     `yaml:"slowStrategyMaxSpreadCents" json:"slowStrategyMaxSpreadCents"`
	SlowStrategyPriceAggressiveness float64 `yaml:"slowStrategyPriceAggressiveness" json:"slowStrategyPriceAggressiveness"`

	// ====== 下单安全参数 ======
	HedgeOffsetCents   int `yaml:"hedgeOffsetCents" json:"hedgeOffsetCents"`
	MinEntryPriceCents int `yaml:"minEntryPriceCents" json:"minEntryPriceCents"`
	MaxEntryPriceCents int `yaml:"maxEntryPriceCents" json:"maxEntryPriceCents"`
	MaxSpreadCents     int `yaml:"maxSpreadCents" json:"maxSpreadCents"`

	// ====== 周期保护 ======
	CycleEndProtectionMinutes int `yaml:"cycleEndProtectionMinutes" json:"cycleEndProtectionMinutes"` // 周期结束前保护（分钟）

	// ====== 订单执行模式 ======
	OrderExecutionMode        string `yaml:"orderExecutionMode" json:"orderExecutionMode"`               // "sequential" | "parallel"
	SequentialCheckIntervalMs int    `yaml:"sequentialCheckIntervalMs" json:"sequentialCheckIntervalMs"` // 轮询订单状态间隔（ms）
	SequentialMaxWaitMs       int    `yaml:"sequentialMaxWaitMs" json:"sequentialMaxWaitMs"`            // Entry 最大等待（ms）

	// ====== 对冲单重下机制 ======
	HedgeReorderTimeoutSeconds        int     `yaml:"hedgeReorderTimeoutSeconds" json:"hedgeReorderTimeoutSeconds"`
	HedgeTimeoutFakSeconds            int     `yaml:"hedgeTimeoutFakSeconds" json:"hedgeTimeoutFakSeconds"`
	AllowNegativeProfitOnHedgeReorder bool    `yaml:"allowNegativeProfitOnHedgeReorder" json:"allowNegativeProfitOnHedgeReorder"`
	MaxNegativeProfitCents            int     `yaml:"maxNegativeProfitCents" json:"maxNegativeProfitCents"`
	InventoryThreshold                float64 `yaml:"inventoryThreshold" json:"inventoryThreshold"`

	// ====== 智能风险管理系统 ======
	RiskManagementEnabled         bool `yaml:"riskManagementEnabled" json:"riskManagementEnabled"`
	RiskManagementCheckIntervalMs int  `yaml:"riskManagementCheckIntervalMs" json:"riskManagementCheckIntervalMs"`
	AggressiveHedgeTimeoutSeconds int  `yaml:"aggressiveHedgeTimeoutSeconds" json:"aggressiveHedgeTimeoutSeconds"`
	MaxAcceptableLossCents        int  `yaml:"maxAcceptableLossCents" json:"maxAcceptableLossCents"`

	// ====== 套利分析大脑模块 ======
	ArbitrageBrainEnabled               bool `yaml:"arbitrageBrainEnabled" json:"arbitrageBrainEnabled"`
	ArbitrageBrainUpdateIntervalSeconds int  `yaml:"arbitrageBrainUpdateIntervalSeconds" json:"arbitrageBrainUpdateIntervalSeconds"`

	// ====== Dashboard UI ======
	DashboardEnabled                           bool `yaml:"dashboardEnabled" json:"dashboardEnabled"`
	DashboardUseNativeTUI                      bool `yaml:"dashboardUseNativeTUI" json:"dashboardUseNativeTUI"` // 默认 false（Bubble Tea）
	DashboardPositionReconcileIntervalSeconds  int  `yaml:"dashboardPositionReconcileIntervalSeconds" json:"dashboardPositionReconcileIntervalSeconds"`
	DashboardRefreshIntervalMs                 int  `yaml:"dashboardRefreshIntervalMs" json:"dashboardRefreshIntervalMs"`

	// ====== 价格优先选择 ======
	PreferHigherPrice      bool `yaml:"preferHigherPrice" json:"preferHigherPrice"`
	MinPreferredPriceCents int  `yaml:"minPreferredPriceCents" json:"minPreferredPriceCents"`

	// ====== 自动合并 complete sets ======
	AutoMerge common.AutoMergeConfig `yaml:"autoMerge" json:"autoMerge"`
}

func (c *Config) Defaults() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}

	if c.OrderSize <= 0 {
		c.OrderSize = 1.2
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

	// 速度快慢判断参数
	if c.FastVelocityThresholdCentsPerSec <= 0 {
		c.FastVelocityThresholdCentsPerSec = 0.5
	}
	if c.VelocityHistoryWindowSeconds <= 0 {
		c.VelocityHistoryWindowSeconds = 60
	}
	if c.VelocityComparisonMultiplier <= 0 {
		c.VelocityComparisonMultiplier = 1.5
	}

	// 慢速策略参数
	if c.SlowStrategyMaxSpreadCents <= 0 {
		c.SlowStrategyMaxSpreadCents = 3
	}
	if c.SlowStrategyPriceAggressiveness <= 0 {
		c.SlowStrategyPriceAggressiveness = 0.8
	}
	if c.SlowStrategyPriceAggressiveness > 1.0 {
		c.SlowStrategyPriceAggressiveness = 1.0
	}

	// 下单安全参数
	if c.HedgeOffsetCents <= 0 {
		c.HedgeOffsetCents = 1
	}
	if c.MaxEntryPriceCents <= 0 {
		c.MaxEntryPriceCents = 89
	}
	if c.MaxSpreadCents < 0 {
		c.MaxSpreadCents = 2
	}

	if c.CycleEndProtectionMinutes <= 0 {
		c.CycleEndProtectionMinutes = 3
	}

	// 订单执行模式
	if c.OrderExecutionMode == "" {
		c.OrderExecutionMode = "sequential"
	}
	if c.SequentialCheckIntervalMs <= 0 {
		c.SequentialCheckIntervalMs = 20
	}
	if c.SequentialMaxWaitMs <= 0 {
		c.SequentialMaxWaitMs = 2000
	}

	// 对冲单重下机制
	if c.HedgeReorderTimeoutSeconds <= 0 {
		c.HedgeReorderTimeoutSeconds = 15
	}
	if c.MaxNegativeProfitCents <= 0 {
		c.MaxNegativeProfitCents = 5
	}
	if !c.AllowNegativeProfitOnHedgeReorder {
		c.AllowNegativeProfitOnHedgeReorder = true
	}

	// 风险管理系统
	if !c.RiskManagementEnabled {
		c.RiskManagementEnabled = true
	}
	if c.RiskManagementCheckIntervalMs <= 0 {
		c.RiskManagementCheckIntervalMs = 5000
	}
	if c.AggressiveHedgeTimeoutSeconds <= 0 {
		c.AggressiveHedgeTimeoutSeconds = 60
	}
	if c.MaxAcceptableLossCents <= 0 {
		c.MaxAcceptableLossCents = 5
	}

	// 套利分析大脑
	if !c.ArbitrageBrainEnabled {
		c.ArbitrageBrainEnabled = true
	}
	if c.ArbitrageBrainUpdateIntervalSeconds <= 0 {
		c.ArbitrageBrainUpdateIntervalSeconds = 10
	}

	// Dashboard
	if c.DashboardEnabled {
		c.DashboardUseNativeTUI = false
	}
	if c.DashboardPositionReconcileIntervalSeconds <= 0 {
		c.DashboardPositionReconcileIntervalSeconds = 15
	}
	if c.DashboardRefreshIntervalMs <= 0 {
		c.DashboardRefreshIntervalMs = 100
	}

	// 价格优先选择
	if c.MinPreferredPriceCents <= 0 {
		c.MinPreferredPriceCents = 50
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
	if c.OrderExecutionMode != "sequential" && c.OrderExecutionMode != "parallel" {
		return fmt.Errorf("orderExecutionMode 必须是 'sequential' 或 'parallel'")
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

// ====== 实现 velocityfollow/brain.ConfigInterface ======
func (c *Config) GetWindowSeconds() int                     { return c.WindowSeconds }
func (c *Config) GetMinMoveCents() int                      { return c.MinMoveCents }
func (c *Config) GetMinVelocityCentsPerSec() float64        { return c.MinVelocityCentsPerSec }
func (c *Config) GetPreferHigherPrice() bool                { return c.PreferHigherPrice }
func (c *Config) GetMinPreferredPriceCents() int            { return c.MinPreferredPriceCents }
func (c *Config) GetHedgeOffsetCents() int                  { return c.HedgeOffsetCents }
func (c *Config) GetMinEntryPriceCents() int                { return c.MinEntryPriceCents }
func (c *Config) GetMaxEntryPriceCents() int                { return c.MaxEntryPriceCents }
func (c *Config) GetOrderSize() float64                     { return c.OrderSize }
func (c *Config) GetHedgeOrderSize() float64                { return c.HedgeOrderSize }
func (c *Config) GetArbitrageBrainEnabled() bool            { return c.ArbitrageBrainEnabled }
func (c *Config) GetArbitrageBrainUpdateIntervalSeconds() int { return c.ArbitrageBrainUpdateIntervalSeconds }
func (c *Config) GetWarmupMs() int                          { return c.WarmupMs }
func (c *Config) GetMaxTradesPerCycle() int                 { return c.MaxTradesPerCycle }
func (c *Config) GetCooldownMs() int                        { return c.CooldownMs }
func (c *Config) GetFastVelocityThresholdCentsPerSec() float64 { return c.FastVelocityThresholdCentsPerSec }
func (c *Config) GetVelocityHistoryWindowSeconds() int      { return c.VelocityHistoryWindowSeconds }
func (c *Config) GetVelocityComparisonMultiplier() float64  { return c.VelocityComparisonMultiplier }
func (c *Config) GetSlowStrategyMaxSpreadCents() int        { return c.SlowStrategyMaxSpreadCents }
func (c *Config) GetSlowStrategyPriceAggressiveness() float64 { return c.SlowStrategyPriceAggressiveness }

// ====== 实现 velocityfollow/oms.ConfigInterface ======
func (c *Config) GetOrderExecutionMode() string           { return c.OrderExecutionMode }
func (c *Config) GetSequentialCheckIntervalMs() int       { return c.SequentialCheckIntervalMs }
func (c *Config) GetSequentialMaxWaitMs() int             { return c.SequentialMaxWaitMs }
func (c *Config) GetRiskManagementEnabled() bool          { return c.RiskManagementEnabled }
func (c *Config) GetRiskManagementCheckIntervalMs() int   { return c.RiskManagementCheckIntervalMs }
func (c *Config) GetAggressiveHedgeTimeoutSeconds() int   { return c.AggressiveHedgeTimeoutSeconds }
func (c *Config) GetMaxAcceptableLossCents() int          { return c.MaxAcceptableLossCents }
func (c *Config) GetHedgeReorderTimeoutSeconds() int      { return c.HedgeReorderTimeoutSeconds }
func (c *Config) GetHedgeTimeoutFakSeconds() int          { return c.HedgeTimeoutFakSeconds }
func (c *Config) GetAllowNegativeProfitOnHedgeReorder() bool { return c.AllowNegativeProfitOnHedgeReorder }
func (c *Config) GetMaxNegativeProfitCents() int          { return c.MaxNegativeProfitCents }

// ====== 实现 velocityfollow/capital.ConfigInterface ======
func (c *Config) GetAutoMerge() common.AutoMergeConfig { return c.AutoMerge }

