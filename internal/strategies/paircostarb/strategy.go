package paircostarb

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/common"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/ports"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/bbgo"
)

func init() {
	bbgo.RegisterStrategy("paircostarb", &Strategy{})
}

// Strategy：基于盘口深度 VWAP 的 pair-cost 套利（分批买入 UP/DOWN，锁定 complete-set 正收益）。
type Strategy struct {
	TradingService *services.TradingService `json:"-" yaml:"-"`
	// BinanceFuturesKlines 由 Environment 注入，用于秒级 K 线信号。
	BinanceFuturesKlines *services.BinanceFuturesKlines `json:"-" yaml:"-"`

	Config `json:",inline" yaml:",inline"`

	orderExecutor bbgo.OrderExecutor
	log           *logrus.Entry

	st state

	started atomic.Bool
}

func (s *Strategy) ID() string   { return "paircostarb" }
func (s *Strategy) Name() string { return s.ID() }

func (s *Strategy) Defaults() error {
	s.Config.Defaults()
	if s.log == nil {
		s.log = logrus.WithField("strategy", s.ID())
	}
	return nil
}

func (s *Strategy) Validate() error {
	s.Config.Defaults()
	return s.Config.Validate()
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	if s.log == nil {
		s.log = logrus.WithField("strategy", s.ID())
	}
	if session == nil {
		return
	}
	// 订单更新：通过 TradingService 的 OrderEngine（优先）+ 兼容 UserWS
	if s.TradingService != nil {
		s.TradingService.OnOrderUpdate(s)
		if session.BestBook() != nil {
			s.TradingService.SetBestBook(session.BestBook())
		}
	}
	if session.UserDataStream != nil {
		session.UserDataStream.OnOrderUpdate(s)
		session.UserDataStream.OnTradeUpdate(s)
	}
}

func (s *Strategy) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	_ = ctx
	s.st.mu.Lock()
	defer s.st.mu.Unlock()

	// 周期切换：重置运行期累计（避免跨周期污染）
	s.st.rt.market = newMarket
	s.st.rt.tradesThisCycle = 0
	s.st.rt.pausedUntil = time.Now().Add(800 * time.Millisecond)
	s.st.rt.inFlightIDs = make(map[string]domain.TokenType)
	s.st.rt.filledSeen = make(map[string]float64)
	s.st.rt.qu, s.st.rt.qd = 0, 0
	s.st.rt.cu, s.st.rt.cd = 0, 0
	s.st.rt.fillsUp = nil
	s.st.rt.fillsDown = nil
	s.st.rt.upCur = fifoCursor{}
	s.st.rt.downCur = fifoCursor{}
	s.st.rt.qPair = 0
	s.st.rt.costPair = 0
	s.st.rt.lastDecisionAt = time.Time{}

	// 周期切换后：可选合并上一周期持仓（异步，不阻塞）
	if oldMarket != nil && s.TradingService != nil && s.Config.AutoMerge.Enabled {
		mkt := oldMarket
		cfg := s.Config.AutoMerge
		ts := s.TradingService
		log := s.log
		go func() {
			// 等待数据同步
			time.Sleep(2 * time.Second)
			var ctl common.AutoMergeController
			ctl.MaybeAutoMerge(
				context.Background(),
				ts,
				mkt,
				cfg,
				func(format string, args ...any) { log.Infof("[上一周期] "+format, args...) },
				nil,
			)
		}()
	}
}

func (s *Strategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	s.orderExecutor = orderExecutor
	s.Config.Defaults()
	if s.log == nil {
		s.log = logrus.WithField("strategy", s.ID())
	}
	if !s.started.Swap(true) {
		s.log.Infof("✅ 策略启动：%s enabled=%v", s.ID(), s.Config.Enabled)
	}
	if !s.Config.Enabled {
		<-ctx.Done()
		return ctx.Err()
	}
	if s.TradingService == nil {
		<-ctx.Done()
		return fmt.Errorf("TradingService is nil")
	}
	if session != nil && session.BestBook() != nil {
		s.TradingService.SetBestBook(session.BestBook())
	}

	interval := time.Duration(s.Config.PollIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}

	// 初始化市场
	if session != nil {
		s.st.mu.Lock()
		s.st.rt.market = session.Market()
		if s.st.rt.filledSeen == nil {
			s.st.rt.filledSeen = make(map[string]float64)
		}
		if s.st.rt.inFlightIDs == nil {
			s.st.rt.inFlightIDs = make(map[string]domain.TokenType)
		}
		if s.Config.EnableBinanceSignal && s.st.rt.binanceSig == nil && s.BinanceFuturesKlines != nil {
			s.st.rt.binanceSig = NewBinanceKlineSignal(s.BinanceFuturesKlines, s.Config)
		}
		s.st.mu.Unlock()
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.tickOnce()
		}
	}
}

// OnOrderUpdate implements ports.OrderUpdateHandler.
func (s *Strategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	_ = ctx
	if order == nil || !s.Config.Enabled {
		return nil
	}
	// 只统计入场买单（BUY）
	if !order.IsEntryOrder || order.Side != types.SideBuy {
		return nil
	}
	if order.TokenType != domain.TokenTypeUp && order.TokenType != domain.TokenTypeDown {
		return nil
	}

	s.st.mu.Lock()
	defer s.st.mu.Unlock()

	mkt := s.st.rt.market
	if mkt == nil || mkt.Slug == "" {
		return nil
	}
	if order.MarketSlug != "" && order.MarketSlug != mkt.Slug {
		return nil
	}
	if s.st.rt.filledSeen == nil {
		s.st.rt.filledSeen = make(map[string]float64)
	}
	if s.st.rt.tradeFilledByOrder == nil {
		s.st.rt.tradeFilledByOrder = make(map[string]float64)
	}
	if s.st.rt.syntheticFilledByOrder == nil {
		s.st.rt.syntheticFilledByOrder = make(map[string]float64)
	}

	// 增量统计成交（处理 partial->filled 的多次回调）
	curFilled := order.FilledSize
	if curFilled <= 0 {
		return nil
	}
	prev := s.st.rt.filledSeen[order.OrderID]
	delta := curFilled - prev
	if delta <= 1e-9 {
		return nil
	}
	s.st.rt.filledSeen[order.OrderID] = curFilled

	// 若已收到 trade 级别回填，则只用 order update 做“缺口补齐”，避免 double count
	// missing = filledSize - sum(tradeSize) - sum(syntheticSize)
	knownTrades := s.st.rt.tradeFilledByOrder[order.OrderID]
	knownSynthetic := s.st.rt.syntheticFilledByOrder[order.OrderID]
	missing := curFilled - knownTrades - knownSynthetic
	if missing <= 1e-9 {
		// 仍可用于释放 in-flight gate
		if s.st.rt.inFlightIDs != nil {
			if _, ok := s.st.rt.inFlightIDs[order.OrderID]; ok {
				if order.IsFinalStatus() || order.Status == domain.OrderStatusPartial {
					delete(s.st.rt.inFlightIDs, order.OrderID)
				}
			}
		}
		return nil
	}

	// 使用 FilledPrice 优先；否则用订单 Price 兜底
	fillPrice := order.Price
	if order.FilledPrice != nil && order.FilledPrice.Pips > 0 {
		fillPrice = *order.FilledPrice
	}
	price := fillPrice.ToDecimal()
	if price <= 0 {
		return nil
	}

	feeRate := float64(s.Config.FeeRateBps) / 10000.0
	feeUSD := price * missing * feeRate
	cost := price*missing + feeUSD

	f := Fill{
		OrderID: order.OrderID,
		Time:    time.Now(),
		Qty:     missing,
		Price:   price,
		FeeUSD:  feeUSD,
		CostUSD: cost,
	}

	if order.TokenType == domain.TokenTypeUp {
		s.st.rt.qu += missing
		s.st.rt.cu += cost
		s.st.rt.fillsUp = append(s.st.rt.fillsUp, f)
	} else {
		s.st.rt.qd += missing
		s.st.rt.cd += cost
		s.st.rt.fillsDown = append(s.st.rt.fillsDown, f)
	}
	s.st.rt.syntheticFilledByOrder[order.OrderID] = knownSynthetic + missing

	// FIFO 配对：一旦两边都有“未配对库存”，立刻按时间顺序配对，得到严格的 qPair/costPair
	pairAvailableRuntimeLocked(&s.st.rt)

	// in-flight gate：订单进入终态/partial 时释放（对 FAK/并发下单更安全，避免卡死）
	if s.st.rt.inFlightIDs != nil {
		if _, ok := s.st.rt.inFlightIDs[order.OrderID]; ok {
			if order.IsFinalStatus() || order.Status == domain.OrderStatusPartial {
				delete(s.st.rt.inFlightIDs, order.OrderID)
			}
		}
	}

	// 执行质量：成交率（对 FAK/流动性差时很关键）
	if order.IsFinalStatus() && order.Size > 0 {
		ratio := order.FilledSize / order.Size
		s.st.rt.quality.InitDefaults()
		s.st.rt.quality.UpdateFillRatio(ratio)
		// 若成交率过低，进入短暂冷却（更像交易员：遇到流动性突然变差先停）
		if s.Config.EnableAdaptiveBuffers && s.Config.MinFillRatio > 0 {
			if v, ok := s.st.rt.quality.FillRatio.Value(); ok && v > 0 && v < s.Config.MinFillRatio {
				if s.Config.QualityCooldownSeconds > 0 {
					s.st.rt.pausedUntil = time.Now().Add(time.Duration(s.Config.QualityCooldownSeconds) * time.Second)
				}
			}
		}
	}

	return nil
}

// HandleTrade implements ports.TradeUpdateHandler.
// 说明：trade 级别回填更接近真实成交（price/size），因此优先用于 FIFO 成本核算；
// order update 仅作为“缺口补齐”（当 trade 丢失/延迟时）。
func (s *Strategy) HandleTrade(ctx context.Context, trade *domain.Trade) {
	_ = ctx
	if trade == nil || !s.Config.Enabled {
		return
	}
	if trade.ID == "" || trade.Size <= 0 || trade.Price.Pips <= 0 {
		return
	}
	if trade.TokenType != domain.TokenTypeUp && trade.TokenType != domain.TokenTypeDown {
		return
	}

	s.st.mu.Lock()
	defer s.st.mu.Unlock()

	mkt := s.st.rt.market
	if mkt == nil || mkt.Slug == "" {
		return
	}
	// 若 trade 带 market 且不匹配当前周期，则忽略
	if trade.Market != nil && trade.Market.Slug != "" && trade.Market.Slug != mkt.Slug {
		return
	}

	if s.st.rt.seenTradeIDs == nil {
		s.st.rt.seenTradeIDs = make(map[string]struct{}, 1024)
	}
	if _, ok := s.st.rt.seenTradeIDs[trade.ID]; ok {
		return
	}
	s.st.rt.seenTradeIDs[trade.ID] = struct{}{}

	if s.st.rt.tradeFilledByOrder == nil {
		s.st.rt.tradeFilledByOrder = make(map[string]float64)
	}
	if s.st.rt.predictedByOrder == nil {
		s.st.rt.predictedByOrder = make(map[string]float64)
	}

	price := trade.Price.ToDecimal()
	qty := trade.Size
	feeUSD := trade.Fee
	if feeUSD < 0 {
		feeUSD = 0
	}
	// 若 trade 未提供 fee，则用配置的 feeRate 做估算（至少不要为 0）
	if feeUSD == 0 {
		feeRate := float64(s.Config.FeeRateBps) / 10000.0
		feeUSD = price * qty * feeRate
	}
	cost := price*qty + feeUSD

	f := Fill{
		OrderID: trade.OrderID,
		Time:    trade.Time,
		Qty:     qty,
		Price:   price,
		FeeUSD:  feeUSD,
		CostUSD: cost,
	}
	if f.Time.IsZero() {
		f.Time = time.Now()
	}

	if trade.TokenType == domain.TokenTypeUp {
		s.st.rt.qu += qty
		s.st.rt.cu += cost
		s.st.rt.fillsUp = append(s.st.rt.fillsUp, f)
	} else {
		s.st.rt.qd += qty
		s.st.rt.cd += cost
		s.st.rt.fillsDown = append(s.st.rt.fillsDown, f)
	}
	if trade.OrderID != "" {
		s.st.rt.tradeFilledByOrder[trade.OrderID] += qty
		// 执行质量：预测 vs 实际
		if pred, ok := s.st.rt.predictedByOrder[trade.OrderID]; ok && pred > 0 {
			absErr := price - pred
			if absErr < 0 {
				absErr = -absErr
			}
			s.st.rt.quality.InitDefaults()
			s.st.rt.quality.UpdateSlipAbs(trade.TokenType, absErr)
		}
	}

	pairAvailableRuntimeLocked(&s.st.rt)
}

// ===== internals =====

type state struct {
	mu sync.Mutex
	rt runtimeState
}

type runtimeState struct {
	market *domain.Market

	// 累计成交（美元成本、shares 份额）
	qu float64
	qd float64
	cu float64
	cd float64

	// FIFO 成交队列（用于精确计算“已配对部分”的成本）
	fillsUp   []Fill
	fillsDown []Fill
	upCur     fifoCursor
	downCur   fifoCursor

	// 已配对份额与成本（严格 FIFO，costPair 含手续费）
	qPair    float64
	costPair float64

	// 去重/增量统计
	filledSeen   map[string]float64 // orderID -> lastFilledSize accounted
	seenTradeIDs map[string]struct{}
	// trade 级别累计：用于 order update 补缺口，避免 double count
	tradeFilledByOrder     map[string]float64 // orderID -> sum(trade.Size)
	syntheticFilledByOrder map[string]float64 // orderID -> sum(synthetic fills from order updates)
	// 预测价格：用于执行质量监控（predicted vwapEff）
	predictedByOrder map[string]float64 // orderID -> predicted vwapEff
	quality          QualityState

	// 控制策略节奏
	pausedUntil     time.Time
	tradesThisCycle int
	lastDecisionAt  time.Time

	// in-flight gate（避免下单风暴）：支持 paired 模式同时两笔订单在途
	inFlightIDs map[string]domain.TokenType // orderID -> tokenType

	// 每策略实例独立的 autoMerge controller
	autoMergeCtl common.AutoMergeController

	// Binance 信号（可选）
	binanceSig *BinanceKlineSignal
}

func (s *Strategy) tickOnce() {
	if !s.Config.Enabled || s.TradingService == nil || s.orderExecutor == nil {
		return
	}

	now := time.Now()

	// ===== 读运行态（必须复制 slice，避免与 OnOrderUpdate 并发写入产生数据竞态）=====
	s.st.mu.Lock()
	market := s.st.rt.market
	pausedUntil := s.st.rt.pausedUntil
	inFlight := len(s.st.rt.inFlightIDs) > 0
	tradesThisCycle := s.st.rt.tradesThisCycle
	qs := s.st.rt.quality
	baseSnap := Snapshot{
		Qu:        s.st.rt.qu,
		Qd:        s.st.rt.qd,
		Cu:        s.st.rt.cu,
		Cd:        s.st.rt.cd,
		FillsUp:   append([]Fill(nil), s.st.rt.fillsUp...),
		FillsDown: append([]Fill(nil), s.st.rt.fillsDown...),
		UpCur:     s.st.rt.upCur,
		DownCur:   s.st.rt.downCur,
		QPair:     s.st.rt.qPair,
		CostPair:  s.st.rt.costPair,
	}
	sig := s.st.rt.binanceSig
	s.st.mu.Unlock()

	if market == nil || !market.IsValid() {
		return
	}
	if !pausedUntil.IsZero() && now.Before(pausedUntil) {
		return
	}

	// 信号（从 Binance 秒级K线）
	sigDir := domain.TokenType("")
	sigActive := false
	if s.Config.EnableBinanceSignal && sig != nil {
		sigDir, sigActive = sig.Evaluate(now)
	}

	// top-of-book asks（用于 quick filter）
	_, yesAsk, _, noAsk, _, err := s.TradingService.GetTopOfBook(context.Background(), market)
	if err != nil {
		return
	}

	pc := PlanContext{
		Now:             now,
		Market:          market,
		Base:            baseSnap,
		TradesThisCycle: tradesThisCycle,
		InFlight:        inFlight,
		InEndProtection: isInCycleEndProtection(s.Config, now, market),
		SigDir:          sigDir,
		SigActive:       sigActive,
		YesAsk:          yesAsk.ToDecimal(),
		NoAsk:           noAsk.ToDecimal(),
	}

	feeRate := float64(s.Config.FeeRateBps) / 10000.0
	qs.InitDefaults()
	slipPadEff := s.Config.SlippagePad
	if s.Config.EnableAdaptiveBuffers {
		dyn := s.Config.SlippagePad + s.Config.AdaptiveSlipMultiplier*qs.MaxSlipAbs()
		if dyn > slipPadEff {
			slipPadEff = dyn
		}
		if s.Config.MaxAdaptiveSlippagePad > 0 && slipPadEff > s.Config.MaxAdaptiveSlippagePad {
			slipPadEff = s.Config.MaxAdaptiveSlippagePad
		}
	}
	est := func(ctx context.Context, assetID string, qty float64) (float64, float64, bool) {
		return estimateBuyCostEff(ctx, s.TradingService.GetOrderBook, assetID, qty, feeRate, slipPadEff)
	}

	plan := PlanNextAction(context.Background(), s.Config, pc, est)
	// 为 stop/日志补全当前快照
	if plan.Kind == PlanStop {
		plan.Sim = baseSnap
	}
	s.executePlan(context.Background(), plan)
}

// ===== compile-time guards =====
var _ bbgo.SingleExchangeStrategy = (*Strategy)(nil)
var _ bbgo.ExchangeSessionSubscriber = (*Strategy)(nil)
var _ bbgo.StrategyDefaulter = (*Strategy)(nil)
var _ bbgo.StrategyValidator = (*Strategy)(nil)
var _ bbgo.CycleAwareStrategy = (*Strategy)(nil)
var _ ports.OrderUpdateHandler = (*Strategy)(nil)
var _ ports.TradeUpdateHandler = (*Strategy)(nil)
