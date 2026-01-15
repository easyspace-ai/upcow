package paircostarb

import (
	"context"
	"fmt"
	"math"
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

type Fill struct {
	OrderID string
	Time    time.Time
	Qty     float64
	Price   float64
	FeeUSD  float64
	CostUSD float64 // 含手续费的成本（USD）
}

type fifoCursor struct {
	idx  int     // 当前 fill index
	used float64 // 当前 fill 已被“配对消耗”的 qty
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
	feeUSD := price * delta * feeRate
	cost := price*delta + feeUSD

	f := Fill{
		OrderID: order.OrderID,
		Time:    time.Now(),
		Qty:     delta,
		Price:   price,
		FeeUSD:  feeUSD,
		CostUSD: cost,
	}

	if order.TokenType == domain.TokenTypeUp {
		s.st.rt.qu += delta
		s.st.rt.cu += cost
		s.st.rt.fillsUp = append(s.st.rt.fillsUp, f)
	} else {
		s.st.rt.qd += delta
		s.st.rt.cd += cost
		s.st.rt.fillsDown = append(s.st.rt.fillsDown, f)
	}

	// FIFO 配对：一旦两边都有“未配对库存”，立刻按时间顺序配对，得到严格的 qPair/costPair
	s.pairAvailableLocked()

	// in-flight gate：订单进入终态/partial 时释放（对 FAK/并发下单更安全，避免卡死）
	if s.st.rt.inFlightIDs != nil {
		if _, ok := s.st.rt.inFlightIDs[order.OrderID]; ok {
			if order.IsFinalStatus() || order.Status == domain.OrderStatusPartial {
				delete(s.st.rt.inFlightIDs, order.OrderID)
			}
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

	// FIFO 成交队列（用于精确计算“已配对部分”的成本）
	fillsUp   []Fill
	fillsDown []Fill
	upCur     fifoCursor
	downCur   fifoCursor

	// 已配对份额与成本（严格 FIFO，costPair 含手续费）
	qPair    float64
	costPair float64

	// 去重/增量统计
	filledSeen map[string]float64 // orderID -> lastFilledSize accounted

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

	inEndProtection := s.isInCycleEndProtection(now, market)

	// ===== 信号模块（Binance 秒级 K 线）=====
	sigDir := domain.TokenType("")
	sigActive := false
	if s.Config.EnableBinanceSignal && sig != nil {
		sigDir, sigActive = sig.Evaluate(now)
	}

	// ===== 基础风控门禁（节奏/周期/信号）=====
	if rr := allowTickBasic(s.Config, now, baseSnap, inFlight, tradesThisCycle, inEndProtection, s.Config.RequireBinanceSignal, sigActive); !rr.OK {
		return
	}

	// ===== Top-of-book 快速筛选（避免频繁拉深度）=====
	_, yesAsk, _, noAsk, _, err := s.TradingService.GetTopOfBook(context.Background(), market)
	if err != nil {
		return
	}
	if yesAsk.Pips <= 0 || noAsk.Pips <= 0 {
		return
	}
	if yesAsk.ToDecimal()+noAsk.ToDecimal() > s.Config.MaxPairCost {
		return
	}

	// ===== 止盈停止（严格 FIFO 指标）=====
	if gp := baseSnap.GuaranteedProfitUSD(s.Config); gp >= s.Config.MinProfitUSD {
		s.st.mu.Lock()
		s.st.rt.pausedUntil = time.Now().Add(time.Duration(s.Config.CooldownAfterStopSeconds) * time.Second)
		mkt := s.st.rt.market
		cfg := s.Config.AutoMerge
		ts := s.TradingService
		ctl := &s.st.rt.autoMergeCtl
		s.st.mu.Unlock()

		s.log.Infof("🛑 达到保证利润阈值，停止本周期继续买入：profit=%.4fUSD qPair=%.2f pairCost=%.4f qu=%.2f qd=%.2f",
			gp, baseSnap.QPairValue(), baseSnap.PairCost(s.Config), baseSnap.Qu, baseSnap.Qd)

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

	dq := s.Config.TradeChunkShares
	if dq <= 0 {
		return
	}
	feeRate := float64(s.Config.FeeRateBps) / 10000.0

	// 深度 VWAP 估算：UP/DOWN
	vwapUpEff, costUpEff, okUp := estimateBuyCostEff(context.Background(), s.TradingService.GetOrderBook, market.YesAssetID, dq, feeRate, s.Config.SlippagePad)
	vwapDownEff, costDownEff, okDown := estimateBuyCostEff(context.Background(), s.TradingService.GetOrderBook, market.NoAssetID, dq, feeRate, s.Config.SlippagePad)

	orderType := orderTypeFromConfig(s.Config)

	// ===== 执行模块：paired（同时下 UP+DOWN）=====
	if s.Config.ExecutionMode == "paired" {
		if !okUp || !okDown {
			return
		}

		primary := preferredPrimary(sigDir, sigActive, vwapUpEff, vwapDownEff)
		sim := baseSnap.Clone()

		upFill := Fill{Qty: dq, Price: vwapUpEff, CostUSD: costUpEff, Time: now}
		downFill := Fill{Qty: dq, Price: vwapDownEff, CostUSD: costDownEff, Time: now}

		if primary == domain.TokenTypeUp {
			sim.AddFill(domain.TokenTypeUp, upFill)
			sim.AddFill(domain.TokenTypeDown, downFill)
		} else {
			sim.AddFill(domain.TokenTypeDown, downFill)
			sim.AddFill(domain.TokenTypeUp, upFill)
		}

		if rr := shouldTradeSnapshot(s.Config, sim); !rr.OK {
			return
		}

		upPad := applyPad(s.Config.LimitPricePadCents, s.Config.HedgePadCents)
		downPad := applyPad(s.Config.LimitPricePadCents, s.Config.HedgePadCents)
		if primary == domain.TokenTypeUp {
			upPad = applyPad(s.Config.LimitPricePadCents, s.Config.PrimaryPadCents)
		} else {
			downPad = applyPad(s.Config.LimitPricePadCents, s.Config.PrimaryPadCents)
		}

		upLimit := clampPrice(vwapUpEff + upPad)
		downLimit := clampPrice(vwapDownEff + downPad)
		if upLimit <= 0 || downLimit <= 0 {
			return
		}

		upOrder := makeBuyOrder(market, market.YesAssetID, domain.TokenTypeUp, dq, upLimit, orderType, false)
		downOrder := makeBuyOrder(market, market.NoAssetID, domain.TokenTypeDown, dq, downLimit, orderType, false)

		if s.Config.DecisionOnly {
			s.log.Infof("🧪 decisionOnly：paired 下单 primary=%s dq=%.2f upVwap=%.4f upLimit=%.4f downVwap=%.4f downLimit=%.4f pairCost'=%.4f imb'=%.3f qPair'=%.2f gp'=%.4f",
				primary, dq, vwapUpEff, upLimit, vwapDownEff, downLimit, sim.PairCost(s.Config), sim.Imbalance(), sim.QPairValue(), sim.GuaranteedProfitUSD(s.Config))
			s.st.mu.Lock()
			s.st.rt.tradesThisCycle += 2
			s.st.rt.pausedUntil = time.Now().Add(250 * time.Millisecond)
			s.st.mu.Unlock()
			return
		}

		submitCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		created, err := s.orderExecutor.SubmitOrders(submitCtx, upOrder, downOrder)
		if err != nil || len(created) == 0 {
			return
		}

		s.st.mu.Lock()
		if s.st.rt.inFlightIDs == nil {
			s.st.rt.inFlightIDs = make(map[string]domain.TokenType, 4)
		}
		for _, o := range created {
			if o == nil || o.OrderID == "" {
				continue
			}
			s.st.rt.inFlightIDs[o.OrderID] = o.TokenType
		}
		s.st.rt.tradesThisCycle += len(created)
		s.st.rt.pausedUntil = time.Now().Add(150 * time.Millisecond)
		s.st.mu.Unlock()

		s.log.Infof("✅ paired 已提交：primary=%s dq=%.2f upLimit=%.4f downLimit=%.4f pairCost'=%.4f imb'=%.3f",
			primary, dq, upLimit, downLimit, sim.PairCost(s.Config), sim.Imbalance())
		return
	}

	// ===== 执行模块：single（每次只下单一边，允许先手）=====
	type singlePlan struct {
		side      domain.TokenType
		assetID   string
		vwapEff   float64
		costEff   float64
		limit     float64
		simSnap   Snapshot
		pairCost  float64
		imbalance float64
		gp        float64
	}

	var best *singlePlan
	try := func(side domain.TokenType, assetID string, vwapEff float64, costEff float64) {
		if vwapEff <= 0 || costEff <= 0 {
			return
		}
		sim := baseSnap.Clone()
		sim.AddFill(side, Fill{Qty: dq, Price: vwapEff, CostUSD: costEff, Time: now})

		// 先手：另一边为 0 时只用“非常便宜 + 未配对上限”门禁
		if (side == domain.TokenTypeUp && baseSnap.Qd <= 0) || (side == domain.TokenTypeDown && baseSnap.Qu <= 0) {
			if vwapEff > s.Config.FirstLegMaxPrice {
				return
			}
			if math.Abs(sim.Qu-sim.Qd) > s.Config.MaxUnpairedShares {
				return
			}
		} else {
			if rr := shouldTradeSnapshot(s.Config, sim); !rr.OK {
				return
			}
		}

		pc := sim.PairCost(s.Config)
		imb := sim.Imbalance()
		gp := sim.GuaranteedProfitUSD(s.Config)
		limit := clampPrice(vwapEff + applyPad(s.Config.LimitPricePadCents, 0))

		plan := &singlePlan{
			side:      side,
			assetID:   assetID,
			vwapEff:   vwapEff,
			costEff:   costEff,
			limit:     limit,
			simSnap:   sim,
			pairCost:  pc,
			imbalance: imb,
			gp:        gp,
		}
		if best == nil || plan.pairCost < best.pairCost || (!isFinite(best.pairCost) && isFinite(plan.pairCost)) {
			best = plan
		}
	}

	if okUp {
		try(domain.TokenTypeUp, market.YesAssetID, vwapUpEff, costUpEff)
	}
	if okDown {
		try(domain.TokenTypeDown, market.NoAssetID, vwapDownEff, costDownEff)
	}
	if best == nil || best.limit <= 0 {
		return
	}

	order := makeBuyOrder(market, best.assetID, best.side, dq, best.limit, orderType, false)
	if s.Config.DecisionOnly {
		s.log.Infof("🧪 decisionOnly：single 下单 side=%s dq=%.2f vwapEff=%.4f limit=%.4f pairCost'=%.4f imb'=%.3f qPair'=%.2f gp'=%.4f",
			best.side, dq, best.vwapEff, best.limit, best.simSnap.PairCost(s.Config), best.simSnap.Imbalance(), best.simSnap.QPairValue(), best.simSnap.GuaranteedProfitUSD(s.Config))
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
	if s.st.rt.inFlightIDs == nil {
		s.st.rt.inFlightIDs = make(map[string]domain.TokenType, 4)
	}
	s.st.rt.inFlightIDs[oid] = best.side
	s.st.rt.tradesThisCycle++
	s.st.rt.pausedUntil = time.Now().Add(150 * time.Millisecond)
	s.st.mu.Unlock()

	s.log.Infof("✅ single 已提交 side=%s orderID=%s dq=%.2f limit=%.4f pairCost'=%.4f imb'=%.3f",
		best.side, oid, dq, best.limit, best.simSnap.PairCost(s.Config), best.simSnap.Imbalance())
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

func (s *Strategy) pairAvailableLocked() {
	// 只在持锁状态下调用（来自 OnOrderUpdate/OnCycle）
	for {
		upAvail, upCostPer, okUp := fifoAvailCostPer(s.st.rt.fillsUp, s.st.rt.upCur)
		downAvail, downCostPer, okDown := fifoAvailCostPer(s.st.rt.fillsDown, s.st.rt.downCur)
		if !okUp || !okDown {
			break
		}
		take := math.Min(upAvail, downAvail)
		if take <= 1e-9 {
			break
		}
		// 增加已配对份额与成本
		s.st.rt.qPair += take
		s.st.rt.costPair += take*upCostPer + take*downCostPer

		// 推进游标
		s.st.rt.upCur = fifoConsumeQty(s.st.rt.fillsUp, s.st.rt.upCur, take)
		s.st.rt.downCur = fifoConsumeQty(s.st.rt.fillsDown, s.st.rt.downCur, take)

		// 轻量压缩，防止 fills 无限增长
		s.st.rt.fillsUp, s.st.rt.upCur = fifoCompact(s.st.rt.fillsUp, s.st.rt.upCur)
		s.st.rt.fillsDown, s.st.rt.downCur = fifoCompact(s.st.rt.fillsDown, s.st.rt.downCur)
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
	// 当头部已消耗很多时做一次切片压缩
	if cur.idx <= 0 {
		return fills, cur
	}
	if cur.idx < 256 && cur.idx < len(fills)/2 {
		return fills, cur
	}
	// copy tail
	newFills := append([]Fill(nil), fills[cur.idx:]...)
	return newFills, fifoCursor{idx: 0, used: cur.used}
}

func simulateConsumeCost(fills []Fill, cur fifoCursor, qty float64) (cost float64, next fifoCursor) {
	next = cur
	if qty <= 0 {
		return 0, next
	}
	for qty > 1e-9 && next.idx < len(fills) {
		f := fills[next.idx]
		if f.Qty <= 0 || f.CostUSD <= 0 {
			// 跳过异常 fill
			next.idx++
			next.used = 0
			continue
		}
		rem := f.Qty - next.used
		if rem <= 1e-9 {
			next.idx++
			next.used = 0
			continue
		}
		take := math.Min(rem, qty)
		costPer := f.CostUSD / f.Qty
		cost += take * costPer
		next.used += take
		qty -= take
		if f.Qty-next.used <= 1e-9 {
			next.idx++
			next.used = 0
		}
	}
	return cost, next
}

func isFinite(x float64) bool {
	return !math.IsInf(x, 0) && !math.IsNaN(x)
}
