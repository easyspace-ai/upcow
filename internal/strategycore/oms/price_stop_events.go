package oms

import (
	"context"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/sirupsen/logrus"
)

// OnPriceChanged 事件驱动的止损评估：每次 WS 价格变化都触发一次评估（无轮询）。
//
// 设计目标：
// - 不依赖 time-based 超时触发（价格不利立即处理）
// - 只读路径尽量轻：读 bestbook 原子快照 + 读本地订单状态缓存
// - 真正的“撤单+FAK”写动作放到 goroutine，避免阻塞价格事件主循环
func (o *OMS) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	_ = ctx
	if o == nil || o.tradingService == nil || e == nil || e.Market == nil || e.Market.Slug == "" {
		return nil
	}

	pp := o.priceStopParams()
	if !pp.enabled {
		return nil
	}

	// WS bestbook 快照（真实 bid/ask）；无快照时无法做“可锁定PnL”评估
	snap, ok := o.tradingService.BestBookSnapshot()
	if !ok || snap.UpdatedAt.IsZero() {
		return nil
	}
	// 过旧快照直接跳过（避免用 stale 盘口触发错误止损）
	if time.Since(snap.UpdatedAt) > 3*time.Second {
		return nil
	}

	marketSlug := e.Market.Slug

	// 计算对冲侧 ask（cents/price）
	yesAsk := domain.Price{Pips: int(snap.YesAskPips)}
	noAsk := domain.Price{Pips: int(snap.NoAskPips)}
	yesAskCents := yesAsk.ToCents()
	noAskCents := noAsk.ToCents()

	type trigger struct {
		entryID       string
		hedgeID       string
		entryToken    domain.TokenType
		entryAskCents int
		remaining     float64
		hedgeAsk      domain.Price
		why           string
		profitNow     int
		firstHedge    string
	}

	now := time.Now()
	triggers := make([]trigger, 0, 2)

	o.mu.Lock()
	if o.priceStopWatches == nil || len(o.priceStopWatches) == 0 {
		o.mu.Unlock()
		return nil
	}
	for entryID, w := range o.priceStopWatches {
		if w == nil || w.marketSlug != marketSlug {
			continue
		}

		hedgeID := ""
		if o.pendingHedges != nil {
			hedgeID = o.pendingHedges[entryID]
		}
		if hedgeID == "" {
			delete(o.priceStopWatches, entryID)
			continue
		}

		// optional throttle：避免极端 WS 高频导致 CPU 过载（默认 interval=0 不节流）
		if pp.interval > 0 && !w.lastEval.IsZero() && now.Sub(w.lastEval) < pp.interval {
			continue
		}
		w.lastEval = now

		// 计算剩余未对冲数量（支持 hedge 部分成交）
		hedgeFilled := 0.0
		if ord, ok := o.tradingService.GetOrder(hedgeID); ok && ord != nil {
			if ord.IsFilled() {
				delete(o.priceStopWatches, entryID)
				continue
			}
			if ord.FilledSize > 0 {
				hedgeFilled = ord.FilledSize
			}
		}
		remaining := w.entryFilledSize - hedgeFilled
		if remaining <= 0 {
			delete(o.priceStopWatches, entryID)
			continue
		}

		// 选择对冲侧 ask
		hedgeAsk := yesAsk
		hedgeAskCents := yesAskCents
		if w.entryToken == domain.TokenTypeUp {
			// Entry=UP => hedge 买 NO
			hedgeAsk = noAsk
			hedgeAskCents = noAskCents
		}
		if hedgeAskCents <= 0 {
			continue
		}

		profitNow := 100 - (w.entryAskCents + hedgeAskCents)

		// hard stop：立即触发
		if !w.triggered && profitNow <= pp.hardLossCents {
			w.triggered = true
			delete(o.priceStopWatches, entryID)
			triggers = append(triggers, trigger{
				entryID:       entryID,
				hedgeID:       hedgeID,
				entryToken:    w.entryToken,
				entryAskCents: w.entryAskCents,
				remaining:     remaining,
				hedgeAsk:      hedgeAsk,
				why:           "hard",
				profitNow:     profitNow,
				firstHedge:    w.firstHedgeOrderID,
			})
			continue
		}

		// soft stop：连续命中确认
		if !w.triggered && profitNow <= pp.softLossCents {
			w.softHits++
			if w.softHits >= pp.confirmTicks {
				w.triggered = true
				delete(o.priceStopWatches, entryID)
				triggers = append(triggers, trigger{
					entryID:       entryID,
					hedgeID:       hedgeID,
					entryToken:    w.entryToken,
					entryAskCents: w.entryAskCents,
					remaining:     remaining,
					hedgeAsk:      hedgeAsk,
					why:           "soft",
					profitNow:     profitNow,
					firstHedge:    w.firstHedgeOrderID,
				})
			}
		} else {
			// 回到安全区：清空命中计数
			w.softHits = 0
		}
	}
	o.mu.Unlock()

	if len(triggers) == 0 {
		return nil
	}

	for _, t := range triggers {
		fields := logrus.Fields{
			"market":     marketSlug,
			"entry":      t.entryID,
			"hedge":      t.hedgeID,
			"profitNow":  t.profitNow,
			"softStop":   pp.softLossCents,
			"hardStop":   pp.hardLossCents,
			"entryCost":  t.entryAskCents,
			"hedgeAsk":   t.hedgeAsk.ToCents(),
			"remaining":  t.remaining,
			"firstHedge": t.firstHedge,
			"why":        t.why,
		}
		if t.why == "hard" {
			priceStopLog.WithFields(fields).Warn("🚨 [PriceStop] hard stop triggered (event-driven), locking loss via FAK")
		} else {
			priceStopLog.WithFields(fields).Warn("⚠️ [PriceStop] soft stop triggered (event-driven), locking loss via FAK")
		}

		// 写动作放 goroutine，避免阻塞价格事件循环
		go func(tt trigger) {
			_ = o.lockLossByFAK(context.Background(), e.Market, tt.entryID, tt.hedgeID, tt.entryToken, tt.hedgeAsk, tt.remaining)
		}(t)
	}

	return nil
}
