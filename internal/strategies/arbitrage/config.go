package arbitrage

import (
	"fmt"
	"time"
)

// ArbitrageStrategyConfig 套利策略配置
type ArbitrageStrategyConfig struct {
	CycleDuration           time.Duration // 周期时长（默认15分钟）
	LockStart               time.Duration // 锁盈阶段起始时间（默认12分钟）
	EarlyLockPriceThreshold float64       // 提前锁盈价格阈值（默认0.85，当UP或DOWN价格达到此阈值时提前进入锁盈阶段）
	TargetUpBase            float64       // UP胜目标利润（USDC，默认100）
	TargetDownBase          float64       // DOWN胜目标利润（USDC，默认60）
	BaseTarget              float64       // 基础建仓目标持仓量（默认1500）
	BuildLotSize            float64       // 建仓阶段单次下单量（默认18）
	MaxUpIncrement          float64       // 锁盈阶段单次最大UP加仓量（默认总持仓的5%）
	MaxDownIncrement        float64       // 锁盈阶段单次最大DOWN加仓量（默认总持仓的5%）
	SmallIncrement          float64       // 反向保险小额加仓量（默认总持仓的1%）
	MinOrderSize            float64       // 最小下单金额（USDC，默认1.2，交易所要求不能小于1）
}

// GetName 实现 StrategyConfig 接口
func (c *ArbitrageStrategyConfig) GetName() string {
	return "arbitrage"
}

// Validate 验证配置
func (c *ArbitrageStrategyConfig) Validate() error {
	if c.CycleDuration <= 0 {
		return fmt.Errorf("周期时长必须大于0")
	}
	if c.LockStart <= 0 || c.LockStart >= c.CycleDuration {
		return fmt.Errorf("锁盈阶段起始时间必须在0和周期时长之间")
	}
	if c.EarlyLockPriceThreshold <= 0 || c.EarlyLockPriceThreshold >= 1 {
		return fmt.Errorf("提前锁盈价格阈值必须在0到1之间")
	}
	if c.TargetUpBase < 0 {
		return fmt.Errorf("UP胜目标利润不能为负数")
	}
	if c.TargetDownBase < 0 {
		return fmt.Errorf("DOWN胜目标利润不能为负数")
	}
	if c.BaseTarget <= 0 {
		return fmt.Errorf("基础建仓目标持仓量必须大于0")
	}
	if c.BuildLotSize <= 0 {
		return fmt.Errorf("建仓阶段单次下单量必须大于0")
	}
	if c.MinOrderSize <= 0 {
		return fmt.Errorf("最小下单规模必须大于0")
	}
	return nil
}

// DefaultArbitrageStrategyConfig 返回默认配置
func DefaultArbitrageStrategyConfig() *ArbitrageStrategyConfig {
	return &ArbitrageStrategyConfig{
		CycleDuration:           15 * time.Minute,
		LockStart:               10 * time.Minute, // 从12分钟提前到10分钟，与高手机器人策略一致
		EarlyLockPriceThreshold: 0.85,             // 当UP或DOWN价格达到0.85时提前进入锁盈
		TargetUpBase:            100.0,
		TargetDownBase:          60.0,
		BaseTarget:              1500.0,
		BuildLotSize:            18.0,
		MaxUpIncrement:          100.0, // 默认值，实际会根据总持仓动态计算
		MaxDownIncrement:        100.0, // 默认值，实际会根据总持仓动态计算
		SmallIncrement:          20.0,  // 默认值，实际会根据总持仓动态计算
		MinOrderSize:            1.2,   // 交易所要求订单金额不能小于1，设置为1.2留安全边际
	}
}
