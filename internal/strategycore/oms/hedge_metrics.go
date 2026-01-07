package oms

import (
	"sync"
	"time"
)

// hedgeMetrics 记录“Entry->Hedge 完成耗时”，用于动态提高对冲确定性。
// 设计原则：
// - 只用轻量 EWMA（不引入复杂统计）
// - 按 marketSlug 聚合（不同市场流动性差异很大）
type hedgeMetrics struct {
	mu sync.Mutex

	// entryOrderID -> (marketSlug, entryFilledAt)
	entryFilledAt map[string]entryFillInfo

	// marketSlug -> ewma seconds
	byMarket map[string]*marketHedgeEWMA

	// EWMA 参数：alpha 越大越“更跟随最新”
	alpha float64
}

type entryFillInfo struct {
	marketSlug string
	at         time.Time
}

type marketHedgeEWMA struct {
	ewmaSeconds float64
	samples     int
	updatedAt   time.Time
}

func newHedgeMetrics() *hedgeMetrics {
	return &hedgeMetrics{
		entryFilledAt: make(map[string]entryFillInfo, 1024),
		byMarket:      make(map[string]*marketHedgeEWMA, 64),
		alpha:         0.2,
	}
}

func (hm *hedgeMetrics) recordEntryFilled(entryOrderID, marketSlug string, at time.Time) {
	if hm == nil || entryOrderID == "" || marketSlug == "" {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	hm.mu.Lock()
	defer hm.mu.Unlock()

	// 防止重复写入（例如重复 order update）
	if _, exists := hm.entryFilledAt[entryOrderID]; exists {
		return
	}
	hm.entryFilledAt[entryOrderID] = entryFillInfo{marketSlug: marketSlug, at: at}
}

// recordHedgeFilled 将 entry->hedge 的耗时记入 EWMA
func (hm *hedgeMetrics) recordHedgeFilled(entryOrderID string, hedgeFilledAt time.Time) {
	if hm == nil || entryOrderID == "" {
		return
	}
	if hedgeFilledAt.IsZero() {
		hedgeFilledAt = time.Now()
	}

	hm.mu.Lock()
	defer hm.mu.Unlock()

	info, ok := hm.entryFilledAt[entryOrderID]
	if !ok {
		return
	}
	delete(hm.entryFilledAt, entryOrderID)

	if info.at.IsZero() {
		return
	}
	sec := hedgeFilledAt.Sub(info.at).Seconds()
	if sec <= 0 {
		return
	}

	st := hm.byMarket[info.marketSlug]
	if st == nil {
		st = &marketHedgeEWMA{}
		hm.byMarket[info.marketSlug] = st
	}
	if st.samples == 0 || st.ewmaSeconds <= 0 {
		st.ewmaSeconds = sec
	} else {
		st.ewmaSeconds = hm.alpha*sec + (1.0-hm.alpha)*st.ewmaSeconds
	}
	st.samples++
	st.updatedAt = time.Now()
}

func (hm *hedgeMetrics) getEWMASec(marketSlug string) float64 {
	if hm == nil || marketSlug == "" {
		return 0
	}
	hm.mu.Lock()
	defer hm.mu.Unlock()
	st := hm.byMarket[marketSlug]
	if st == nil {
		return 0
	}
	return st.ewmaSeconds
}

