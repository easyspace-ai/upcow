package paircostarb

import (
	"fmt"

	"github.com/betbot/gobet/internal/common"
)

// Config 基于“pair-cost（配对成本）”的成对套利策略配置：
// - 通过 CLOB 盘口深度估算 VWAP + 滑点垫子 + 手续费，得到“有效成本”
// - 以运行期累计均价计算 pair_cost（avgUp + avgDown + buffer）
// - 满足 pair_cost/imbalance/时间保护后分批买入 UP 或 DOWN，直到达到最小保证利润后停止并可选 autoMerge
type Config struct {
	Enabled      bool `json:"enabled" yaml:"enabled"`
	DecisionOnly bool `json:"decisionOnly" yaml:"decisionOnly"`

	// PollIntervalMs 主循环频率（毫秒）。建议 200~1000ms。
	PollIntervalMs int `json:"pollIntervalMs" yaml:"pollIntervalMs"`

	// TradeChunkShares 每次尝试买入的份额（shares）。
	TradeChunkShares float64 `json:"tradeChunkShares" yaml:"tradeChunkShares"`

	// MaxPairCost 配对成本上限（美元）。例如 0.98。
	MaxPairCost float64 `json:"maxPairCost" yaml:"maxPairCost"`

	// PairCostBuffer 用于覆盖不可建模误差（盘口跳动/结算/claim/估算偏差）的额外缓冲（美元）。
	PairCostBuffer float64 `json:"pairCostBuffer" yaml:"pairCostBuffer"`

	// FeeRateBps 估算费率（bps）。若不确定可先设 0，并通过 PairCostBuffer 留足缓冲。
	FeeRateBps int `json:"feeRateBps" yaml:"feeRateBps"`

	// SlippagePad 深度估算之外的额外滑点垫子（美元），用于保守估计 VWAP。
	SlippagePad float64 `json:"slippagePad" yaml:"slippagePad"`

	// FirstLegMaxPrice 当另一边仓位为 0 时，仅允许“非常便宜”的单边先手买入（美元）。
	FirstLegMaxPrice float64 `json:"firstLegMaxPrice" yaml:"firstLegMaxPrice"`

	// MaxUnpairedShares 单边未配对仓位的最大允许份额（shares）。
	MaxUnpairedShares float64 `json:"maxUnpairedShares" yaml:"maxUnpairedShares"`

	// MaxImbalance 两边份额不平衡上限（max/min），例如 1.15。
	MaxImbalance float64 `json:"maxImbalance" yaml:"maxImbalance"`

	// MinProfitUSD 达到该“保证利润（美元）”后停止本周期继续交易（并可选 autoMerge）。
	MinProfitUSD float64 `json:"minProfitUSD" yaml:"minProfitUSD"`

	// StopTimeBufferSeconds 距离周期结束（market start + duration）剩余该秒数时停止开新仓。
	StopTimeBufferSeconds int `json:"stopTimeBufferSeconds" yaml:"stopTimeBufferSeconds"`

	// CooldownAfterStopSeconds 达到止盈停止条件后，进入该冷却时间（秒），避免来回抖动。
	CooldownAfterStopSeconds int `json:"cooldownAfterStopSeconds" yaml:"cooldownAfterStopSeconds"`

	// OrderType 下单类型：taker(FAK) 或 limit(GTC)。
	// - taker：更适合基于深度 VWAP 的“确定成交成本”估算
	// - limit：更省手续费但成交不确定，VWAP 估算意义会弱很多
	OrderType string `json:"orderType" yaml:"orderType"`

	// LimitPricePadCents 限价单（或 taker 的最大可成交价）在 VWAP_eff 上再加的 pad（分）。
	LimitPricePadCents int `json:"limitPricePadCents" yaml:"limitPricePadCents"`

	// CycleEndProtectionMinutes 周期结束前 N 分钟停止开新仓（与 StopTimeBufferSeconds 二选一/叠加）。
	CycleEndProtectionMinutes int `json:"cycleEndProtectionMinutes" yaml:"cycleEndProtectionMinutes"`

	// 每周期最多下几笔（0=不限制）。注意：这里按“成功提交订单次数”计数。
	MaxTradesPerCycle int `json:"maxTradesPerCycle" yaml:"maxTradesPerCycle"`

	// 成交后自动 merge complete sets（YES+NO -> USDC），用于释放资金继续交易
	AutoMerge common.AutoMergeConfig `json:"autoMerge" yaml:"autoMerge"`
}

func (c *Config) Defaults() {
	if c == nil {
		return
	}
	if c.PollIntervalMs <= 0 {
		c.PollIntervalMs = 500
	}
	if c.TradeChunkShares <= 0 {
		c.TradeChunkShares = 5
	}
	if c.MaxPairCost <= 0 {
		c.MaxPairCost = 0.98
	}
	if c.PairCostBuffer < 0 {
		c.PairCostBuffer = 0
	}
	if c.FeeRateBps < 0 {
		c.FeeRateBps = 0
	}
	if c.SlippagePad < 0 {
		c.SlippagePad = 0
	}
	if c.FirstLegMaxPrice <= 0 {
		c.FirstLegMaxPrice = 0.10
	}
	if c.MaxUnpairedShares <= 0 {
		c.MaxUnpairedShares = 20
	}
	if c.MaxImbalance <= 0 {
		c.MaxImbalance = 1.15
	}
	if c.MinProfitUSD <= 0 {
		c.MinProfitUSD = 0.10
	}
	if c.StopTimeBufferSeconds <= 0 {
		c.StopTimeBufferSeconds = 20
	}
	if c.CooldownAfterStopSeconds < 0 {
		c.CooldownAfterStopSeconds = 0
	}
	if c.OrderType == "" {
		c.OrderType = "taker"
	}
	if c.LimitPricePadCents < 0 {
		c.LimitPricePadCents = 0
	}
	if c.CycleEndProtectionMinutes < 0 {
		c.CycleEndProtectionMinutes = 0
	}
	if c.MaxTradesPerCycle < 0 {
		c.MaxTradesPerCycle = 0
	}
	c.AutoMerge.Normalize()
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}
	if !c.Enabled {
		return nil
	}
	if c.PollIntervalMs <= 0 {
		return fmt.Errorf("pollIntervalMs must be > 0")
	}
	if c.TradeChunkShares <= 0 {
		return fmt.Errorf("tradeChunkShares must be > 0")
	}
	if c.MaxPairCost <= 0 || c.MaxPairCost >= 1.0 {
		return fmt.Errorf("maxPairCost must be within (0,1)")
	}
	if c.PairCostBuffer < 0 || c.PairCostBuffer >= 1.0 {
		return fmt.Errorf("pairCostBuffer must be within [0,1)")
	}
	if c.FeeRateBps < 0 || c.FeeRateBps > 10000 {
		return fmt.Errorf("feeRateBps must be within [0,10000]")
	}
	if c.SlippagePad < 0 || c.SlippagePad >= 1.0 {
		return fmt.Errorf("slippagePad must be within [0,1)")
	}
	if c.FirstLegMaxPrice <= 0 || c.FirstLegMaxPrice >= 1.0 {
		return fmt.Errorf("firstLegMaxPrice must be within (0,1)")
	}
	if c.MaxUnpairedShares <= 0 {
		return fmt.Errorf("maxUnpairedShares must be > 0")
	}
	if c.MaxImbalance < 1.0 {
		return fmt.Errorf("maxImbalance must be >= 1.0")
	}
	if c.MinProfitUSD <= 0 {
		return fmt.Errorf("minProfitUSD must be > 0")
	}
	if c.StopTimeBufferSeconds < 0 {
		return fmt.Errorf("stopTimeBufferSeconds must be >= 0")
	}
	if c.CooldownAfterStopSeconds < 0 {
		return fmt.Errorf("cooldownAfterStopSeconds must be >= 0")
	}
	switch c.OrderType {
	case "taker", "limit":
	default:
		return fmt.Errorf("orderType must be one of: taker|limit")
	}
	if c.LimitPricePadCents < 0 {
		return fmt.Errorf("limitPricePadCents must be >= 0")
	}
	if c.CycleEndProtectionMinutes < 0 {
		return fmt.Errorf("cycleEndProtectionMinutes must be >= 0")
	}
	if c.MaxTradesPerCycle < 0 {
		return fmt.Errorf("maxTradesPerCycle must be >= 0")
	}
	return nil
}
