package cyclehedge

import "fmt"

// Config：BTC 15m “周期对冲锁利”策略。
//
// 核心思想（complete-set）：
// - 同一 market/cycle 内买入等量 YES + NO（两腿），持有到结算获取 $1/份
// - 只要两腿的“建仓总成本” <= 1 - profit，就锁定了确定收益
//
// 与老的 arbitrage/pairlock 不同点：
// - 以 maker(GTC) 为主，目标是稳定锁 1~5c，而不是高频吃单
// - 出现单腿成交时：在超时/临近结算前自动补齐或回平，避免裸露风险
// - 每周期按余额自动放大目标 Notional（滚动复利）
type Config struct {
	// ===== 周期参数 =====
	// 默认 15m=900 秒（本策略目标就是 btc 15m）。
	CycleDurationSeconds int `yaml:"cycleDurationSeconds" json:"cycleDurationSeconds"`

	// ===== 锁利目标（cents）=====
	ProfitMinCents int `yaml:"profitMinCents" json:"profitMinCents"` // 最小锁利（分），默认 1
	ProfitMaxCents int `yaml:"profitMaxCents" json:"profitMaxCents"` // 最大锁利（分），默认 5

	// 当需要“补齐对冲（taker）”时允许的最小剩余利润（分）。
	// 例如设为 0 表示只要不亏就允许补齐；设为 1/2 表示仍要保留一点确定收益。
	MinProfitAfterCompleteCents int `yaml:"minProfitAfterCompleteCents" json:"minProfitAfterCompleteCents"`

	// ===== 每周期资金目标（USDC notional）=====
	// 目标：每个周期投入的总资金规模（两腿合计成本）。
	// 若 FixedNotionalUSDC > 0，则本周期使用固定 notional（不随余额滚动）。
	FixedNotionalUSDC float64 `yaml:"fixedNotionalUSDC" json:"fixedNotionalUSDC"`
	MinNotionalUSDC float64 `yaml:"minNotionalUSDC" json:"minNotionalUSDC"` // 最小投入（用于小资金起步）
	MaxNotionalUSDC float64 `yaml:"maxNotionalUSDC" json:"maxNotionalUSDC"` // 最大投入（用于风控上限，例如 3000）
	BalanceAllocationPct float64 `yaml:"balanceAllocationPct" json:"balanceAllocationPct"` // 使用余额比例（0..1），默认 0.8

	// ===== 执行与风控 =====
	RequoteMs int `yaml:"requoteMs" json:"requoteMs"` // 挂单刷新间隔，默认 800ms

	// 临近周期结算不再开新仓，并撤掉未成交挂单（秒）。
	EntryCutoffSeconds int `yaml:"entryCutoffSeconds" json:"entryCutoffSeconds"` // 默认 25s

	// 每周期最大单向持仓（shares），用于限制“只成交一腿/临时偏斜”导致的风险累积。
	// 当任一边持仓 >= 该阈值时：
	// - 策略不会继续扩大目标规模
	// - 若出现裸露且超时，将更倾向于回平而非继续加仓
	MaxSingleSideShares float64 `yaml:"maxSingleSideShares" json:"maxSingleSideShares"`

	// 单腿裸露最长允许时长（秒）。
	// 超过该时长仍无法完成对冲，则触发补齐(taker) 或回平(flatten)。
	UnhedgedTimeoutSeconds int `yaml:"unhedgedTimeoutSeconds" json:"unhedgedTimeoutSeconds"` // 默认 10s

	AllowTakerComplete bool `yaml:"allowTakerComplete" json:"allowTakerComplete"` // 默认 true：优先补齐避免裸奔
	AllowFlatten       bool `yaml:"allowFlatten" json:"allowFlatten"`             // 默认 true：无法补齐则回平

	// 最小触发回平/补齐的裸露份额（shares），避免小数噪声频繁操作
	MinUnhedgedShares float64 `yaml:"minUnhedgedShares" json:"minUnhedgedShares"` // 默认 1

	// ===== 盘口质量 gate（可选，建议开启）=====
	EnableMarketQualityGate *bool `yaml:"enableMarketQualityGate" json:"enableMarketQualityGate"`
	MarketQualityMinScore   int   `yaml:"marketQualityMinScore" json:"marketQualityMinScore"` // 默认 70
	MarketQualityMaxSpreadCents int `yaml:"marketQualityMaxSpreadCents" json:"marketQualityMaxSpreadCents"` // 默认 5
	MarketQualityMaxBookAgeMs   int `yaml:"marketQualityMaxBookAgeMs" json:"marketQualityMaxBookAgeMs"`     // 默认 3000

	// ===== 动态 profit 选择 =====
	// 动态模式会在 [profitMinCents, profitMaxCents] 内选择得分最高的 profit：
	// - profit 越大越好（收益高）
	// - 但挂单离盘口越远越差（成交概率低）
	// 评分：score = profit - distancePenaltyBps * maxDistanceCents
	// （maxDistanceCents 指 yes/no 两腿中离 bestBid 更远的一腿）
	EnableDynamicProfit bool `yaml:"enableDynamicProfit" json:"enableDynamicProfit"`
	DistancePenaltyBps  int  `yaml:"distancePenaltyBps" json:"distancePenaltyBps"` // 默认 30（=0.30c penalty per 1c distance）
}

func boolPtr(b bool) *bool { return &b }

func (c *Config) Validate() error {
	if c.CycleDurationSeconds <= 0 {
		c.CycleDurationSeconds = 15 * 60
	}
	if c.CycleDurationSeconds < 60 {
		return fmt.Errorf("cycleDurationSeconds 太小：至少 60 秒")
	}

	if c.ProfitMinCents <= 0 {
		c.ProfitMinCents = 1
	}
	if c.ProfitMaxCents <= 0 {
		c.ProfitMaxCents = 5
	}
	if c.ProfitMinCents > c.ProfitMaxCents {
		return fmt.Errorf("profitMinCents 不能大于 profitMaxCents")
	}
	if c.ProfitMaxCents > 20 {
		return fmt.Errorf("profitMaxCents 建议不要超过 20（否则几乎无法成交）")
	}
	if c.MinProfitAfterCompleteCents < 0 || c.MinProfitAfterCompleteCents > 20 {
		return fmt.Errorf("minProfitAfterCompleteCents 必须在 [0,20] 范围内")
	}

	if c.FixedNotionalUSDC < 0 {
		return fmt.Errorf("fixedNotionalUSDC 不能为负数")
	}
	if c.MinNotionalUSDC <= 0 {
		c.MinNotionalUSDC = 30
	}
	if c.MaxNotionalUSDC <= 0 {
		c.MaxNotionalUSDC = 3000
	}
	if c.MaxNotionalUSDC < c.MinNotionalUSDC {
		return fmt.Errorf("maxNotionalUSDC 不能小于 minNotionalUSDC")
	}
	if c.FixedNotionalUSDC == 0 {
		if c.BalanceAllocationPct <= 0 {
			c.BalanceAllocationPct = 0.8
		}
		if c.BalanceAllocationPct <= 0 || c.BalanceAllocationPct > 1.0 {
			return fmt.Errorf("balanceAllocationPct 必须在 (0,1] 范围内")
		}
	}

	if c.RequoteMs <= 0 {
		c.RequoteMs = 800
	}
	if c.RequoteMs < 200 {
		c.RequoteMs = 200
	}
	if c.EntryCutoffSeconds <= 0 {
		c.EntryCutoffSeconds = 25
	}
	if c.MaxSingleSideShares < 0 {
		return fmt.Errorf("maxSingleSideShares 不能为负数")
	}
	if c.UnhedgedTimeoutSeconds <= 0 {
		c.UnhedgedTimeoutSeconds = 10
	}
	if c.MinUnhedgedShares <= 0 {
		c.MinUnhedgedShares = 1.0
	}
	if c.EnableMarketQualityGate == nil {
		c.EnableMarketQualityGate = boolPtr(true)
	}
	if c.MarketQualityMinScore <= 0 {
		c.MarketQualityMinScore = 70
	}
	if c.MarketQualityMinScore < 0 || c.MarketQualityMinScore > 100 {
		return fmt.Errorf("marketQualityMinScore 必须在 0-100 之间")
	}
	if c.MarketQualityMaxSpreadCents <= 0 {
		c.MarketQualityMaxSpreadCents = 5
	}
	if c.MarketQualityMaxBookAgeMs <= 0 {
		c.MarketQualityMaxBookAgeMs = 3000
	}
	if c.MarketQualityMaxBookAgeMs < 0 {
		return fmt.Errorf("marketQualityMaxBookAgeMs 不能为负数")
	}

	if !c.EnableDynamicProfit {
		// 默认开启动态 profit：更贴合“稳定成交 + 锁利”的目标
		c.EnableDynamicProfit = true
	}
	if c.DistancePenaltyBps <= 0 {
		c.DistancePenaltyBps = 30
	}
	if c.DistancePenaltyBps < 0 || c.DistancePenaltyBps > 500 {
		return fmt.Errorf("distancePenaltyBps 建议在 [1,500] 范围内")
	}

	// 默认开启补齐/回平：这是“确定性锁利”最关键的风险控制
	if !c.AllowTakerComplete && !c.AllowFlatten {
		// 两个都关会导致裸奔无法自救
		return fmt.Errorf("allowTakerComplete 和 allowFlatten 不能同时为 false")
	}
	return nil
}

