package pairedtrading

import (
	"fmt"
	"time"
)

// PairedTradingConfig 成对交易策略配置
type PairedTradingConfig struct {
	// 阶段控制参数
	BuildDuration  time.Duration `yaml:"build_duration"`  // 建仓阶段持续时间
	LockStart      time.Duration `yaml:"lock_start"`      // 锁定阶段开始时间
	AmplifyStart   time.Duration `yaml:"amplify_start"`   // 放大阶段开始时间
	CycleDuration  time.Duration `yaml:"cycle_duration"`  // 周期总时长
	EarlyLockPrice float64       `yaml:"early_lock_price"` // 提前进入锁定阶段的价格阈值
	EarlyAmplifyPrice float64    `yaml:"early_amplify_price"` // 提前进入放大阶段的价格阈值

	// 建仓参数
	BaseTarget      float64 `yaml:"base_target"`       // 基础建仓目标（shares）
	BuildLotSize    float64 `yaml:"build_lot_size"`    // 单次建仓数量
	BuildThreshold  float64 `yaml:"build_threshold"`   // 建仓价格上限
	MinRatio        float64 `yaml:"min_ratio"`         // 最小持仓比例
	MaxRatio        float64 `yaml:"max_ratio"`         // 最大持仓比例

	// 锁定参数
	LockThreshold    float64 `yaml:"lock_threshold"`     // 触发锁定的风险阈值（USDC）
	LockPriceMax     float64 `yaml:"lock_price_max"`     // 锁定阶段最高买入价格
	ExtremeHigh      float64 `yaml:"extreme_high"`       // 极端价格阈值
	TargetProfitBase float64 `yaml:"target_profit_base"` // 目标利润（每个方向，USDC）
	InsuranceSize    float64 `yaml:"insurance_size"`     // 反向保险数量

	// 放大参数
	AmplifyTarget        float64 `yaml:"amplify_target"`         // 放大目标利润（USDC）
	AmplifyPriceMax      float64 `yaml:"amplify_price_max"`      // 放大阶段最高买入价格
	InsurancePriceMax    float64 `yaml:"insurance_price_max"`    // 反向保险最高价格
	DirectionThreshold   float64 `yaml:"direction_threshold"`    // 主方向判定阈值

	// 通用参数
	MinOrderSize         float64 `yaml:"min_order_size"`          // 最小下单金额（USDC）
	MaxBuySlippageCents  int     `yaml:"max_buy_slippage_cents"`  // 最大买入滑点（分）
	AutoAdjustSize       bool    `yaml:"auto_adjust_size"`        // 是否自动调整数量以满足最小金额（默认true）
	MaxSizeAdjustRatio   float64 `yaml:"max_size_adjust_ratio"`   // 最大数量调整倍数（默认5.0）
}

// Validate 验证配置
func (c *PairedTradingConfig) Validate() error {
	// 验证阶段时间
	if c.BuildDuration <= 0 {
		return fmt.Errorf("build_duration 必须大于 0")
	}
	if c.LockStart <= 0 {
		return fmt.Errorf("lock_start 必须大于 0")
	}
	if c.AmplifyStart <= 0 {
		return fmt.Errorf("amplify_start 必须大于 0")
	}
	if c.CycleDuration <= 0 {
		return fmt.Errorf("cycle_duration 必须大于 0")
	}
	if c.LockStart > c.AmplifyStart || c.AmplifyStart > c.CycleDuration {
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
		BuildDuration:     5 * time.Minute,  // 建仓阶段 0-5 分钟
		LockStart:         5 * time.Minute,  // 锁定阶段 5-10 分钟
		AmplifyStart:      10 * time.Minute, // 放大阶段 10-15 分钟
		CycleDuration:     15 * time.Minute, // 总周期 15 分钟
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
		MinOrderSize:        1.1, // 最小下单1.1 USDC
		MaxBuySlippageCents: 3,   // 最大滑点3分
		AutoAdjustSize:      true, // 自动调整数量
		MaxSizeAdjustRatio:  5.0,  // 最大调整5倍
	}
}

// PairedTradingConfigAdapter 配置适配器（BBGO风格）
type PairedTradingConfigAdapter struct{}

// AdaptFromMap 从 map 适配配置
func (a *PairedTradingConfigAdapter) AdaptFromMap(m map[string]interface{}) (interface{}, error) {
	config := DefaultPairedTradingConfig()

	// 阶段控制参数
	if v, ok := m["build_duration"]; ok {
		if d, err := time.ParseDuration(fmt.Sprintf("%vs", v)); err == nil {
			config.BuildDuration = d
		}
	}
	if v, ok := m["lock_start"]; ok {
		if d, err := time.ParseDuration(fmt.Sprintf("%vs", v)); err == nil {
			config.LockStart = d
		}
	}
	if v, ok := m["amplify_start"]; ok {
		if d, err := time.ParseDuration(fmt.Sprintf("%vs", v)); err == nil {
			config.AmplifyStart = d
		}
	}
	if v, ok := m["cycle_duration"]; ok {
		if d, err := time.ParseDuration(fmt.Sprintf("%vs", v)); err == nil {
			config.CycleDuration = d
		}
	}
	if v, ok := m["early_lock_price"]; ok {
		if f, ok := v.(float64); ok {
			config.EarlyLockPrice = f
		}
	}
	if v, ok := m["early_amplify_price"]; ok {
		if f, ok := v.(float64); ok {
			config.EarlyAmplifyPrice = f
		}
	}

	// 建仓参数
	if v, ok := m["base_target"]; ok {
		if f, ok := v.(float64); ok {
			config.BaseTarget = f
		}
	}
	if v, ok := m["build_lot_size"]; ok {
		if f, ok := v.(float64); ok {
			config.BuildLotSize = f
		}
	}
	if v, ok := m["build_threshold"]; ok {
		if f, ok := v.(float64); ok {
			config.BuildThreshold = f
		}
	}
	if v, ok := m["min_ratio"]; ok {
		if f, ok := v.(float64); ok {
			config.MinRatio = f
		}
	}
	if v, ok := m["max_ratio"]; ok {
		if f, ok := v.(float64); ok {
			config.MaxRatio = f
		}
	}

	// 锁定参数
	if v, ok := m["lock_threshold"]; ok {
		if f, ok := v.(float64); ok {
			config.LockThreshold = f
		}
	}
	if v, ok := m["lock_price_max"]; ok {
		if f, ok := v.(float64); ok {
			config.LockPriceMax = f
		}
	}
	if v, ok := m["extreme_high"]; ok {
		if f, ok := v.(float64); ok {
			config.ExtremeHigh = f
		}
	}
	if v, ok := m["target_profit_base"]; ok {
		if f, ok := v.(float64); ok {
			config.TargetProfitBase = f
		}
	}
	if v, ok := m["insurance_size"]; ok {
		if f, ok := v.(float64); ok {
			config.InsuranceSize = f
		}
	}

	// 放大参数
	if v, ok := m["amplify_target"]; ok {
		if f, ok := v.(float64); ok {
			config.AmplifyTarget = f
		}
	}
	if v, ok := m["amplify_price_max"]; ok {
		if f, ok := v.(float64); ok {
			config.AmplifyPriceMax = f
		}
	}
	if v, ok := m["insurance_price_max"]; ok {
		if f, ok := v.(float64); ok {
			config.InsurancePriceMax = f
		}
	}
	if v, ok := m["direction_threshold"]; ok {
		if f, ok := v.(float64); ok {
			config.DirectionThreshold = f
		}
	}

	// 通用参数
	if v, ok := m["min_order_size"]; ok {
		if f, ok := v.(float64); ok {
			config.MinOrderSize = f
		}
	}
	if v, ok := m["max_buy_slippage_cents"]; ok {
		if i, ok := v.(int); ok {
			config.MaxBuySlippageCents = i
		} else if f, ok := v.(float64); ok {
			config.MaxBuySlippageCents = int(f)
		}
	}
	if v, ok := m["auto_adjust_size"]; ok {
		if b, ok := v.(bool); ok {
			config.AutoAdjustSize = b
		}
	}
	if v, ok := m["max_size_adjust_ratio"]; ok {
		if f, ok := v.(float64); ok {
			config.MaxSizeAdjustRatio = f
		}
	}

	return config, nil
}
