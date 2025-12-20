package pairedtrading

import (
	"fmt"
	"time"

	"github.com/betbot/gobet/internal/strategies/common"
)

// PairedTradingConfig 成对交易策略配置
type PairedTradingConfig struct {
	// 阶段控制参数
	BuildDuration     common.Duration `json:"buildDuration" yaml:"buildDuration"`   // 例如 "5m"
	LockStart         common.Duration `json:"lockStart" yaml:"lockStart"`           // 例如 "5m"
	AmplifyStart      common.Duration `json:"amplifyStart" yaml:"amplifyStart"`     // 例如 "10m"
	CycleDuration     common.Duration `json:"cycleDuration" yaml:"cycleDuration"`   // 例如 "15m"
	EarlyLockPrice    float64         `json:"earlyLockPrice" yaml:"earlyLockPrice"` // 提前进入锁定阶段的价格阈值
	EarlyAmplifyPrice float64         `json:"earlyAmplifyPrice" yaml:"earlyAmplifyPrice"` // 提前进入放大阶段的价格阈值

	// 建仓参数
	BaseTarget     float64 `json:"baseTarget" yaml:"baseTarget"`     // 基础建仓目标（shares）
	BuildLotSize   float64 `json:"buildLotSize" yaml:"buildLotSize"` // 单次建仓数量
	BuildThreshold float64 `json:"buildThreshold" yaml:"buildThreshold"` // 建仓价格上限
	MinRatio       float64 `json:"minRatio" yaml:"minRatio"`         // 最小持仓比例
	MaxRatio       float64 `json:"maxRatio" yaml:"maxRatio"`         // 最大持仓比例

	// 锁定参数
	LockThreshold    float64 `json:"lockThreshold" yaml:"lockThreshold"`     // 触发锁定的风险阈值（USDC）
	LockPriceMax     float64 `json:"lockPriceMax" yaml:"lockPriceMax"`       // 锁定阶段最高买入价格
	ExtremeHigh      float64 `json:"extremeHigh" yaml:"extremeHigh"`         // 极端价格阈值
	TargetProfitBase float64 `json:"targetProfitBase" yaml:"targetProfitBase"` // 目标利润（每个方向，USDC）
	InsuranceSize    float64 `json:"insuranceSize" yaml:"insuranceSize"`     // 反向保险数量

	// 放大参数
	AmplifyTarget      float64 `json:"amplifyTarget" yaml:"amplifyTarget"`         // 放大目标利润（USDC）
	AmplifyPriceMax    float64 `json:"amplifyPriceMax" yaml:"amplifyPriceMax"`     // 放大阶段最高买入价格
	InsurancePriceMax  float64 `json:"insurancePriceMax" yaml:"insurancePriceMax"` // 反向保险最高价格
	DirectionThreshold float64 `json:"directionThreshold" yaml:"directionThreshold"` // 主方向判定阈值

	// 通用参数
	MinOrderSize        float64 `json:"minOrderSize" yaml:"minOrderSize"`                 // 最小下单金额（USDC）
	MaxBuySlippageCents int     `json:"maxBuySlippageCents" yaml:"maxBuySlippageCents"`   // 最大买入滑点（分）
	AutoAdjustSize      bool    `json:"autoAdjustSize" yaml:"autoAdjustSize"`             // 是否自动调整数量以满足最小金额（默认true）
	MaxSizeAdjustRatio  float64 `json:"maxSizeAdjustRatio" yaml:"maxSizeAdjustRatio"`     // 最大数量调整倍数（默认5.0）
}

// GetName implements strategies.StrategyConfig (compat).
func (c *PairedTradingConfig) GetName() string { return ID }

// Validate 验证配置
func (c *PairedTradingConfig) Validate() error {
	// 验证阶段时间
	if c.BuildDuration.Duration <= 0 {
		return fmt.Errorf("build_duration 必须大于 0")
	}
	if c.LockStart.Duration <= 0 {
		return fmt.Errorf("lock_start 必须大于 0")
	}
	if c.AmplifyStart.Duration <= 0 {
		return fmt.Errorf("amplify_start 必须大于 0")
	}
	if c.CycleDuration.Duration <= 0 {
		return fmt.Errorf("cycle_duration 必须大于 0")
	}
	if c.LockStart.Duration > c.AmplifyStart.Duration || c.AmplifyStart.Duration > c.CycleDuration.Duration {
		return fmt.Errorf("阶段时间必须满足: build_duration < lock_start < amplify_start < cycle_duration")
	}

	// 验证价格阈值
	if c.EarlyLockPrice <= 0 || c.EarlyLockPrice >= 1.0 {
		return fmt.Errorf("early_lock_price 必须在 (0, 1) 范围内")
	}
	if c.EarlyAmplifyPrice <= 0 || c.EarlyAmplifyPrice >= 1.0 {
		return fmt.Errorf("early_amplify_price 必须在 (0, 1) 范围内")
	}
	if c.BuildThreshold <= 0 || c.BuildThreshold >= 1.0 {
		return fmt.Errorf("build_threshold 必须在 (0, 1) 范围内")
	}
	if c.LockPriceMax <= 0 || c.LockPriceMax >= 1.0 {
		return fmt.Errorf("lock_price_max 必须在 (0, 1) 范围内")
	}
	if c.AmplifyPriceMax <= 0 || c.AmplifyPriceMax >= 1.0 {
		return fmt.Errorf("amplify_price_max 必须在 (0, 1) 范围内")
	}
	if c.InsurancePriceMax <= 0 || c.InsurancePriceMax >= 1.0 {
		return fmt.Errorf("insurance_price_max 必须在 (0, 1) 范围内")
	}
	if c.DirectionThreshold <= 0 || c.DirectionThreshold >= 1.0 {
		return fmt.Errorf("direction_threshold 必须在 (0, 1) 范围内")
	}
	if c.ExtremeHigh <= 0 || c.ExtremeHigh >= 1.0 {
		return fmt.Errorf("extreme_high 必须在 (0, 1) 范围内")
	}

	// 验证持仓比例
	if c.MinRatio <= 0 || c.MinRatio >= 1.0 {
		return fmt.Errorf("min_ratio 必须在 (0, 1) 范围内")
	}
	if c.MaxRatio <= 0 || c.MaxRatio >= 1.0 {
		return fmt.Errorf("max_ratio 必须在 (0, 1) 范围内")
	}
	if c.MinRatio >= c.MaxRatio {
		return fmt.Errorf("min_ratio 必须小于 max_ratio")
	}

	// 验证数量参数
	if c.BaseTarget <= 0 {
		return fmt.Errorf("base_target 必须大于 0")
	}
	if c.BuildLotSize <= 0 {
		return fmt.Errorf("build_lot_size 必须大于 0")
	}
	if c.InsuranceSize <= 0 {
		return fmt.Errorf("insurance_size 必须大于 0")
	}

	// 验证利润参数
	if c.LockThreshold < 0 {
		return fmt.Errorf("lock_threshold 必须大于等于 0")
	}
	if c.TargetProfitBase <= 0 {
		return fmt.Errorf("target_profit_base 必须大于 0")
	}
	if c.AmplifyTarget <= 0 {
		return fmt.Errorf("amplify_target 必须大于 0")
	}

	// 验证通用参数
	if c.MinOrderSize <= 0 {
		return fmt.Errorf("min_order_size 必须大于 0")
	}

	// 验证数量调整参数
	if c.MaxSizeAdjustRatio <= 0 {
		return fmt.Errorf("max_size_adjust_ratio 必须大于 0")
	}

	return nil
}

// DefaultPairedTradingConfig 返回默认配置
func DefaultPairedTradingConfig() *PairedTradingConfig {
	return &PairedTradingConfig{
		// 阶段控制（默认15分钟周期）
		BuildDuration:     common.Duration{Duration: 5 * time.Minute},  // 建仓阶段 0-5 分钟
		LockStart:         common.Duration{Duration: 5 * time.Minute},  // 锁定阶段 5-10 分钟
		AmplifyStart:      common.Duration{Duration: 10 * time.Minute}, // 放大阶段 10-15 分钟
		CycleDuration:     common.Duration{Duration: 15 * time.Minute}, // 总周期 15 分钟
		EarlyLockPrice:    0.85,             // 价格超过0.85提前进入锁定
		EarlyAmplifyPrice: 0.90,             // 价格超过0.90提前进入放大

		// 建仓参数（适合40 USDC资金量）
		BaseTarget:     30.0, // 目标建仓30 shares
		BuildLotSize:   3.0,  // 单次建仓3 shares
		BuildThreshold: 0.60, // 价格低于0.60才建仓
		MinRatio:       0.40, // 最小持仓比例40%
		MaxRatio:       0.60, // 最大持仓比例60%

		// 锁定参数
		LockThreshold:    5.0,  // 风险超过5 USDC触发锁定
		LockPriceMax:     0.70, // 锁定阶段最高买入价0.70
		ExtremeHigh:      0.80, // 极端价格阈值0.80
		TargetProfitBase: 2.0,  // 目标利润2 USDC
		InsuranceSize:    1.5,  // 反向保险1.5 shares

		// 放大参数
		AmplifyTarget:      5.0,  // 放大目标利润5 USDC
		AmplifyPriceMax:    0.85, // 放大阶段最高买入价0.85
		InsurancePriceMax:  0.20, // 反向保险最高价0.20
		DirectionThreshold: 0.70, // 主方向判定阈值0.70

		// 通用参数
		MinOrderSize:        1.1,  // 最小下单1.1 USDC
		MaxBuySlippageCents: 3,    // 最大滑点3分
		AutoAdjustSize:      true, // 自动调整数量
		MaxSizeAdjustRatio:  5.0,  // 最大调整5倍
	}
}
