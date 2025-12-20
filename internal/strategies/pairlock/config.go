package pairlock

import "fmt"

// PairLockStrategyConfig 成对锁定（Complete-Set）滚动策略配置
//
// 设计目标：
// - 在单个 market/cycle 内进行多轮“买入 YES + 买入 NO”的成对交易
// - 通过控制两腿总成本 <= 100 - ProfitTargetCents 来锁定到期收益（无需方向判断）
type PairLockStrategyConfig struct {
	// OrderSize 每轮目标下单 shares（两腿保持相同 size）
	OrderSize float64 `json:"orderSize" yaml:"orderSize"`

	// MinOrderSize 最小下单金额（USDC），用于确保两腿单笔金额都 >= 交易所要求
	MinOrderSize float64 `json:"minOrderSize" yaml:"minOrderSize"`

	// ProfitTargetCents 锁定利润目标（分），要求两腿总成本 <= 100 - ProfitTargetCents
	ProfitTargetCents int `json:"profitTargetCents" yaml:"profitTargetCents"`

	// MaxRoundsPerPeriod 单个周期（market）内最多开启轮数
	MaxRoundsPerPeriod int `json:"maxRoundsPerPeriod" yaml:"maxRoundsPerPeriod"`

	// EnableParallel 是否允许并行跑多轮（默认 false：串行）
	// 注意：并行只会提高频率，但也会放大“锁不住”的未对冲风险，因此必须配合 MaxConcurrentPlans 控制。
	EnableParallel bool `json:"enableParallel" yaml:"enableParallel"`

	// MaxConcurrentPlans 最大并行轮数（EnableParallel=true 时生效，默认 1）
	MaxConcurrentPlans int `json:"maxConcurrentPlans" yaml:"maxConcurrentPlans"`

	// MaxTotalUnhedgedShares 全局未锁定风险预算（shares）。
	// 含义（保守口径）：所有“在途轮次”的 TargetSize 之和不超过该值，避免最坏情况下单腿成交导致累计裸露过大。
	// 默认（当 EnableParallel=true 且未显式配置时）：等于 OrderSize（即最多允许“一个轮次规模”的最坏未对冲风险）。
	MaxTotalUnhedgedShares float64 `json:"maxTotalUnhedgedShares" yaml:"maxTotalUnhedgedShares"`

	// MaxPlanAgeSeconds 单轮最大存活时间（秒）。
	// 超过该时间仍未完成锁定，则判定该轮失败并触发 OnFailAction。
	MaxPlanAgeSeconds int `json:"maxPlanAgeSeconds" yaml:"maxPlanAgeSeconds"`

	// OnFailAction 失败动作：
	// - "pause": 仅暂停策略（默认，最安全）
	// - "cancel_pause": 取消该轮相关未成交订单后暂停
	// - "flatten_pause": 取消未成交订单，并尝试卖出多出来的一腿（把未对冲差额回平）后暂停
	OnFailAction string `json:"onFailAction" yaml:"onFailAction"`

	// FailMaxSellSlippageCents 失败回平（flatten）时允许的卖出滑点下限（分，0=关闭）。
	// 若启用：卖出价不允许低于（最近观测价 - slippage）。
	FailMaxSellSlippageCents int `json:"failMaxSellSlippageCents" yaml:"failMaxSellSlippageCents"`

	// FailFlattenMinShares 失败回平（flatten）最小回平差额（shares）。
	// 只有当未对冲差额 >= 该值时才触发卖出回平，避免小额噪声频繁交易。
	FailFlattenMinShares float64 `json:"failFlattenMinShares" yaml:"failFlattenMinShares"`

	// CooldownMs 信号触发冷却时间（毫秒），避免高频重复开轮
	CooldownMs int `json:"cooldownMs" yaml:"cooldownMs"`

	// MaxSupplementAttempts 单轮“补齐对冲”最大尝试次数（当一腿成交另一腿没成交时）
	MaxSupplementAttempts int `json:"maxSupplementAttempts" yaml:"maxSupplementAttempts"`

	// EntryMaxBuySlippageCents 买入滑点保护（分，0=关闭）。
	// 若启用：bestAsk 必须 <= (最近观测价 + slippage) 才允许下单（两腿都检查）。
	EntryMaxBuySlippageCents int `json:"entryMaxBuySlippageCents" yaml:"entryMaxBuySlippageCents"`
}

func (c *PairLockStrategyConfig) GetName() string { return ID }

func (c *PairLockStrategyConfig) Validate() error {
	if c.OrderSize <= 0 {
		return fmt.Errorf("order_size 必须 > 0")
	}
	if c.MinOrderSize <= 0 {
		c.MinOrderSize = 1.1
	}
	if c.MinOrderSize < 1.0 {
		return fmt.Errorf("min_order_size 必须 >= 1.0 USDC（交易所要求）")
	}
	if c.ProfitTargetCents < 0 || c.ProfitTargetCents > 100 {
		return fmt.Errorf("profit_target_cents 必须在 [0,100] 范围内")
	}
	if c.MaxRoundsPerPeriod <= 0 {
		c.MaxRoundsPerPeriod = 1
	}
	if !c.EnableParallel {
		c.MaxConcurrentPlans = 1
	}
	if c.EnableParallel && c.MaxConcurrentPlans <= 0 {
		c.MaxConcurrentPlans = 2
	}
	if c.EnableParallel {
		if c.MaxTotalUnhedgedShares <= 0 {
			// 默认保守：只允许一个轮次规模的“最坏未锁定风险”
			c.MaxTotalUnhedgedShares = c.OrderSize
		}
	} else {
		// 串行模式下该参数不生效，但保持为 0 以表达“无需预算”
		if c.MaxTotalUnhedgedShares < 0 {
			return fmt.Errorf("max_total_unhedged_shares 不能为负数")
		}
	}
	if c.MaxPlanAgeSeconds <= 0 {
		c.MaxPlanAgeSeconds = 60
	}
	if c.OnFailAction == "" {
		c.OnFailAction = "pause"
	}
	switch c.OnFailAction {
	case "pause", "cancel_pause", "flatten_pause":
	default:
		return fmt.Errorf("on_fail_action 无效: %s (允许: pause/cancel_pause/flatten_pause)", c.OnFailAction)
	}
	if c.FailMaxSellSlippageCents < 0 {
		return fmt.Errorf("fail_max_sell_slippage_cents 不能为负数")
	}
	if c.FailFlattenMinShares < 0 {
		return fmt.Errorf("fail_flatten_min_shares 不能为负数")
	}
	if c.FailFlattenMinShares == 0 {
		c.FailFlattenMinShares = 1.0
	}
	if c.CooldownMs <= 0 {
		c.CooldownMs = 250
	}
	if c.MaxSupplementAttempts <= 0 {
		c.MaxSupplementAttempts = 3
	}
	if c.EntryMaxBuySlippageCents < 0 {
		return fmt.Errorf("entry_max_buy_slippage_cents 不能为负数")
	}
	return nil
}

