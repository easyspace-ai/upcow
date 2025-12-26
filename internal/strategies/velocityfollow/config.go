package velocityfollow

import (
	"fmt"

	"github.com/sirupsen/logrus"
)

const ID = "velocityfollow"

// PartialTakeProfit 分批止盈配置：当利润达到 profitCents 时，卖出 fraction 比例的当前持仓。
// 注意：
// - fraction 范围 (0,1]，表示“当前剩余持仓”的比例（非初始持仓）。
// - 同一 level 只会触发一次（由策略内部状态跟踪）。
type PartialTakeProfit struct {
	ProfitCents int     `yaml:"profitCents" json:"profitCents"`
	Fraction    float64 `yaml:"fraction" json:"fraction"`
}

// Config：监控价格变化速度，触发“强势侧吃单 + 弱势侧挂互补限价单”。
//
// 例：UP 迅速拉升到 70c，触发：
// - 吃单买 UP @ 70c
// - 同时挂 DOWN 买单 @ (100-70-3)=27c
type Config struct {
	// 交易参数
	OrderSize      float64 `yaml:"orderSize" json:"orderSize"`           // 吃单买入 shares
	HedgeOrderSize float64 `yaml:"hedgeOrderSize" json:"hedgeOrderSize"` // 对侧挂单 shares（0 表示跟随 orderSize）

	// “速度快”判定参数（建议从保守开始）
	WindowSeconds          int     `yaml:"windowSeconds" json:"windowSeconds"`                   // 速度计算窗口（秒）
	MinMoveCents           int     `yaml:"minMoveCents" json:"minMoveCents"`                     // 窗口内最小上行位移（分）
	MinVelocityCentsPerSec float64 `yaml:"minVelocityCentsPerSec" json:"minVelocityCentsPerSec"` // 最小速度（分/秒）
	CooldownMs             int     `yaml:"cooldownMs" json:"cooldownMs"`                         // 触发冷却（毫秒）
	OncePerCycle           bool    `yaml:"oncePerCycle" json:"oncePerCycle"`                     // [已废弃] 每周期最多触发一次，请使用 maxTradesPerCycle
	WarmupMs               int     `yaml:"warmupMs" json:"warmupMs"`                             // 启动/换周期后的预热窗口（毫秒）
	MaxTradesPerCycle      int     `yaml:"maxTradesPerCycle" json:"maxTradesPerCycle"`           // 每周期最多交易次数（0=不设限）

	// 下单安全参数
	HedgeOffsetCents   int `yaml:"hedgeOffsetCents" json:"hedgeOffsetCents"`     // 对侧挂单 = (100 - entryAskCents - offset)
	MinEntryPriceCents int `yaml:"minEntryPriceCents" json:"minEntryPriceCents"` // 吃单价下限（分），避免低价时 size 被放大（静态配置，动态调整启用时会被覆盖）
	MaxEntryPriceCents int `yaml:"maxEntryPriceCents" json:"maxEntryPriceCents"` // 吃单价上限（分），避免 99/100 假盘口（静态配置，动态调整启用时会被覆盖）
	MaxSpreadCents     int `yaml:"maxSpreadCents" json:"maxSpreadCents"`         // 盘口价差上限（分），避免极差盘口误触发

	// Binance K线融合（可选）：用“本周期开盘第 1 根 1m K 线阴阳”作为 bias/过滤器
	UseBinanceOpen1mBias bool   `yaml:"useBinanceOpen1mBias" json:"useBinanceOpen1mBias"`
	BiasMode             string `yaml:"biasMode" json:"biasMode"` // "hard" | "soft"
	Open1mMaxWaitSeconds int    `yaml:"open1mMaxWaitSeconds" json:"open1mMaxWaitSeconds"`
	Open1mMinBodyBps     int    `yaml:"open1mMinBodyBps" json:"open1mMinBodyBps"` // 实体最小阈值（bps，1bp=0.01%）
	Open1mMaxWickBps     int    `yaml:"open1mMaxWickBps" json:"open1mMaxWickBps"` // 影线最大阈值（bps）
	RequireBiasReady     bool   `yaml:"requireBiasReady" json:"requireBiasReady"` // 开启 bias 后，是否必须等开盘 1m 收线再允许交易

	// 当候选方向与开盘 1m bias 相反时，提高触发门槛（仅 BiasMode="soft" 时生效）
	OppositeBiasVelocityMultiplier float64 `yaml:"oppositeBiasVelocityMultiplier" json:"oppositeBiasVelocityMultiplier"`
	OppositeBiasMinMoveExtraCents  int     `yaml:"oppositeBiasMinMoveExtraCents" json:"oppositeBiasMinMoveExtraCents"`

	// 可选：用 Binance 1s 方向做确认（借鉴 momentum bot 的"底层硬动"过滤）
	UseBinanceMoveConfirm    bool `yaml:"useBinanceMoveConfirm" json:"useBinanceMoveConfirm"`
	MoveConfirmWindowSeconds int  `yaml:"moveConfirmWindowSeconds" json:"moveConfirmWindowSeconds"` // lookback 秒数
	MinUnderlyingMoveBps     int  `yaml:"minUnderlyingMoveBps" json:"minUnderlyingMoveBps"`         // 最小底层波动（bps）

	// 价格优先选择：当 UP/DOWN 都满足速度条件时，优先选择价格更高的一边
	// 因为订单簿是镜像的，速度通常相同，价格更高的胜率更大
	PreferHigherPrice      bool `yaml:"preferHigherPrice" json:"preferHigherPrice"`           // 是否启用价格优先选择
	MinPreferredPriceCents int  `yaml:"minPreferredPriceCents" json:"minPreferredPriceCents"` // 优先价格阈值（分），例如 50 或 60.60（转换为 6060）

	// 订单执行模式：sequential（顺序）或 parallel（并发）
	// sequential: 先下 Entry 订单，等待成交后再下 Hedge 订单（风险低，速度慢）
	// parallel: 同时提交 Entry 和 Hedge 订单（速度快，风险高）
	OrderExecutionMode string `yaml:"orderExecutionMode" json:"orderExecutionMode"` // "sequential" | "parallel"，默认 "sequential"

	// 顺序下单模式的参数（仅在 orderExecutionMode="sequential" 时生效）
	SequentialCheckIntervalMs int `yaml:"sequentialCheckIntervalMs" json:"sequentialCheckIntervalMs"` // 检查订单状态的间隔（毫秒），默认 20ms（更频繁）
	SequentialMaxWaitMs       int `yaml:"sequentialMaxWaitMs" json:"sequentialMaxWaitMs"`             // 最大等待时间（毫秒），默认 2000ms（FAK 订单通常立即成交，但纸交易模式可能需要更长时间）

	// 周期结束保护：在周期结束前 N 分钟不开新单（降低风险）
	CycleEndProtectionMinutes int `yaml:"cycleEndProtectionMinutes" json:"cycleEndProtectionMinutes"` // 周期结束前保护时间（分钟），默认 3 分钟

	// 对冲单重下机制：主单成交后，如果对冲单在指定时间内未成交，重新下单
	HedgeReorderTimeoutSeconds int `yaml:"hedgeReorderTimeoutSeconds" json:"hedgeReorderTimeoutSeconds"` // 对冲单重下超时时间（秒），默认 30 秒

	// 库存偏斜机制：当净持仓超过阈值时，降低该方向的交易频率
	InventoryThreshold float64 `yaml:"inventoryThreshold" json:"inventoryThreshold"` // 净持仓阈值（shares），默认 0（禁用）

	// ====== 市场质量过滤（提升胜率） ======
	EnableMarketQualityGate     *bool `yaml:"enableMarketQualityGate" json:"enableMarketQualityGate"`         // 是否启用盘口质量 gate（默认 true）
	MarketQualityMinScore       int   `yaml:"marketQualityMinScore" json:"marketQualityMinScore"`             // 最小质量分（0..100，默认 70）
	MarketQualityMaxSpreadCents int   `yaml:"marketQualityMaxSpreadCents" json:"marketQualityMaxSpreadCents"` // 最大一档价差（分，默认使用 maxSpreadCents；<=0 表示使用 maxSpreadCents）
	MarketQualityMaxBookAgeMs   int   `yaml:"marketQualityMaxBookAgeMs" json:"marketQualityMaxBookAgeMs"`     // WS 盘口最大年龄（毫秒，默认 3000）

	// ====== 出场（平仓）参数：让策略形成“可盈利闭环” ======
	// 说明：Entry 是买入（BUY），出场为卖出（SELL）。
	// - takeProfitCents / stopLossCents 是“相对入场均价”的价差（分），用当前 bestBid 触发并用 SELL FAK 执行。
	// - maxHoldSeconds 是时间止损：超过该时间仍未触发 TP/SL，则强制平仓。
	// - exitCooldownMs 防止频繁重复下卖单（与执行层去重共同作用）。
	TakeProfitCents       int   `yaml:"takeProfitCents" json:"takeProfitCents"`             // 止盈阈值（分，>=0；0=禁用）
	StopLossCents         int   `yaml:"stopLossCents" json:"stopLossCents"`                 // 止损阈值（分，>=0；0=禁用）
	MaxHoldSeconds        int   `yaml:"maxHoldSeconds" json:"maxHoldSeconds"`               // 最大持仓时间（秒，>=0；0=禁用）
	ExitCooldownMs        int   `yaml:"exitCooldownMs" json:"exitCooldownMs"`               // 出场冷却（毫秒，默认 1500）
	ExitBothSidesIfHedged *bool `yaml:"exitBothSidesIfHedged" json:"exitBothSidesIfHedged"` // 若同周期同时持有 UP/DOWN，则同时卖出平仓（默认 true）

	// ====== 分批止盈（提升盈亏比） ======
	PartialTakeProfits []PartialTakeProfit `yaml:"partialTakeProfits" json:"partialTakeProfits"` // 分批止盈列表（按 profitCents 递增建议）

	// ====== 追踪止盈（trailing） ======
	EnableTrailingTakeProfit bool `yaml:"enableTrailingTakeProfit" json:"enableTrailingTakeProfit"` // 是否启用追踪止盈
	TrailStartCents          int  `yaml:"trailStartCents" json:"trailStartCents"`                   // 达到该利润后开始追踪（分，默认 4）
	TrailDistanceCents       int  `yaml:"trailDistanceCents" json:"trailDistanceCents"`             // 回撤触发距离（分，默认 2）
}

func boolPtr(b bool) *bool { return &b }

func (c *Config) Validate() error {
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
		// 3c/10s = 0.3c/s
		c.MinVelocityCentsPerSec = float64(c.MinMoveCents) / float64(c.WindowSeconds)
	}
	if c.CooldownMs <= 0 {
		c.CooldownMs = 1500
	}
	if c.WarmupMs < 0 {
		c.WarmupMs = 0
	}
	// maxTradesPerCycle: 0 表示不设限，>0 表示限制次数
	// [向后兼容] 如果 oncePerCycle=true 且 maxTradesPerCycle=0，则自动设置为 1
	if c.MaxTradesPerCycle < 0 {
		c.MaxTradesPerCycle = 0
	}
	if c.OncePerCycle && c.MaxTradesPerCycle == 0 {
		c.MaxTradesPerCycle = 1
		logrus.Warnf("[velocityfollow] oncePerCycle 已废弃，已自动设置 maxTradesPerCycle=1，建议直接使用 maxTradesPerCycle")
	}
	if c.HedgeOffsetCents <= 0 {
		c.HedgeOffsetCents = 3
	}
	// minEntryPriceCents: 0 表示不设下限
	if c.MinEntryPriceCents < 0 {
		c.MinEntryPriceCents = 0
	}
	if c.MaxEntryPriceCents <= 0 {
		c.MaxEntryPriceCents = 95
	}
	if c.MaxSpreadCents < 0 {
		c.MaxSpreadCents = 0
	}
	if c.HedgeOrderSize < 0 {
		c.HedgeOrderSize = 0
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
		c.Open1mMinBodyBps = 3 // 0.03%
	}
	if c.Open1mMaxWickBps <= 0 {
		c.Open1mMaxWickBps = 25 // 0.25%
	}
	// 默认：如果你显式开启 bias，我们就等 1m 收线再做（更贴合你说的“阴阳影响很大”）
	if c.UseBinanceOpen1mBias && !c.RequireBiasReady {
		c.RequireBiasReady = true
	}
	if c.OppositeBiasVelocityMultiplier <= 0 {
		c.OppositeBiasVelocityMultiplier = 1.5
	}
	if c.OppositeBiasMinMoveExtraCents < 0 {
		c.OppositeBiasMinMoveExtraCents = 0
	}

	// Binance move confirm defaults
	if c.MoveConfirmWindowSeconds <= 0 {
		c.MoveConfirmWindowSeconds = 60
	}
	if c.MinUnderlyingMoveBps <= 0 {
		c.MinUnderlyingMoveBps = 20 // 0.20%
	}

	// 价格优先选择默认值
	if c.PreferHigherPrice && c.MinPreferredPriceCents <= 0 {
		c.MinPreferredPriceCents = 50 // 默认 50c
	}

	// 订单执行模式默认值
	if c.OrderExecutionMode == "" {
		c.OrderExecutionMode = "sequential" // 默认顺序下单（更安全）
	}
	if c.OrderExecutionMode != "sequential" && c.OrderExecutionMode != "parallel" {
		return fmt.Errorf("orderExecutionMode 必须是 sequential 或 parallel")
	}

	// 顺序下单模式参数默认值
	if c.SequentialCheckIntervalMs <= 0 {
		c.SequentialCheckIntervalMs = 20 // 默认 20ms（更频繁的检测，提高响应速度）
	}
	if c.SequentialMaxWaitMs <= 0 {
		c.SequentialMaxWaitMs = 2000 // 默认 2 秒（FAK 订单通常立即成交，但纸交易模式可能需要更长时间）
	}

	// 周期结束保护默认值
	if c.CycleEndProtectionMinutes <= 0 {
		c.CycleEndProtectionMinutes = 3 // 默认 3 分钟
	}

	// 对冲单重下机制默认值
	if c.HedgeReorderTimeoutSeconds <= 0 {
		c.HedgeReorderTimeoutSeconds = 30 // 默认 30 秒
	}

	// 库存偏斜机制默认值
	// 如果未设置，默认为 0（禁用）
	// 如果设置为 > 0，则启用库存偏斜机制
	// 建议值：根据订单大小设置，例如 orderSize=6.5，threshold=50 意味着约 7-8 个订单的净持仓
	if c.InventoryThreshold < 0 {
		c.InventoryThreshold = 0
	}

	// 市场质量 gate 默认值
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

	// 出场参数默认值与校验
	if c.TakeProfitCents < 0 {
		return fmt.Errorf("takeProfitCents 不能为负数")
	}
	if c.StopLossCents < 0 {
		return fmt.Errorf("stopLossCents 不能为负数")
	}
	if c.MaxHoldSeconds < 0 {
		return fmt.Errorf("maxHoldSeconds 不能为负数")
	}
	if c.ExitCooldownMs <= 0 {
		c.ExitCooldownMs = 1500
	}
	if c.ExitBothSidesIfHedged == nil {
		c.ExitBothSidesIfHedged = boolPtr(true)
	}

	// 分批止盈校验
	for i, lv := range c.PartialTakeProfits {
		if lv.ProfitCents <= 0 {
			return fmt.Errorf("partialTakeProfits[%d].profitCents 必须 > 0", i)
		}
		if lv.Fraction <= 0 || lv.Fraction > 1.0 {
			return fmt.Errorf("partialTakeProfits[%d].fraction 必须在 (0,1] 之间", i)
		}
	}

	// trailing 默认值/校验
	if c.EnableTrailingTakeProfit {
		if c.TrailStartCents <= 0 {
			c.TrailStartCents = 4
		}
		if c.TrailDistanceCents <= 0 {
			c.TrailDistanceCents = 2
		}
		if c.TrailStartCents < 0 || c.TrailDistanceCents < 0 {
			return fmt.Errorf("trailStartCents / trailDistanceCents 不能为负数")
		}
	}

	return nil
}
