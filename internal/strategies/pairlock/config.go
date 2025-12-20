package pairlock

import "fmt"

// PairLockStrategyConfig 成对锁定（Complete-Set）滚动策略配置
//
// 设计目标：
// - 在单个 market/cycle 内进行多轮“买入 YES + 买入 NO”的成对交易
// - 通过控制两腿总成本 <= 100 - ProfitTargetCents 来锁定到期收益（无需方向判断）
type PairLockStrategyConfig struct {
	// OrderSize 每轮目标下单 shares（两腿保持相同 size）
	OrderSize float64

	// MinOrderSize 最小下单金额（USDC），用于确保两腿单笔金额都 >= 交易所要求
	MinOrderSize float64

	// ProfitTargetCents 锁定利润目标（分），要求两腿总成本 <= 100 - ProfitTargetCents
	ProfitTargetCents int

	// MaxRoundsPerPeriod 单个周期（market）内最多开启轮数
	MaxRoundsPerPeriod int

	// EnableParallel 是否允许并行跑多轮（默认 false：串行）
	// 注意：并行只会提高频率，但也会放大“锁不住”的未对冲风险，因此必须配合 MaxConcurrentPlans 控制。
	EnableParallel bool

	// MaxConcurrentPlans 最大并行轮数（EnableParallel=true 时生效，默认 1）
	MaxConcurrentPlans int

	// MaxTotalUnhedgedShares 全局未锁定风险预算（shares）。
	// 含义（保守口径）：所有“在途轮次”的 TargetSize 之和不超过该值，避免最坏情况下单腿成交导致累计裸露过大。
	// 默认（当 EnableParallel=true 且未显式配置时）：等于 OrderSize（即最多允许“一个轮次规模”的最坏未对冲风险）。
	MaxTotalUnhedgedShares float64

	// CooldownMs 信号触发冷却时间（毫秒），避免高频重复开轮
	CooldownMs int

	// MaxSupplementAttempts 单轮“补齐对冲”最大尝试次数（当一腿成交另一腿没成交时）
	MaxSupplementAttempts int

	// EntryMaxBuySlippageCents 买入滑点保护（分，0=关闭）。
	// 若启用：bestAsk 必须 <= (最近观测价 + slippage) 才允许下单（两腿都检查）。
	EntryMaxBuySlippageCents int
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

