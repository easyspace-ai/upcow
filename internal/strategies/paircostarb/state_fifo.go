package paircostarb

import "math"

// pairAvailableRuntimeLocked 只在持锁状态下调用（来自 OnOrderUpdate/OnCycle）。
// 它会把两边未配对库存按 FIFO 配对，累积到 qPair/costPair，并推进游标。
func pairAvailableRuntimeLocked(rt *runtimeState) {
	if rt == nil {
		return
	}
	for {
		upAvail, upCostPer, okUp := fifoAvailCostPer(rt.fillsUp, rt.upCur)
		downAvail, downCostPer, okDown := fifoAvailCostPer(rt.fillsDown, rt.downCur)
		if !okUp || !okDown {
			break
		}
		take := math.Min(upAvail, downAvail)
		if take <= 1e-9 {
			break
		}

		rt.qPair += take
		rt.costPair += take*upCostPer + take*downCostPer

		rt.upCur = fifoConsumeQty(rt.fillsUp, rt.upCur, take)
		rt.downCur = fifoConsumeQty(rt.fillsDown, rt.downCur, take)

		rt.fillsUp, rt.upCur = fifoCompact(rt.fillsUp, rt.upCur)
		rt.fillsDown, rt.downCur = fifoCompact(rt.fillsDown, rt.downCur)
	}
}

func fifoAvailCostPer(fills []Fill, cur fifoCursor) (avail float64, costPerShare float64, ok bool) {
	if cur.idx < 0 || cur.idx >= len(fills) {
		return 0, 0, false
	}
	f := fills[cur.idx]
	if f.Qty <= 0 || f.CostUSD <= 0 {
		return 0, 0, false
	}
	rem := f.Qty - cur.used
	if rem <= 1e-9 {
		return 0, 0, false
	}
	return rem, f.CostUSD / f.Qty, true
}

func fifoConsumeQty(fills []Fill, cur fifoCursor, qty float64) fifoCursor {
	if qty <= 0 {
		return cur
	}
	for qty > 1e-9 && cur.idx < len(fills) {
		f := fills[cur.idx]
		rem := f.Qty - cur.used
		if rem <= 1e-9 {
			cur.idx++
			cur.used = 0
			continue
		}
		take := math.Min(rem, qty)
		cur.used += take
		qty -= take
		if f.Qty-cur.used <= 1e-9 {
			cur.idx++
			cur.used = 0
		}
	}
	return cur
}

func fifoCompact(fills []Fill, cur fifoCursor) ([]Fill, fifoCursor) {
	if cur.idx <= 0 {
		return fills, cur
	}
	if cur.idx < 256 && cur.idx < len(fills)/2 {
		return fills, cur
	}
	newFills := append([]Fill(nil), fills[cur.idx:]...)
	return newFills, fifoCursor{idx: 0, used: cur.used}
}
