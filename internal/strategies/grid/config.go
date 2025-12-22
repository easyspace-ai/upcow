package grid

import "fmt"

// Config：BTC 15m 网格策略（新架构）
//
// 设计目标：
// - 离散价位触发（gridLevels 或 start/gap/end 自动生成）
// - 下单统一走 ExecuteMultiLeg（单腿）
// - 用 session.OnOrderUpdate 感知成交/部分成交，从而挂止盈单
// - 通过 warmup/cooldown/maxOpenOrders/stopNewEntriesSeconds 等约束避免信号风暴
//
// 说明：
// - 价格单位：cents（1-99）
// - orderSize：shares
// - minOrderSize：USDC（交易所要求 >= 1.0；默认按 1.1 做安全垫）
type Config struct {
	// 网格层级（优先使用）。例如：[45, 50, 55, 60]
	GridLevels []int `json:"gridLevels" yaml:"gridLevels"`
	// 兼容旧配置（snake_case）
	GridLevelsLegacy []int `json:"-" yaml:"grid_levels"`

	// 自动生成网格：当 GridLevels 为空时使用。
	GridStart int `json:"gridStart" yaml:"gridStart"`
	GridGap   int `json:"gridGap" yaml:"gridGap"`
	GridEnd   int `json:"gridEnd" yaml:"gridEnd"`

	// 双向网格：true 时同时对 YES(UP) 与 NO(DOWN) 运行相同的网格逻辑。
	EnableDoubleSide bool `json:"enableDoubleSide" yaml:"enableDoubleSide"`
	// 单向模式：当 EnableDoubleSide=false 时使用；可取 "up"/"down"/"yes"/"no"。
	TokenType string `json:"tokenType" yaml:"tokenType"`

	// 每次入场下单数量（shares）
	OrderSize float64 `json:"orderSize" yaml:"orderSize"`

	// 最小下单金额（USDC）
	MinOrderSize float64 `json:"minOrderSize" yaml:"minOrderSize"`
	// 是否自动放大 size 以满足最小金额
	AutoAdjustSize bool `json:"autoAdjustSize" yaml:"autoAdjustSize"`
	// 最大放大倍数（防止低价时过度放大仓位）
	MaxSizeAdjustRatio float64 `json:"maxSizeAdjustRatio" yaml:"maxSizeAdjustRatio"`

	// 止盈：入场成交后，挂出场卖单价格 = entryPrice + profitTargetCents
	ProfitTargetCents int `json:"profitTargetCents" yaml:"profitTargetCents"`

	// 入场滑点容忍：允许 bestAsk 略高于网格层级（cents）。
	// 例如 gridLevel=62, slippage=2，则允许 bestAsk<=64 触发入场。
	GridLevelSlippageCents int `json:"gridLevelSlippageCents" yaml:"gridLevelSlippageCents"`

	// 轮次控制：
	// - 0 表示不限制轮次（但仍受 MaxEntriesPerPeriod 限制）
	// - >0 表示每个周期最多完成多少“完整轮次”（完成的定义见策略实现）
	MaxRoundsPerPeriod int `json:"maxRoundsPerPeriod" yaml:"maxRoundsPerPeriod"`
	// 是否等待当前轮次完全止盈/结束后才开始下一轮（默认 true）。
	WaitForRoundComplete *bool `json:"waitForRoundComplete" yaml:"waitForRoundComplete"`

	// 风控：极端共识区间（触发冻结，不再新增仓位）
	FreezeHighCents int `json:"freezeHighCents" yaml:"freezeHighCents"`
	FreezeLowCents  int `json:"freezeLowCents" yaml:"freezeLowCents"`
	// 冻结时是否撤掉未成交的入场单
	CancelEntryOrdersOnFreeze bool `json:"cancelEntryOrdersOnFreeze" yaml:"cancelEntryOrdersOnFreeze"`

	// 周期后段不再新增入场（秒）。例如 120 表示最后 2 分钟不再开新仓。
	StopNewEntriesSeconds int `json:"stopNewEntriesSeconds" yaml:"stopNewEntriesSeconds"`

	// 预热（ms）：刚连上 WS 的脏快照期间不交易
	WarmupMs int `json:"warmupMs" yaml:"warmupMs"`
	// 冷却（ms）：两次“提交下单请求”之间的最小间隔
	CooldownMs int `json:"cooldownMs" yaml:"cooldownMs"`

	// 限制：本周期最多触发多少次入场（用于 15m 控制节奏）
	MaxEntriesPerPeriod int `json:"maxEntriesPerPeriod" yaml:"maxEntriesPerPeriod"`
	// 限制：最多同时挂多少笔“入场单”（不含止盈单）
	MaxOpenEntryOrders int `json:"maxOpenEntryOrders" yaml:"maxOpenEntryOrders"`
}

func (c *Config) WaitForRoundCompleteEnabled() bool {
	if c == nil || c.WaitForRoundComplete == nil {
		return true
	}
	return *c.WaitForRoundComplete
}

func (c *Config) Normalize() {
	// 兼容旧配置：snake_case -> camelCase
	if len(c.GridLevels) == 0 && len(c.GridLevelsLegacy) > 0 {
		c.GridLevels = append([]int(nil), c.GridLevelsLegacy...)
	}
}

func (c *Config) Validate() error {
	c.Normalize()

	if c.OrderSize <= 0 {
		return fmt.Errorf("orderSize 必须 > 0")
	}
	if c.MinOrderSize <= 0 {
		c.MinOrderSize = 1.1
	}
	if c.MaxSizeAdjustRatio <= 0 {
		c.MaxSizeAdjustRatio = 5.0
	}
	if c.ProfitTargetCents <= 0 {
		c.ProfitTargetCents = 2
	}
	if c.GridLevelSlippageCents < 0 {
		c.GridLevelSlippageCents = 0
	}
	if c.MaxRoundsPerPeriod < 0 {
		c.MaxRoundsPerPeriod = 0
	}

	// freeze 默认：接近 0/100 时不再加仓
	if c.FreezeHighCents <= 0 {
		c.FreezeHighCents = 95
	}
	if c.FreezeLowCents < 0 {
		c.FreezeLowCents = 0
	}
	if c.StopNewEntriesSeconds < 0 {
		c.StopNewEntriesSeconds = 0
	}
	if c.WarmupMs < 0 {
		c.WarmupMs = 0
	}
	if c.CooldownMs <= 0 {
		c.CooldownMs = 250
	}
	if c.MaxEntriesPerPeriod <= 0 {
		c.MaxEntriesPerPeriod = 6
	}
	if c.MaxOpenEntryOrders <= 0 {
		c.MaxOpenEntryOrders = 4
	}

	// 网格层级校验
	if len(c.GridLevels) == 0 {
		// 用 start/gap/end 生成
		if c.GridStart <= 0 || c.GridEnd <= 0 || c.GridGap <= 0 {
			return fmt.Errorf("gridLevels 为空时，必须提供 gridStart/gridGap/gridEnd")
		}
		if c.GridStart >= c.GridEnd {
			return fmt.Errorf("gridStart 必须 < gridEnd")
		}
		if c.GridGap < 1 || c.GridGap > 20 {
			return fmt.Errorf("gridGap 建议在 [1,20] 范围内")
		}
	} else {
		if len(c.GridLevels) < 2 {
			return fmt.Errorf("gridLevels 至少需要 2 个层级")
		}
		last := -1
		for i, lv := range c.GridLevels {
			if lv < 1 || lv > 99 {
				return fmt.Errorf("gridLevels[%d]=%d 超出有效范围 [1,99]", i, lv)
			}
			if last >= 0 && lv <= last {
				return fmt.Errorf("gridLevels 必须严格递增（发现 %d <= %d）", lv, last)
			}
			last = lv
		}
	}

	if c.ProfitTargetCents < 1 || c.ProfitTargetCents > 20 {
		return fmt.Errorf("profitTargetCents 建议在 [1,20] 范围内（15m 市场）")
	}
	if c.FreezeHighCents < 50 || c.FreezeHighCents > 99 {
		return fmt.Errorf("freezeHighCents 建议在 [50,99] 范围内")
	}
	if c.FreezeLowCents < 0 || c.FreezeLowCents > 50 {
		return fmt.Errorf("freezeLowCents 建议在 [0,50] 范围内")
	}

	return nil
}

