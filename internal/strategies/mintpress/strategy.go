package mintpress

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/bbgo"
)

const ID = "mintpress"

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy：做市式 complete-set “印钞机”
//
// 设计选择（务实版）：
// - 只做双边 BUY(GTC) 挂单（maker），不做主动吃单的 complete-set（taker）
// - 用 OnOrderUpdate 驱动部分成交记账；出现净裸露超过阈值时，立即 SELL(FAK) 回平
// - 控制两腿挂单价格之和 <= 100 - ProfitTargetCents，尽量使“成交后接近锁定收益”
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	loopOnce   sync.Once
	loopCancel context.CancelFunc

	signalC chan struct{}

	priceMu  sync.Mutex
	latestPx map[domain.TokenType]*events.PriceChangedEvent

	orderC chan *domain.Order

	// loop 内状态（只允许 loop goroutine 读写）
	guard         common.MarketSlugGuard
	firstSeenAt   time.Time
	lastActionAt  time.Time

	// 当前周期累计：我们认为“已锁定”的 complete-set 份额（min(Qyes,Qno)）
	lockedSets float64

	// 净持仓（shares）与成本（USDC）
	qYes, qNo float64
	cYes, cNo float64

	// 我们当前挂着的订单（每侧最多一个）
	openYes *trackedOrder
	openNo  *trackedOrder
}

type trackedOrder struct {
	OrderID   string
	AssetID   string
	TokenType domain.TokenType
	Side      types.Side
	PriceCents int
	Size      float64

	SeenFilled float64
	Status     domain.OrderStatus
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }

func (s *Strategy) Defaults() error { return nil }
func (s *Strategy) Validate() error { return s.Config.Validate() }

func (s *Strategy) Initialize() error {
	if s.signalC == nil {
		s.signalC = make(chan struct{}, 1)
	}
	if s.latestPx == nil {
		s.latestPx = make(map[domain.TokenType]*events.PriceChangedEvent)
	}
	if s.orderC == nil {
		s.orderC = make(chan *domain.Order, 4096)
	}
	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	session.OnOrderUpdate(s)
	log.Infof("✅ [mintpress] 已订阅价格+订单更新 (session=%s)", session.Name)
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	common.StartLoopOnce(ctx, &s.loopOnce, func(cancel context.CancelFunc) { s.loopCancel = cancel }, 0, s.loop)
	<-ctx.Done()
	return ctx.Err()
}

func (s *Strategy) OnPriceChanged(_ context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil {
		return nil
	}
	s.priceMu.Lock()
	s.latestPx[e.TokenType] = e
	s.priceMu.Unlock()
	common.TrySignal(s.signalC)
	return nil
}

func (s *Strategy) OnOrderUpdate(_ context.Context, order *domain.Order) error {
	if order == nil {
		return nil
	}
	select {
	case s.orderC <- order:
	default:
		// 丢弃而不阻塞 session
	}
	common.TrySignal(s.signalC)
	return nil
}

func (s *Strategy) loop(loopCtx context.Context, _ <-chan time.Time) {
	for {
		select {
		case <-loopCtx.Done():
			return
		case <-s.signalC:
			s.step(loopCtx)
		}
	}
}

func (s *Strategy) step(loopCtx context.Context) {
	if s.TradingService == nil {
		return
	}

	// 1) 合并价格事件（拿到一个 market 上下文）
	s.priceMu.Lock()
	evUp := s.latestPx[domain.TokenTypeUp]
	evDown := s.latestPx[domain.TokenTypeDown]
	s.latestPx = make(map[domain.TokenType]*events.PriceChangedEvent)
	s.priceMu.Unlock()

	var m *domain.Market
	now := time.Now()
	if evUp != nil && evUp.Market != nil {
		m = evUp.Market
		if !evUp.Timestamp.IsZero() {
			now = evUp.Timestamp
		}
	}
	if m == nil && evDown != nil && evDown.Market != nil {
		m = evDown.Market
		if !evDown.Timestamp.IsZero() {
			now = evDown.Timestamp
		}
	}
	if m == nil || !m.IsValid() {
		// 没有 market 上下文时，只能先处理订单更新队列
		s.drainOrderUpdates()
		return
	}

	// 2) 周期切换：重置状态
	if s.guard.Update(m.Slug) {
		s.firstSeenAt = now
		s.lastActionAt = time.Time{}
		s.lockedSets = 0
		s.qYes, s.qNo, s.cYes, s.cNo = 0, 0, 0, 0
		s.openYes, s.openNo = nil, nil
		log.Infof("🔄 [mintpress] 周期切换，重置状态: market=%s", m.Slug)
	}
	if s.firstSeenAt.IsZero() {
		s.firstSeenAt = now
	}

	// 3) 处理订单更新（更新成交/清理 open 订单引用）
	s.drainOrderUpdates()
	// 3.1) 仓位对账：以 OrderEngine 的 positions 为准，避免“本地以为有仓但实际无仓”导致裸卖单
	s.reconcileHoldingsFromPositions(m.Slug)

	// 4) 冷却（避免高频抖动撤单重挂）
	if !s.lastActionAt.IsZero() && now.Sub(s.lastActionAt) < time.Duration(s.CooldownMs)*time.Millisecond {
		return
	}

	// 5) 周期末停止新增挂单
	if s.StopBeforeEndSeconds > 0 && m.Timestamp > 0 {
		remain := time.Until(time.Unix(m.Timestamp, 0))
		if remain > 0 && remain <= time.Duration(s.StopBeforeEndSeconds)*time.Second {
			// 临近结束：撤掉挂单，避免尾段乱成交（可选策略：保留已挂单让其自然成交）
			s.cancelBoth(loopCtx)
			s.lastActionAt = now
			return
		}
	}

	// 6) 风控：净裸露过大则回平（优先撤单）
	net := math.Abs(s.qYes - s.qNo)
	if net >= s.MaxNetExposureShares {
		s.cancelBoth(loopCtx)
		s.tryFlatten(loopCtx, m)
		s.lastActionAt = now
		return
	}

	// 7) 达到锁定份额上限则停止新增挂单（但不强制撤单）
	if s.lockedSets >= s.MaxSetsPerPeriod {
		return
	}

	// 8) 计算双边挂单价格（maker bids），并提交/重挂
	s.quoteAndPlace(loopCtx, m)
	s.lastActionAt = now
}

func (s *Strategy) drainOrderUpdates() {
	for {
		select {
		case o := <-s.orderC:
			s.onOrderUpdateInternal(o)
		default:
			return
		}
	}
}

func (s *Strategy) onOrderUpdateInternal(o *domain.Order) {
	if o == nil || o.OrderID == "" {
		return
	}

	// 只追踪我们自己当前挂单的两个 orderID（每侧一个）
	var t *trackedOrder
	if s.openYes != nil && s.openYes.OrderID == o.OrderID {
		t = s.openYes
	}
	if s.openNo != nil && s.openNo.OrderID == o.OrderID {
		t = s.openNo
	}
	if t == nil {
		return
	}

	// 记账：只处理 BUY 成交增量
	filled := o.FilledSize
	if filled < 0 {
		filled = 0
	}
	delta := filled - t.SeenFilled
	if delta < 0 {
		delta = 0
	}
	if delta > 0 && t.Side == types.SideBuy {
		cost := float64(t.PriceCents) / 100.0 * delta
		if t.TokenType == domain.TokenTypeUp {
			s.qYes += delta
			s.cYes += cost
		} else {
			s.qNo += delta
			s.cNo += cost
		}
		prevLocked := s.lockedSets
		s.lockedSets = math.Min(s.qYes, s.qNo)
		if s.lockedSets > prevLocked {
			log.Infof("💰 [mintpress] 新增锁定份额: +%.4f (locked=%.4f) net=%.4f cost=%.2f market=%s",
				s.lockedSets-prevLocked, s.lockedSets, math.Abs(s.qYes-s.qNo), s.cYes+s.cNo, s.guard.Current())
		}
	}
	t.SeenFilled = filled

	// 状态更新与清理
	t.Status = o.Status
	if o.Status == domain.OrderStatusFilled || o.Status == domain.OrderStatusCanceled || o.Status == domain.OrderStatusFailed {
		if s.openYes != nil && s.openYes.OrderID == o.OrderID {
			s.openYes = nil
		}
		if s.openNo != nil && s.openNo.OrderID == o.OrderID {
			s.openNo = nil
		}
	}
}

// reconcileHoldingsFromPositions 以 OrderEngine 的 positions 快照为准同步持仓数量。
//
// 目的：
// - 避免 WS/orderUpdate 丢包/延迟时，本地 qYes/qNo 漂移
// - 更重要：避免回平时根据“幻觉持仓”去下 SELL（交易所会拒绝无仓卖单）
func (s *Strategy) reconcileHoldingsFromPositions(marketSlug string) {
	if s.TradingService == nil || marketSlug == "" {
		return
	}
	positions := s.TradingService.GetOpenPositionsForMarket(marketSlug)
	var yesHeld, noHeld float64
	for _, p := range positions {
		if p == nil {
			continue
		}
		switch p.TokenType {
		case domain.TokenTypeUp:
			if p.Size > 0 {
				yesHeld += p.Size
			}
		case domain.TokenTypeDown:
			if p.Size > 0 {
				noHeld += p.Size
			}
		}
	}
	// 允许轻微误差（浮点/分批成交）
	const eps = 0.0001
	if math.Abs(s.qYes-yesHeld) > eps || math.Abs(s.qNo-noHeld) > eps {
		log.Warnf("🧾 [mintpress] 仓位对账修正: qYes %.4f→%.4f qNo %.4f→%.4f (market=%s)",
			s.qYes, yesHeld, s.qNo, noHeld, marketSlug)
		s.qYes, s.qNo = yesHeld, noHeld
		// lockedSets 也随之修正
		s.lockedSets = math.Min(s.qYes, s.qNo)
	}
}

func (s *Strategy) cancelBoth(ctx context.Context) {
	if s.openYes != nil && s.openYes.OrderID != "" {
		_ = s.TradingService.CancelOrder(ctx, s.openYes.OrderID)
	}
	if s.openNo != nil && s.openNo.OrderID != "" {
		_ = s.TradingService.CancelOrder(ctx, s.openNo.OrderID)
	}
	// 注意：不立即清空 openYes/openNo，等待 OnOrderUpdate 回流更一致；
	// 这里允许下一轮 step 再次尝试取消（CancelOrder 对已取消/已成交会返回成功或可忽略的错误）。
}

func (s *Strategy) tryFlatten(ctx context.Context, m *domain.Market) {
	// 再次基于真实仓位对账，确保不会裸卖
	s.reconcileHoldingsFromPositions(m.Slug)

	excess := s.qYes - s.qNo
	var assetID string
	var tokenType domain.TokenType
	if excess > 0 {
		assetID = m.YesAssetID
		tokenType = domain.TokenTypeUp
	} else if excess < 0 {
		excess = -excess
		assetID = m.NoAssetID
		tokenType = domain.TokenTypeDown
	} else {
		return
	}

	// 交易所不允许无仓卖：回平卖出数量必须 <= 实际持仓
	var held float64
	if tokenType == domain.TokenTypeUp {
		held = s.qYes
	} else {
		held = s.qNo
	}
	if held <= 0 {
		log.Warnf("🚫 [mintpress] 回平被跳过：检测到无可卖持仓 token=%s excess=%.4f market=%s",
			tokenType, excess, m.Slug)
		return
	}
	if excess > held {
		excess = held
	}

	// 取 bestBid（允许大价差）并减去 offset，快速回平
	bestBid, _, err := s.TradingService.GetBestPriceWithMaxSpread(ctx, assetID, s.MaxQuoteSpreadCents)
	if err != nil || bestBid <= 0 {
		return
	}
	priceCents := int(bestBid*100 + 0.5)
	priceCents -= s.HedgeSellOffsetCents
	if priceCents < 1 {
		priceCents = 1
	}

	order := &domain.Order{
		MarketSlug:   m.Slug,
		AssetID:      assetID,
		Side:         types.SideSell,
		Price:        domain.Price{Cents: priceCents},
		Size:         excess,
		TokenType:    tokenType,
		IsEntryOrder: false,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
		OrderType:    types.OrderTypeFAK,
	}
	_, _ = s.TradingService.PlaceOrder(ctx, order)
	log.Warnf("🧯 [mintpress] 触发回平: sell %s size=%.4f @ %dc netBefore=%.4f market=%s",
		tokenType, excess, priceCents, math.Abs(s.qYes-s.qNo), m.Slug)
}

func (s *Strategy) quoteAndPlace(ctx context.Context, m *domain.Market) {
	// 获取双边 top-of-book（允许大价差；但仍需 bid/ask 双边存在）
	yesBid, yesAsk, err := s.TradingService.GetBestPriceWithMaxSpread(ctx, m.YesAssetID, s.MaxQuoteSpreadCents)
	if err != nil || yesBid <= 0 || yesAsk <= 0 {
		return
	}
	noBid, noAsk, err := s.TradingService.GetBestPriceWithMaxSpread(ctx, m.NoAssetID, s.MaxQuoteSpreadCents)
	if err != nil || noBid <= 0 || noAsk <= 0 {
		return
	}

	yesBidC := int(yesBid*100 + 0.5)
	yesAskC := int(yesAsk*100 + 0.5)
	noBidC := int(noBid*100 + 0.5)
	noAskC := int(noAsk*100 + 0.5)

	// 目标：挂在 bestBid + improve，但必须 < bestAsk 才能保持 maker
	targetYes := yesBidC + s.ImproveCents
	targetNo := noBidC + s.ImproveCents
	if targetYes >= yesAskC {
		targetYes = yesAskC - 1
	}
	if targetNo >= noAskC {
		targetNo = noAskC - 1
	}
	if targetYes < 1 || targetNo < 1 {
		return
	}

	// complete-set 约束：两腿价格之和 <= 100 - profitTarget
	maxTotal := 100 - s.ProfitTargetCents
	total := targetYes + targetNo
	if total > maxTotal {
		// 简单降价：优先把更贵的一腿下调到满足约束
		excess := total - maxTotal
		if targetYes >= targetNo {
			targetYes -= excess
		} else {
			targetNo -= excess
		}
	}
	if targetYes < 1 || targetNo < 1 {
		return
	}
	// 再次保证 maker（避免降价后变成 >= ask 的情况理论上不会发生，但防御一下）
	if targetYes >= yesAskC {
		targetYes = yesAskC - 1
	}
	if targetNo >= noAskC {
		targetNo = noAskC - 1
	}
	if targetYes < 1 || targetNo < 1 {
		return
	}
	if targetYes+targetNo > maxTotal {
		// 仍然不满足就放弃这一轮
		return
	}

	// size：保证两腿单笔金额 >= MinOrderSize
	size := s.OrderSize
	if float64(targetYes) > 0 {
		minShares := s.MinOrderSize / (float64(targetYes) / 100.0)
		size = math.Max(size, minShares)
	}
	if float64(targetNo) > 0 {
		minShares := s.MinOrderSize / (float64(targetNo) / 100.0)
		size = math.Max(size, minShares)
	}
	if size <= 0 || math.IsInf(size, 0) || math.IsNaN(size) {
		return
	}

	// 重挂逻辑：若已有 open 订单且价差变化不大，则不动
	if !s.shouldRequote(s.openYes, targetYes, size) && !s.shouldRequote(s.openNo, targetNo, size) {
		return
	}

	// 先撤旧（避免同侧挂多个），再挂新
	if s.openYes != nil && s.openYes.OrderID != "" {
		_ = s.TradingService.CancelOrder(ctx, s.openYes.OrderID)
	}
	if s.openNo != nil && s.openNo.OrderID != "" {
		_ = s.TradingService.CancelOrder(ctx, s.openNo.OrderID)
	}

	yesOrder := &domain.Order{
		MarketSlug:   m.Slug,
		AssetID:      m.YesAssetID,
		Side:         types.SideBuy,
		Price:        domain.Price{Cents: targetYes},
		Size:         size,
		TokenType:    domain.TokenTypeUp,
		IsEntryOrder: true,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
		OrderType:    types.OrderTypeGTC,
	}
	noOrder := &domain.Order{
		MarketSlug:   m.Slug,
		AssetID:      m.NoAssetID,
		Side:         types.SideBuy,
		Price:        domain.Price{Cents: targetNo},
		Size:         size,
		TokenType:    domain.TokenTypeDown,
		IsEntryOrder: true,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
		OrderType:    types.OrderTypeGTC,
	}

	// 并发下单：减少跨腿时差
	var wg sync.WaitGroup
	wg.Add(2)
	var yesCreated, noCreated *domain.Order
	go func() {
		defer wg.Done()
		yesCreated, _ = s.TradingService.PlaceOrder(ctx, yesOrder)
	}()
	go func() {
		defer wg.Done()
		noCreated, _ = s.TradingService.PlaceOrder(ctx, noOrder)
	}()
	wg.Wait()

	if yesCreated != nil && yesCreated.OrderID != "" {
		s.openYes = &trackedOrder{
			OrderID:   yesCreated.OrderID,
			AssetID:   m.YesAssetID,
			TokenType: domain.TokenTypeUp,
			Side:      types.SideBuy,
			PriceCents: targetYes,
			Size:      size,
			SeenFilled: 0,
			Status:     yesCreated.Status,
		}
	}
	if noCreated != nil && noCreated.OrderID != "" {
		s.openNo = &trackedOrder{
			OrderID:   noCreated.OrderID,
			AssetID:   m.NoAssetID,
			TokenType: domain.TokenTypeDown,
			Side:      types.SideBuy,
			PriceCents: targetNo,
			Size:      size,
			SeenFilled: 0,
			Status:     noCreated.Status,
		}
	}

	log.Infof("☕️ [mintpress] 挂双边: yes=%dc no=%dc total=%dc<=%dc size=%.4f locked=%.4f net=%.4f market=%s",
		targetYes, targetNo, targetYes+targetNo, maxTotal, size, s.lockedSets, math.Abs(s.qYes-s.qNo), m.Slug)
}

func (s *Strategy) shouldRequote(cur *trackedOrder, targetPriceCents int, targetSize float64) bool {
	if cur == nil || cur.OrderID == "" {
		return true
	}
	// 若订单已不在 open/partial 状态，也应重挂
	if cur.Status != domain.OrderStatusOpen && cur.Status != domain.OrderStatusPartial && cur.Status != domain.OrderStatusPending {
		return true
	}
	if absInt(cur.PriceCents-targetPriceCents) >= s.RequoteThresholdCents {
		return true
	}
	// size 差异太大也重挂（避免 minOrderSize 调整后漂移）
	if math.Abs(cur.Size-targetSize) >= math.Max(0.01, 0.01*targetSize) {
		return true
	}
	return false
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

