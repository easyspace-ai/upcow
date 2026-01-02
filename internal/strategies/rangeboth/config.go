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
	LookbackSeconds  int   `yaml:"lookbackSeconds" json:"lookbackSeconds"`   // 窗口（秒），例如 5
	MaxRangeCents    int   `yaml:"maxRangeCents" json:"maxRangeCents"`       // 最大波动（分/点），例如 5
	RequireBothSides *bool `yaml:"requireBothSides" json:"requireBothSides"` // 是否要求 UP/DOWN 两边都满足窄幅条件（默认 true）

	// 触发节流
	WarmupMs            int `yaml:"warmupMs" json:"warmupMs"`                       // 启动/换周期预热（毫秒）
	CooldownMs          int `yaml:"cooldownMs" json:"cooldownMs"`                   // 触发冷却（毫秒）
	MaxTriggersPerCycle int `yaml:"maxTriggersPerCycle" json:"maxTriggersPerCycle"` // 每周期最多触发次数（0=不限制）

	// 下单参数
	OrderExecutionMode string `yaml:"orderExecutionMode" json:"orderExecutionMode"` // "sequential" | "parallel"

	// 限价选择：默认用 bestBid 做 maker 买单；可选在 bid 上加一小档，但必须保证不穿 ask。
	LimitPriceOffsetCents int `yaml:"limitPriceOffsetCents" json:"limitPriceOffsetCents"` // 买价 = bestBid + offset（但 < bestAsk）
	MaxSpreadCents        int `yaml:"maxSpreadCents" json:"maxSpreadCents"`               // 单边价差过滤（分）；<=0 不过滤

	// 顺序模式优先级（仅 orderExecutionMode=sequential 生效）
	SequentialPriorityMode       string `yaml:"sequentialPriorityMode" json:"sequentialPriorityMode"`             // "price_above" | "higher_price" | "up_first" | "down_first"
	SequentialPriorityPriceCents int    `yaml:"sequentialPriorityPriceCents" json:"sequentialPriorityPriceCents"` // 例如 55

	// 默认关闭自动对冲（本策略是双边同时挂单，不做自动平衡）
	AutoMerge common.AutoMergeConfig `yaml:"autoMerge" json:"autoMerge"`
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

	// 默认 true；用户显式配置 false 时生效
	if c.RequireBothSides == nil {
		v := true
		c.RequireBothSides = &v
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

	return nil
}
