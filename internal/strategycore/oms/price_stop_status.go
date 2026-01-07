package oms

import (
	"context"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

// GetPriceStopWatchesStatus 获取价格盯盘状态（用于 dashboard 显示）
func (o *OMS) GetPriceStopWatchesStatus(ctx context.Context, marketSlug string) *PriceStopWatchesStatus {
	_ = ctx
	if o == nil {
		return nil
	}

	pp := o.priceStopParams()
	if !pp.enabled {
		return &PriceStopWatchesStatus{
			Enabled: false,
		}
	}

	status := &PriceStopWatchesStatus{
		Enabled:         true,
		SoftLossCents:   pp.softLossCents,
		HardLossCents:   pp.hardLossCents,
		TakeProfitCents: pp.takeProfitCents,
		ConfirmTicks:    pp.confirmTicks,
	}

	o.mu.RLock()
	if len(o.priceStopWatches) == 0 {
		o.mu.RUnlock()
		status.ActiveWatches = 0
		return status
	}

	// 获取当前市场价格（用于计算实时 profitNow）
	var yesAskCents, noAskCents int
	if o.tradingService != nil {
		if snap, ok := o.tradingService.BestBookSnapshot(); ok && snap.UpdatedAt.After(time.Now().Add(-3*time.Second)) {
			yesAsk := domain.Price{Pips: int(snap.YesAskPips)}
			noAsk := domain.Price{Pips: int(snap.NoAskPips)}
			yesAskCents = yesAsk.ToCents()
			noAskCents = noAsk.ToCents()
		}
	}

	watchDetails := make([]PriceStopWatchInfo, 0, len(o.priceStopWatches))
	latestEvalTime := time.Time{}

	for entryID, w := range o.priceStopWatches {
		if w == nil {
			continue
		}
		// 如果指定了 marketSlug，只返回该市场的盯盘
		if marketSlug != "" && w.marketSlug != marketSlug {
			continue
		}

		// 获取当前 hedgeOrderID
		hedgeID := ""
		if o.pendingHedges != nil {
			hedgeID = o.pendingHedges[entryID]
		}
		if hedgeID == "" {
			hedgeID = w.firstHedgeOrderID
		}

		// 计算当前可锁定 PnL
		profitNow := 0
		if yesAskCents > 0 && noAskCents > 0 {
			hedgeAskCents := yesAskCents
			if w.entryToken == domain.TokenTypeUp {
				hedgeAskCents = noAskCents
			}
			if hedgeAskCents > 0 {
				profitNow = 100 - (w.entryAskCents + hedgeAskCents)
			}
		}

		// 确定状态
		statusStr := "monitoring"
		if w.triggered {
			statusStr = "triggered"
		} else if hedgeID != "" && o.tradingService != nil {
			if ord, ok := o.tradingService.GetOrder(hedgeID); ok && ord != nil && ord.IsFilled() {
				statusStr = "completed"
			}
		}

		watchDetails = append(watchDetails, PriceStopWatchInfo{
			EntryOrderID:      entryID,
			EntryTokenType:    string(w.entryToken),
			EntryPriceCents:   w.entryAskCents,
			EntrySize:         w.entryFilledSize,
			HedgeOrderID:      hedgeID,
			CurrentProfitCents: profitNow,
			SoftHits:          w.softHits,
			TakeProfitHits:    w.tpHits,
			LastEvalTime:      w.lastEval,
			Status:            statusStr,
		})

		if !w.lastEval.IsZero() && (latestEvalTime.IsZero() || w.lastEval.After(latestEvalTime)) {
			latestEvalTime = w.lastEval
		}
	}
	o.mu.RUnlock()

	status.ActiveWatches = len(watchDetails)
	status.WatchDetails = watchDetails
	status.LastEvalTime = latestEvalTime

	return status
}

// PriceStopWatchesStatus 价格盯盘状态（避免循环导入）
type PriceStopWatchesStatus struct {
	Enabled         bool
	ActiveWatches   int
	WatchDetails    []PriceStopWatchInfo
	SoftLossCents   int
	HardLossCents   int
	TakeProfitCents int
	ConfirmTicks    int
	LastEvalTime    time.Time
}

// PriceStopWatchInfo 单个价格盯盘协程的详细信息
type PriceStopWatchInfo struct {
	EntryOrderID       string
	EntryTokenType     string
	EntryPriceCents    int
	EntrySize          float64
	HedgeOrderID       string
	CurrentProfitCents int
	SoftHits           int
	TakeProfitHits     int
	LastEvalTime       time.Time
	Status             string // "monitoring" | "triggered" | "completed"
}
