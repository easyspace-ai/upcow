package gates

import (
	"math"
	"time"

	"github.com/betbot/gobet/internal/services"
)

type stabilityPoint struct {
	at time.Time

	yesMid float64
	noMid  float64

	yesSpreadCents float64
	noSpreadCents  float64
}

type stabilityWindow struct {
	window              time.Duration
	maxSpreadFilterCents int // 数据清洗：过滤价差超过此阈值（分）的异常数据
	points              []stabilityPoint
}

func newStabilityWindow(window time.Duration, maxSpreadFilterCents int) *stabilityWindow {
	if window <= 0 {
		window = 5 * time.Second
	}
	if maxSpreadFilterCents <= 0 {
		maxSpreadFilterCents = 15 // 默认值
	}
	return &stabilityWindow{
		window:              window,
		maxSpreadFilterCents: maxSpreadFilterCents,
		points:              make([]stabilityPoint, 0, 128),
	}
}

func (sw *stabilityWindow) add(now time.Time, mq *services.MarketQuality) {
	if sw == nil || mq == nil {
		return
	}

	// pips -> decimal
	p2d := func(pips int) float64 { return float64(pips) / 10000.0 }
	// pips -> cents（1c = 100 pips）
	p2c := func(pips int) float64 { return float64(pips) / 100.0 }

	yesBid := p2d(mq.Top.YesBidPips)
	yesAsk := p2d(mq.Top.YesAskPips)
	noBid := p2d(mq.Top.NoBidPips)
	noAsk := p2d(mq.Top.NoAskPips)

	// 数据清洗：过滤异常数据
	// 1) 检查同向 ask/bid（bid >= ask 表示异常）
	yesValid := yesBid > 0 && yesAsk > 0 && yesBid < yesAsk
	noValid := noBid > 0 && noAsk > 0 && noBid < noAsk

	// 2) 检查价差是否超过阈值（过滤错误 websocket 数据）
	yesSpreadCents := 0.0
	noSpreadCents := 0.0
	if yesValid {
		if mq.YesSpreadPips > 0 {
			yesSpreadCents = p2c(mq.YesSpreadPips)
		} else {
			// 如果没有直接提供 spread，从 bid/ask 计算
			yesSpreadCents = (yesAsk - yesBid) * 100.0 // decimal -> cents
		}
		if yesSpreadCents > float64(sw.maxSpreadFilterCents) {
			yesValid = false // 价差过大，过滤掉
		}
	}
	if noValid {
		if mq.NoSpreadPips > 0 {
			noSpreadCents = p2c(mq.NoSpreadPips)
		} else {
			// 如果没有直接提供 spread，从 bid/ask 计算
			noSpreadCents = (noAsk - noBid) * 100.0 // decimal -> cents
		}
		if noSpreadCents > float64(sw.maxSpreadFilterCents) {
			noValid = false // 价差过大，过滤掉
		}
	}

	// 如果两边都无效，直接跳过这个数据点
	if !yesValid && !noValid {
		return
	}

	// mid 只在双边存在且有效时计算；否则记 0（stats 时会跳过）
	yesMid := 0.0
	noMid := 0.0
	if yesValid {
		yesMid = (yesBid + yesAsk) / 2
	}
	if noValid {
		noMid = (noBid + noAsk) / 2
	}

	// 如果之前没有计算 spread，现在补上（用于 stats）
	if yesSpreadCents == 0 && yesValid {
		yesSpreadCents = (yesAsk - yesBid) * 100.0
	}
	if noSpreadCents == 0 && noValid {
		noSpreadCents = (noAsk - noBid) * 100.0
	}

	sw.points = append(sw.points, stabilityPoint{
		at:             now,
		yesMid:         yesMid,
		noMid:          noMid,
		yesSpreadCents: yesSpreadCents,
		noSpreadCents:  noSpreadCents,
	})

	sw.trim(now)
}

func (sw *stabilityWindow) trim(now time.Time) {
	if sw == nil || len(sw.points) == 0 {
		return
	}
	cutoff := now.Add(-sw.window)
	// 找第一个 >= cutoff
	i := 0
	for i < len(sw.points) && sw.points[i].at.Before(cutoff) {
		i++
	}
	if i > 0 {
		// drop prefix
		sw.points = append([]stabilityPoint(nil), sw.points[i:]...)
	}
}

// stats 返回：
// - maxPriceChangePct: yes/no 中更大的窗口内百分比振幅
// - spreadVolPct: yes/no spread 的窗口内“振幅/均值”百分比（更大的一个）
func (sw *stabilityWindow) stats(now time.Time) (maxPriceChangePct float64, spreadVolPct float64) {
	if sw == nil {
		return 0, 0
	}
	sw.trim(now)
	if len(sw.points) < 2 {
		return 0, 0
	}

	priceChangePct := func(get func(p stabilityPoint) float64) float64 {
		minV := math.MaxFloat64
		maxV := 0.0
		for _, p := range sw.points {
			v := get(p)
			if v <= 0 {
				continue
			}
			if v < minV {
				minV = v
			}
			if v > maxV {
				maxV = v
			}
		}
		if minV == math.MaxFloat64 || minV <= 0 || maxV <= 0 {
			return 0
		}
		return (maxV - minV) / minV * 100.0
	}

	spreadVol := func(get func(p stabilityPoint) float64) float64 {
		// 收集有效值
		values := make([]float64, 0, len(sw.points))
		for _, p := range sw.points {
			v := get(p)
			if v > 0 {
				values = append(values, v)
			}
		}
		if len(values) < 2 {
			return 0
		}

		// 排序以计算中位数和分位数
		sorted := make([]float64, len(values))
		copy(sorted, values)
		// 简单排序（冒泡即可，数据量小）
		for i := 0; i < len(sorted)-1; i++ {
			for j := i + 1; j < len(sorted); j++ {
				if sorted[i] > sorted[j] {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}

		// 计算中位数（作为基准，不受异常值影响）
		median := sorted[len(sorted)/2]
		if len(sorted)%2 == 0 && len(sorted) > 0 {
			median = (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
		}

		// 如果中位数太小（< 0.1 分），说明数据质量不稳定，直接返回 0
		if median < 0.1 {
			return 0
		}

		// 使用 IQR（四分位距）方法过滤异常值
		// 计算 Q1 和 Q3
		q1Idx := len(sorted) / 4
		q3Idx := len(sorted) * 3 / 4
		if q3Idx >= len(sorted) {
			q3Idx = len(sorted) - 1
		}
		q1 := sorted[q1Idx]
		q3 := sorted[q3Idx]
		iqr := q3 - q1

		// 过滤异常值：超出 [Q1 - 1.5*IQR, Q3 + 1.5*IQR] 范围的值
		filtered := make([]float64, 0, len(values))
		lowerBound := q1 - 1.5*iqr
		upperBound := q3 + 1.5*iqr
		for _, v := range values {
			if v >= lowerBound && v <= upperBound {
				filtered = append(filtered, v)
			}
		}

		// 如果过滤后数据不足，说明窗口内数据质量很差，返回 0
		if len(filtered) < 2 {
			return 0
		}

		// 对过滤后的数据计算波动率：使用中位数作为基准（更稳健）
		minV := math.MaxFloat64
		maxV := 0.0
		for _, v := range filtered {
			if v < minV {
				minV = v
			}
			if v > maxV {
				maxV = v
			}
		}

		// 使用中位数作为基准计算波动率（而不是均值，避免被异常值影响）
		amp := maxV - minV
		if median <= 0 {
			return 0
		}
		volPct := amp / median * 100.0

		// 额外保护：如果计算出的波动率异常大（> 500%），说明可能仍有异常值漏过
		// 此时改用绝对振幅判断：如果绝对振幅 > 5 分，才认为波动过大
		if volPct > 500.0 {
			const maxAbsAmpThreshold = 5.0 // 最大绝对振幅阈值（分）
			if amp > maxAbsAmpThreshold {
				// 返回一个合理的百分比（基于中位数），避免爆炸
				return (amp / median) * 100.0
			}
			return 0
		}

		return volPct
	}

	yesPricePct := priceChangePct(func(p stabilityPoint) float64 { return p.yesMid })
	noPricePct := priceChangePct(func(p stabilityPoint) float64 { return p.noMid })
	if yesPricePct > noPricePct {
		maxPriceChangePct = yesPricePct
	} else {
		maxPriceChangePct = noPricePct
	}

	yesSpreadVol := spreadVol(func(p stabilityPoint) float64 { return p.yesSpreadCents })
	noSpreadVol := spreadVol(func(p stabilityPoint) float64 { return p.noSpreadCents })
	if yesSpreadVol > noSpreadVol {
		spreadVolPct = yesSpreadVol
	} else {
		spreadVolPct = noSpreadVol
	}

	return maxPriceChangePct, spreadVolPct
}

