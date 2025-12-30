package velocityhedgehold

import (
	"fmt"

	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/sirupsen/logrus"
)

const ID = "velocityhedgehold"

// Config：动量触发 Entry（taker FAK）+ 对侧互补价挂 Hedge（maker GTC）。
//
// 策略目标：
// - Hedge 成功：双边等量持仓，持有到结算（不做止盈/主动出场）。
// - 注意：按当前策略版本/配置，本策略默认不做止损（只做对冲与重挂）。
type Config struct {
	// ===== 交易参数 =====
	OrderSize float64 `yaml:"orderSize" json:"orderSize"` // Entry 期望下单 shares（最终以实际成交为准）

	// SkipBalanceCheck：跳过本地下单前的 USDC 余额预检查（OrderEngine 内）。
	// 用于解决“启动/周期切换时余额尚未初始化，短暂显示 0 导致误拒单”的问题。
	// 注意：这只是跳过本地检查；若账户确实没钱，交易所仍会拒单。
	SkipBalanceCheck bool `yaml:"skipBalanceCheck" json:"skipBalanceCheck"`

	// StrictOneToOneHedge：严格“一对一对冲”。
	// - true：对冲单不会被系统自动放大（禁用 minOrderSize/minShareSize 自动调整）；若达不到最小金额则不下单并持续重试。
	// - false：允许系统为满足最小金额/最小份额自动调整（可能导致过度对冲）。
	StrictOneToOneHedge *bool `yaml:"strictOneToOneHedge" json:"strictOneToOneHedge"`

	// EnableHedgeTakerFallback：当剩余对冲量太小无法用 GTC 完成时，是否允许用 FAK(taker) 做小额兜底对冲。
	EnableHedgeTakerFallback *bool `yaml:"enableHedgeTakerFallback" json:"enableHedgeTakerFallback"`

	// HedgeMonitorIntervalMs：对冲监控轮询间隔（毫秒）。越小对冲越积极，但 IO/撤单会更频繁。
	HedgeMonitorIntervalMs int `yaml:"hedgeMonitorIntervalMs" json:"hedgeMonitorIntervalMs"`

	// ===== 动量信号参数 =====
	WindowSeconds          int     `yaml:"windowSeconds" json:"windowSeconds"`                   // 速度计算窗口（秒）
	MinMoveCents           int     `yaml:"minMoveCents" json:"minMoveCents"`                     // 窗口内最小上行位移（分）
	MinVelocityCentsPerSec float64 `yaml:"minVelocityCentsPerSec" json:"minVelocityCentsPerSec"` // 最小速度（分/秒）
	CooldownMs             int     `yaml:"cooldownMs" json:"cooldownMs"`                         // 触发冷却（毫秒）
	WarmupMs               int     `yaml:"warmupMs" json:"warmupMs"`                             // 周期切换/启动预热（毫秒）
	MaxTradesPerCycle      int     `yaml:"maxTradesPerCycle" json:"maxTradesPerCycle"`           // 每周期最多触发次数（0=不设限）
	OncePerCycle           bool    `yaml:"oncePerCycle" json:"oncePerCycle"`                     // [兼容] 已废弃

	// ===== 信号模式（为“绝对变化/双向变化/盘口跳变”服务）=====
	// signalMode:
	// - "abs"：单边价格的绝对变化（双向）；delta>0 买 signalToken，delta<0 买 opposite(signalToken)
	// - "legacy"：旧逻辑（分别计算 UP/DOWN 的上行速度，只在 delta>0 时触发）
	SignalMode string `yaml:"signalMode" json:"signalMode"`
	// signalToken: "up" | "down"（仅在 signalMode=abs 时生效；表示你盯哪一边做信号）
	SignalToken string `yaml:"signalToken" json:"signalToken"`
	// signalSource:
	// - "event"：使用 PriceChangedEvent.NewPrice（最后成交价/中间价兜底）
	// - "best_mid"：使用 WS best_bid/best_ask 的 mid（盘口跳变更敏感）
	// - "best_ask"：使用 WS best_ask（更贴近 taker 成本）
	// - "best_bid"：使用 WS best_bid（更贴近可卖出价）
	SignalSource string `yaml:"signalSource" json:"signalSource"`

	// ===== 对冲定价参数 =====
	// Hedge 互补挂单价 = 100 - entryPriceCents - hedgeOffsetCents
	// 理论上：若 Hedge 以该价成交，则两腿总成本 <= 100 - hedgeOffsetCents（cents）。
	HedgeOffsetCents int `yaml:"hedgeOffsetCents" json:"hedgeOffsetCents"`

	// 对冲重挂：若在该时间内未成交，撤单重挂（仍遵守互补价上界 + 不穿价）。
	HedgeReorderTimeoutSeconds int `yaml:"hedgeReorderTimeoutSeconds" json:"hedgeReorderTimeoutSeconds"`

	// ===== 过度对冲控制 =====
	// 当 Hedge 订单金额 < minOrderSize 时，是否允许适度过度对冲以满足最小金额要求
	// true：允许适度过度对冲（不超过 MaxOverHedgeRatio）
	// false：直接止损（保守策略）
	AllowModerateOverHedge bool `yaml:"allowModerateOverHedge" json:"allowModerateOverHedge"`
	// 最大过度对冲比例（例如 0.1 表示允许 hedgeShares 最多比 entryFilledSize 多 10%）
	MaxOverHedgeRatio float64 `yaml:"maxOverHedgeRatio" json:"maxOverHedgeRatio"`

	// ===== 未对冲止损（已废弃/禁用）=====
	// 兼容保留：当前版本按用户要求不做止损，仅做持续对冲。
	UnhedgedMaxSeconds    int `yaml:"unhedgedMaxSeconds" json:"unhedgedMaxSeconds"`
	UnhedgedStopLossCents int `yaml:"unhedgedStopLossCents" json:"unhedgedStopLossCents"`

	// ===== 周期尾部保护 =====
	CycleEndProtectionMinutes int `yaml:"cycleEndProtectionMinutes" json:"cycleEndProtectionMinutes"`

	// ===== 价格优先选择（当 UP/DOWN 都满足速度条件） =====
	PreferHigherPrice      bool `yaml:"preferHigherPrice" json:"preferHigherPrice"`
	MinPreferredPriceCents int  `yaml:"minPreferredPriceCents" json:"minPreferredPriceCents"`

	// ===== 市场质量过滤 =====
	EnableMarketQualityGate     *bool `yaml:"enableMarketQualityGate" json:"enableMarketQualityGate"`
	MarketQualityMinScore       int   `yaml:"marketQualityMinScore" json:"marketQualityMinScore"`
	MarketQualityMaxSpreadCents int   `yaml:"marketQualityMaxSpreadCents" json:"marketQualityMaxSpreadCents"`
	MarketQualityMaxBookAgeMs   int   `yaml:"marketQualityMaxBookAgeMs" json:"marketQualityMaxBookAgeMs"`

	// ===== Binance bias / confirm（可选，与 velocityfollow 保持一致思路） =====
	UseBinanceOpen1mBias bool   `yaml:"useBinanceOpen1mBias" json:"useBinanceOpen1mBias"`
	BiasMode             string `yaml:"biasMode" json:"biasMode"` // "hard" | "soft"
	Open1mMaxWaitSeconds int    `yaml:"open1mMaxWaitSeconds" json:"open1mMaxWaitSeconds"`
	Open1mMinBodyBps     int    `yaml:"open1mMinBodyBps" json:"open1mMinBodyBps"`
	Open1mMaxWickBps     int    `yaml:"open1mMaxWickBps" json:"open1mMaxWickBps"`
	RequireBiasReady     bool   `yaml:"requireBiasReady" json:"requireBiasReady"`

	OppositeBiasVelocityMultiplier float64 `yaml:"oppositeBiasVelocityMultiplier" json:"oppositeBiasVelocityMultiplier"`
	OppositeBiasMinMoveExtraCents  int     `yaml:"oppositeBiasMinMoveExtraCents" json:"oppositeBiasMinMoveExtraCents"`

	// 秒级方向 bias：用 Binance 1s Kline 的短窗方向作为“胜率更高一方”的优先判定。
	// 典型用法：
	// - BiasMode=hard：只允许顺着 fast bias 方向开仓（更像“胜率过滤器”）
	// - BiasMode=soft：只对逆势方向提高阈值（更像“降噪/降频”）
	UseBinanceFastBias     bool `yaml:"useBinanceFastBias" json:"useBinanceFastBias"`
	FastBiasWindowSeconds  int  `yaml:"fastBiasWindowSeconds" json:"fastBiasWindowSeconds"`   // 计算窗口（秒），默认 30
	FastBiasMinMoveBps     int  `yaml:"fastBiasMinMoveBps" json:"fastBiasMinMoveBps"`         // 触发 bias 的最小底层波动（bps），默认 15
	FastBiasMinHoldSeconds int  `yaml:"fastBiasMinHoldSeconds" json:"fastBiasMinHoldSeconds"` // bias 最小保持时间（秒），默认 2（抗抖）

	UseBinanceMoveConfirm    bool `yaml:"useBinanceMoveConfirm" json:"useBinanceMoveConfirm"`
	MoveConfirmWindowSeconds int  `yaml:"moveConfirmWindowSeconds" json:"moveConfirmWindowSeconds"`
	MinUnderlyingMoveBps     int  `yaml:"minUnderlyingMoveBps" json:"minUnderlyingMoveBps"`

	AutoMerge common.AutoMergeConfig `yaml:"autoMerge" json:"autoMerge"`
}

func boolPtr(b bool) *bool { return &b }

func (c *Config) Validate() error {
	c.AutoMerge.Normalize()

	if c.OrderSize <= 0 {
		return fmt.Errorf("orderSize 必须 > 0")
	}
	if c.WindowSeconds <= 0 {
		c.WindowSeconds = 10
	}
	if c.MinMoveCents <= 0 {
		c.MinMoveCents = 3
	}
	if c.MinVelocityCentsPerSec <= 0 {
		c.MinVelocityCentsPerSec = float64(c.MinMoveCents) / float64(c.WindowSeconds)
	}

	// 信号模式默认：按你的需求启用“绝对变化/双向变化”；并默认用盘口 mid 作为信号源
	if c.SignalMode == "" {
		c.SignalMode = "abs"
	}
	switch c.SignalMode {
	case "abs", "legacy":
	default:
		return fmt.Errorf("signalMode 必须是 abs 或 legacy")
	}
	if c.SignalToken == "" {
		c.SignalToken = "up"
	}
	if c.SignalToken != "up" && c.SignalToken != "down" {
		return fmt.Errorf("signalToken 必须是 up 或 down")
	}
	if c.SignalSource == "" {
		c.SignalSource = "best_mid"
	}
	switch c.SignalSource {
	case "event", "best_mid", "best_ask", "best_bid":
	default:
		return fmt.Errorf("signalSource 必须是 event/best_mid/best_ask/best_bid")
	}
	if c.CooldownMs <= 0 {
		c.CooldownMs = 1500
	}
	if c.WarmupMs < 0 {
		c.WarmupMs = 0
	}
	if c.MaxTradesPerCycle < 0 {
		c.MaxTradesPerCycle = 0
	}
	if c.OncePerCycle && c.MaxTradesPerCycle == 0 {
		c.MaxTradesPerCycle = 1
		logrus.Warnf("[%s] oncePerCycle 已废弃，已自动设置 maxTradesPerCycle=1，建议直接使用 maxTradesPerCycle", ID)
	}

	if c.HedgeOffsetCents <= 0 {
		c.HedgeOffsetCents = 3
	}
	if c.HedgeReorderTimeoutSeconds <= 0 {
		c.HedgeReorderTimeoutSeconds = 30
	}

	// 对冲行为默认值（按“只对冲/尽量一对一”偏保守）
	if c.StrictOneToOneHedge == nil {
		c.StrictOneToOneHedge = boolPtr(true)
	}
	if c.EnableHedgeTakerFallback == nil {
		c.EnableHedgeTakerFallback = boolPtr(true)
	}
	if c.HedgeMonitorIntervalMs <= 0 {
		c.HedgeMonitorIntervalMs = 1000
	}
	if c.HedgeMonitorIntervalMs < 100 {
		c.HedgeMonitorIntervalMs = 100
	}
	// 过度对冲控制默认值
	if c.MaxOverHedgeRatio <= 0 {
		c.MaxOverHedgeRatio = 0.1 // 默认允许 10% 的过度对冲
	}
	if c.MaxOverHedgeRatio > 0.5 {
		return fmt.Errorf("maxOverHedgeRatio 不能超过 0.5（50%%）")
	}
	if c.UnhedgedMaxSeconds <= 0 {
		c.UnhedgedMaxSeconds = 120
	}
	if c.UnhedgedStopLossCents < 0 {
		return fmt.Errorf("unhedgedStopLossCents 不能为负数")
	}
	if c.CycleEndProtectionMinutes <= 0 {
		c.CycleEndProtectionMinutes = 3
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
	if c.MarketQualityMaxBookAgeMs <= 0 {
		c.MarketQualityMaxBookAgeMs = 3000
	}
	if c.MarketQualityMaxBookAgeMs < 0 {
		return fmt.Errorf("marketQualityMaxBookAgeMs 不能为负数")
	}
	if c.MarketQualityMaxSpreadCents < 0 {
		return fmt.Errorf("marketQualityMaxSpreadCents 不能为负数")
	}

	// Binance bias defaults
	if c.BiasMode == "" {
		c.BiasMode = "hard"
	}
	if c.BiasMode != "hard" && c.BiasMode != "soft" {
		return fmt.Errorf("biasMode 必须是 hard 或 soft")
	}
	if c.Open1mMaxWaitSeconds <= 0 {
		c.Open1mMaxWaitSeconds = 120
	}
	if c.Open1mMinBodyBps <= 0 {
		c.Open1mMinBodyBps = 3
	}
	if c.Open1mMaxWickBps <= 0 {
		c.Open1mMaxWickBps = 25
	}
	if c.UseBinanceOpen1mBias && !c.RequireBiasReady {
		c.RequireBiasReady = true
	}
	if c.OppositeBiasVelocityMultiplier <= 0 {
		c.OppositeBiasVelocityMultiplier = 1.5
	}
	if c.OppositeBiasMinMoveExtraCents < 0 {
		c.OppositeBiasMinMoveExtraCents = 0
	}

	// Binance fast bias defaults
	if c.FastBiasWindowSeconds <= 0 {
		c.FastBiasWindowSeconds = 30
	}
	if c.FastBiasMinMoveBps <= 0 {
		c.FastBiasMinMoveBps = 15
	}
	if c.FastBiasMinHoldSeconds <= 0 {
		c.FastBiasMinHoldSeconds = 2
	}
	// fast bias 与 move confirm 独立，不做联动默认值

	// Binance move confirm defaults
	if c.MoveConfirmWindowSeconds <= 0 {
		c.MoveConfirmWindowSeconds = 60
	}
	if c.MinUnderlyingMoveBps <= 0 {
		c.MinUnderlyingMoveBps = 20
	}

	if c.PreferHigherPrice && c.MinPreferredPriceCents <= 0 {
		c.MinPreferredPriceCents = 50
	}
	return nil
}
