package strategies

// StrategyConfig 策略配置接口（兼容旧代码）
// 各个策略配置类型应该实现此接口
type StrategyConfig interface {
	GetName() string
	Validate() error
}

