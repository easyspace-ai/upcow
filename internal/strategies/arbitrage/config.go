package arbitrage

import (
	"fmt"
	"time"

	"github.com/betbot/gobet/internal/strategies/common"
)

// ArbitrageStrategyConfig 套利策略配置
type ArbitrageStrategyConfig struct {
	CycleDuration           common.Duration `json:"cycleDuration" yaml:"cycleDuration"` // 周期时长（例如 "15m"）
	LockStart               common.Duration `json:"lockStart" yaml:"lockStart"`         // 锁盈阶段起始时间（例如 "10m"）
	EarlyLockPriceThreshold float64         `json:"earlyLockPriceThreshold" yaml:"earlyLockPriceThreshold"`
	TargetUpBase            float64         `json:"targetUpBase" yaml:"targetUpBase"`
	TargetDownBase          float64         `json:"targetDownBase" yaml:"targetDownBase"`
	BaseTarget              float64         `json:"baseTarget" yaml:"baseTarget"`
	BuildLotSize            float64         `json:"buildLotSize" yaml:"buildLotSize"`
	MaxUpIncrement          float64         `json:"maxUpIncrement" yaml:"maxUpIncrement"`
	MaxDownIncrement        float64         `json:"maxDownIncrement" yaml:"maxDownIncrement"`
	SmallIncrement          float64         `json:"smallIncrement" yaml:"smallIncrement"`
	MinOrderSize            float64         `json:"minOrderSize" yaml:"minOrderSize"`
	MaxBuySlippageCents     int             `json:"maxBuySlippageCents" yaml:"maxBuySlippageCents"`
}

// GetName 实现 StrategyConfig 接口
func (c *ArbitrageStrategyConfig) GetName() string {
	return "arbitrage"
}

// Validate 验证配置
func (c *ArbitrageStrategyConfig) Validate() error {
	if c.CycleDuration.Duration <= 0 {
		return fmt.Errorf("周期时长必须大于0")
	}
	if c.LockStart.Duration <= 0 || c.LockStart.Duration >= c.CycleDuration.Duration {
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
	if c.MaxBuySlippageCents < 0 {
		return fmt.Errorf("滑点配置不能为负数")
	}
	return nil
}

// DefaultArbitrageStrategyConfig 返回默认配置
func DefaultArbitrageStrategyConfig() *ArbitrageStrategyConfig {
	return &ArbitrageStrategyConfig{
		CycleDuration:           common.Duration{Duration: 15 * time.Minute},
		LockStart:               common.Duration{Duration: 10 * time.Minute}, // 从12分钟提前到10分钟，与高手机器人策略一致
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
