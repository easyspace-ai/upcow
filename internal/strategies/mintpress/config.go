package mintpress

import "fmt"

// Config：做市式 complete-set “印钞机”（双边挂单）策略配置。
//
// 核心思路：
// - 在 YES/NO 两边持续挂 BUY(GTC) 限价单，尽量做 maker
// - 控制两边买入价格之和 <= 100 - ProfitTargetCents（期望锁定到期收益）
// - 若只成交一边导致裸露超过阈值，则 SELL(FAK) 回平
type Config struct {
	// OrderSize 每次挂单的目标 shares（YES 与 NO 相同）
	OrderSize float64 `json:"orderSize" yaml:"orderSize"`

	// MinOrderSize 单腿最小下单金额（USDC），用于满足交易所最小金额要求
	MinOrderSize float64 `json:"minOrderSize" yaml:"minOrderSize"`

	// ProfitTargetCents 目标锁定利润（分）。
	// 要求：yesBid + noBid <= 100 - ProfitTargetCents
	ProfitTargetCents int `json:"profitTargetCents" yaml:"profitTargetCents"`

	// ImproveCents 在 bestBid 基础上“加价”的档位（分），用于提高被动成交概率。
	// 例如 ImproveCents=1 表示挂在 bestBid+1（但仍需 < bestAsk 才能保持 maker）。
	ImproveCents int `json:"improveCents" yaml:"improveCents"`

	// MaxQuoteSpreadCents 允许用于报价的最大 bid/ask 价差（分）。
	// 贴子里提到“15m 市场价差经常打开”，因此做市需要放宽。
	// <=0 表示不限制（仍要求 bid/ask 双边存在）。
	MaxQuoteSpreadCents int `json:"maxQuoteSpreadCents" yaml:"maxQuoteSpreadCents"`

	// RequoteThresholdCents 当目标价格变化达到该阈值（分）才撤单重挂，避免抖动。
	RequoteThresholdCents int `json:"requoteThresholdCents" yaml:"requoteThresholdCents"`

	// CooldownMs 最短重挂/下单间隔（毫秒）
	CooldownMs int `json:"cooldownMs" yaml:"cooldownMs"`

	// MaxSetsPerPeriod 单周期最多“完成锁定”的份额（complete sets）上限，用于控制资金占用/风险
	MaxSetsPerPeriod float64 `json:"maxSetsPerPeriod" yaml:"maxSetsPerPeriod"`

	// StopBeforeEndSeconds 距离 market.Timestamp（周期结束时间戳）还有多久（秒）就停止新增挂单
	StopBeforeEndSeconds int `json:"stopBeforeEndSeconds" yaml:"stopBeforeEndSeconds"`

	// MaxNetExposureShares 最大允许净裸露（shares）。超过则触发回平。
	MaxNetExposureShares float64 `json:"maxNetExposureShares" yaml:"maxNetExposureShares"`

	// HedgeSellOffsetCents 回平卖出价 = bestBid - offset（分），越大越容易成交但越“吃价”
	HedgeSellOffsetCents int `json:"hedgeSellOffsetCents" yaml:"hedgeSellOffsetCents"`
}

func (c *Config) Validate() error {
	if c.OrderSize <= 0 {
		return fmt.Errorf("orderSize 必须 > 0")
	}
	if c.MinOrderSize <= 0 {
		c.MinOrderSize = 1.1
	}
	if c.ProfitTargetCents < 0 || c.ProfitTargetCents > 50 {
		return fmt.Errorf("profitTargetCents 建议在 [0,50] 范围内")
	}
	if c.ImproveCents < 0 {
		return fmt.Errorf("improveCents 不能为负数")
	}
	if c.RequoteThresholdCents <= 0 {
		c.RequoteThresholdCents = 1
	}
	if c.CooldownMs <= 0 {
		c.CooldownMs = 300
	}
	if c.MaxSetsPerPeriod <= 0 {
		c.MaxSetsPerPeriod = 200 // 默认较保守（shares）
	}
	if c.StopBeforeEndSeconds <= 0 {
		c.StopBeforeEndSeconds = 20
	}
	if c.MaxNetExposureShares <= 0 {
		c.MaxNetExposureShares = c.OrderSize // 默认：最多裸露一个挂单规模
	}
	if c.HedgeSellOffsetCents <= 0 {
		c.HedgeSellOffsetCents = 2
	}
	return nil
}

