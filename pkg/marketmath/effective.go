package marketmath

import "fmt"

// TopOfBook 表示 YES/NO 的一档盘口（单位：pips = price * 10000）。
//
// 说明：
// - Polymarket 的 tick size 可能为 0.0001，因此用 pips 表达能覆盖所有 tick。
// - 本结构只承载“最小决策必要信息”，策略/服务层可在其上构建更丰富的 processed orderbook。
type TopOfBook struct {
	YesBidPips int
	YesAskPips int
	NoBidPips  int
	NoAskPips  int
}

func (t TopOfBook) Validate() error {
	// 允许单边为 0（表示缺失），但不能全缺。
	if t.YesBidPips <= 0 && t.YesAskPips <= 0 && t.NoBidPips <= 0 && t.NoAskPips <= 0 {
		return fmt.Errorf("top-of-book is empty")
	}
	// 基本范围校验（不要求 strict，因为极端情况下可能出现 0.9999 等）
	check := func(name string, v int) error {
		if v == 0 {
			return nil
		}
		if v < 0 || v > 10000 {
			return fmt.Errorf("%s out of range: %d", name, v)
		}
		return nil
	}
	if err := check("yesBidPips", t.YesBidPips); err != nil {
		return err
	}
	if err := check("yesAskPips", t.YesAskPips); err != nil {
		return err
	}
	if err := check("noBidPips", t.NoBidPips); err != nil {
		return err
	}
	if err := check("noAskPips", t.NoAskPips); err != nil {
		return err
	}
	return nil
}

// EffectivePrices 有效价格（考虑订单簿镜像特性）。
//
// 核心等价关系（poly-sdk 文档）：
//   Buy YES @ P  ≡  Sell NO @ (1-P)
//   Buy NO  @ P  ≡  Sell YES @ (1-P)
//
// 因此，买入某一侧的“有效成本”应同时考虑：
// - 直接在该 token 的 ask 买入
// - 通过对侧 bid 的镜像价格买入
type EffectivePrices struct {
	EffectiveBuyYesPips  int
	EffectiveBuyNoPips   int
	EffectiveSellYesPips int
	EffectiveSellNoPips  int
}

// GetEffectivePrices 计算有效价格（pips）。
func GetEffectivePrices(t TopOfBook) (EffectivePrices, error) {
	if err := t.Validate(); err != nil {
		return EffectivePrices{}, err
	}

	// helper: min/max but ignore <=0 values
	minPos := func(a, b int) int {
		if a <= 0 {
			return b
		}
		if b <= 0 {
			return a
		}
		if a < b {
			return a
		}
		return b
	}
	maxPos := func(a, b int) int {
		if a <= 0 {
			return b
		}
		if b <= 0 {
			return a
		}
		if a > b {
			return a
		}
		return b
	}

	// 镜像换算：1 - price
	// - 这里用 pips 表达：1.0 == 10000 pips
	mirror := func(pips int) int {
		if pips <= 0 {
			return 0
		}
		return 10000 - pips
	}

	e := EffectivePrices{
		// 买 YES：min(YES.ask, 1 - NO.bid)
		EffectiveBuyYesPips: minPos(t.YesAskPips, mirror(t.NoBidPips)),
		// 买 NO：min(NO.ask, 1 - YES.bid)
		EffectiveBuyNoPips: minPos(t.NoAskPips, mirror(t.YesBidPips)),
		// 卖 YES：max(YES.bid, 1 - NO.ask)
		EffectiveSellYesPips: maxPos(t.YesBidPips, mirror(t.NoAskPips)),
		// 卖 NO：max(NO.bid, 1 - YES.ask)
		EffectiveSellNoPips: maxPos(t.NoBidPips, mirror(t.YesAskPips)),
	}
	return e, nil
}

type ArbitrageOpportunity struct {
	Type string // "long" or "short"

	// ProfitPips: 利润（pips，即 profit * 10000）
	ProfitPips int

	// 解释字段（用于可观测性/复盘）
	LongCostPips      int
	ShortRevenuePips  int
	BuyYesPips        int
	BuyNoPips         int
	SellYesPips       int
	SellNoPips        int
}

// CheckArbitrage 使用有效价格判断 complete-set 的套利机会：
// - long: Buy YES + Buy NO < 1
// - short: Sell YES + Sell NO > 1
func CheckArbitrage(t TopOfBook) (*ArbitrageOpportunity, error) {
	eff, err := GetEffectivePrices(t)
	if err != nil {
		return nil, err
	}

	longCost := eff.EffectiveBuyYesPips + eff.EffectiveBuyNoPips
	shortRev := eff.EffectiveSellYesPips + eff.EffectiveSellNoPips

	// long profit = 1 - cost
	if eff.EffectiveBuyYesPips > 0 && eff.EffectiveBuyNoPips > 0 {
		if profit := 10000 - longCost; profit > 0 {
			return &ArbitrageOpportunity{
				Type:            "long",
				ProfitPips:      profit,
				LongCostPips:    longCost,
				ShortRevenuePips: shortRev,
				BuyYesPips:      eff.EffectiveBuyYesPips,
				BuyNoPips:       eff.EffectiveBuyNoPips,
				SellYesPips:     eff.EffectiveSellYesPips,
				SellNoPips:      eff.EffectiveSellNoPips,
			}, nil
		}
	}

	// short profit = revenue - 1
	if eff.EffectiveSellYesPips > 0 && eff.EffectiveSellNoPips > 0 {
		if profit := shortRev - 10000; profit > 0 {
			return &ArbitrageOpportunity{
				Type:            "short",
				ProfitPips:      profit,
				LongCostPips:    longCost,
				ShortRevenuePips: shortRev,
				BuyYesPips:      eff.EffectiveBuyYesPips,
				BuyNoPips:       eff.EffectiveBuyNoPips,
				SellYesPips:     eff.EffectiveSellYesPips,
				SellNoPips:      eff.EffectiveSellNoPips,
			}, nil
		}
	}

	return nil, nil
}

