package grid

import (
	"fmt"
)

// GridStrategyConfig 网格策略配置
type GridStrategyConfig struct {
	GridLevels                    []int   `json:"gridLevels" yaml:"gridLevels"`
	OrderSize                     float64 `json:"orderSize" yaml:"orderSize"`
	MinOrderSize                  float64 `json:"minOrderSize" yaml:"minOrderSize"`
	EnableRebuy                   bool    `json:"enableRebuy" yaml:"enableRebuy"`
	EnableDoubleSide              bool    `json:"enableDoubleSide" yaml:"enableDoubleSide"`
	ProfitTarget                  int     `json:"profitTarget" yaml:"profitTarget"`
	MaxUnhedgedLoss               int     `json:"maxUnhedgedLoss" yaml:"maxUnhedgedLoss"`
	HardStopPrice                 int     `json:"hardStopPrice" yaml:"hardStopPrice"`
	ElasticStopPrice              int     `json:"elasticStopPrice" yaml:"elasticStopPrice"`
	MaxRoundsPerPeriod            int     `json:"maxRoundsPerPeriod" yaml:"maxRoundsPerPeriod"`
	EntryMaxBuySlippageCents      int     `json:"entryMaxBuySlippageCents" yaml:"entryMaxBuySlippageCents"`
	SupplementMaxBuySlippageCents int     `json:"supplementMaxBuySlippageCents" yaml:"supplementMaxBuySlippageCents"`

	// 实盘工程化参数
	HealthLogIntervalSeconds   int  `json:"healthLogIntervalSeconds" yaml:"healthLogIntervalSeconds"`
	StrongHedgeDebounceSeconds int  `json:"strongHedgeDebounceSeconds" yaml:"strongHedgeDebounceSeconds"`
	EnableAdhocStrongHedge     bool `json:"enableAdhocStrongHedge" yaml:"enableAdhocStrongHedge"`
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

	// 实盘工程化默认值
	if c.HealthLogIntervalSeconds <= 0 {
		c.HealthLogIntervalSeconds = 15
	}
	if c.StrongHedgeDebounceSeconds <= 0 {
		c.StrongHedgeDebounceSeconds = 2
	}
	return nil
}
