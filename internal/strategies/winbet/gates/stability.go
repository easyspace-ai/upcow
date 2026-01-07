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
	window time.Duration
	points []stabilityPoint
}

func newStabilityWindow(window time.Duration) *stabilityWindow {
	if window <= 0 {
		window = 5 * time.Second
	}
	return &stabilityWindow{window: window, points: make([]stabilityPoint, 0, 128)}
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

	// mid 只在双边存在时计算；否则记 0（stats 时会跳过）
	yesMid := 0.0
	noMid := 0.0
	if yesBid > 0 && yesAsk > 0 {
		yesMid = (yesBid + yesAsk) / 2
	}
	if noBid > 0 && noAsk > 0 {
		noMid = (noBid + noAsk) / 2
	}

	yesSpreadCents := 0.0
	noSpreadCents := 0.0
	if mq.YesSpreadPips > 0 {
		yesSpreadCents = p2c(mq.YesSpreadPips)
	}
	if mq.NoSpreadPips > 0 {
		noSpreadCents = p2c(mq.NoSpreadPips)
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
		minV := math.MaxFloat64
		maxV := 0.0
		sum := 0.0
		n := 0.0
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
			sum += v
			n++
		}
		if n == 0 {
			return 0
		}
		mean := sum / n
		if mean <= 0 {
			return 0
		}
		amp := maxV - minV
		return amp / mean * 100.0
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

