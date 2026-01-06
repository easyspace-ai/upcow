package velocityfollow

import (
	"fmt"

	"github.com/betbot/gobet/internal/common"
)

const ID = "velocityfollow"

// PartialTakeProfit 分批止盈配置
type PartialTakeProfit struct {
	ProfitCents int     `yaml:"profitCents" json:"profitCents"` // 利润阈值（分）
	Fraction    float64 `yaml:"fraction" json:"fraction"`      // 卖出比例 (0,1]
}

// Config 策略配置
type Config struct {
	// ====== 交易参数 ======
	OrderSize      float64 `yaml:"orderSize" json:"orderSize"`           // Entry 订单 shares 数量
	HedgeOrderSize float64 `yaml:"hedgeOrderSize" json:"hedgeOrderSize"` // Hedge 订单 shares 数量（0=跟随 orderSize）

	// ====== 速度判定参数 ======
	WindowSeconds          int     `yaml:"windowSeconds" json:"windowSeconds"`                   // 速度计算窗口（秒）
	MinMoveCents           int     `yaml:"minMoveCents" json:"minMoveCents"`                    // 窗口内最小上行位移（分）
	MinVelocityCentsPerSec float64 `yaml:"minVelocityCentsPerSec" json:"minVelocityCentsPerSec"` // 最小速度（分/秒）
	CooldownMs             int     `yaml:"cooldownMs" json:"cooldownMs"`                        // 触发冷却（毫秒）
	WarmupMs               int     `yaml:"warmupMs" json:"warmupMs"`                              // 启动/换周期后的预热窗口（毫秒）
	MaxTradesPerCycle      int     `yaml:"maxTradesPerCycle" json:"maxTradesPerCycle"`          // 每周期最多交易次数（0=不设限）

	// ====== 速度快慢判断参数 ======
	FastVelocityThresholdCentsPerSec float64 `yaml:"fastVelocityThresholdCentsPerSec" json:"fastVelocityThresholdCentsPerSec"` // 快速速度阈值（分/秒）
	VelocityHistoryWindowSeconds      int     `yaml:"velocityHistoryWindowSeconds" json:"velocityHistoryWindowSeconds"`         // 历史速度窗口（秒）
	VelocityComparisonMultiplier      float64 `yaml:"velocityComparisonMultiplier" json:"velocityComparisonMultiplier"`       // 速度比较倍数（相对于历史平均）

	// ====== 慢速策略参数 ======
	SlowStrategyMaxSpreadCents      int     `yaml:"slowStrategyMaxSpreadCents" json:"slowStrategyMaxSpreadCents"`           // 慢速策略最大价差（分）
	SlowStrategyPriceAggressiveness float64 `yaml:"slowStrategyPriceAggressiveness" json:"slowStrategyPriceAggressiveness"` // 慢速策略价格激进程度（0-1）

	// ====== 下单安全参数 ======
	HedgeOffsetCents   int `yaml:"hedgeOffsetCents" json:"hedgeOffsetCents"`     // 对侧挂单价偏移（分）
	MinEntryPriceCents int `yaml:"minEntryPriceCents" json:"minEntryPriceCents"` // 吃单价下限（分），0=不设下限
	MaxEntryPriceCents int `yaml:"maxEntryPriceCents" json:"maxEntryPriceCents"` // 吃单价上限（分）
	MaxSpreadCents     int `yaml:"maxSpreadCents" json:"maxSpreadCents"`        // 盘口价差上限（分），0=不设限

	// ====== 订单执行模式 ======
	OrderExecutionMode        string `yaml:"orderExecutionMode" json:"orderExecutionMode"`               // "sequential" | "parallel"
	SequentialCheckIntervalMs int    `yaml:"sequentialCheckIntervalMs" json:"sequentialCheckIntervalMs"` // 检查订单状态的间隔（毫秒）
	SequentialMaxWaitMs       int    `yaml:"sequentialMaxWaitMs" json:"sequentialMaxWaitMs"`            // 最大等待时间（毫秒）

	// ====== 周期和风险控制 ======
	CycleEndProtectionMinutes int `yaml:"cycleEndProtectionMinutes" json:"cycleEndProtectionMinutes"` // 周期结束前保护时间（分钟）

	// ====== 对冲单重下机制 ======
	HedgeReorderTimeoutSeconds      int  `yaml:"hedgeReorderTimeoutSeconds" json:"hedgeReorderTimeoutSeconds"` // 对冲单重下超时时间（秒）
	HedgeTimeoutFakSeconds          int  `yaml:"hedgeTimeoutFakSeconds" json:"hedgeTimeoutFakSeconds"`         // 对冲单超时后以 FAK 吃单的时间（秒），0=禁用
	AllowNegativeProfitOnHedgeReorder bool `yaml:"allowNegativeProfitOnHedgeReorder" json:"allowNegativeProfitOnHedgeReorder"` // 是否允许负收益
	MaxNegativeProfitCents          int  `yaml:"maxNegativeProfitCents" json:"maxNegativeProfitCents"`          // 最大允许负收益（分）
	InventoryThreshold              float64 `yaml:"inventoryThreshold" json:"inventoryThreshold"`            // 库存偏斜阈值（shares）

	// ====== 智能风险管理系统 ======
	RiskManagementEnabled          bool `yaml:"riskManagementEnabled" json:"riskManagementEnabled"`                   // 是否启用风险管理系统
	RiskManagementCheckIntervalMs  int  `yaml:"riskManagementCheckIntervalMs" json:"riskManagementCheckIntervalMs"` // 风险检查间隔（毫秒）
	AggressiveHedgeTimeoutSeconds   int  `yaml:"aggressiveHedgeTimeoutSeconds" json:"aggressiveHedgeTimeoutSeconds"`  // Entry成交后，Hedge未成交超过此时间（秒）则激进对冲
	MaxAcceptableLossCents         int  `yaml:"maxAcceptableLossCents" json:"maxAcceptableLossCents"`                // 激进对冲时允许的最大亏损（分）

	// ====== 套利分析大脑模块 ======
	ArbitrageBrainEnabled              bool `yaml:"arbitrageBrainEnabled" json:"arbitrageBrainEnabled"`                         // 是否启用套利分析大脑
	ArbitrageBrainUpdateIntervalSeconds int  `yaml:"arbitrageBrainUpdateIntervalSeconds" json:"arbitrageBrainUpdateIntervalSeconds"` // 套利分析更新间隔（秒）

	// ====== Dashboard UI ======
	DashboardEnabled                      bool `yaml:"dashboardEnabled" json:"dashboardEnabled"`                                   // 是否启用Dashboard UI
	DashboardUseNativeTUI                 bool `yaml:"dashboardUseNativeTUI" json:"dashboardUseNativeTUI"`                         // 是否使用原生TUI（默认 true，使用 tcell），false 则使用 Bubble Tea
	DashboardPositionReconcileIntervalSeconds int `yaml:"dashboardPositionReconcileIntervalSeconds" json:"dashboardPositionReconcileIntervalSeconds"` // Dashboard持仓同步间隔（秒）
	DashboardRefreshIntervalMs            int  `yaml:"dashboardRefreshIntervalMs" json:"dashboardRefreshIntervalMs"`             // Dashboard UI刷新间隔（毫秒），用于实时显示价格

	// ====== 价格优先选择 ======
	PreferHigherPrice      bool `yaml:"preferHigherPrice" json:"preferHigherPrice"`           // 是否启用价格优先选择
	MinPreferredPriceCents int  `yaml:"minPreferredPriceCents" json:"minPreferredPriceCents"` // 优先价格阈值（分）

	// ====== Binance 开盘 1m K 线阴阳 bias ======
	UseBinanceOpen1mBias      bool    `yaml:"useBinanceOpen1mBias" json:"useBinanceOpen1mBias"`           // 是否启用 Binance bias
	BiasMode                  string  `yaml:"biasMode" json:"biasMode"`                                   // "hard" | "soft"
	Open1mMaxWaitSeconds      int     `yaml:"open1mMaxWaitSeconds" json:"open1mMaxWaitSeconds"`           // 等待开盘 1m K 线的最大时间（秒）
	Open1mMinBodyBps          int     `yaml:"open1mMinBodyBps" json:"open1mMinBodyBps"`                  // 实体最小阈值（bps）
	Open1mMaxWickBps          int     `yaml:"open1mMaxWickBps" json:"open1mMaxWickBps"`                    // 影线最大阈值（bps）
	RequireBiasReady          bool    `yaml:"requireBiasReady" json:"requireBiasReady"`                  // 是否必须等开盘 1m 收线再允许交易
	OppositeBiasVelocityMultiplier float64 `yaml:"oppositeBiasVelocityMultiplier" json:"oppositeBiasVelocityMultiplier"` // 速度倍数
	OppositeBiasMinMoveExtraCents   int     `yaml:"oppositeBiasMinMoveExtraCents" json:"oppositeBiasMinMoveExtraCents"`   // 额外最小位移（分）

	// ====== Binance 1s "底层硬动确认" ======
	UseBinanceMoveConfirm      bool `yaml:"useBinanceMoveConfirm" json:"useBinanceMoveConfirm"`           // 是否启用底层硬动确认
	MoveConfirmWindowSeconds   int  `yaml:"moveConfirmWindowSeconds" json:"moveConfirmWindowSeconds"`       // lookback 秒数
	MinUnderlyingMoveBps      int  `yaml:"minUnderlyingMoveBps" json:"minUnderlyingMoveBps"`             // 最小底层波动（bps）

	// ====== 市场质量过滤 ======
	EnableMarketQualityGate    bool    `yaml:"enableMarketQualityGate" json:"enableMarketQualityGate"`         // 是否启用盘口质量 gate
	MarketQualityMinScore      float64 `yaml:"marketQualityMinScore" json:"marketQualityMinScore"`           // 最小质量分（0..100）
	MarketQualityMaxSpreadCents int    `yaml:"marketQualityMaxSpreadCents" json:"marketQualityMaxSpreadCents"` // 最大一档价差（分）
	MarketQualityMaxBookAgeMs   int    `yaml:"marketQualityMaxBookAgeMs" json:"marketQualityMaxBookAgeMs"`     // WS 盘口最大年龄（毫秒）

	// ====== 出场（平仓）参数 ======
	TakeProfitCents        int  `yaml:"takeProfitCents" json:"takeProfitCents"`              // 止盈阈值（分），0=禁用
	StopLossCents          int  `yaml:"stopLossCents" json:"stopLossCents"`                  // 止损阈值（分），0=禁用
	MaxHoldSeconds         int  `yaml:"maxHoldSeconds" json:"maxHoldSeconds"`                 // 最大持仓时间（秒），0=禁用
	ExitCooldownMs         int  `yaml:"exitCooldownMs" json:"exitCooldownMs"`                // 出场冷却（毫秒）
	ExitBothSidesIfHedged  bool `yaml:"exitBothSidesIfHedged" json:"exitBothSidesIfHedged"`  // 若同周期同时持有 UP/DOWN，则同时卖出平仓

	// ====== 分批止盈 ======
	PartialTakeProfits []PartialTakeProfit `yaml:"partialTakeProfits" json:"partialTakeProfits"` // 分批止盈列表

	// ====== 追踪止盈 ======
	EnableTrailingTakeProfit bool `yaml:"enableTrailingTakeProfit" json:"enableTrailingTakeProfit"` // 是否启用追踪止盈
	TrailStartCents          int  `yaml:"trailStartCents" json:"trailStartCents"`                   // 达到该利润后开始追踪（分）
	TrailDistanceCents       int  `yaml:"trailDistanceCents" json:"trailDistanceCents"`           // 回撤触发距离（分）

	// ====== 市场指标相关配置（决策引擎）======
	PriceStabilityCheckEnabled bool    `yaml:"priceStabilityCheckEnabled" json:"priceStabilityCheckEnabled"` // 是否启用价格稳定性检查
	MaxPriceChangePercent      float64 `yaml:"maxPriceChangePercent" json:"maxPriceChangePercent"`          // 最大价格变化百分比
	PriceChangeWindowSeconds   int     `yaml:"priceChangeWindowSeconds" json:"priceChangeWindowSeconds"`     // 价格变化检查窗口（秒）
	MinLiquidityScore          float64 `yaml:"minLiquidityScore" json:"minLiquidityScore"`                  // 最小流动性评分（0-10）
	MinDepthAt1Percent         float64 `yaml:"minDepthAt1Percent" json:"minDepthAt1Percent"`                // 1% 深度最小要求（shares）
	MinTotalLiquidity          float64 `yaml:"minTotalLiquidity" json:"minTotalLiquidity"`                   // 最小总流动性（shares）
	MaxSpreadVolatilityPercent float64 `yaml:"maxSpreadVolatilityPercent" json:"maxSpreadVolatilityPercent"` // 最大价差波动百分比

	// ====== 自动合并订单 ======
	AutoMerge common.AutoMergeConfig `yaml:"autoMerge" json:"autoMerge"`
}

// Defaults 设置默认值
func (c *Config) Defaults() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}

	// 交易参数
	if c.OrderSize <= 0 {
		c.OrderSize = 1.2
	}
	if c.HedgeOrderSize <= 0 {
		c.HedgeOrderSize = 0 // 0 表示跟随 orderSize
	}

	// 速度判定参数
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
		c.FastVelocityThresholdCentsPerSec = 0.5 // 默认 0.5 分/秒
	}
	if c.VelocityHistoryWindowSeconds <= 0 {
		c.VelocityHistoryWindowSeconds = 60 // 默认 60 秒历史窗口
	}
	if c.VelocityComparisonMultiplier <= 0 {
		c.VelocityComparisonMultiplier = 1.5 // 默认 1.5 倍历史平均
	}

	// 慢速策略参数
	if c.SlowStrategyMaxSpreadCents <= 0 {
		c.SlowStrategyMaxSpreadCents = 3 // 默认 3 分
	}
	if c.SlowStrategyPriceAggressiveness <= 0 {
		c.SlowStrategyPriceAggressiveness = 0.8 // 默认 0.8（接近ask价）
	}
	if c.SlowStrategyPriceAggressiveness > 1.0 {
		c.SlowStrategyPriceAggressiveness = 1.0 // 最大不超过1.0
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

	// 周期和风险控制
	if c.CycleEndProtectionMinutes <= 0 {
		c.CycleEndProtectionMinutes = 3
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

	// Dashboard UI
	// 注意：不再强制设置 DashboardEnabled 为 true，允许用户通过配置禁用
	// 如果启用了Dashboard，默认使用原生TUI
	// 注意：由于bool零值是false，无法区分"未设置"和"明确设置为false"
	// 我们采用策略：如果DashboardEnabled为true，默认设置dashboardUseNativeTUI为true
	// 如果用户想用Bubble Tea，必须在yaml中明确设置 dashboardUseNativeTUI: false
	if c.DashboardEnabled {
		// 默认使用原生TUI（如果yaml中未设置，bool零值是false，我们需要设置为true）
		c.DashboardUseNativeTUI = true
	}
	if c.DashboardPositionReconcileIntervalSeconds <= 0 {
		c.DashboardPositionReconcileIntervalSeconds = 15
	}
	if c.DashboardRefreshIntervalMs <= 0 {
		c.DashboardRefreshIntervalMs = 100 // 默认 100ms，与 go-polymarket-watcher 一致
	}

	// 价格优先选择
	if c.MinPreferredPriceCents <= 0 {
		c.MinPreferredPriceCents = 50
	}

	// Binance bias
	if c.BiasMode == "" {
		c.BiasMode = "hard"
	}
	if c.Open1mMaxWaitSeconds <= 0 {
		c.Open1mMaxWaitSeconds = 120
	}
	if c.Open1mMinBodyBps <= 0 {
		c.Open1mMinBodyBps = 3
	}
	if c.Open1mMaxWickBps <= 0 {
		c.Open1mMaxWickBps = 25
	}
	if c.OppositeBiasVelocityMultiplier <= 0 {
		c.OppositeBiasVelocityMultiplier = 1.5
	}

	// Binance 1s 底层硬动确认
	if c.MoveConfirmWindowSeconds <= 0 {
		c.MoveConfirmWindowSeconds = 60
	}
	if c.MinUnderlyingMoveBps <= 0 {
		c.MinUnderlyingMoveBps = 20
	}

	// 市场质量过滤
	if c.MarketQualityMinScore <= 0 {
		c.MarketQualityMinScore = 70
	}
	if c.MarketQualityMaxSpreadCents <= 0 {
		c.MarketQualityMaxSpreadCents = c.MaxSpreadCents
	}
	if c.MarketQualityMaxBookAgeMs <= 0 {
		c.MarketQualityMaxBookAgeMs = 3000
	}

	// 出场参数
	if c.ExitCooldownMs <= 0 {
		c.ExitCooldownMs = 1500
	}

	// 追踪止盈
	if c.TrailStartCents <= 0 {
		c.TrailStartCents = 4
	}
	if c.TrailDistanceCents <= 0 {
		c.TrailDistanceCents = 2
	}

	// 市场指标
	if !c.PriceStabilityCheckEnabled {
		c.PriceStabilityCheckEnabled = true
	}
	if c.MaxPriceChangePercent <= 0 {
		c.MaxPriceChangePercent = 2.0
	}
	if c.PriceChangeWindowSeconds <= 0 {
		c.PriceChangeWindowSeconds = 5
	}
	if c.MinLiquidityScore <= 0 {
		c.MinLiquidityScore = 2.0
	}
	if c.MinDepthAt1Percent <= 0 {
		c.MinDepthAt1Percent = 10.0
	}
	if c.MinTotalLiquidity <= 0 {
		c.MinTotalLiquidity = 20.0
	}
	if c.MaxSpreadVolatilityPercent <= 0 {
		c.MaxSpreadVolatilityPercent = 50.0
	}

	// 自动合并
	if !c.AutoMerge.Enabled {
		// 默认启用自动合并（如果用户没有明确禁用）
		c.AutoMerge.Enabled = true
	}
	// 设置默认值
	if c.AutoMerge.MergeRatio <= 0 {
		c.AutoMerge.MergeRatio = 1.0 // 默认合并所有 complete sets
	}
	if c.AutoMerge.MinCompleteSets <= 0 {
		c.AutoMerge.MinCompleteSets = 0.1 // 默认最小 0.1 shares
	}
	if c.AutoMerge.IntervalSeconds <= 0 {
		c.AutoMerge.IntervalSeconds = 60 // 默认 60 秒间隔
	}
	c.AutoMerge.Normalize()

	return nil
}

// Validate 验证配置
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

	if c.BiasMode != "" && c.BiasMode != "hard" && c.BiasMode != "soft" {
		return fmt.Errorf("biasMode 必须是 'hard' 或 'soft'")
	}

	// 验证分批止盈配置
	for i, ptp := range c.PartialTakeProfits {
		if ptp.ProfitCents <= 0 {
			return fmt.Errorf("partialTakeProfits[%d].profitCents 必须 > 0", i)
		}
		if ptp.Fraction <= 0 || ptp.Fraction > 1.0 {
			return fmt.Errorf("partialTakeProfits[%d].fraction 必须在 (0,1] 范围内", i)
		}
	}

	return nil
}

// 实现 ConfigInterface 接口（避免循环导入）
func (c *Config) GetWindowSeconds() int { return c.WindowSeconds }
func (c *Config) GetMinMoveCents() int { return c.MinMoveCents }
func (c *Config) GetMinVelocityCentsPerSec() float64 { return c.MinVelocityCentsPerSec }
func (c *Config) GetPreferHigherPrice() bool { return c.PreferHigherPrice }
func (c *Config) GetMinPreferredPriceCents() int { return c.MinPreferredPriceCents }
func (c *Config) GetHedgeOffsetCents() int { return c.HedgeOffsetCents }
func (c *Config) GetMinEntryPriceCents() int { return c.MinEntryPriceCents }
func (c *Config) GetMaxEntryPriceCents() int { return c.MaxEntryPriceCents }
func (c *Config) GetOrderSize() float64 { return c.OrderSize }
func (c *Config) GetHedgeOrderSize() float64 { return c.HedgeOrderSize }
func (c *Config) GetArbitrageBrainEnabled() bool { return c.ArbitrageBrainEnabled }
func (c *Config) GetArbitrageBrainUpdateIntervalSeconds() int { return c.ArbitrageBrainUpdateIntervalSeconds }
func (c *Config) GetWarmupMs() int { return c.WarmupMs }
func (c *Config) GetMaxTradesPerCycle() int { return c.MaxTradesPerCycle }
func (c *Config) GetCooldownMs() int { return c.CooldownMs }

// 实现 capital.ConfigInterface
func (c *Config) GetAutoMerge() common.AutoMergeConfig { return c.AutoMerge }

// 实现 oms.ConfigInterface
func (c *Config) GetOrderExecutionMode() string { return c.OrderExecutionMode }
func (c *Config) GetSequentialCheckIntervalMs() int { return c.SequentialCheckIntervalMs }
func (c *Config) GetSequentialMaxWaitMs() int { return c.SequentialMaxWaitMs }

// RiskManager 配置方法
func (c *Config) GetRiskManagementEnabled() bool { return c.RiskManagementEnabled }
func (c *Config) GetRiskManagementCheckIntervalMs() int { return c.RiskManagementCheckIntervalMs }
func (c *Config) GetAggressiveHedgeTimeoutSeconds() int { return c.AggressiveHedgeTimeoutSeconds }
func (c *Config) GetMaxAcceptableLossCents() int { return c.MaxAcceptableLossCents }

// HedgeReorder 配置方法
func (c *Config) GetHedgeReorderTimeoutSeconds() int { return c.HedgeReorderTimeoutSeconds }
func (c *Config) GetHedgeTimeoutFakSeconds() int { return c.HedgeTimeoutFakSeconds }
func (c *Config) GetAllowNegativeProfitOnHedgeReorder() bool { return c.AllowNegativeProfitOnHedgeReorder }
func (c *Config) GetMaxNegativeProfitCents() int { return c.MaxNegativeProfitCents }

// 速度快慢判断配置方法
func (c *Config) GetFastVelocityThresholdCentsPerSec() float64 { return c.FastVelocityThresholdCentsPerSec }
func (c *Config) GetVelocityHistoryWindowSeconds() int { return c.VelocityHistoryWindowSeconds }
func (c *Config) GetVelocityComparisonMultiplier() float64 { return c.VelocityComparisonMultiplier }

// 慢速策略配置方法
func (c *Config) GetSlowStrategyMaxSpreadCents() int { return c.SlowStrategyMaxSpreadCents }
func (c *Config) GetSlowStrategyPriceAggressiveness() float64 { return c.SlowStrategyPriceAggressiveness }
