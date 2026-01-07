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
	GetPriceStopSoftLossCents() int      // 触发阈值（例如 -5）
	GetPriceStopHardLossCents() int      // 紧急阈值（例如 -10）
	GetPriceTakeProfitCents() int        // 锁利阈值（例如 +5）；0 表示禁用
	GetPriceTakeProfitConfirmTicks() int // 锁利触发连续命中次数（防抖）
	GetPriceStopCheckIntervalMs() int    // 盯盘频率
	GetPriceStopConfirmTicks() int       // soft 触发需要连续命中次数（防抖）
}

type priceStopParams struct {
	enabled                bool
	softLossCents          int
	hardLossCents          int
	takeProfitCents        int
	interval               time.Duration
	confirmTicks           int
	takeProfitConfirmTicks int
}

type priceStopWatch struct {
	marketSlug        string
	entryToken        domain.TokenType
	entryAskCents     int
	entryFilledSize   float64
	firstHedgeOrderID string

	softHits  int
	tpHits    int
	triggered bool
	lastEval  time.Time
}

func (o *OMS) priceStopParams() priceStopParams {
	// 默认：保守（只在配置实现且 enabled=true 时启动）
	p := priceStopParams{
		enabled:                false,
		softLossCents:          -5,
		hardLossCents:          -10,
		takeProfitCents:        0, // 默认不开启“吃单锁利”，避免增加 taker 成本；可按策略目标开启
		interval:               0, // 事件驱动默认不节流（每次 WS 价格变化都评估）
		confirmTicks:           2,
		takeProfitConfirmTicks: 2,
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
	if v := c.GetPriceTakeProfitCents(); v != 0 {
		p.takeProfitCents = v
	}
	if n := c.GetPriceTakeProfitConfirmTicks(); n > 0 {
		p.takeProfitConfirmTicks = n
	}
	// 约束：soft 必须“比 hard 更不极端”（例如 -5 > -10）
	if p.softLossCents < p.hardLossCents {
		// 若用户填反了，自动纠正
		p.softLossCents, p.hardLossCents = p.hardLossCents, p.softLossCents
	}
	if ms := c.GetPriceStopCheckIntervalMs(); ms > 0 {
		p.interval = time.Duration(ms) * time.Millisecond
	}
	// interval==0 表示不节流；>0 则做合理限幅（避免误配导致 CPU 风暴）
	if p.interval > 0 && p.interval < 20*time.Millisecond {
		p.interval = 20 * time.Millisecond
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
	if p.takeProfitConfirmTicks < 1 {
		p.takeProfitConfirmTicks = 1
	}
	if p.takeProfitConfirmTicks > 10 {
		p.takeProfitConfirmTicks = 10
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
	if o.priceStopWatches == nil {
		o.priceStopWatches = make(map[string]*priceStopWatch)
	}
	// 防止重复注册
	if _, exists := o.priceStopWatches[entryID]; exists {
		o.mu.Unlock()
		return
	}
	o.mu.Unlock()

	// entry 成本（优先成交价）
	entryAskCents := entryOrder.Price.ToCents()
	if entryOrder.FilledPrice != nil {
		entryAskCents = entryOrder.FilledPrice.ToCents()
	}
	if entryAskCents <= 0 {
		return
	}

	// entry 成交量（用于计算剩余未对冲数量）
	entryFilledSize := entryOrder.FilledSize
	if entryFilledSize <= 0 {
		entryFilledSize = entryOrder.Size
	}
	if entryFilledSize <= 0 {
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
	}).Info("📉 [PriceStop] register watcher (event-driven)")

	o.mu.Lock()
	o.priceStopWatches[entryID] = &priceStopWatch{
		marketSlug:        marketSlug,
		entryToken:        entryToken,
		entryAskCents:     entryAskCents,
		entryFilledSize:   entryFilledSize,
		firstHedgeOrderID: hedgeOrderID,
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
		// 风控动作：允许绕过短时 risk-off（否则极端情况下可能拒单，导致敞口扩大）
		BypassRiskOff: true,
		// 对冲/止损属于严格一对一：避免系统自动放大 size 造成过度对冲
		DisableSizeAdjust: true,
		Status:            domain.OrderStatusPending,
		CreatedAt:         time.Now(),
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
		// 事件驱动 watcher：对冲完成后移除监控
		o.mu.Lock()
		if o.priceStopWatches != nil {
			delete(o.priceStopWatches, entryOrderID)
		}
		o.mu.Unlock()
	}

	return nil
}
