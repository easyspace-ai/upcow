package volmm

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/rtds"
	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

type inventorySnapshot struct {
	Up   float64
	Down float64
	Net  float64 // Up - Down
}

// Strategy: 盘中波动做市（Delta 近中性）。
//
// 输入：
// - Market WS：UP/DOWN top-of-book（用于挂单）
// - RTDS Chainlink：BTC/USD（用于 strike + 实时价格）
//
// 输出：
// - 常规窗口：四边 GTC 报价（UP buy/sell, DOWN buy/sell）
// - 风控窗口：撤单 +（可选）只降风险的 flatten（SELL FAK）
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	mu sync.Mutex

	autoMerge common.AutoMergeController

	// rtds (chainlink)
	rtdsClient *rtds.Client
	underlying *underlyingTracker
	lastChainlinkPrice float64
	lastChainlinkAt    time.Time

	// market state
	currentSlug   string
	cycleStartSec int64
	strikePrice   float64
	strikeSet     bool

	// precision / constraints
	tickPips     int
	orderTickSize types.TickSize
	negRisk       *bool
	minShareSize  float64

	// quoting
	lastQuoteAt   time.Time
	lastFlattenAt time.Time
	quoteOrders   map[quoteKey]*trackedOrder

	// window switch state
	inRiskOnly bool
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }
func (s *Strategy) Defaults() error { return nil }
func (s *Strategy) Validate() error { return s.Config.Validate() }

func (s *Strategy) Initialize() error {
	if s.quoteOrders == nil {
		s.quoteOrders = make(map[quoteKey]*trackedOrder)
	}
	s.underlying = newUnderlyingTracker(s.Config)

	// global constraints
	gc := config.Get()
	if gc != nil {
		s.minShareSize = gc.MinShareSize
		if s.minShareSize <= 0 {
			s.minShareSize = 5
		}
		// tick/negRisk from global market precision if present
		if gc.Market.Precision != nil {
			if tickPips, err := tickPipsFromTickSizeStr(gc.Market.Precision.TickSize); err == nil && tickPips > 0 {
				s.tickPips = tickPips
			}
			if ts, err := parseTickSizeForOrder(gc.Market.Precision.TickSize); err == nil {
				s.orderTickSize = ts
			}
			s.negRisk = boolPtr(gc.Market.Precision.NegRisk)
		}
	}
	if s.tickPips <= 0 {
		// fallback to default 0.001 => 10 pips
		s.tickPips = 10
	}
	if s.orderTickSize == "" {
		// fallback: match 0.001
		s.orderTickSize = types.TickSize0001
	}

	// connect RTDS chainlink
	rtdsCfg := rtds.DefaultClientConfig()
	// proxy env is already set by app; keep rtdsCfg.ProxyURL empty unless user set env.
	s.rtdsClient = rtds.NewClientWithConfig(rtdsCfg)
	if err := s.rtdsClient.Connect(); err != nil {
		return fmt.Errorf("[%s] 连接 RTDS 失败: %w", ID, err)
	}

	chainlinkHandler := rtds.CreateCryptoPriceHandler(func(p *rtds.CryptoPrice) error {
		sym := strings.ToLower(strings.TrimSpace(p.Symbol))
		if sym != strings.ToLower(strings.TrimSpace(s.Config.ChainlinkSymbol)) {
			return nil
		}
		val := p.Value.Float64()
		if val <= 0 {
			return nil
		}
		ts := time.Unix(p.Timestamp/1000, (p.Timestamp%1000)*1000000)

		s.mu.Lock()
		s.lastChainlinkPrice = val
		s.lastChainlinkAt = ts
		s.underlying.Update(ts, val)
		// 如果本周期 strike 还没设置，使用“首次可用的 chainlink 报价”作为 strike
		if s.currentSlug != "" && !s.strikeSet && s.cycleStartSec > 0 {
			// 只在周期开始后才允许设置（避免盘前消息写入）
			if ts.Unix() >= s.cycleStartSec {
				s.strikePrice = val
				s.strikeSet = true
				log.Infof("🎯 [%s] strike 已设置: %.2f (cycle=%s start=%d)", ID, s.strikePrice, s.currentSlug, s.cycleStartSec)
			}
		}
		s.mu.Unlock()
		return nil
	})

	s.rtdsClient.RegisterHandler("crypto_prices_chainlink", chainlinkHandler)
	if err := s.rtdsClient.SubscribeToCryptoPrices("chainlink", s.Config.ChainlinkSymbol); err != nil {
		return fmt.Errorf("[%s] 订阅 Chainlink 失败: %w", ID, err)
	}

	// register order update callback (recommended)
	if s.TradingService != nil {
		handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
		s.TradingService.OnOrderUpdate(handler)
	}
	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	// Subscribe 中也注册一次回调作为兜底（类似 velocityfollow）
	if s.TradingService != nil {
		handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
		s.TradingService.OnOrderUpdate(handler)
	}
	log.Infof("✅ [%s] 已订阅价格变化事件 (session=%s)", ID, session.Name)
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, newMarket *domain.Market) {
	if newMarket == nil || newMarket.Slug == "" {
		return
	}
	s.mu.Lock()
	s.currentSlug = newMarket.Slug
	s.cycleStartSec = newMarket.Timestamp
	s.strikePrice = 0
	s.strikeSet = false
	s.inRiskOnly = false
	s.lastQuoteAt = time.Time{}
	s.lastFlattenAt = time.Time{}
	s.quoteOrders = make(map[quoteKey]*trackedOrder)
	s.mu.Unlock()
	log.Infof("🔄 [%s] 周期切换: market=%s start=%d", ID, newMarket.Slug, newMarket.Timestamp)
}

func (s *Strategy) OnOrderUpdate(_ context.Context, order *domain.Order) error {
	if s == nil || order == nil || order.OrderID == "" {
		return nil
	}
	// 清理本策略跟踪的 quoteOrders（若订单结束）
	if order.Status != domain.OrderStatusFilled &&
		order.Status != domain.OrderStatusCanceled &&
		order.Status != domain.OrderStatusFailed {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for k, tr := range s.quoteOrders {
		if tr != nil && tr.OrderID == order.OrderID {
			delete(s.quoteOrders, k)
			break
		}
	}
	return nil
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}
	s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)

	// 过滤旧周期事件：确保事件 market 与 TradingService 当前 market 一致
	if cur := s.TradingService.GetCurrentMarket(); cur != "" && cur != e.Market.Slug {
		return nil
	}

	now := e.Timestamp
	if now.IsZero() {
		now = time.Now()
	}

	// 兜底：如果 OnCycle 尚未初始化（极端竞态），这里初始化一次
	s.mu.Lock()
	if s.currentSlug == "" || s.currentSlug != e.Market.Slug {
		s.currentSlug = e.Market.Slug
		s.cycleStartSec = e.Market.Timestamp
		s.strikePrice = 0
		s.strikeSet = false
		s.inRiskOnly = false
		s.quoteOrders = make(map[quoteKey]*trackedOrder)
	}
	cycleStart := s.cycleStartSec
	tradeStart := s.Config.TradeStartAtSeconds
	tradeStop := s.Config.TradeStopAtSeconds
	riskEnabled := s.Config.RiskOnlyEnabled != nil && *s.Config.RiskOnlyEnabled
	s.mu.Unlock()

	if cycleStart <= 0 {
		return nil
	}

	elapsed := int(now.Unix() - cycleStart)
	if elapsed < 0 {
		// 盘前：elapsed<0 说明 market.Timestamp 在未来，直接跳过（默认不做盘前）
		return nil
	}
	if elapsed < tradeStart {
		return nil
	}

	if riskEnabled && elapsed >= tradeStop {
		return s.onRiskOnly(ctx, e.Market, now)
	}
	return s.onNormal(ctx, e.Market, now)
}

func (s *Strategy) onRiskOnly(ctx context.Context, market *domain.Market, now time.Time) error {
	_ = now
	s.mu.Lock()
	already := s.inRiskOnly
	s.inRiskOnly = true
	cancelQuotes := (s.Config.RiskOnlyCancelAllQuotes != nil && *s.Config.RiskOnlyCancelAllQuotes) ||
		(s.Config.CancelOnWindowSwitch != nil && *s.Config.CancelOnWindowSwitch)
	s.mu.Unlock()

	// 进入 risk-only 时撤掉做市单（只做一次）
	if cancelQuotes && !already {
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		s.cancelAllQuotes(cctx, market.Slug)
		cancel()
	}

	// 仍可执行 “只降风险” 的 flatten
	orderCtx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()

	yesBid, _, noBid, _, _, err := s.TradingService.GetTopOfBook(orderCtx, market)
	if err != nil {
		return nil
	}
	inv := s.inventoryForMarket(market.Slug)
	s.flattenIfNeeded(orderCtx, market, yesBid, noBid, inv.Net, inv)
	return nil
}

func (s *Strategy) onNormal(ctx context.Context, market *domain.Market, now time.Time) error {
	// throttle quote loop
	s.mu.Lock()
	if !s.lastQuoteAt.IsZero() && now.Sub(s.lastQuoteAt) < time.Duration(s.Config.QuoteIntervalMs)*time.Millisecond {
		s.mu.Unlock()
		return nil
	}
	s.lastQuoteAt = now
	strike := s.strikePrice
	strikeSet := s.strikeSet
	chainlink := s.lastChainlinkPrice
	chainlinkAt := s.lastChainlinkAt
	s.mu.Unlock()

	// strike 未就绪时不报价（避免无锚定）
	if !strikeSet || strike <= 0 || chainlink <= 0 {
		return nil
	}
	// 避免使用太旧的 chainlink（防止断流时乱报价）
	if !chainlinkAt.IsZero() && time.Since(chainlinkAt) > 15*time.Second {
		return nil
	}

	// market quality gate（可选）
	if mqOpt := s.mkMQOptions(); mqOpt != nil {
		mq, err := s.TradingService.GetMarketQuality(ctx, market, mqOpt)
		if err != nil || mq == nil || mq.Score < s.Config.MarketQualityMinScore {
			return nil
		}
	}

	// top-of-book
	orderCtx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	yesBid, yesAsk, noBid, noAsk, _, err := s.TradingService.GetTopOfBook(orderCtx, market)
	if err != nil {
		return nil
	}
	if yesBid.Pips <= 0 || yesAsk.Pips <= 0 || noBid.Pips <= 0 || noAsk.Pips <= 0 {
		// 盘口不完整时不做市（避免单边成交）
		return nil
	}

	// compute remaining seconds using wall clock; event time is local receive time
	elapsed := float64(now.Unix() - market.Timestamp)
	remaining := float64(s.Config.MarketIntervalSeconds) - elapsed
	if remaining < 1 {
		remaining = 1
	}

	// momentum features
	s.mu.Lock()
	feat := s.underlying.Features(now)
	s.mu.Unlock()
	pr := computePricing(s.Config, strike, chainlink, remaining, feat)
	if pr.FairUp <= 0 || pr.FairDown <= 0 {
		return nil
	}

	// spread and skew
	sHalf := mathMax3(
		s.Config.SMin,
		s.Config.Alpha*mathAbs(pr.Feat.VelNorm),
		s.Config.Beta/math.Sqrt(remaining),
	)
	inv := s.inventoryForMarket(market.Slug)
	skew := s.Config.KDelta * clip(inv.Net/s.Config.DeltaMaxShares, -1, 1) * sHalf

	// desired prices in pips (probability space)
	upBuy := domain.PriceFromDecimal(pr.FairUp - sHalf - skew).Pips
	upSell := domain.PriceFromDecimal(pr.FairUp + sHalf - skew).Pips
	downBuy := domain.PriceFromDecimal(pr.FairDown - sHalf + skew).Pips
	downSell := domain.PriceFromDecimal(pr.FairDown + sHalf + skew).Pips

	// round/clamp to tick
	upBuy = clampPricePips(roundDownToTick(upBuy, s.tickPips), s.tickPips)
	downBuy = clampPricePips(roundDownToTick(downBuy, s.tickPips), s.tickPips)
	upSell = clampPricePips(roundUpToTick(upSell, s.tickPips), s.tickPips)
	downSell = clampPricePips(roundUpToTick(downSell, s.tickPips), s.tickPips)

	// sizes
	qSize := s.Config.QuoteSizeShares
	if qSize < s.minShareSize {
		qSize = s.minShareSize
	}

	// For sells, cap by available inventory (avoid short).
	upSellSize := qSize
	if inv.Up > 0 && upSellSize > inv.Up {
		upSellSize = inv.Up
	}
	downSellSize := qSize
	if inv.Down > 0 && downSellSize > inv.Down {
		downSellSize = inv.Down
	}

	quotes := []desiredQuote{
		{key: quoteKey{token: domain.TokenTypeUp, side: sideBuy}, pricePips: upBuy, size: qSize},
		{key: quoteKey{token: domain.TokenTypeUp, side: sideSell}, pricePips: upSell, size: upSellSize},
		{key: quoteKey{token: domain.TokenTypeDown, side: sideBuy}, pricePips: downBuy, size: qSize},
		{key: quoteKey{token: domain.TokenTypeDown, side: sideSell}, pricePips: downSell, size: downSellSize},
	}

	// apply
	s.mu.Lock()
	// copy orders map reference; we hold lock while placing/canceling? avoid; but TradingService IO should be outside lock.
	s.mu.Unlock()

	for _, q := range quotes {
		if q.key.side == sideSell && q.size < s.minShareSize {
			// 无可卖库存，跳过该 sell
			continue
		}
		assetID := market.GetAssetID(q.key.token)
		bestBidPips := yesBid.Pips
		bestAskPips := yesAsk.Pips
		if q.key.token == domain.TokenTypeDown {
			bestBidPips = noBid.Pips
			bestAskPips = noAsk.Pips
		}

		s.mu.Lock()
		// ensure map exists
		if s.quoteOrders == nil {
			s.quoteOrders = make(map[quoteKey]*trackedOrder)
		}
		s.mu.Unlock()

		s.syncQuote(orderCtx, market, assetID, q, bestBidPips, bestAskPips)
	}

	return nil
}

func (s *Strategy) inventoryForMarket(marketSlug string) inventorySnapshot {
	var up, down float64
	if s == nil || s.TradingService == nil || marketSlug == "" {
		return inventorySnapshot{}
	}
	positions := s.TradingService.GetOpenPositionsForMarket(marketSlug)
	for _, p := range positions {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}
		if p.TokenType == domain.TokenTypeUp {
			up += p.Size
		} else if p.TokenType == domain.TokenTypeDown {
			down += p.Size
		}
	}
	return inventorySnapshot{Up: up, Down: down, Net: up - down}
}

func clip(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func mathMax3(a, b, c float64) float64 {
	m := a
	if b > m {
		m = b
	}
	if c > m {
		m = c
	}
	return m
}

