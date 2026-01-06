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
}
