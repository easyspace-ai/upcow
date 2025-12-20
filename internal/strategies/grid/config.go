package grid

import (
	"fmt"
)

// GridStrategyConfig 网格策略配置
type GridStrategyConfig struct {
	GridLevels        []int   // 手工定义的网格层级列表（分），例如 [62, 65, 71, ...]
	OrderSize         float64 // 订单大小（share数量），每次买入的share数量
	MinOrderSize      float64 // 最小下单金额（USDC），默认1.1，交易所要求不能小于1
	EnableRebuy        bool    // 允许重新买入
	EnableDoubleSide   bool    // 双向交易
	ProfitTarget       int     // 止盈目标（分）
	MaxUnhedgedLoss    int     // 最大未对冲损失（分）
	HardStopPrice      int     // 硬止损价格（分）
	ElasticStopPrice   int     // 弹性止损价格（分）
	MaxRoundsPerPeriod int     // 每个周期最大轮数
	EntryMaxBuySlippageCents      int // 入场买入最大滑点（分），相对 gridLevel（0=关闭）
	SupplementMaxBuySlippageCents int // 补仓/强对冲买入最大滑点（分），相对当前价（0=关闭）
}

// GetName 实现 StrategyConfig 接口
func (c *GridStrategyConfig) GetName() string {
	return "grid"
}

// Validate 验证配置
func (c *GridStrategyConfig) Validate() error {
	// 验证网格层级列表（必须设置）
	if len(c.GridLevels) == 0 {
		return fmt.Errorf("网格层级列表 grid_levels 不能为空")
	}
	
	// 验证层级列表是否有效（必须大于0，且按升序排列）
	prev := 0
	for i, level := range c.GridLevels {
		if level <= 0 {
			return fmt.Errorf("网格层级必须大于 0，第 %d 个层级 %d 无效", i+1, level)
		}
		if i > 0 && level <= prev {
			return fmt.Errorf("网格层级必须按升序排列，第 %d 个层级 %d 小于等于前一个层级 %d", i+1, level, prev)
		}
		prev = level
	}
	
	// 设置默认最小订单金额
	if c.MinOrderSize <= 0 {
		c.MinOrderSize = 1.1 // 默认值
	}
	
	// 验证最小订单金额
	if c.MinOrderSize < 1.0 {
		return fmt.Errorf("最小下单金额 min_order_size 必须 >= 1.0 USDC（交易所要求）")
	}
	
	// 验证订单大小
	if c.OrderSize <= 0 {
		return fmt.Errorf("订单大小 order_size 必须大于 0")
	}
	if c.EntryMaxBuySlippageCents < 0 || c.SupplementMaxBuySlippageCents < 0 {
		return fmt.Errorf("滑点配置不能为负数")
	}
	return nil
}

