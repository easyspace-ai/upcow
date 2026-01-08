package brain

// ConfigInterface 配置接口，避免循环导入
type ConfigInterface interface {
	GetWindowSeconds() int
	GetMinMoveCents() int
	GetMinVelocityCentsPerSec() float64
	GetPreferHigherPrice() bool
	GetMinPreferredPriceCents() int
	GetHedgeOffsetCents() int
	GetMinEntryPriceCents() int
	GetMaxEntryPriceCents() int
	GetOrderSize() float64
	GetHedgeOrderSize() float64
	GetArbitrageBrainEnabled() bool
	GetArbitrageBrainUpdateIntervalSeconds() int
	GetWarmupMs() int
	GetMaxTradesPerCycle() int
	GetCooldownMs() int
	// 速度快慢判断配置
	GetFastVelocityThresholdCentsPerSec() float64
	GetVelocityHistoryWindowSeconds() int
	GetVelocityComparisonMultiplier() float64
	// 慢速策略配置
	GetSlowStrategyMaxSpreadCents() int
	GetSlowStrategyPriceAggressiveness() float64

	// ====== PositionMonitor（持仓监控/自动对冲）配置 ======
	GetPositionMonitorEnabled() bool
	GetPositionMonitorCheckIntervalMs() int
	GetPositionMonitorMaxExposureThreshold() float64
	GetPositionMonitorMaxExposureRatio() float64
	GetPositionMonitorMaxLossCents() int
	// MinHedgeSize: 小于该差异时不做“平衡性对冲”（除非触发亏损风险）。
	GetPositionMonitorMinHedgeSize() float64
	// CooldownMs: 两次自动对冲之间的最小间隔，防止抖动风暴。
	GetPositionMonitorCooldownMs() int
}

