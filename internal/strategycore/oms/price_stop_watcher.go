package oms

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/sirupsen/logrus"
)

var priceStopLog = logrus.WithField("module", "price_stop_watcher")

// 可选配置（仅 winbet 等策略实现；不强制所有策略更新配置接口）。
type priceStopConfig interface {
	GetPriceStopEnabled() bool
	GetPriceStopSoftLossCents() int   // 触发阈值（例如 -5）
	GetPriceStopHardLossCents() int   // 紧急阈值（例如 -10）
	GetPriceStopCheckIntervalMs() int // 盯盘频率
	GetPriceStopConfirmTicks() int    // soft 触发需要连续命中次数（防抖）
}

type priceStopParams struct {
	enabled       bool
	softLossCents int
	hardLossCents int
	interval      time.Duration
	confirmTicks  int
}

func (o *OMS) priceStopParams() priceStopParams {
	// 默认：保守（只在配置实现且 enabled=true 时启动）
	p := priceStopParams{
		enabled:       false,
		softLossCents: -5,
		hardLossCents: -10,
		interval:      200 * time.Millisecond,
		confirmTicks:  2,
	}

	if o == nil || o.config == nil {
		return p
	}
	c, ok := o.config.(priceStopConfig)
	if !ok || !c.GetPriceStopEnabled() {
		return p
	}
	p.enabled = true
	if v := c.GetPriceStopSoftLossCents(); v != 0 {
		p.softLossCents = v
	}
	if v := c.GetPriceStopHardLossCents(); v != 0 {
		p.hardLossCents = v
	}
	// 约束：soft 必须“比 hard 更不极端”（例如 -5 > -10）
	if p.softLossCents < p.hardLossCents {
		// 若用户填反了，自动纠正
		p.softLossCents, p.hardLossCents = p.hardLossCents, p.softLossCents
	}
	if ms := c.GetPriceStopCheckIntervalMs(); ms > 0 {
		p.interval = time.Duration(ms) * time.Millisecond
	}
	if p.interval < 50*time.Millisecond {
		p.interval = 50 * time.Millisecond
	}
	if p.interval > 2*time.Second {
		p.interval = 2 * time.Second
	}
	if n := c.GetPriceStopConfirmTicks(); n > 0 {
		p.confirmTicks = n
	}
	if p.confirmTicks < 1 {
		p.confirmTicks = 1
	}
	if p.confirmTicks > 10 {
		p.confirmTicks = 10
	}
	return p
}

func (o *OMS) startPriceStopWatcher(entryOrder *domain.Order, hedgeOrderID string) {
	if o == nil || entryOrder == nil || entryOrder.OrderID == "" || hedgeOrderID == "" {
		return
	}

	pp := o.priceStopParams()
	if !pp.enabled {
		return
	}

	entryID := entryOrder.OrderID

	o.mu.Lock()
	// 防止重复启动
	if o.priceWatchCancel == nil {
		o.priceWatchCancel = make(map[string]context.CancelFunc)
	}
	if _, exists := o.priceWatchCancel[entryID]; exists {
		o.mu.Unlock()
		return
	}
	wCtx, cancel := context.WithCancel(context.Background())
	o.priceWatchCancel[entryID] = cancel
	o.mu.Unlock()

	// entry 成本（优先成交价）
	entryAskCents := entryOrder.Price.ToCents()
	if entryOrder.FilledPrice != nil {
		entryAskCents = entryOrder.FilledPrice.ToCents()
	}
	if entryAskCents <= 0 {
		cancel()
		o.mu.Lock()
		delete(o.priceWatchCancel, entryID)
		o.mu.Unlock()
		return
	}

	// entry 成交量（用于计算剩余未对冲数量）
	entryFilledSize := entryOrder.FilledSize
	if entryFilledSize <= 0 {
		entryFilledSize = entryOrder.Size
	}
	if entryFilledSize <= 0 {
		cancel()
		o.mu.Lock()
		delete(o.priceWatchCancel, entryID)
		o.mu.Unlock()
		return
	}

	entryToken := entryOrder.TokenType
	marketSlug := entryOrder.MarketSlug

	priceStopLog.WithFields(logrus.Fields{
		"market":         marketSlug,
		"entryOrderID":   entryID,
		"hedgeOrderID":   hedgeOrderID,
		"entryAskCents":  entryAskCents,
		"entrySize":      entryFilledSize,
		"softLossCents":  pp.softLossCents,
		"hardLossCents":  pp.hardLossCents,
		"interval":       pp.interval.String(),
		"confirmTicks":   pp.confirmTicks,
		"entryTokenType": entryToken,
	}).Info("📉 [PriceStop] start watcher")

	go o.priceStopLoop(wCtx, pp, marketSlug, entryID, hedgeOrderID, entryToken, entryAskCents, entryFilledSize)
}

func (o *OMS) priceStopLoop(
	ctx context.Context,
	pp priceStopParams,
	marketSlug string,
	entryOrderID string,
	initialHedgeOrderID string,
	entryToken domain.TokenType,
	entryAskCents int,
	entryFilledSize float64,
) {
	ticker := time.NewTicker(pp.interval)
	defer ticker.Stop()

	softHits := 0
	triggered := false

	for {
		select {
		case <-ctx.Done():
			o.cleanupPriceStop(entryOrderID)
			return
		case <-ticker.C:
			if o == nil || o.tradingService == nil {
				continue
			}

			// 若 entry 已不再处于 pending hedge，则停止（说明已对冲完成或被外部流程清理）
			hedgeOrderID := ""
			o.mu.RLock()
			if o.pendingHedges != nil {
				hedgeOrderID = o.pendingHedges[entryOrderID]
			}
			o.mu.RUnlock()
			if hedgeOrderID == "" {
				o.cleanupPriceStop(entryOrderID)
				return
			}

			// 如果 hedge 订单已成交，停止
			hedgeFilledSize := 0.0
			if ord, ok := o.tradingService.GetOrder(hedgeOrderID); ok && ord != nil {
				if ord.IsFilled() {
					o.cleanupPriceStop(entryOrderID)
					return
				}
				hedgeFilledSize = ord.FilledSize
				if hedgeFilledSize < 0 {
					hedgeFilledSize = 0
				}
			}

			remaining := entryFilledSize - hedgeFilledSize
			if remaining <= 0 {
				o.cleanupPriceStop(entryOrderID)
				return
			}

			market := o.getMarketForSlug(marketSlug)
			if market == nil {
				// 市场对象拿不到时不做强动作；等下一轮（更安全）
				continue
			}

			tobCtx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
			_, yesAsk, _, noAsk, _, err := o.tradingService.GetTopOfBook(tobCtx, market)
			cancel()
			if err != nil {
				continue
			}

			hedgeAsk := yesAsk
			if entryToken == domain.TokenTypeUp {
				// Entry=UP => hedge 买 NO，用 noAsk
				hedgeAsk = noAsk
			}
			hedgeAskCents := hedgeAsk.ToCents()
			if hedgeAskCents <= 0 {
				continue
			}

			lockedProfitCentsNow := 100 - (entryAskCents + hedgeAskCents)

			// hard stop：无需确认，立即触发
			if lockedProfitCentsNow <= pp.hardLossCents && !triggered {
				triggered = true
				priceStopLog.WithFields(logrus.Fields{
					"market":     marketSlug,
					"entry":      entryOrderID,
					"hedge":      hedgeOrderID,
					"profitNow":  lockedProfitCentsNow,
					"hardStop":   pp.hardLossCents,
					"softStop":   pp.softLossCents,
					"entryCost":  entryAskCents,
					"hedgeAsk":   hedgeAskCents,
					"remaining":  remaining,
					"firstHedge": initialHedgeOrderID,
				}).Warn("🚨 [PriceStop] hard stop triggered, locking loss via FAK")
				_ = o.lockLossByFAK(ctx, market, entryOrderID, hedgeOrderID, entryToken, hedgeAsk, remaining)
				continue
			}

			// soft stop：连续命中确认（防抖）
			if lockedProfitCentsNow <= pp.softLossCents && !triggered {
				softHits++
				if softHits >= pp.confirmTicks {
					triggered = true
					priceStopLog.WithFields(logrus.Fields{
						"market":     marketSlug,
						"entry":      entryOrderID,
						"hedge":      hedgeOrderID,
						"profitNow":  lockedProfitCentsNow,
						"softStop":   pp.softLossCents,
						"hardStop":   pp.hardLossCents,
						"entryCost":  entryAskCents,
						"hedgeAsk":   hedgeAskCents,
						"remaining":  remaining,
						"hits":       softHits,
						"firstHedge": initialHedgeOrderID,
					}).Warn("⚠️ [PriceStop] soft stop confirmed, locking loss via FAK")
					_ = o.lockLossByFAK(ctx, market, entryOrderID, hedgeOrderID, entryToken, hedgeAsk, remaining)
				}
				continue
			}

			// 回到安全区，清空计数
			softHits = 0
		}
	}
}

func (o *OMS) cleanupPriceStop(entryOrderID string) {
	if o == nil || entryOrderID == "" {
		return
	}
	o.mu.Lock()
	if o.priceWatchCancel != nil {
		if cancel, ok := o.priceWatchCancel[entryOrderID]; ok {
			// 避免外部已 cancel 时重复调用造成误解（cancel 本身幂等）
			if cancel != nil {
				cancel()
			}
			delete(o.priceWatchCancel, entryOrderID)
		}
	}
	o.mu.Unlock()
}

func (o *OMS) getMarketForSlug(marketSlug string) *domain.Market {
	if o == nil || o.tradingService == nil || marketSlug == "" {
		return nil
	}
	// 1) 当前市场
	if m := o.tradingService.GetCurrentMarketInfo(); m != nil && m.IsValid() && m.Slug == marketSlug {
		return m
	}
	// 2) 从持仓取（更稳）
	positions := o.tradingService.GetOpenPositionsForMarket(marketSlug)
	for _, p := range positions {
		if p != nil && p.Market != nil && p.Market.IsValid() && p.Market.Slug == marketSlug {
			return p.Market
		}
	}
	return nil
}

func (o *OMS) lockLossByFAK(
	ctx context.Context,
	market *domain.Market,
	entryOrderID string,
	currentHedgeOrderID string,
	entryToken domain.TokenType,
	hedgeAskPrice domain.Price,
	remaining float64,
) error {
	if o == nil || o.tradingService == nil || market == nil || entryOrderID == "" {
		return fmt.Errorf("invalid params")
	}
	if remaining <= 0 {
		return nil
	}
	remaining = math.Max(0, remaining)

	// 预算：记录（不阻断安全动作）
	if market.Slug != "" {
		o.RecordFAK(entryOrderID, market.Slug, time.Now())
	}

	// 先撤掉当前 GTC hedge（避免“撤单前后成交/残量”造成状态混乱）
	if currentHedgeOrderID != "" {
		cancelCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_ = cancelCtx
		_ = cancel
		// per-entry 撤单记录（用于冷静期/统计）
		if market.Slug != "" {
			o.RecordCancel(entryOrderID, market.Slug, time.Now())
		}
		_ = o.cancelOrder(cancelCtx, currentHedgeOrderID)
		cancel()
		time.Sleep(200 * time.Millisecond)
	}

	// 重新确认剩余数量（如果 hedge 在撤单前已部分成交）
	if currentHedgeOrderID != "" {
		if ord, ok := o.tradingService.GetOrder(currentHedgeOrderID); ok && ord != nil {
			if ord.IsFilled() {
				return nil
			}
			if ord.FilledSize > 0 {
				remaining = math.Max(0, remaining-ord.FilledSize)
			}
		}
		if remaining <= 0 {
			return nil
		}
	}

	hedgeToken := domain.TokenTypeDown
	hedgeAsset := market.NoAssetID
	if entryToken == domain.TokenTypeDown {
		hedgeToken = domain.TokenTypeUp
		hedgeAsset = market.YesAssetID
	}
	if hedgeAsset == "" {
		return fmt.Errorf("missing hedge assetID")
	}
	if hedgeAskPrice.Pips <= 0 {
		return fmt.Errorf("invalid hedge ask price")
	}

	fakOrder := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      hedgeAsset,
		TokenType:    hedgeToken,
		Side:         types.SideBuy,
		Price:        hedgeAskPrice,
		Size:         remaining,
		OrderType:    types.OrderTypeFAK,
		IsEntryOrder: false,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
	}
	entryRef := entryOrderID
	fakOrder.HedgeOrderID = &entryRef

	fakCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	res, err := o.placeOrder(fakCtx, fakOrder)
	if err != nil {
		return err
	}
	if res == nil || res.OrderID == "" {
		return fmt.Errorf("fak hedge orderID empty")
	}

	// 更新映射（关键：让成交后 merge/清理链路能跑通）
	o.RecordPendingHedge(entryOrderID, res.OrderID)
	if o.riskManager != nil {
		o.riskManager.UpdateHedgeOrderID(entryOrderID, res.OrderID)
	}

	// 若立刻成交，尽量主动清理（仍以 OnOrderUpdate 为准）
	if res.IsFilled() {
		o.mu.Lock()
		if o.pendingHedges != nil {
			if cur, ok := o.pendingHedges[entryOrderID]; ok && cur == res.OrderID {
				delete(o.pendingHedges, entryOrderID)
				o.clearEntryBudget(entryOrderID)
			}
		}
		o.mu.Unlock()

		// 触发 merge（与 aggressiveHedge 同思路，不等待回调）
		if o.capital != nil {
			go func(m *domain.Market) {
				time.Sleep(500 * time.Millisecond)
				o.capital.TryMergeCurrentCycle(context.Background(), m)
			}(market)
		}
		o.cleanupPriceStop(entryOrderID)
	}

	return nil
}
