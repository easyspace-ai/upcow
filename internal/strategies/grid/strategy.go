package grid

import (
	"context"
	"fmt"
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
		s.tracked = make(map[string]*trackedOrder)
		s.usedLevel = make(map[domain.TokenType]map[int]bool)
		log.Infof("🔄 [grid] 周期切换，重置状态: market=%s", m.Slug)
	}
	if s.firstSeenAt.IsZero() {
		s.firstSeenAt = now
	}

	// 4) 预热
	if s.WarmupMs > 0 && now.Sub(s.firstSeenAt) < time.Duration(s.WarmupMs)*time.Millisecond {
		return
	}

	// 5) 冷却 + 入场次数上限
	if !s.lastSubmitAt.IsZero() && now.Sub(s.lastSubmitAt) < time.Duration(s.CooldownMs)*time.Millisecond {
		return
	}
	if s.entriesThisCycle >= s.MaxEntriesPerPeriod {
		return
	}

	// 6) 周期后段不再新增入场
	if s.StopNewEntriesSeconds > 0 && m.Timestamp > 0 {
		elapsed := now.Unix() - m.Timestamp
		remain := int64(900) - elapsed
		if remain <= int64(s.StopNewEntriesSeconds) {
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
		bestAsk, size, skipped, _, _, _, _, err := common.QuoteAndAdjustBuy(
			orderCtx,
			s.TradingService,
			assetID,
			*level, // maxCents：把网格层级当作硬上限
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

	// 12) 处理订单更新：推进成交/挂止盈/清理状态
	s.drainOrderUpdates(loopCtx, m)
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
				exitSize := o.FilledSize
				if exitSize <= 0 {
					continue
				}
				target := domain.Price{Cents: meta.TargetExitCents}
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
						OrderType: types.OrderTypeGTC,
					}},
					Hedge: execution.AutoHedgeConfig{Enabled: false},
				}
				orderCtx, cancel := context.WithTimeout(loopCtx, 25*time.Second)
				created, err := s.TradingService.ExecuteMultiLeg(orderCtx, req)
				cancel()
				if err == nil && len(created) > 0 && created[0] != nil && created[0].OrderID != "" {
					meta.ExitPlaced = true
					// 追踪出场单，便于后续清理
					s.tracked[created[0].OrderID] = &trackedOrder{
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
					log.Infof("🎯 [grid] 挂止盈: token=%s entry=%dc tp=%dc size=%.4f market=%s",
						meta.TokenType, meta.EntryPriceCents, meta.TargetExitCents, exitSize, m.Slug)
				}
			}

			// 清理：已结束的订单就不再追踪（避免 map 无限增长）
			if o.Status == domain.OrderStatusFilled || o.Status == domain.OrderStatusCanceled || o.Status == domain.OrderStatusFailed {
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

