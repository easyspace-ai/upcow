package paircostarb

import (
	"context"
	"fmt"
	"math"
	"strconv"
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
	gcfg "github.com/betbot/gobet/pkg/config"
)

func init() {
	bbgo.RegisterStrategy("paircostarb", &Strategy{})
}

// Strategy：基于盘口深度 VWAP 的 pair-cost 套利（分批买入 UP/DOWN，锁定 complete-set 正收益）。
type Strategy struct {
	TradingService *services.TradingService `json:"-" yaml:"-"`

	Config `json:",inline" yaml:",inline"`

	orderExecutor bbgo.OrderExecutor
	log           *logrus.Entry

	st state

	started atomic.Bool
}

func (s *Strategy) ID() string   { return "paircostarb" }
func (s *Strategy) Name() string { return s.ID() }

type candidate struct {
	side        domain.TokenType
	assetID     string
	vwapEff     float64
	costEff     float64
	newPairCost float64
	newImb      float64
}

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
	s.st.rt.inFlightOrderID = ""
	s.st.rt.inFlightSide = ""
	s.st.rt.filledSeen = make(map[string]float64)
	s.st.rt.qu, s.st.rt.qd = 0, 0
	s.st.rt.cu, s.st.rt.cd = 0, 0
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
	cost := price * delta * (1.0 + feeRate)

	if order.TokenType == domain.TokenTypeUp {
		s.st.rt.qu += delta
		s.st.rt.cu += cost
	} else {
		s.st.rt.qd += delta
		s.st.rt.cd += cost
	}

	// 若该订单是当前 in-flight，且已进入终态，则释放 in-flight gate
	if s.st.rt.inFlightOrderID != "" && order.OrderID == s.st.rt.inFlightOrderID {
		if order.IsFinalStatus() || order.Status == domain.OrderStatusPartial {
			// 对于 FAK：partial 通常会很快终态；这里先放开，避免策略被卡死
			s.st.rt.inFlightOrderID = ""
			s.st.rt.inFlightSide = ""
		}
	}

	return nil
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

	// 去重/增量统计
	filledSeen map[string]float64 // orderID -> lastFilledSize accounted

	// 控制策略节奏
	pausedUntil     time.Time
	tradesThisCycle int
	lastDecisionAt  time.Time

	// 简单 in-flight gate（避免下单风暴）
	inFlightOrderID string
	inFlightSide    string // "UP" / "DOWN"

	// 每策略实例独立的 autoMerge controller
	autoMergeCtl common.AutoMergeController
}

func (s *Strategy) tickOnce() {
	if !s.Config.Enabled || s.TradingService == nil || s.orderExecutor == nil {
		return
	}

	now := time.Now()
	s.st.mu.Lock()
	market := s.st.rt.market
	pausedUntil := s.st.rt.pausedUntil
	inFlight := s.st.rt.inFlightOrderID != ""
	tradesThisCycle := s.st.rt.tradesThisCycle
	qu, qd := s.st.rt.qu, s.st.rt.qd
	cu, cd := s.st.rt.cu, s.st.rt.cd
	s.st.mu.Unlock()

	if market == nil || !market.IsValid() {
		return
	}
	if !pausedUntil.IsZero() && now.Before(pausedUntil) {
		return
	}
	if inFlight {
		return
	}
	if s.Config.MaxTradesPerCycle > 0 && tradesThisCycle >= s.Config.MaxTradesPerCycle {
		return
	}
	if s.isInCycleEndProtection(now, market) {
		return
	}

	// 先用 WS 一档快速筛选：bestAskYes + bestAskNo 若已经 > MaxPairCost，则无需打 REST 获取深度
	_, yesAsk, _, noAsk, _, err := s.TradingService.GetTopOfBook(context.Background(), market)
	if err != nil {
		return
	}
	if yesAsk.Pips <= 0 || noAsk.Pips <= 0 {
		return
	}
	sumBestAsk := yesAsk.ToDecimal() + noAsk.ToDecimal()
	if sumBestAsk > s.Config.MaxPairCost {
		// 深度 VWAP 只会更差
		return
	}

	// 止盈停止条件（用当前均价做保守估计）
	if ok, profit := s.shouldStop(qu, qd, cu, cd); ok {
		s.st.mu.Lock()
		s.st.rt.pausedUntil = time.Now().Add(time.Duration(s.Config.CooldownAfterStopSeconds) * time.Second)
		mkt := s.st.rt.market
		cfg := s.Config.AutoMerge
		ts := s.TradingService
		ctl := &s.st.rt.autoMergeCtl
		s.st.mu.Unlock()

		s.log.Infof("🛑 达到保证利润阈值，停止本周期继续买入：profit=%.4fUSD qu=%.2f qd=%.2f pairCost=%.4f",
			profit, qu, qd, s.currentPairCost(qu, qd, cu, cd))

		// 可选：自动合并 complete sets 释放资金
		if cfg.Enabled && ts != nil && mkt != nil {
			ctl.MaybeAutoMerge(
				context.Background(),
				ts,
				mkt,
				cfg,
				func(format string, args ...any) { s.log.Infof(format, args...) },
				nil,
			)
		}
		return
	}

	// 深度 VWAP：分别模拟买 UP / 买 DOWN，一个 tick 内只做一次重决策
	dq := s.Config.TradeChunkShares
	if dq <= 0 {
		return
	}

	feeRate := float64(s.Config.FeeRateBps) / 10000.0
	vwapUpEff, costUpEff, okUp := s.estimateBuyCostEff(context.Background(), market.YesAssetID, dq, feeRate, s.Config.SlippagePad)
	vwapDownEff, costDownEff, okDown := s.estimateBuyCostEff(context.Background(), market.NoAssetID, dq, feeRate, s.Config.SlippagePad)

	var best *candidate
	if okUp {
		if c, ok := s.simulateCandidate(domain.TokenTypeUp, market.YesAssetID, vwapUpEff, costUpEff, dq, qu, qd, cu, cd); ok {
			best = &c
		}
	}
	if okDown {
		if c, ok := s.simulateCandidate(domain.TokenTypeDown, market.NoAssetID, vwapDownEff, costDownEff, dq, qu, qd, cu, cd); ok {
			if best == nil || c.newPairCost < best.newPairCost {
				best = &c
			}
		}
	}
	if best == nil {
		return
	}

	// 下单
	limitPrice := best.vwapEff + float64(s.Config.LimitPricePadCents)/100.0
	if limitPrice <= 0 {
		return
	}
	if limitPrice > 0.9999 {
		limitPrice = 0.9999
	}
	orderType := types.OrderTypeGTC
	if s.Config.OrderType == "taker" {
		orderType = types.OrderTypeFAK
	}
	order := domain.Order{
		MarketSlug:        market.Slug,
		AssetID:           best.assetID,
		Side:              types.SideBuy,
		Price:             domain.PriceFromDecimal(limitPrice),
		Size:              dq,
		TokenType:         best.side,
		IsEntryOrder:      true,
		Status:            domain.OrderStatusPending,
		CreatedAt:         time.Now(),
		OrderType:         orderType,
		DisableSizeAdjust: true, // 严格按 dq 份额下单，避免系统自动放大导致不平衡
	}

	if s.Config.DecisionOnly {
		s.log.Infof("🧪 decisionOnly：将下单 side=%s dq=%.2f vwapEff=%.4f pairCost'=%.4f imb'=%.3f limit=%.4f type=%s",
			best.side, dq, best.vwapEff, best.newPairCost, best.newImb, limitPrice, orderType)
		s.st.mu.Lock()
		s.st.rt.tradesThisCycle++
		s.st.rt.pausedUntil = time.Now().Add(250 * time.Millisecond)
		s.st.mu.Unlock()
		return
	}

	submitCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	created, err := s.orderExecutor.SubmitOrders(submitCtx, order)
	if err != nil || len(created) == 0 || created[0] == nil || created[0].OrderID == "" {
		return
	}
	oid := created[0].OrderID

	s.st.mu.Lock()
	s.st.rt.inFlightOrderID = oid
	if best.side == domain.TokenTypeUp {
		s.st.rt.inFlightSide = "UP"
	} else {
		s.st.rt.inFlightSide = "DOWN"
	}
	s.st.rt.tradesThisCycle++
	// 给 WS 回执/引擎更新一点窗口，避免 tick 立即重复下单
	s.st.rt.pausedUntil = time.Now().Add(150 * time.Millisecond)
	s.st.mu.Unlock()

	s.log.Infof("✅ 下单已提交 side=%s orderID=%s dq=%.2f limit=%.4f pairCost'=%.4f imb'=%.3f",
		best.side, oid, dq, limitPrice, best.newPairCost, best.newImb)
}

func (s *Strategy) estimateBuyCostEff(ctx context.Context, assetID string, qty float64, feeRate float64, slippagePad float64) (vwapEff float64, costEff float64, ok bool) {
	if s.TradingService == nil || assetID == "" || qty <= 0 {
		return 0, 0, false
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	book, err := s.TradingService.GetOrderBook(cctx, assetID)
	if err != nil || book == nil {
		return 0, 0, false
	}
	vwap, ok := estimateBuyVWAP(book.Asks, qty)
	if !ok || vwap <= 0 {
		return 0, 0, false
	}
	vwapEff = vwap + slippagePad
	if vwapEff <= 0 {
		return 0, 0, false
	}
	// 成本：vwapEff * qty * (1+feeRate)
	costEff = vwapEff * qty * (1.0 + feeRate)
	return vwapEff, costEff, true
}

func estimateBuyVWAP(asks []types.OrderSummary, qty float64) (vwap float64, ok bool) {
	if qty <= 0 {
		return 0, false
	}
	filled := 0.0
	cost := 0.0
	for _, lv := range asks {
		if filled >= qty {
			break
		}
		p, err := strconv.ParseFloat(lv.Price, 64)
		if err != nil || p <= 0 {
			continue
		}
		sz, err := strconv.ParseFloat(lv.Size, 64)
		if err != nil || sz <= 0 {
			continue
		}
		take := math.Min(sz, qty-filled)
		if take <= 0 {
			continue
		}
		cost += p * take
		filled += take
	}
	if filled+1e-9 < qty {
		return 0, false
	}
	return cost / qty, true
}

func (s *Strategy) simulateCandidate(
	side domain.TokenType,
	assetID string,
	vwapEff float64,
	costEff float64,
	dq float64,
	qu, qd, cu, cd float64,
) (candidate, bool) {
	if dq <= 0 || vwapEff <= 0 || costEff <= 0 {
		return candidate{}, false
	}
	newQu, newQd := qu, qd
	newCu, newCd := cu, cd
	if side == domain.TokenTypeUp {
		newQu += dq
		newCu += costEff
	} else {
		newQd += dq
		newCd += costEff
	}

	// 单边先手：只有当另一边为 0，且当前 vwapEff 足够便宜，且不超过 MaxUnpairedShares 才允许
	if (side == domain.TokenTypeUp && qd <= 0) || (side == domain.TokenTypeDown && qu <= 0) {
		if vwapEff > s.Config.FirstLegMaxPrice {
			return candidate{}, false
		}
		unpaired := newQu
		if side == domain.TokenTypeDown {
			unpaired = newQd
		}
		if unpaired > s.Config.MaxUnpairedShares {
			return candidate{}, false
		}
		// 先手阶段无法计算 pair_cost，给一个“伪指标”用于比较（越便宜越优先）
		return candidate{
			side:        side,
			assetID:     assetID,
			vwapEff:     vwapEff,
			costEff:     costEff,
			newPairCost: vwapEff, // 仅用于比较
			newImb:      math.Inf(1),
		}, true
	}

	// 两边都有仓位：计算 pair_cost 与 imbalance
	if newQu <= 0 || newQd <= 0 {
		return candidate{}, false
	}
	avgUp := newCu / newQu
	avgDown := newCd / newQd
	pairCost := avgUp + avgDown + s.Config.PairCostBuffer

	imb := math.Max(newQu, newQd) / math.Min(newQu, newQd)
	if imb < 1.0 {
		imb = 1.0
	}
	if pairCost > s.Config.MaxPairCost {
		return candidate{}, false
	}
	if imb > s.Config.MaxImbalance {
		return candidate{}, false
	}
	return candidate{
		side:        side,
		assetID:     assetID,
		vwapEff:     vwapEff,
		costEff:     costEff,
		newPairCost: pairCost,
		newImb:      imb,
	}, true
}

func (s *Strategy) currentPairCost(qu, qd, cu, cd float64) float64 {
	if qu <= 0 || qd <= 0 {
		return math.Inf(1)
	}
	return (cu/qu + cd/qd) + s.Config.PairCostBuffer
}

func (s *Strategy) shouldStop(qu, qd, cu, cd float64) (ok bool, profitUSD float64) {
	if qu <= 0 || qd <= 0 {
		return false, 0
	}
	qPair := math.Min(qu, qd)
	if qPair <= 0 {
		return false, 0
	}
	avgUp := cu / qu
	avgDown := cd / qd
	costPerPair := avgUp + avgDown
	// 保守：不把 PairCostBuffer 当作真实成本，但把它当作“利润安全垫子”扣掉
	profitPerPair := 1.0 - costPerPair - s.Config.PairCostBuffer
	profitUSD = qPair * profitPerPair
	return profitUSD >= s.Config.MinProfitUSD, profitUSD
}

func (s *Strategy) isInCycleEndProtection(now time.Time, market *domain.Market) bool {
	if market == nil || market.Timestamp <= 0 {
		return false
	}
	// 优先用 StopTimeBufferSeconds
	if s.Config.StopTimeBufferSeconds > 0 {
		cycleDur := 15 * time.Minute
		if gc := gcfg.Get(); gc != nil {
			if sp, err := gc.Market.Spec(); err == nil {
				if d := sp.Duration(); d > 0 {
					cycleDur = d
				}
			}
		}
		start := time.Unix(market.Timestamp, 0)
		end := start.Add(cycleDur)
		return end.Sub(now) <= time.Duration(s.Config.StopTimeBufferSeconds)*time.Second
	}
	if s.Config.CycleEndProtectionMinutes <= 0 {
		return false
	}
	cycleDur := 15 * time.Minute
	if gc := gcfg.Get(); gc != nil {
		if sp, err := gc.Market.Spec(); err == nil {
			if d := sp.Duration(); d > 0 {
				cycleDur = d
			}
		}
	}
	start := time.Unix(market.Timestamp, 0)
	end := start.Add(cycleDur)
	protect := time.Duration(s.Config.CycleEndProtectionMinutes) * time.Minute
	return end.Sub(now) <= protect
}

// ===== compile-time guards =====
var _ bbgo.SingleExchangeStrategy = (*Strategy)(nil)
var _ bbgo.ExchangeSessionSubscriber = (*Strategy)(nil)
var _ bbgo.StrategyDefaulter = (*Strategy)(nil)
var _ bbgo.StrategyValidator = (*Strategy)(nil)
var _ bbgo.CycleAwareStrategy = (*Strategy)(nil)
var _ ports.OrderUpdateHandler = (*Strategy)(nil)
