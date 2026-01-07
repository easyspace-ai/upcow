package goodluck

import (
	"fmt"

	"github.com/betbot/gobet/internal/common"
)

const ID = "goodluck"

// Config GoodLuck 策略配置
// 基于 WinBet，支持两种模式：自动下单模式 和 手动下单模式
type Config struct {
	// ====== 下单模式选择 ======
	// ManualOrderMode: true=手动下单模式（只做对冲，不主动下单），false=自动下单模式（自动下单和对冲）
	ManualOrderMode bool `yaml:"manualOrderMode" json:"manualOrderMode"` // 是否启用手动下单模式

	// ====== 交易参数 ======
	OrderSize      float64 `yaml:"orderSize" json:"orderSize"`           // Entry shares 数量
	HedgeOrderSize float64 `yaml:"hedgeOrderSize" json:"hedgeOrderSize"` // Hedge shares 数量（0=自动跟随 orderSize）

	// ====== 速度判定参数 ======
	WindowSeconds          int     `yaml:"windowSeconds" json:"windowSeconds"`                   // 速度计算窗口（秒）
	MinMoveCents           int     `yaml:"minMoveCents" json:"minMoveCents"`                     // 窗口内最小位移（分）
	MinVelocityCentsPerSec float64 `yaml:"minVelocityCentsPerSec" json:"minVelocityCentsPerSec"` // 最小速度（分/秒）
	CooldownMs             int     `yaml:"cooldownMs" json:"cooldownMs"`                         // 触发冷却（毫秒）
	WarmupMs               int     `yaml:"warmupMs" json:"warmupMs"`                             // 周期切换后的预热（毫秒）
	MaxTradesPerCycle      int     `yaml:"maxTradesPerCycle" json:"maxTradesPerCycle"`           // 每周期最大交易次数（0=不设限）

	// ====== 速度快慢判断参数 ======
	FastVelocityThresholdCentsPerSec float64 `yaml:"fastVelocityThresholdCentsPerSec" json:"fastVelocityThresholdCentsPerSec"`
	VelocityHistoryWindowSeconds     int     `yaml:"velocityHistoryWindowSeconds" json:"velocityHistoryWindowSeconds"`
	VelocityComparisonMultiplier     float64 `yaml:"velocityComparisonMultiplier" json:"velocityComparisonMultiplier"`

	// ====== 慢速策略参数 ======
	SlowStrategyMaxSpreadCents      int     `yaml:"slowStrategyMaxSpreadCents" json:"slowStrategyMaxSpreadCents"`
	SlowStrategyPriceAggressiveness float64 `yaml:"slowStrategyPriceAggressiveness" json:"slowStrategyPriceAggressiveness"`

	// ====== 下单安全参数 ======
	HedgeOffsetCents   int     `yaml:"hedgeOffsetCents" json:"hedgeOffsetCents"`
	MinEntryPriceCents int     `yaml:"minEntryPriceCents" json:"minEntryPriceCents"`
	MaxEntryPriceCents int     `yaml:"maxEntryPriceCents" json:"maxEntryPriceCents"`
	MaxSpreadCents     int     `yaml:"maxSpreadCents" json:"maxSpreadCents"`
	MinOrderUSDC       float64 `yaml:"minOrderUSDC" json:"minOrderUSDC"` // 最小订单金额（USDC），Polymarket 要求市场买入订单至少 $1

	// ====== 周期保护 ======
	CycleEndProtectionMinutes int `yaml:"cycleEndProtectionMinutes" json:"cycleEndProtectionMinutes"` // 周期结束前保护（分钟）

	// ====== 订单执行模式 ======
	OrderExecutionMode        string `yaml:"orderExecutionMode" json:"orderExecutionMode"`               // "sequential" | "parallel"
	SequentialCheckIntervalMs int    `yaml:"sequentialCheckIntervalMs" json:"sequentialCheckIntervalMs"` // 轮询订单状态间隔（ms）
	SequentialMaxWaitMs       int    `yaml:"sequentialMaxWaitMs" json:"sequentialMaxWaitMs"`             // Entry 最大等待（ms）

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

	// ====== 价格盯盘止损（Entry 成交后盯"可锁定PnL"，不利则立即吃单锁损） ======
	PriceStopEnabled            bool `yaml:"priceStopEnabled" json:"priceStopEnabled"`
	PriceStopSoftLossCents      int  `yaml:"priceStopSoftLossCents" json:"priceStopSoftLossCents"`           // 例如 -5：触发撤单+FAK 锁损
	PriceStopHardLossCents      int  `yaml:"priceStopHardLossCents" json:"priceStopHardLossCents"`           // 例如 -10：紧急锁损（不做确认）
	PriceTakeProfitCents        int  `yaml:"priceTakeProfitCents" json:"priceTakeProfitCents"`               // 例如 +5：达到阈值时吃单锁利（0=禁用）
	PriceTakeProfitConfirmTicks int  `yaml:"priceTakeProfitConfirmTicks" json:"priceTakeProfitConfirmTicks"` // 锁利触发防抖
	PriceStopCheckIntervalMs    int  `yaml:"priceStopCheckIntervalMs" json:"priceStopCheckIntervalMs"`       // 盯盘频率
	PriceStopConfirmTicks       int  `yaml:"priceStopConfirmTicks" json:"priceStopConfirmTicks"`             // soft 触发连续命中次数（防抖）

	// ====== per-entry 执行预算 + 冷静期（防止单笔把系统拖进重下风暴） ======
	PerEntryMaxHedgeReorders int `yaml:"perEntryMaxHedgeReorders" json:"perEntryMaxHedgeReorders"`
	PerEntryMaxHedgeCancels  int `yaml:"perEntryMaxHedgeCancels" json:"perEntryMaxHedgeCancels"`
	PerEntryMaxHedgeFAK      int `yaml:"perEntryMaxHedgeFAK" json:"perEntryMaxHedgeFAK"`
	PerEntryMaxAgeSeconds    int `yaml:"perEntryMaxAgeSeconds" json:"perEntryMaxAgeSeconds"`
	PerEntryCooldownSeconds  int `yaml:"perEntryCooldownSeconds" json:"perEntryCooldownSeconds"`

	// ====== 套利分析大脑模块 ======
	ArbitrageBrainEnabled               bool `yaml:"arbitrageBrainEnabled" json:"arbitrageBrainEnabled"`
	ArbitrageBrainUpdateIntervalSeconds int  `yaml:"arbitrageBrainUpdateIntervalSeconds" json:"arbitrageBrainUpdateIntervalSeconds"`

	// ====== Dashboard UI ======
	DashboardEnabled                          bool `yaml:"dashboardEnabled" json:"dashboardEnabled"`
	DashboardUseNativeTUI                     bool `yaml:"dashboardUseNativeTUI" json:"dashboardUseNativeTUI"` // 默认 false（Bubble Tea）
	DashboardPositionReconcileIntervalSeconds int  `yaml:"dashboardPositionReconcileIntervalSeconds" json:"dashboardPositionReconcileIntervalSeconds"`
	DashboardRefreshIntervalMs                int  `yaml:"dashboardRefreshIntervalMs" json:"dashboardRefreshIntervalMs"`

	// ====== 价格优先选择 ======
	PreferHigherPrice      bool `yaml:"preferHigherPrice" json:"preferHigherPrice"`
	MinPreferredPriceCents int  `yaml:"minPreferredPriceCents" json:"minPreferredPriceCents"`

	// ====== 自动合并 complete sets ======
	AutoMerge common.AutoMergeConfig `yaml:"autoMerge" json:"autoMerge"`

	// ====== 市场质量过滤（强烈建议开启） ======
	EnableMarketQualityGate     bool    `yaml:"enableMarketQualityGate" json:"enableMarketQualityGate"`         // 是否启用盘口质量 gate
	MarketQualityMinScore       float64 `yaml:"marketQualityMinScore" json:"marketQualityMinScore"`             // 最小质量分（0..100）
	MarketQualityMaxSpreadCents int     `yaml:"marketQualityMaxSpreadCents" json:"marketQualityMaxSpreadCents"` // 最大一档价差（分）
	MarketQualityMaxBookAgeMs   int     `yaml:"marketQualityMaxBookAgeMs" json:"marketQualityMaxBookAgeMs"`     // WS 盘口最大年龄（毫秒）

	// ====== 价格稳定性过滤（强烈建议开启） ======
	PriceStabilityCheckEnabled         bool    `yaml:"priceStabilityCheckEnabled" json:"priceStabilityCheckEnabled"`                 // 是否启用价格稳定性检查
	MaxPriceChangePercent              float64 `yaml:"maxPriceChangePercent" json:"maxPriceChangePercent"`                           // 最大价格变化百分比（窗口内）
	PriceChangeWindowSeconds           int     `yaml:"priceChangeWindowSeconds" json:"priceChangeWindowSeconds"`                     // 价格变化检查窗口（秒）
	MaxSpreadVolatilityPercent         float64 `yaml:"maxSpreadVolatilityPercent" json:"maxSpreadVolatilityPercent"`                 // 最大价差波动百分比（窗口内）
	PriceStabilityMaxSpreadFilterCents int     `yaml:"priceStabilityMaxSpreadFilterCents" json:"priceStabilityMaxSpreadFilterCents"` // 数据清洗：过滤价差超过此阈值（分）的异常数据，避免错误 websocket 数据影响 maxChange 计算

	// ====== （预留）流动性阈值（后续接入更深档数据再启用） ======
	MinLiquidityScore  float64 `yaml:"minLiquidityScore" json:"minLiquidityScore"`
	MinDepthAt1Percent float64 `yaml:"minDepthAt1Percent" json:"minDepthAt1Percent"`
	MinTotalLiquidity  float64 `yaml:"minTotalLiquidity" json:"minTotalLiquidity"`
}

func (c *Config) Defaults() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}

	// ManualOrderMode 默认值：false（自动下单模式）
	// 如果设置为 true，则启用手动下单模式（只做对冲，不主动下单）
	if !c.ManualOrderMode {
		c.ManualOrderMode = false // 默认自动下单模式
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
	// 最小订单金额：默认 1.01 USDC（留一点余量，避免舍入误差）
	if c.MinOrderUSDC <= 0 {
		c.MinOrderUSDC = 1.01
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

	// 价格盯盘止损（默认开启：更像职业交易执行，先挂 hedge，若不利则立刻锁损）
	if !c.PriceStopEnabled {
		c.PriceStopEnabled = true
	}
	if c.PriceStopSoftLossCents == 0 {
		c.PriceStopSoftLossCents = -5
	}
	if c.PriceStopHardLossCents == 0 {
		c.PriceStopHardLossCents = -10
	}
	// 默认开启"达到 +5 立即吃单锁利"，符合"多做对冲单/提高周转"的目标。
	if c.PriceTakeProfitCents == 0 {
		c.PriceTakeProfitCents = 5
	}
	if c.PriceTakeProfitConfirmTicks <= 0 {
		c.PriceTakeProfitConfirmTicks = 2
	}
	// 事件驱动默认不节流（0=每次 WS 价格变化都评估）；若要限频可显式配置 >0
	if c.PriceStopCheckIntervalMs < 0 {
		c.PriceStopCheckIntervalMs = 0
	}
	if c.PriceStopConfirmTicks <= 0 {
		c.PriceStopConfirmTicks = 2
	}

	// per-entry 执行预算 + 冷静期
	if c.PerEntryMaxHedgeReorders <= 0 {
		c.PerEntryMaxHedgeReorders = 3
	}
	if c.PerEntryMaxHedgeCancels <= 0 {
		c.PerEntryMaxHedgeCancels = 6
	}
	if c.PerEntryMaxHedgeFAK <= 0 {
		c.PerEntryMaxHedgeFAK = 1
	}
	if c.PerEntryMaxAgeSeconds <= 0 {
		c.PerEntryMaxAgeSeconds = 120
	}
	if c.PerEntryCooldownSeconds <= 0 {
		c.PerEntryCooldownSeconds = 30
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

	// 市场质量过滤
	// 默认启用（更像交易员策略：宁可不做，也别在脏盘口里做）
	if !c.EnableMarketQualityGate {
		c.EnableMarketQualityGate = true
	}
	if c.MarketQualityMinScore <= 0 {
		c.MarketQualityMinScore = 70
	}
	if c.MarketQualityMaxSpreadCents <= 0 {
		// 与 MaxSpreadCents 对齐：若用户设置了更严格的 MaxSpreadCents，则沿用
		if c.MaxSpreadCents > 0 {
			c.MarketQualityMaxSpreadCents = c.MaxSpreadCents
		} else {
			c.MarketQualityMaxSpreadCents = 2
		}
	}
	if c.MarketQualityMaxBookAgeMs <= 0 {
		c.MarketQualityMaxBookAgeMs = 3000
	}

	// 价格稳定性过滤
	if !c.PriceStabilityCheckEnabled {
		c.PriceStabilityCheckEnabled = true
	}
	if c.MaxPriceChangePercent <= 0 {
		c.MaxPriceChangePercent = 2.0
	}
	if c.PriceChangeWindowSeconds <= 0 {
		c.PriceChangeWindowSeconds = 5
	}
	if c.MaxSpreadVolatilityPercent <= 0 {
		c.MaxSpreadVolatilityPercent = 50.0
	}
	// 数据清洗：过滤异常价差数据（默认 15 分，用户观察正常情况下不会大于 10）
	if c.PriceStabilityMaxSpreadFilterCents <= 0 {
		c.PriceStabilityMaxSpreadFilterCents = 15
	}
	// 预留：流动性阈值（暂不启用计算，先保留字段与默认）
	if c.MinLiquidityScore <= 0 {
		c.MinLiquidityScore = 2.0
	}
	if c.MinDepthAt1Percent <= 0 {
		c.MinDepthAt1Percent = 10.0
	}
	if c.MinTotalLiquidity <= 0 {
		c.MinTotalLiquidity = 20.0
	}
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
	if c.MarketQualityMinScore < 0 || c.MarketQualityMinScore > 100 {
		return fmt.Errorf("marketQualityMinScore 必须在 [0,100] 范围内")
	}
	if c.MarketQualityMaxSpreadCents < 0 {
		return fmt.Errorf("marketQualityMaxSpreadCents 不合法")
	}
	if c.MarketQualityMaxBookAgeMs < 0 {
		return fmt.Errorf("marketQualityMaxBookAgeMs 不合法")
	}
	if c.MaxPriceChangePercent < 0 {
		return fmt.Errorf("maxPriceChangePercent 不合法")
	}
	if c.PriceChangeWindowSeconds < 0 {
		return fmt.Errorf("priceChangeWindowSeconds 不合法")
	}
	if c.MaxSpreadVolatilityPercent < 0 {
		return fmt.Errorf("maxSpreadVolatilityPercent 不合法")
	}
	if c.PriceStopCheckIntervalMs < 0 {
		return fmt.Errorf("priceStopCheckIntervalMs 不合法")
	}
	if c.PriceStopConfirmTicks < 0 {
		return fmt.Errorf("priceStopConfirmTicks 不合法")
	}
	if c.PriceTakeProfitConfirmTicks < 0 {
		return fmt.Errorf("priceTakeProfitConfirmTicks 不合法")
	}
	if c.PerEntryMaxHedgeReorders < 0 {
		return fmt.Errorf("perEntryMaxHedgeReorders 不合法")
	}
	if c.PerEntryMaxHedgeCancels < 0 {
		return fmt.Errorf("perEntryMaxHedgeCancels 不合法")
	}
	if c.PerEntryMaxHedgeFAK < 0 {
		return fmt.Errorf("perEntryMaxHedgeFAK 不合法")
	}
	if c.PerEntryMaxAgeSeconds < 0 {
		return fmt.Errorf("perEntryMaxAgeSeconds 不合法")
	}
	if c.PerEntryCooldownSeconds < 0 {
		return fmt.Errorf("perEntryCooldownSeconds 不合法")
	}
	c.AutoMerge.Normalize()
	return nil
}

// ====== 实现 velocityfollow/brain.ConfigInterface ======
func (c *Config) GetWindowSeconds() int              { return c.WindowSeconds }
func (c *Config) GetMinMoveCents() int               { return c.MinMoveCents }
func (c *Config) GetMinVelocityCentsPerSec() float64 { return c.MinVelocityCentsPerSec }
func (c *Config) GetPreferHigherPrice() bool         { return c.PreferHigherPrice }
func (c *Config) GetMinPreferredPriceCents() int     { return c.MinPreferredPriceCents }
func (c *Config) GetHedgeOffsetCents() int           { return c.HedgeOffsetCents }
func (c *Config) GetMinEntryPriceCents() int         { return c.MinEntryPriceCents }
func (c *Config) GetMaxEntryPriceCents() int         { return c.MaxEntryPriceCents }
func (c *Config) GetOrderSize() float64              { return c.OrderSize }
func (c *Config) GetHedgeOrderSize() float64         { return c.HedgeOrderSize }
func (c *Config) GetArbitrageBrainEnabled() bool     { return c.ArbitrageBrainEnabled }
func (c *Config) GetArbitrageBrainUpdateIntervalSeconds() int {
	return c.ArbitrageBrainUpdateIntervalSeconds
}
func (c *Config) GetWarmupMs() int          { return c.WarmupMs }
func (c *Config) GetMaxTradesPerCycle() int { return c.MaxTradesPerCycle }
func (c *Config) GetCooldownMs() int        { return c.CooldownMs }
func (c *Config) GetFastVelocityThresholdCentsPerSec() float64 {
	return c.FastVelocityThresholdCentsPerSec
}
func (c *Config) GetVelocityHistoryWindowSeconds() int     { return c.VelocityHistoryWindowSeconds }
func (c *Config) GetVelocityComparisonMultiplier() float64 { return c.VelocityComparisonMultiplier }
func (c *Config) GetSlowStrategyMaxSpreadCents() int       { return c.SlowStrategyMaxSpreadCents }
func (c *Config) GetSlowStrategyPriceAggressiveness() float64 {
	return c.SlowStrategyPriceAggressiveness
}

// ====== 实现 velocityfollow/oms.ConfigInterface ======
func (c *Config) GetOrderExecutionMode() string         { return c.OrderExecutionMode }
func (c *Config) GetSequentialCheckIntervalMs() int     { return c.SequentialCheckIntervalMs }
func (c *Config) GetSequentialMaxWaitMs() int           { return c.SequentialMaxWaitMs }
func (c *Config) GetRiskManagementEnabled() bool        { return c.RiskManagementEnabled }
func (c *Config) GetRiskManagementCheckIntervalMs() int { return c.RiskManagementCheckIntervalMs }
func (c *Config) GetAggressiveHedgeTimeoutSeconds() int { return c.AggressiveHedgeTimeoutSeconds }
func (c *Config) GetMaxAcceptableLossCents() int        { return c.MaxAcceptableLossCents }
func (c *Config) GetHedgeReorderTimeoutSeconds() int    { return c.HedgeReorderTimeoutSeconds }
func (c *Config) GetHedgeTimeoutFakSeconds() int        { return c.HedgeTimeoutFakSeconds }
func (c *Config) GetAllowNegativeProfitOnHedgeReorder() bool {
	return c.AllowNegativeProfitOnHedgeReorder
}
func (c *Config) GetMaxNegativeProfitCents() int { return c.MaxNegativeProfitCents }
func (c *Config) GetMinOrderUSDC() float64      { return c.MinOrderUSDC }

// ====== 价格盯盘止损（strategycore/oms 可选读取） ======
func (c *Config) GetPriceStopEnabled() bool           { return c.PriceStopEnabled }
func (c *Config) GetPriceStopSoftLossCents() int      { return c.PriceStopSoftLossCents }
func (c *Config) GetPriceStopHardLossCents() int      { return c.PriceStopHardLossCents }
func (c *Config) GetPriceTakeProfitCents() int        { return c.PriceTakeProfitCents }
func (c *Config) GetPriceTakeProfitConfirmTicks() int { return c.PriceTakeProfitConfirmTicks }
func (c *Config) GetPriceStopCheckIntervalMs() int    { return c.PriceStopCheckIntervalMs }
func (c *Config) GetPriceStopConfirmTicks() int       { return c.PriceStopConfirmTicks }

// ====== per-entry 执行预算 + 冷静期（strategycore/oms 可选读取） ======
func (c *Config) GetPerEntryMaxHedgeReorders() int { return c.PerEntryMaxHedgeReorders }
func (c *Config) GetPerEntryMaxHedgeCancels() int  { return c.PerEntryMaxHedgeCancels }
func (c *Config) GetPerEntryMaxHedgeFAK() int      { return c.PerEntryMaxHedgeFAK }
func (c *Config) GetPerEntryMaxAgeSeconds() int    { return c.PerEntryMaxAgeSeconds }
func (c *Config) GetPerEntryCooldownSeconds() int  { return c.PerEntryCooldownSeconds }

// ====== 实现 velocityfollow/capital.ConfigInterface ======
func (c *Config) GetAutoMerge() common.AutoMergeConfig { return c.AutoMerge }

// ====== goodluck/gates 配置 getter ======
func (c *Config) GetEnableMarketQualityGate() bool       { return c.EnableMarketQualityGate }
func (c *Config) GetMarketQualityMinScore() float64      { return c.MarketQualityMinScore }
func (c *Config) GetMarketQualityMaxSpreadCents() int    { return c.MarketQualityMaxSpreadCents }
func (c *Config) GetMarketQualityMaxBookAgeMs() int      { return c.MarketQualityMaxBookAgeMs }
func (c *Config) GetPriceStabilityCheckEnabled() bool    { return c.PriceStabilityCheckEnabled }
func (c *Config) GetMaxPriceChangePercent() float64      { return c.MaxPriceChangePercent }
func (c *Config) GetPriceChangeWindowSeconds() int       { return c.PriceChangeWindowSeconds }
func (c *Config) GetMaxSpreadVolatilityPercent() float64 { return c.MaxSpreadVolatilityPercent }
func (c *Config) GetPriceStabilityMaxSpreadFilterCents() int {
	return c.PriceStabilityMaxSpreadFilterCents
}
