package adaptive

import "fmt"

const ID = "adaptive"

// Config 策略配置（bbgo main 风格）
type Config struct {
	// 策略参数
	K                  float64 `yaml:"k" json:"k"`                                   // 定价模型参数 k
	C                  float64 `yaml:"c" json:"c"`                                   // 定价模型参数 c
	SizePerTrade       float64 `yaml:"sizePerTrade" json:"sizePerTrade"`             // 每次交易数量（shares）
	InventorySkewFactor float64 `yaml:"inventorySkewFactor" json:"inventorySkewFactor"` // 库存偏斜因子

	// Edge 参数
	BaseMinEdgeMaker  float64 `yaml:"baseMinEdgeMaker" json:"baseMinEdgeMaker"`   // Maker 基础最小边距（0.0005 = 0.05%）
	BaseMinEdgeTaker  float64 `yaml:"baseMinEdgeTaker" json:"baseMinEdgeTaker"`   // Taker 基础最小边距（0.003 = 0.30%）
	MarketWeight      float64 `yaml:"marketWeight" json:"marketWeight"`           // 市场权重（0.7 = 70%）

	// 分级熔断参数
	DecayStartTime    float64 `yaml:"decayStartTime" json:"decayStartTime"`       // 剩余时间开始衰减（秒，默认300）
	ReduceOnlyTime    float64 `yaml:"reduceOnlyTime" json:"reduceOnlyTime"`       // 只减不加时间（秒，默认300）
	ForceCloseTime    float64 `yaml:"forceCloseTime" json:"forceCloseTime"`        // 强制平仓时间（秒，默认180）
	MaxEdgeAtZero     float64 `yaml:"maxEdgeAtZero" json:"maxEdgeAtZero"`         // 结束时要求的额外利润门槛（默认0.02）

	// 风控参数
	HedgeThreshold      float64 `yaml:"hedgeThreshold" json:"hedgeThreshold"`         // 净持仓阈值（默认80）
	StopQuoteThreshold   float64 `yaml:"stopQuoteThreshold" json:"stopQuoteThreshold"` // 停止报价阈值（默认60）
	HedgeSizeMultiplier float64 `yaml:"hedgeSizeMultiplier" json:"hedgeSizeMultiplier"` // 对冲数量倍数（默认1.5）

	// 订单限制
	MinOrderSize float64 `yaml:"minOrderSize" json:"minOrderSize"` // 最小订单金额（USDC，默认1.1）

	// 市场周期配置（从 CONFIG.MARKET.INTERVAL_SECONDS 获取，默认900秒=15分钟）
	MarketIntervalSeconds int `yaml:"marketIntervalSeconds" json:"marketIntervalSeconds"` // 市场周期长度（秒）
}

// Validate 验证配置
func (c *Config) Validate() error {
	if c.K <= 0 {
		return fmt.Errorf("k 必须 > 0")
	}
	if c.SizePerTrade <= 0 {
		return fmt.Errorf("sizePerTrade 必须 > 0")
	}
	if c.BaseMinEdgeMaker < 0 || c.BaseMinEdgeMaker > 1 {
		return fmt.Errorf("baseMinEdgeMaker 必须在 [0, 1] 范围内")
	}
	if c.BaseMinEdgeTaker < 0 || c.BaseMinEdgeTaker > 1 {
		return fmt.Errorf("baseMinEdgeTaker 必须在 [0, 1] 范围内")
	}
	if c.MarketWeight < 0 || c.MarketWeight > 1 {
		return fmt.Errorf("marketWeight 必须在 [0, 1] 范围内")
	}
	if c.DecayStartTime < 0 {
		return fmt.Errorf("decayStartTime 必须 >= 0")
	}
	if c.ReduceOnlyTime < 0 {
		return fmt.Errorf("reduceOnlyTime 必须 >= 0")
	}
	if c.ForceCloseTime < 0 {
		return fmt.Errorf("forceCloseTime 必须 >= 0")
	}
	if c.MinOrderSize < 1.0 {
		return fmt.Errorf("minOrderSize 必须 >= 1.0 USDC")
	}
	if c.MarketIntervalSeconds <= 0 {
		c.MarketIntervalSeconds = 900 // 默认15分钟
	}
	return nil
}

