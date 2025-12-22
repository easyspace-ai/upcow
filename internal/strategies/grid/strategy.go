package grid

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/internal/strategies/orderutil"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/bbgo"
)

const ID = "grid"

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy：BTC 15m 网格策略（按新架构）
//
// 核心实现点：
// - Subscribe: 同时订阅价格与订单更新
// - OnPriceChanged/OnOrderUpdate: 只做事件合并/入队，不做 IO
// - loop: 单 goroutine 推进状态机，并通过 ExecuteMultiLeg 下单
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	loopOnce   sync.Once
	loopCancel context.CancelFunc

	signalC chan struct{}

	// 合并后的最新价格（按 tokenType）
	priceMu  sync.Mutex
	latestPx map[domain.TokenType]*events.PriceChangedEvent

	// 订单更新队列（来自 session.OnOrderUpdate）
	orderC chan *domain.Order

	// loop 内状态（只允许在 loop goroutine 中读写）
	guard        common.MarketSlugGuard
	firstSeenAt  time.Time
	lastSubmitAt time.Time
	entriesThisCycle int
	roundsCompleted  int
	flattenedThisCycle bool

	// 追踪我们自己提交的订单：orderID -> meta
	tracked map[string]*trackedOrder
	// 已经使用过的 gridLevel（防止重复“同一层级反复入场”）
	usedLevel map[domain.TokenType]map[int]bool
}

type trackedOrderKind string

const (
	kindEntry trackedOrderKind = "entry"
	kindExit  trackedOrderKind = "exit"
)

type trackedOrder struct {
	Kind      trackedOrderKind
	TokenType domain.TokenType
	AssetID   string
	MarketSlug string

	// 入场网格层级（触发层）
	GridLevel int

	// 下单参数（用于部分成交 delta 记账/补挂止盈）
	Side         types.Side
	EntryPriceCents int
	TargetExitCents int
	RequestedSize  float64

	// 已处理的成交量（用于从 OrderUpdate 计算 delta）
	SeenFilled float64

	// 出场单是否已挂（部分成交时也会挂）
	ExitPlaced bool

	// 出场下单重试（应对“刚成交立刻卖但平台还没同步持仓”的延迟）
	ExitAttempts     int
	NextExitAttemptAt time.Time
	LastExitError    string
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
		s.orderC = make(chan *domain.Order, 2048)
	}
	if s.tracked == nil {
		s.tracked = make(map[string]*trackedOrder)
	}
	if s.usedLevel == nil {
		s.usedLevel = make(map[domain.TokenType]map[int]bool)
	}
	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	// 关键：网格必须感知成交/撤单才能挂止盈与清理状态
	session.OnOrderUpdate(s)
	log.Infof("✅ [grid] 策略已订阅价格+订单更新 (session=%s)", session.Name)
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
		// 队列满时丢弃（不阻塞 session），网格会在下一轮通过 open orders/价格继续收敛
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

	// 1) 抽取合并后的价格事件
	s.priceMu.Lock()
	evUp := s.latestPx[domain.TokenTypeUp]
	evDown := s.latestPx[domain.TokenTypeDown]
	s.latestPx = make(map[domain.TokenType]*events.PriceChangedEvent)
	s.priceMu.Unlock()

	// 2) 选择一个市场上下文（以任一 token 的事件为准）
	var m *domain.Market
	var now time.Time
	if evUp != nil && evUp.Market != nil {
		m = evUp.Market
		now = evUp.Timestamp
	}
	if m == nil && evDown != nil && evDown.Market != nil {
		m = evDown.Market
		now = evDown.Timestamp
	}
	if m == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}

	// 3) 周期切换：重置状态
	if s.guard.Update(m.Slug) {
		s.firstSeenAt = now
		s.lastSubmitAt = time.Time{}
		s.entriesThisCycle = 0
		s.roundsCompleted = 0
		s.flattenedThisCycle = false
		s.tracked = make(map[string]*trackedOrder)
		s.usedLevel = make(map[domain.TokenType]map[int]bool)
		log.Infof("🔄 [grid] 周期切换，重置状态: market=%s", m.Slug)
	}
	if s.firstSeenAt.IsZero() {
		s.firstSeenAt = now
	}

	// 4) 预热
	if s.WarmupMs > 0 && now.Sub(s.firstSeenAt) < time.Duration(s.WarmupMs)*time.Millisecond {
		// 即使预热，也要处理订单更新（避免刚启动时错过成交/撤单导致状态不收敛）
		s.drainOrderUpdates(loopCtx, m)
		return
	}

	// 5) 先处理订单更新（止盈/清理必须不受 cooldown/stopNewEntries 等限制）
	s.drainOrderUpdates(loopCtx, m)
	// 5.1) 出场重试：即使没有新的订单更新，也要按计划重试挂止盈
	s.retryPendingExits(loopCtx, m)
	// 轮次推进：当上一轮所有订单都结束后，按配置决定是否开启下一轮
	s.maybeAdvanceRound(m.Slug)

	// 5) 冷却 + 入场次数上限
	if !s.lastSubmitAt.IsZero() && now.Sub(s.lastSubmitAt) < time.Duration(s.CooldownMs)*time.Millisecond {
		return
	}
	if s.entriesThisCycle >= s.MaxEntriesPerPeriod {
		return
	}
	// 轮次上限：达到上限后不再新增入场（但仍会继续处理订单更新）
	if s.MaxRoundsPerPeriod > 0 && s.roundsCompleted >= s.MaxRoundsPerPeriod {
		return
	}

	// 6) 周期后段控制：清仓/停止新增
	if m.Timestamp > 0 {
		elapsed := now.Unix() - m.Timestamp
		remain := int64(900) - elapsed

		// 6.1 清仓：不赌方向 —— 周期结束前把本周期持仓出清
		if !s.flattenedThisCycle {
			flattenSeconds := s.flattenSecondsBeforeEnd()
			if flattenSeconds > 0 && remain <= int64(flattenSeconds) {
				s.flattenPositions(loopCtx, m, remain)
				s.flattenedThisCycle = true
				return
			}
		}

		// 6.2 停止新增入场
		if s.StopNewEntriesSeconds > 0 && remain <= int64(s.StopNewEntriesSeconds) {
			return
		}
	}

	// 7) 冻结检测：任一 side 进入极端共识区间则冻结（不再新增）
	if (evUp != nil && s.isFrozenPrice(evUp.NewPrice.Cents)) || (evDown != nil && s.isFrozenPrice(evDown.NewPrice.Cents)) {
		if s.CancelEntryOrdersOnFreeze {
			s.cancelAllEntryOrders(loopCtx, m.Slug)
		}
		return
	}

	// 8) 限制并发入场单数量
	if s.countOpenEntryOrders(m.Slug) >= s.MaxOpenEntryOrders {
		return
	}

	// 9) 计算网格层级列表
	levels := s.gridLevels()
	if len(levels) < 2 {
		return
	}

	// 10) 选择要交易的 token 列表
	tokenTargets := s.targetTokens()
	if len(tokenTargets) == 0 {
		return
	}
	// 10.1 库存中性 gating：净敞口过大时，只允许补“较少的一侧”
	tokenTargets = s.applyInventoryNeutrality(m.Slug, tokenTargets)
	if len(tokenTargets) == 0 {
		return
	}

	// 11) 对每个 token 尝试“最近下方层级”入场（每轮最多提交一次，避免同时双向下单风暴）
	for _, tt := range tokenTargets {
		var ev *events.PriceChangedEvent
		var assetID string
		if tt == domain.TokenTypeUp {
			ev = evUp
			assetID = m.YesAssetID
		} else {
			ev = evDown
			assetID = m.NoAssetID
		}
		if ev == nil || assetID == "" {
			continue
		}

		priceCents := ev.NewPrice.Cents
		level := nearestLowerOrEqual(levels, priceCents)
		if level == nil {
			continue
		}

		// 已在该层级入场过：跳过（本周期内不重复）
		if s.isLevelUsed(tt, *level) {
			continue
		}

		// 盘口 quote：要求 bestAsk <= level 才入场
		orderCtx, cancel := context.WithTimeout(loopCtx, 25*time.Second)
		maxCents := *level + s.GridLevelSlippageCents
		if maxCents > 99 {
			maxCents = 99
		}
		bestAsk, size, skipped, _, _, _, _, err := common.QuoteAndAdjustBuy(
			orderCtx,
			s.TradingService,
			assetID,
			maxCents, // maxCents：允许层级上方一定滑点容忍
			s.OrderSize,
			s.MinOrderSize,
			s.AutoAdjustSize,
			s.MaxSizeAdjustRatio,
		)
		cancel()
		if err != nil || skipped || bestAsk.Cents <= 0 || size <= 0 {
			continue
		}

		targetExit := bestAsk.Cents + s.ProfitTargetCents
		if targetExit > 99 {
			targetExit = 99
		}

		req := execution.MultiLegRequest{
			Name:      fmt.Sprintf("grid_entry_%s_%dc", strings.ToLower(string(tt)), *level),
			MarketSlug: m.Slug,
			Legs: []execution.LegIntent{{
				Name:      "buy",
				AssetID:   assetID,
				TokenType: tt,
				Side:      types.SideBuy,
				Price:     bestAsk,
				Size:      size,
				OrderType: types.OrderTypeFAK,
			}},
			Hedge: execution.AutoHedgeConfig{Enabled: false},
		}

		orderCtx2, cancel2 := context.WithTimeout(loopCtx, 25*time.Second)
		created, err := s.TradingService.ExecuteMultiLeg(orderCtx2, req)
		cancel2()
		if err != nil || len(created) < 1 || created[0] == nil || created[0].OrderID == "" {
			continue
		}

		oid := created[0].OrderID
		s.tracked[oid] = &trackedOrder{
			Kind:           kindEntry,
			TokenType:      tt,
			AssetID:        assetID,
			MarketSlug:     m.Slug,
			GridLevel:      *level,
			Side:           types.SideBuy,
			EntryPriceCents: bestAsk.Cents,
			TargetExitCents: targetExit,
			RequestedSize:   size,
		}
		s.markLevelUsed(tt, *level)
		s.entriesThisCycle++
		s.lastSubmitAt = now
		log.Infof("📌 [grid] 入场: token=%s level=%dc ask=%dc size=%.4f tp=%dc market=%s",
			tt, *level, bestAsk.Cents, size, targetExit, m.Slug)
		return
	}
}

func (s *Strategy) retryPendingExits(loopCtx context.Context, m *domain.Market) {
	if s == nil || s.TradingService == nil || m == nil {
		return
	}
	now := time.Now()
	for _, meta := range s.tracked {
		if meta == nil {
			continue
		}
		if meta.Kind != kindEntry || meta.ExitPlaced {
			continue
		}
		if meta.MarketSlug != "" && m.Slug != "" && meta.MarketSlug != m.Slug {
			continue
		}
		if meta.SeenFilled <= 0 {
			continue
		}
		if !meta.NextExitAttemptAt.IsZero() && now.Before(meta.NextExitAttemptAt) {
			continue
		}
		s.tryPlaceExit(loopCtx, m, meta)
	}
}

func (s *Strategy) shouldRetryExit(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// 覆盖常见“持仓/余额尚未同步”的报错关键词（交易所/网关差异较大，宁可宽松一点）
	for _, kw := range []string{
		"position",
		"balance",
		"insufficient",
		"not enough",
		"available",
		"allowance",
		"holdings",
		"shares",
		"amount",
	} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	// 默认也重试（但由退避控制频率），避免因为关键词不匹配而永远不挂止盈
	return true
}

func (s *Strategy) scheduleExitRetry(loopCtx context.Context, meta *trackedOrder) {
	if meta == nil {
		return
	}
	// 指数退避：200ms * 2^k，封顶 8s
	k := meta.ExitAttempts
	if k < 0 {
		k = 0
	}
	delay := 200 * time.Millisecond * time.Duration(1<<minInt(k, 6)) // 200ms..12.8s-ish，后面再 cap
	if delay > 8*time.Second {
		delay = 8 * time.Second
	}
	meta.NextExitAttemptAt = time.Now().Add(delay)

	// 无 tick 的 loop：用一次性定时唤醒来触发重试
	go func(next time.Time) {
		d := time.Until(next)
		if d < 0 {
			d = 0
		}
		select {
		case <-time.After(d):
			common.TrySignal(s.signalC)
		case <-loopCtx.Done():
			return
		}
	}(meta.NextExitAttemptAt)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s *Strategy) tryPlaceExit(loopCtx context.Context, m *domain.Market, meta *trackedOrder) {
	if s == nil || s.TradingService == nil || m == nil || meta == nil {
		return
	}

	// 以最新累计成交量为准（可能从 partial -> filled）
	exitSize := meta.SeenFilled
	if exitSize <= 0 {
		return
	}

	target := domain.Price{Cents: meta.TargetExitCents}
	exitOrderType := types.OrderTypeGTC
	// 保护：很小的 size 用 FAK 兜底（避免交易所最小 shares 约束导致挂单被拒）
	if exitSize < 5.0 {
		exitOrderType = types.OrderTypeFAK
	}

	req := execution.MultiLegRequest{
		Name:      fmt.Sprintf("grid_exit_%s_%dc", strings.ToLower(string(meta.TokenType)), meta.GridLevel),
		MarketSlug: m.Slug,
		Legs: []execution.LegIntent{{
			Name:      "sell_tp",
			AssetID:   meta.AssetID,
			TokenType: meta.TokenType,
			Side:      types.SideSell,
			Price:     target,
			Size:      exitSize,
			OrderType: exitOrderType,
		}},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}

	orderCtx, cancel := context.WithTimeout(loopCtx, 25*time.Second)
	created, err := s.TradingService.ExecuteMultiLeg(orderCtx, req)
	cancel()
	if err != nil || len(created) == 0 || created[0] == nil || created[0].OrderID == "" {
		meta.ExitAttempts++
		if err != nil {
			meta.LastExitError = err.Error()
		} else {
			meta.LastExitError = "unknown exit order failure"
		}

		if s.shouldRetryExit(err) {
			log.Warnf("⏳ [grid] 挂止盈失败，准备重试: token=%s level=%dc tp=%dc size=%.4f attempts=%d err=%s",
				meta.TokenType, meta.GridLevel, meta.TargetExitCents, exitSize, meta.ExitAttempts, meta.LastExitError)
			s.scheduleExitRetry(loopCtx, meta)
		}
		return
	}

	// 成功：标记并追踪出场单
	meta.ExitPlaced = true
	meta.NextExitAttemptAt = time.Time{}
	oid := created[0].OrderID
	s.tracked[oid] = &trackedOrder{
		Kind:            kindExit,
		TokenType:       meta.TokenType,
		AssetID:         meta.AssetID,
		MarketSlug:      m.Slug,
		GridLevel:       meta.GridLevel,
		Side:            types.SideSell,
		EntryPriceCents: meta.EntryPriceCents,
		TargetExitCents: meta.TargetExitCents,
		RequestedSize:   exitSize,
	}
	log.Infof("🎯 [grid] 挂止盈成功: token=%s entry=%dc tp=%dc size=%.4f orderType=%s market=%s",
		meta.TokenType, meta.EntryPriceCents, meta.TargetExitCents, exitSize, exitOrderType, m.Slug)
}

func (s *Strategy) applyInventoryNeutrality(marketSlug string, targets []domain.TokenType) []domain.TokenType {
	if s == nil || s.TradingService == nil {
		return targets
	}
	if s.MaxNetExposureShares <= 0 {
		return targets
	}
	if !s.EnableDoubleSide {
		// 单向模式下不做净敞口限制（否则会把策略锁死）
		return targets
	}

	upSize, downSize := s.currentInventoryShares(marketSlug)
	net := upSize - downSize
	if math.Abs(net) < s.MaxNetExposureShares {
		return targets
	}

	need := domain.TokenTypeDown
	if net < 0 {
		need = domain.TokenTypeUp
	}

	out := make([]domain.TokenType, 0, 1)
	for _, tt := range targets {
		if tt == need {
			out = append(out, tt)
		}
	}
	return out
}

func (s *Strategy) currentInventoryShares(marketSlug string) (upSize float64, downSize float64) {
	if s == nil || s.TradingService == nil {
		return 0, 0
	}
	for _, p := range s.TradingService.GetOpenPositionsForMarket(marketSlug) {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}
		if p.TokenType == domain.TokenTypeUp {
			upSize += p.Size
		} else if p.TokenType == domain.TokenTypeDown {
			downSize += p.Size
		}
	}
	return upSize, downSize
}

func (s *Strategy) flattenSecondsBeforeEnd() int {
	if s == nil || s.FlattenSecondsBeforeEnd == nil {
		return 0
	}
	if *s.FlattenSecondsBeforeEnd <= 0 {
		return 0
	}
	return *s.FlattenSecondsBeforeEnd
}

func (s *Strategy) flattenPositions(loopCtx context.Context, m *domain.Market, remain int64) {
	if s == nil || s.TradingService == nil || m == nil {
		return
	}
	// 先撤掉所有入场单，避免清仓时又被入场单“补回去”
	cancelCtx, cancel := context.WithTimeout(loopCtx, 10*time.Second)
	s.cancelAllEntryOrders(cancelCtx, m.Slug)
	cancel()

	// 汇总本周期持仓（按 tokenType）
	upSize, downSize := s.currentInventoryShares(m.Slug)
	if upSize <= 0 && downSize <= 0 {
		log.Infof("🧹 [grid] 清仓窗口到达(remain=%ds)，但无持仓需要处理: market=%s", remain, m.Slug)
		return
	}

	log.Warnf("🧹 [grid] 清仓窗口到达(remain=%ds)：开始出清持仓 up=%.4f down=%.4f market=%s",
		remain, upSize, downSize, m.Slug)

	// 逐边用 FAK 快速卖出（不赌方向：宁可小滑点，也不要带仓进结算）
	sellOne := func(tt domain.TokenType, assetID string, size float64) {
		if size <= 0 || assetID == "" {
			return
		}
		ctx, cancel := context.WithTimeout(loopCtx, 20*time.Second)
		defer cancel()
		bestBid, err := orderutil.QuoteSellPrice(ctx, s.TradingService, assetID, 0)
		if err != nil || bestBid.Cents <= 0 {
			return
		}
		req := execution.MultiLegRequest{
			Name:      fmt.Sprintf("grid_flatten_%s", strings.ToLower(string(tt))),
			MarketSlug: m.Slug,
			Legs: []execution.LegIntent{{
				Name:      "sell_flatten",
				AssetID:   assetID,
				TokenType: tt,
				Side:      types.SideSell,
				Price:     bestBid,
				Size:      size,
				OrderType: types.OrderTypeFAK,
			}},
			Hedge: execution.AutoHedgeConfig{Enabled: false},
		}
		_, _ = s.TradingService.ExecuteMultiLeg(ctx, req)
	}

	sellOne(domain.TokenTypeUp, m.YesAssetID, upSize)
	sellOne(domain.TokenTypeDown, m.NoAssetID, downSize)
}

func (s *Strategy) maybeAdvanceRound(marketSlug string) {
	if s == nil || s.TradingService == nil {
		return
	}
	// 没有用过任何层级，说明还没开始一轮
	if !s.hasAnyUsedLevel() {
		return
	}
	// 等待本轮完全结束（默认 true）
	if s.WaitForRoundCompleteEnabled() && !s.isRoundComplete(marketSlug) {
		return
	}
	// 本轮已结束：清空 usedLevel，让下一轮可以复用层级
	// 注意：roundsCompleted 表示“已完成轮次”计数；到达上限后，入场逻辑会被短路。
	s.roundsCompleted++
	s.usedLevel = make(map[domain.TokenType]map[int]bool)
	log.Infof("🔁 [grid] 本轮已完成，开始下一轮: completed=%d market=%s", s.roundsCompleted, marketSlug)
}

func (s *Strategy) hasAnyUsedLevel() bool {
	for _, m := range s.usedLevel {
		if len(m) > 0 {
			return true
		}
	}
	return false
}

func (s *Strategy) isRoundComplete(marketSlug string) bool {
	// round complete 的定义：没有任何“我们追踪的”入场/止盈单仍处于 open/pending/partial
	orders := s.TradingService.GetActiveOrders()
	for _, o := range orders {
		if o == nil || o.OrderID == "" {
			continue
		}
		if marketSlug != "" && o.MarketSlug != "" && o.MarketSlug != marketSlug {
			continue
		}
		meta := s.tracked[o.OrderID]
		if meta == nil {
			continue
		}
		if meta.Kind != kindEntry && meta.Kind != kindExit {
			continue
		}
		if o.Status == domain.OrderStatusOpen || o.Status == domain.OrderStatusPartial || o.Status == domain.OrderStatusPending {
			return false
		}
	}
	return true
}

func (s *Strategy) drainOrderUpdates(loopCtx context.Context, m *domain.Market) {
	for {
		select {
		case o := <-s.orderC:
			if o == nil || o.OrderID == "" {
				continue
			}
			meta := s.tracked[o.OrderID]
			if meta == nil {
				continue
			}
			// 严格隔离：只处理本周期订单
			if meta.MarketSlug != "" && m.Slug != "" && meta.MarketSlug != m.Slug {
				continue
			}

			// 更新 delta filled
			if o.FilledSize > meta.SeenFilled {
				meta.SeenFilled = o.FilledSize
			}

			// 入场单：只要出现“有成交且尚未挂止盈”，就挂止盈（覆盖 FAK 的 partial fill）
			if meta.Kind == kindEntry && !meta.ExitPlaced && o.FilledSize > 0 {
				// 触发一次立即尝试；失败会进入重试队列（指数退避）
				s.tryPlaceExit(loopCtx, m, meta)
			}

			// 清理：已结束的订单就不再追踪（避免 map 无限增长）
			if o.Status == domain.OrderStatusFilled || o.Status == domain.OrderStatusCanceled || o.Status == domain.OrderStatusFailed {
				// 关键：让网格“多轮次”跑起来 —— 当一个层级的订单生命周期结束后，释放该层级可再次入场。
				// - 入场单（FAK）如果没成交就结束：应释放 usedLevel（否则会永久跳过该层级）
				// - 止盈单（GTC/FAK）成交：代表该层级完成一轮获利，释放 usedLevel 以便再次在同层级循环
				// - 止盈单取消/失败：通常仍持仓未了结，避免加倍暴露，因此不自动释放
				if meta.Kind == kindEntry {
					// entry 生命周期结束（常见：FAK 未成交 -> canceled/failed）
					if o.FilledSize <= 0 && (o.Status == domain.OrderStatusCanceled || o.Status == domain.OrderStatusFailed) {
						s.releaseLevel(meta.TokenType, meta.GridLevel)
					}
				} else if meta.Kind == kindExit {
					// exit 成交：完成一轮
					if o.Status == domain.OrderStatusFilled {
						s.releaseLevel(meta.TokenType, meta.GridLevel)
					}
				}
				delete(s.tracked, o.OrderID)
			}
		default:
			return
		}
	}
}

func (s *Strategy) cancelAllEntryOrders(ctx context.Context, marketSlug string) {
	if s.TradingService == nil {
		return
	}
	orders := s.TradingService.GetActiveOrders()
	for _, o := range orders {
		if o == nil || o.OrderID == "" {
			continue
		}
		meta := s.tracked[o.OrderID]
		if meta == nil || meta.Kind != kindEntry {
			continue
		}
		if marketSlug != "" && o.MarketSlug != "" && o.MarketSlug != marketSlug {
			continue
		}
		_ = s.TradingService.CancelOrder(ctx, o.OrderID)
	}
}

func (s *Strategy) countOpenEntryOrders(marketSlug string) int {
	if s.TradingService == nil {
		return 0
	}
	orders := s.TradingService.GetActiveOrders()
	n := 0
	for _, o := range orders {
		if o == nil || o.OrderID == "" {
			continue
		}
		meta := s.tracked[o.OrderID]
		if meta == nil || meta.Kind != kindEntry {
			continue
		}
		if marketSlug != "" && o.MarketSlug != "" && o.MarketSlug != marketSlug {
			continue
		}
		// open/partial/pending 都算
		if o.Status == domain.OrderStatusOpen || o.Status == domain.OrderStatusPartial || o.Status == domain.OrderStatusPending {
			n++
		}
	}
	return n
}

func (s *Strategy) gridLevels() []int {
	if len(s.GridLevels) > 0 {
		levels := append([]int(nil), s.GridLevels...)
		sort.Ints(levels)
		return levels
	}
	// auto
	if s.GridStart <= 0 || s.GridEnd <= 0 || s.GridGap <= 0 {
		return nil
	}
	g := domain.NewGrid(s.GridStart, s.GridGap, s.GridEnd)
	if g == nil {
		return nil
	}
	return append([]int(nil), g.Levels...)
}

func (s *Strategy) targetTokens() []domain.TokenType {
	if s.EnableDoubleSide {
		return []domain.TokenType{domain.TokenTypeUp, domain.TokenTypeDown}
	}
	t := strings.ToLower(strings.TrimSpace(s.TokenType))
	switch t {
	case "", "up", "yes":
		return []domain.TokenType{domain.TokenTypeUp}
	case "down", "no":
		return []domain.TokenType{domain.TokenTypeDown}
	default:
		return []domain.TokenType{domain.TokenTypeUp}
	}
}

func (s *Strategy) isFrozenPrice(cents int) bool {
	if cents <= 0 {
		return false
	}
	if s.FreezeHighCents > 0 && cents >= s.FreezeHighCents {
		return true
	}
	if s.FreezeLowCents > 0 && cents <= s.FreezeLowCents {
		return true
	}
	return false
}

func (s *Strategy) isLevelUsed(tt domain.TokenType, level int) bool {
	m := s.usedLevel[tt]
	if m == nil {
		return false
	}
	return m[level]
}

func (s *Strategy) markLevelUsed(tt domain.TokenType, level int) {
	m := s.usedLevel[tt]
	if m == nil {
		m = make(map[int]bool)
		s.usedLevel[tt] = m
	}
	m[level] = true
}

func (s *Strategy) releaseLevel(tt domain.TokenType, level int) {
	m := s.usedLevel[tt]
	if m == nil {
		return
	}
	delete(m, level)
}

func nearestLowerOrEqual(levels []int, priceCents int) *int {
	if len(levels) == 0 {
		return nil
	}
	// 找到 <= price 的最大 level
	i := sort.Search(len(levels), func(i int) bool { return levels[i] > priceCents })
	if i == 0 {
		return nil
	}
	v := levels[i-1]
	return &v
}

// 编译期断言：确保实现了回调接口
var _ bbgo.SingleExchangeStrategy = (*Strategy)(nil)
var _ bbgo.ExchangeSessionSubscriber = (*Strategy)(nil)
var _ orderutil.BestPriceGetter = (*services.TradingService)(nil)

