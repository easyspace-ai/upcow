package rangeboth

import (
	"fmt"
	"strings"

	"github.com/betbot/gobet/internal/strategies/common"
)

const ID = "rangeboth"

// Config：窄幅震荡检测 + 双边挂限价单（UP/DOWN 都挂 BUY GTC）。
//
// 触发条件（默认）：
// - 在 lookbackSeconds 窗口内，UP 价格的 (max-min) <= maxRangeCents
// - 在 lookbackSeconds 窗口内，DOWN 价格的 (max-min) <= maxRangeCents
//
// 触发动作：
// - 同时/顺序（可配）提交两笔限价买单（GTC），分别买 UP 与 DOWN。
type Config struct {
	// 基础交易参数（shares）
	OrderSize     float64 `yaml:"orderSize" json:"orderSize"`
	OrderSizeUp   float64 `yaml:"orderSizeUp" json:"orderSizeUp"`     // 可选：覆盖 UP 的 size（0 表示用 orderSize）
	OrderSizeDown float64 `yaml:"orderSizeDown" json:"orderSizeDown"` // 可选：覆盖 DOWN 的 size（0 表示用 orderSize）

	// 震荡判定参数
	LookbackSeconds int `yaml:"lookbackSeconds" json:"lookbackSeconds"` // 窗口（秒），例如 5
	MaxRangeCents   int `yaml:"maxRangeCents" json:"maxRangeCents"`     // 最大波动（分/点），例如 5
	// 注意：由于订单簿是镜像的（UP+DOWN=100分），只需要UP或DOWN其中一个满足窄幅条件即可

	// 触发节流
	WarmupMs            int `yaml:"warmupMs" json:"warmupMs"`                       // 启动/换周期预热（毫秒）
	CooldownMs          int `yaml:"cooldownMs" json:"cooldownMs"`                   // 触发冷却（毫秒）
	MaxTriggersPerCycle int `yaml:"maxTriggersPerCycle" json:"maxTriggersPerCycle"` // 每周期最多触发次数（0=不限制）

	// 价格区间限制
	MinPriceCents int `yaml:"minPriceCents" json:"minPriceCents"` // 最小价格（分），低于此价格不交易，默认20
	MaxPriceCents int `yaml:"maxPriceCents" json:"maxPriceCents"` // 最大价格（分），高于此价格不交易，默认80

	// 周期结束保护
	CycleEndProtectionSeconds int `yaml:"cycleEndProtectionSeconds" json:"cycleEndProtectionSeconds"` // 周期结束前保护时间（秒），默认180（3分钟）

	// 下单参数
	OrderExecutionMode string `yaml:"orderExecutionMode" json:"orderExecutionMode"` // "sequential" | "parallel"

	// 限价选择：默认用 bestBid 做 maker 买单；可选在 bid 上加一小档，但必须保证不穿 ask。
	LimitPriceOffsetCents float64 `yaml:"limitPriceOffsetCents" json:"limitPriceOffsetCents"` // 买价 = bestBid + offset（但 < bestAsk），支持小数（如 0.1 美分）
	MaxSpreadCents        int     `yaml:"maxSpreadCents" json:"maxSpreadCents"`               // 单边价差过滤（分）；<=0 不过滤

	// 顺序模式优先级（仅 orderExecutionMode=sequential 生效）
	SequentialPriorityMode       string `yaml:"sequentialPriorityMode" json:"sequentialPriorityMode"`             // "price_above" | "higher_price" | "up_first" | "down_first"
	SequentialPriorityPriceCents int    `yaml:"sequentialPriorityPriceCents" json:"sequentialPriorityPriceCents"` // 例如 55

	// 默认关闭自动对冲（本策略是双边同时挂单，不做自动平衡）
	AutoMerge common.AutoMergeConfig `yaml:"autoMerge" json:"autoMerge"`

	// 智能对冲参数（基于价格触发）
	RebalanceEnabled              bool    `yaml:"rebalanceEnabled" json:"rebalanceEnabled"`                           // 是否启用智能对冲（默认true）
	RebalanceTriggerPriceCents   int     `yaml:"rebalanceTriggerPriceCents" json:"rebalanceTriggerPriceCents"`       // 触发补仓的价格阈值（分），默认90，当UP或DOWN价格>=此值时进入补仓阶段
	RebalanceMinProfit            float64 `yaml:"rebalanceMinProfit" json:"rebalanceMinProfit"`                       // 最小收益目标（USDC），默认0.01
	RebalanceCheckIntervalSeconds int     `yaml:"rebalanceCheckIntervalSeconds" json:"rebalanceCheckIntervalSeconds"` // 检查间隔（秒），默认10
	RebalanceMaxOrderSize         float64 `yaml:"rebalanceMaxOrderSize" json:"rebalanceMaxOrderSize"`                 // 单次补单最大数量（shares），默认50
}

func (c *Config) Validate() error {
	c.AutoMerge.Normalize()

	if c.OrderSize <= 0 && (c.OrderSizeUp <= 0 || c.OrderSizeDown <= 0) {
		return fmt.Errorf("orderSize 必须 > 0（或同时设置 orderSizeUp 与 orderSizeDown > 0）")
	}

	if c.LookbackSeconds <= 0 {
		c.LookbackSeconds = 5
	}
	if c.MaxRangeCents <= 0 {
		c.MaxRangeCents = 5
	}
	if c.CooldownMs <= 0 {
		c.CooldownMs = 1500
	}
	if c.WarmupMs < 0 {
		c.WarmupMs = 0
	}
	if c.MaxTriggersPerCycle < 0 {
		c.MaxTriggersPerCycle = 0
	}

	// 价格区间限制默认值
	if c.MinPriceCents <= 0 {
		c.MinPriceCents = 20 // 默认20分
	}
	if c.MaxPriceCents <= 0 {
		c.MaxPriceCents = 80 // 默认80分
	}
	if c.MinPriceCents >= c.MaxPriceCents {
		return fmt.Errorf("minPriceCents(%d) 必须小于 maxPriceCents(%d)", c.MinPriceCents, c.MaxPriceCents)
	}

	// 周期结束保护默认值
	if c.CycleEndProtectionSeconds <= 0 {
		c.CycleEndProtectionSeconds = 180 // 默认3分钟（180秒）
	}

	if c.OrderExecutionMode == "" {
		c.OrderExecutionMode = "sequential"
	}
	if c.OrderExecutionMode != "sequential" && c.OrderExecutionMode != "parallel" {
		return fmt.Errorf("orderExecutionMode 必须是 sequential 或 parallel")
	}

	if c.LimitPriceOffsetCents < 0 {
		return fmt.Errorf("limitPriceOffsetCents 不能为负数")
	}
	// 注意：如果用户显式设置为 0，则使用 0（不加偏移）
	// 如果用户未设置（也是 0），建议在配置文件中显式设置 0.1 以获得更好成交
	if c.MaxSpreadCents < 0 {
		return fmt.Errorf("maxSpreadCents 不能为负数")
	}

	if c.SequentialPriorityMode == "" {
		c.SequentialPriorityMode = "price_above"
	}
	c.SequentialPriorityMode = strings.ToLower(strings.TrimSpace(c.SequentialPriorityMode))
	switch c.SequentialPriorityMode {
	case "price_above", "higher_price", "up_first", "down_first":
	default:
		return fmt.Errorf("sequentialPriorityMode 必须是 price_above/higher_price/up_first/down_first")
	}
	if c.SequentialPriorityPriceCents <= 0 {
		c.SequentialPriorityPriceCents = 55
	}

	// 智能对冲默认值
	if c.RebalanceTriggerPriceCents <= 0 {
		c.RebalanceTriggerPriceCents = 90 // 默认90分触发补仓
	}
	if c.RebalanceMinProfit <= 0 {
		c.RebalanceMinProfit = 0.01 // 默认最小收益$0.01
	}
	if c.RebalanceCheckIntervalSeconds <= 0 {
		c.RebalanceCheckIntervalSeconds = 10 // 默认每10秒检查一次
	}
	if c.RebalanceMaxOrderSize <= 0 {
		c.RebalanceMaxOrderSize = 50 // 默认单次最多补50 shares
	}
	// RebalanceEnabled默认为true（如果未设置）
	// 注意：bool类型在Go中默认为false，但我们可以通过其他方式判断是否显式设置

	return nil
}
