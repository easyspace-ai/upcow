package binancepredict

import (
	"fmt"

	"github.com/betbot/gobet/internal/strategies/common"
)

const ID = "binancepredict"

// Config：基于 Binance 秒级 K 线预测的镜像套利策略配置
//
// 策略目标：
// - 使用 Binance Futures 1s K 线预测 BTC 涨跌方向
// - 在 Polymarket 镜像订单薄上进行双向对冲套利
// - Entry（Taker）+ Hedge（Maker）锁定利润
type Config struct {
	// ===== 交易参数 =====
	OrderSize float64 `yaml:"orderSize" json:"orderSize"` // 每次交易 shares

	// SkipBalanceCheck：跳过本地下单前的 USDC 余额预检查
	SkipBalanceCheck bool `yaml:"skipBalanceCheck" json:"skipBalanceCheck"`

	// MaxTotalCapitalUSDC：最大总资金限制（USDC）
	// 当总持仓价值（UP + DOWN）超过此限制时，禁止开新单
	// 0 表示不限制
	MaxTotalCapitalUSDC float64 `yaml:"maxTotalCapitalUSDC" json:"maxTotalCapitalUSDC"`

	// RequireFullyHedgedBeforeNewEntry：是否要求完全对冲后才能开新单
	RequireFullyHedgedBeforeNewEntry bool `yaml:"requireFullyHedgedBeforeNewEntry" json:"requireFullyHedgedBeforeNewEntry"`

	// ===== Binance 预测参数 =====
	PredictionWindowSeconds int `yaml:"predictionWindowSeconds" json:"predictionWindowSeconds"` // 预测窗口（秒）
	MinPriceChangeBps        int `yaml:"minPriceChangeBps" json:"minPriceChangeBps"`             // 最小价格变化（bps）才触发预测
	PredictionCooldownMs     int `yaml:"predictionCooldownMs" json:"predictionCooldownMs"`       // 预测冷却时间（毫秒）

	// ===== 订单参数 =====
	EntryPriceOffsetCents int `yaml:"entryPriceOffsetCents" json:"entryPriceOffsetCents"` // Entry 价格偏移（cents，0=best ask/bid）
	HedgePriceOffsetCents  int `yaml:"hedgePriceOffsetCents" json:"hedgePriceOffsetCents"` // Hedge 价格偏移（cents，负数=比镜像价低）
	MinProfitCents         int `yaml:"minProfitCents" json:"minProfitCents"`                 // 最小利润要求（cents）

	// ===== 风险控制 =====
	HedgeTimeoutSeconds    int  `yaml:"hedgeTimeoutSeconds" json:"hedgeTimeoutSeconds"`     // Hedge 超时时间（秒）
	MaxUnhedgedLossCents   int  `yaml:"maxUnhedgedLossCents" json:"maxUnhedgedLossCents"`   // 未对冲最大亏损（cents）
	EnableStopLoss         bool `yaml:"enableStopLoss" json:"enableStopLoss"`               // 启用止损
	CycleEndProtectionMinutes int `yaml:"cycleEndProtectionMinutes" json:"cycleEndProtectionMinutes"` // 周期结束前保护（分钟）

	// ===== 镜像价格验证 =====
	MaxMirrorDeviationCents int `yaml:"maxMirrorDeviationCents" json:"maxMirrorDeviationCents"` // 最大镜像偏差（cents），超过此值跳过交易

	// ===== 市场质量过滤 =====
	EnableMarketQualityGate     *bool `yaml:"enableMarketQualityGate" json:"enableMarketQualityGate"`
	MarketQualityMinScore       int   `yaml:"marketQualityMinScore" json:"marketQualityMinScore"`
	MarketQualityMaxSpreadCents int   `yaml:"marketQualityMaxSpreadCents" json:"marketQualityMaxSpreadCents"`
	MarketQualityMaxBookAgeMs   int   `yaml:"marketQualityMaxBookAgeMs" json:"marketQualityMaxBookAgeMs"`

	// ===== 自动合并订单 =====
	AutoMerge common.AutoMergeConfig `yaml:"autoMerge" json:"autoMerge"`
}

func boolPtr(b bool) *bool { return &b }

func (c *Config) Validate() error {
	c.AutoMerge.Normalize()

	if c.OrderSize <= 0 {
		return fmt.Errorf("orderSize 必须 > 0")
	}

	// Binance 预测参数默认值
	if c.PredictionWindowSeconds <= 0 {
		c.PredictionWindowSeconds = 5
	}
	if c.MinPriceChangeBps <= 0 {
		c.MinPriceChangeBps = 10
	}
	if c.PredictionCooldownMs <= 0 {
		c.PredictionCooldownMs = 1000
	}

	// 订单参数默认值
	if c.EntryPriceOffsetCents == 0 {
		c.EntryPriceOffsetCents = 0 // 默认使用 best ask/bid
	}
	if c.HedgePriceOffsetCents == 0 {
		c.HedgePriceOffsetCents = -1 // 默认比镜像价低 1 cent
	}
	if c.MinProfitCents <= 0 {
		c.MinProfitCents = 2 // 默认最小利润 2 cents
	}

	// 风险控制默认值
	if c.HedgeTimeoutSeconds <= 0 {
		c.HedgeTimeoutSeconds = 10
	}
	if c.MaxUnhedgedLossCents <= 0 {
		c.MaxUnhedgedLossCents = 5
	}
	if c.CycleEndProtectionMinutes <= 0 {
		c.CycleEndProtectionMinutes = 2
	}

	// 镜像价格验证默认值
	if c.MaxMirrorDeviationCents <= 0 {
		c.MaxMirrorDeviationCents = 1 // 默认允许 1 cent 偏差
	}

	// 市场质量过滤默认值
	if c.EnableMarketQualityGate == nil {
		c.EnableMarketQualityGate = boolPtr(true)
	}
	if c.MarketQualityMinScore <= 0 {
		c.MarketQualityMinScore = 60
	}
	if c.MarketQualityMinScore < 0 || c.MarketQualityMinScore > 100 {
		return fmt.Errorf("marketQualityMinScore 必须在 0-100 之间")
	}
	if c.MarketQualityMaxBookAgeMs <= 0 {
		c.MarketQualityMaxBookAgeMs = 2500
	}
	if c.MarketQualityMaxSpreadCents < 0 {
		return fmt.Errorf("marketQualityMaxSpreadCents 不能为负数")
	}

	// 总资金限制默认值
	if c.MaxTotalCapitalUSDC < 0 {
		c.MaxTotalCapitalUSDC = 0
	}

	// 默认启用"完全对冲后才能开新单"
	if !c.RequireFullyHedgedBeforeNewEntry {
		c.RequireFullyHedgedBeforeNewEntry = true
	}

	return nil
}
