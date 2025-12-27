package volmm

import (
	"fmt"
	"strings"

	"github.com/betbot/gobet/pkg/config"
)

const ID = "volmm"

// Config: BTC 15m Up/Down 盘中波动做市（Delta 近中性）策略配置。
//
// 设计目标：
// - 全参数可配置（交易窗口、risk-only、点差、动能、下单频率、库存约束）
// - 默认值偏保守，适合小资金（如单期 40U）先跑起来
type Config struct {
	// ====== 交易窗口（秒）======
	// TradeStartAtSeconds: 开盘后多少秒开始交易（默认 0）。
	TradeStartAtSeconds int `yaml:"tradeStartAtSeconds" json:"tradeStartAtSeconds"`
	// TradeStopAtSeconds: 开盘后多少秒停止常规做市/交易（默认 12*60）。
	TradeStopAtSeconds int `yaml:"tradeStopAtSeconds" json:"tradeStopAtSeconds"`

	// ====== 风控窗口（risk-only）======
	RiskOnlyEnabled         *bool   `yaml:"riskOnlyEnabled" json:"riskOnlyEnabled"`
	RiskOnlyCancelAllQuotes *bool   `yaml:"riskOnlyCancelAllQuotes" json:"riskOnlyCancelAllQuotes"`
	RiskOnlyAllowFlatten    *bool   `yaml:"riskOnlyAllowFlatten" json:"riskOnlyAllowFlatten"`
	RiskOnlyMaxDeltaShares  float64 `yaml:"riskOnlyMaxDeltaShares" json:"riskOnlyMaxDeltaShares"` // 允许残留净敞口（shares）

	FlattenIntervalMs   int     `yaml:"flattenIntervalMs" json:"flattenIntervalMs"`
	FlattenMaxOrderSize float64 `yaml:"flattenMaxOrderSize" json:"flattenMaxOrderSize"` // 单次降风险最大 shares

	// ====== 报价/订单管理 ======
	QuoteSizeShares        float64 `yaml:"quoteSizeShares" json:"quoteSizeShares"`
	DeltaMaxShares         float64 `yaml:"deltaMaxShares" json:"deltaMaxShares"` // 做市阶段允许的净敞口上限（shares），用于 skew 归一化
	QuoteIntervalMs        int     `yaml:"quoteIntervalMs" json:"quoteIntervalMs"`
	ReplaceThresholdTicks  int     `yaml:"replaceThresholdTicks" json:"replaceThresholdTicks"` // 目标价格变化超过 N 个 tick 才撤改
	CancelOnWindowSwitch   *bool   `yaml:"cancelOnWindowSwitch" json:"cancelOnWindowSwitch"`   // 进入 risk-only 是否强制撤单（与 RiskOnlyCancelAllQuotes 类似，保留别名）

	// ====== 市场质量 gate（可选）======
	EnableMarketQualityGate *bool `yaml:"enableMarketQualityGate" json:"enableMarketQualityGate"`
	MarketQualityMinScore   int   `yaml:"marketQualityMinScore" json:"marketQualityMinScore"`
	MarketQualityMaxBookAgeMs int `yaml:"marketQualityMaxBookAgeMs" json:"marketQualityMaxBookAgeMs"`
	MarketQualityMaxSpreadCents int `yaml:"marketQualityMaxSpreadCents" json:"marketQualityMaxSpreadCents"`

	// ====== 定价：距离 + 时间 + 动能（Chainlink）======
	// MarketIntervalSeconds: 周期长度（秒）。默认从全局 market spec 推导；不可用时默认为 900。
	MarketIntervalSeconds int `yaml:"marketIntervalSeconds" json:"marketIntervalSeconds"`

	// ChainlinkSymbol: RTDS chainlink 的 symbol（默认从全局 market 推导：btc/usd）。
	ChainlinkSymbol string `yaml:"chainlinkSymbol" json:"chainlinkSymbol"`

	// 基础映射：p = Φ( K * ( (S - S0)/sqrt(t) ) + C )
	K float64 `yaml:"k" json:"k"`
	C float64 `yaml:"c" json:"c"`
	PMin float64 `yaml:"pMin" json:"pMin"` // 概率下限（避免 0/1）

	// 动能修正（可选）：z += Kv*velNorm + Ka*accNorm
	Kv float64 `yaml:"kv" json:"kv"`
	Ka float64 `yaml:"ka" json:"ka"`
	VelWindowSeconds int `yaml:"velWindowSeconds" json:"velWindowSeconds"`
	AccWindowSeconds int `yaml:"accWindowSeconds" json:"accWindowSeconds"`
	VolLookbackSeconds int `yaml:"volLookbackSeconds" json:"volLookbackSeconds"`

	// ====== 点差（概率空间）======
	// s = max(SMin, Alpha*|velNorm|, Beta/sqrt(t))
	SMin  float64 `yaml:"sMin" json:"sMin"`
	Alpha float64 `yaml:"alpha" json:"alpha"`
	Beta  float64 `yaml:"beta" json:"beta"`

	// ====== 库存倾斜 ======
	// skew = KDelta * clip(deltaInv/deltaMaxShares, -1, 1) * s
	KDelta float64 `yaml:"kDelta" json:"kDelta"`
}

func boolPtr(b bool) *bool { return &b }

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config 不能为空")
	}

	// defaults
	if c.TradeStartAtSeconds < 0 {
		c.TradeStartAtSeconds = 0
	}
	if c.TradeStopAtSeconds <= 0 {
		c.TradeStopAtSeconds = 12 * 60
	}
	if c.RiskOnlyEnabled == nil {
		c.RiskOnlyEnabled = boolPtr(true)
	}
	if c.RiskOnlyCancelAllQuotes == nil {
		c.RiskOnlyCancelAllQuotes = boolPtr(true)
	}
	if c.RiskOnlyAllowFlatten == nil {
		c.RiskOnlyAllowFlatten = boolPtr(true)
	}
	if c.FlattenIntervalMs <= 0 {
		c.FlattenIntervalMs = 500
	}
	if c.FlattenMaxOrderSize <= 0 {
		c.FlattenMaxOrderSize = 10
	}

	if c.QuoteSizeShares <= 0 {
		c.QuoteSizeShares = 5
	}
	if c.DeltaMaxShares <= 0 {
		c.DeltaMaxShares = 10
	}
	if c.QuoteIntervalMs <= 0 {
		c.QuoteIntervalMs = 250
	}
	if c.ReplaceThresholdTicks <= 0 {
		c.ReplaceThresholdTicks = 3
	}
	if c.CancelOnWindowSwitch == nil {
		// 与 RiskOnlyCancelAllQuotes 保持一致默认
		c.CancelOnWindowSwitch = boolPtr(true)
	}

	if c.EnableMarketQualityGate == nil {
		c.EnableMarketQualityGate = boolPtr(false) // 默认先关闭，避免“没数据/被 gate”导致看起来不工作
	}
	if c.MarketQualityMinScore <= 0 {
		c.MarketQualityMinScore = 70
	}
	if c.MarketQualityMaxBookAgeMs <= 0 {
		c.MarketQualityMaxBookAgeMs = 3000
	}
	if c.MarketQualityMaxSpreadCents <= 0 {
		c.MarketQualityMaxSpreadCents = 10
	}

	if c.MarketIntervalSeconds <= 0 {
		c.MarketIntervalSeconds = 900
		if gc := config.Get(); gc != nil {
			if sp, err := gc.Market.Spec(); err == nil {
				c.MarketIntervalSeconds = int(sp.Duration().Seconds())
			}
		}
	}

	if strings.TrimSpace(c.ChainlinkSymbol) == "" {
		// 从全局 market 推导（btc/usd）
		sym := "btc"
		if gc := config.Get(); gc != nil {
			if strings.TrimSpace(gc.Market.Symbol) != "" {
				sym = strings.TrimSpace(gc.Market.Symbol)
			}
		}
		c.ChainlinkSymbol = strings.ToLower(sym) + "/usd"
	}

	if c.K == 0 {
		c.K = 0.08
	}
	// 默认不偏置
	// c.C 默认 0
	if c.PMin <= 0 {
		c.PMin = 0.01
	}
	if c.PMin >= 0.5 {
		return fmt.Errorf("pMin 过大：必须 < 0.5")
	}
	if c.Kv == 0 {
		c.Kv = 0.5
	}
	if c.Ka == 0 {
		c.Ka = 0.2
	}
	if c.VelWindowSeconds <= 0 {
		c.VelWindowSeconds = 10
	}
	if c.AccWindowSeconds <= 0 {
		c.AccWindowSeconds = 30
	}
	if c.VolLookbackSeconds <= 0 {
		c.VolLookbackSeconds = 120
	}

	if c.SMin <= 0 {
		c.SMin = 0.003
	}
	if c.Alpha <= 0 {
		c.Alpha = 0.003
	}
	if c.Beta <= 0 {
		c.Beta = 0.02
	}

	if c.KDelta == 0 {
		c.KDelta = 1.0
	}

	// sanity
	if c.TradeStopAtSeconds < c.TradeStartAtSeconds {
		return fmt.Errorf("tradeStopAtSeconds 必须 >= tradeStartAtSeconds")
	}
	if c.QuoteSizeShares <= 0 {
		return fmt.Errorf("quoteSizeShares 必须 > 0")
	}
	if c.DeltaMaxShares <= 0 {
		return fmt.Errorf("deltaMaxShares 必须 > 0")
	}
	if c.QuoteIntervalMs < 50 {
		// 过低会造成大量撤改
		c.QuoteIntervalMs = 50
	}

	return nil
}

