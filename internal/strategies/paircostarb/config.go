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

	// ===== 信号模块（Binance 秒级 K 线）=====
	// EnableBinanceSignal: 启用 Binance Futures 1s K 线信号（从 Environment 注入 BinanceFuturesKlines）。
	EnableBinanceSignal bool `json:"enableBinanceSignal" yaml:"enableBinanceSignal"`
	// RequireBinanceSignal: true 时仅在信号窗口内允许开仓（否则只做内部 pair-cost 逻辑）。
	RequireBinanceSignal bool `json:"requireBinanceSignal" yaml:"requireBinanceSignal"`
	// BinanceInterval: "1s" 或 "1m"（默认 1s）。
	BinanceInterval string `json:"binanceInterval" yaml:"binanceInterval"`
	// BinanceLookbackSeconds: 计算动量/涨跌幅的回看窗口（秒）。
	BinanceLookbackSeconds int `json:"binanceLookbackSeconds" yaml:"binanceLookbackSeconds"`
	// BinanceReturnThresholdBps: 触发阈值（bps）。例如 10 = 0.10%。
	BinanceReturnThresholdBps float64 `json:"binanceReturnThresholdBps" yaml:"binanceReturnThresholdBps"`
	// BinanceActiveSeconds: 触发后保持 ACTIVE 的秒数（防抖/窗口）。
	BinanceActiveSeconds int `json:"binanceActiveSeconds" yaml:"binanceActiveSeconds"`

	// ===== 执行模块 =====
	// ExecutionMode:
	// - "single": 每次只下单一边（沿用原 pair-cost 累计逻辑）
	// - "paired": 每次同时下 UP + DOWN（信号为 UP 时“上涨买入UP，同时对冲DOWN”，信号为 DOWN 时反之）
	// - "auto":  信号 ACTIVE 时走 paired，否则走 single（更像专业交易系统：信号只做激活/节奏）
	ExecutionMode string `json:"executionMode" yaml:"executionMode"`

	// TradeChunkShares 每次尝试买入的份额（shares）。
	TradeChunkShares float64 `json:"tradeChunkShares" yaml:"tradeChunkShares"`

	// ===== 动态下单 chunk（更专业：按执行质量缩放）=====
	EnableDynamicChunk  bool    `json:"enableDynamicChunk" yaml:"enableDynamicChunk"`
	MinTradeChunkShares float64 `json:"minTradeChunkShares" yaml:"minTradeChunkShares"`
	MaxTradeChunkShares float64 `json:"maxTradeChunkShares" yaml:"maxTradeChunkShares"`
	// DynamicChunkMinMultiplier: 没有质量数据时的最小缩放（避免直接缩到 0）
	DynamicChunkMinMultiplier float64 `json:"dynamicChunkMinMultiplier" yaml:"dynamicChunkMinMultiplier"`

	// MaxPairCost 配对成本上限（美元）。例如 0.98。
	MaxPairCost float64 `json:"maxPairCost" yaml:"maxPairCost"`

	// PairCostBuffer 用于覆盖不可建模误差（盘口跳动/结算/claim/估算偏差）的额外缓冲（美元）。
	PairCostBuffer float64 `json:"pairCostBuffer" yaml:"pairCostBuffer"`

	// FeeRateBps 估算费率（bps）。若不确定可先设 0，并通过 PairCostBuffer 留足缓冲。
	FeeRateBps int `json:"feeRateBps" yaml:"feeRateBps"`

	// SlippagePad 深度估算之外的额外滑点垫子（美元），用于保守估计 VWAP。
	SlippagePad float64 `json:"slippagePad" yaml:"slippagePad"`

	// ===== 执行质量自适应（更专业：动态 buffer / 风控降频）=====
	EnableAdaptiveBuffers bool `json:"enableAdaptiveBuffers" yaml:"enableAdaptiveBuffers"`
	// AdaptiveSlipMultiplier: slippagePad_eff = slippagePad + multiplier*EWMA(abs(actual-predicted))
	AdaptiveSlipMultiplier float64 `json:"adaptiveSlipMultiplier" yaml:"adaptiveSlipMultiplier"`
	// MaxAdaptiveSlippagePad: slippagePad_eff 的上限（美元）
	MaxAdaptiveSlippagePad float64 `json:"maxAdaptiveSlippagePad" yaml:"maxAdaptiveSlippagePad"`
	// MinFillRatio: 最近 EWMA 成交率低于该阈值时，暂停开仓（尤其对 FAK/流动性差的场景）
	MinFillRatio float64 `json:"minFillRatio" yaml:"minFillRatio"`
	// QualityCooldownSeconds: 触发质量风控后进入冷却的秒数
	QualityCooldownSeconds int `json:"qualityCooldownSeconds" yaml:"qualityCooldownSeconds"`

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

	// PrimaryPadCents/HedgePadCents：在 LimitPricePadCents 基础上，分别对主腿/对冲腿增加额外 pad（分）。
	PrimaryPadCents int `json:"primaryPadCents" yaml:"primaryPadCents"`
	HedgePadCents   int `json:"hedgePadCents" yaml:"hedgePadCents"`

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
	if c.BinanceInterval == "" {
		c.BinanceInterval = "1s"
	}
	if c.BinanceLookbackSeconds <= 0 {
		c.BinanceLookbackSeconds = 5
	}
	if c.BinanceReturnThresholdBps <= 0 {
		c.BinanceReturnThresholdBps = 10
	}
	if c.BinanceActiveSeconds <= 0 {
		c.BinanceActiveSeconds = 15
	}
	if c.ExecutionMode == "" {
		if c.EnableBinanceSignal {
			c.ExecutionMode = "auto"
		} else {
			c.ExecutionMode = "single"
		}
	}
	if c.TradeChunkShares <= 0 {
		c.TradeChunkShares = 5
	}
	if c.MinTradeChunkShares <= 0 {
		c.MinTradeChunkShares = 1
	}
	if c.MaxTradeChunkShares <= 0 {
		c.MaxTradeChunkShares = c.TradeChunkShares
	}
	if c.DynamicChunkMinMultiplier <= 0 {
		c.DynamicChunkMinMultiplier = 0.3
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
	if c.AdaptiveSlipMultiplier < 0 {
		c.AdaptiveSlipMultiplier = 0
	}
	if c.MaxAdaptiveSlippagePad <= 0 {
		c.MaxAdaptiveSlippagePad = 0.01
	}
	if c.MinFillRatio <= 0 {
		c.MinFillRatio = 0.65
	}
	if c.QualityCooldownSeconds <= 0 {
		c.QualityCooldownSeconds = 5
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
	if c.PrimaryPadCents < 0 {
		c.PrimaryPadCents = 0
	}
	if c.HedgePadCents < 0 {
		c.HedgePadCents = 0
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
	if c.BinanceLookbackSeconds < 0 {
		return fmt.Errorf("binanceLookbackSeconds must be >= 0")
	}
	if c.BinanceReturnThresholdBps < 0 {
		return fmt.Errorf("binanceReturnThresholdBps must be >= 0")
	}
	if c.BinanceActiveSeconds < 0 {
		return fmt.Errorf("binanceActiveSeconds must be >= 0")
	}
	switch c.ExecutionMode {
	case "", "single", "paired", "auto":
	default:
		return fmt.Errorf("executionMode must be one of: single|paired|auto")
	}
	if c.TradeChunkShares <= 0 {
		return fmt.Errorf("tradeChunkShares must be > 0")
	}
	if c.MinTradeChunkShares < 0 {
		return fmt.Errorf("minTradeChunkShares must be >= 0")
	}
	if c.MaxTradeChunkShares < 0 {
		return fmt.Errorf("maxTradeChunkShares must be >= 0")
	}
	if c.DynamicChunkMinMultiplier < 0 || c.DynamicChunkMinMultiplier > 1.0 {
		return fmt.Errorf("dynamicChunkMinMultiplier must be within [0,1]")
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
	if c.AdaptiveSlipMultiplier < 0 {
		return fmt.Errorf("adaptiveSlipMultiplier must be >= 0")
	}
	if c.MaxAdaptiveSlippagePad < 0 || c.MaxAdaptiveSlippagePad >= 1.0 {
		return fmt.Errorf("maxAdaptiveSlippagePad must be within [0,1)")
	}
	if c.MinFillRatio < 0 || c.MinFillRatio > 1.0 {
		return fmt.Errorf("minFillRatio must be within [0,1]")
	}
	if c.QualityCooldownSeconds < 0 {
		return fmt.Errorf("qualityCooldownSeconds must be >= 0")
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
	if c.PrimaryPadCents < 0 {
		return fmt.Errorf("primaryPadCents must be >= 0")
	}
	if c.HedgePadCents < 0 {
		return fmt.Errorf("hedgePadCents must be >= 0")
	}
	if c.CycleEndProtectionMinutes < 0 {
		return fmt.Errorf("cycleEndProtectionMinutes must be >= 0")
	}
	if c.MaxTradesPerCycle < 0 {
		return fmt.Errorf("maxTradesPerCycle must be >= 0")
	}
	return nil
}
