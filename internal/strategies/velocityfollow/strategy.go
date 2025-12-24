package velocityfollow

import (
	"context"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

type sample struct {
	ts         time.Time
	priceCents int
}

type metrics struct {
	ok       bool
	delta    int
	seconds  float64
	velocity float64 // cents/sec
}

type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	mu sync.Mutex

	samples map[domain.TokenType][]sample

	// cycle / throttle
	firstSeenAt     time.Time
	lastTriggerAt   time.Time
	tradedThisCycle bool

	// filter: only handle current configured market
	marketSlugPrefix string

	// sizing constraints from global config
	minOrderSize float64
	minShareSize float64
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }

func (s *Strategy) Defaults() error { return nil }

func (s *Strategy) Validate() error { return s.Config.Validate() }

func (s *Strategy) Initialize() error {
	if s.samples == nil {
		s.samples = make(map[domain.TokenType][]sample)
	}

	// 读取全局 market 配置：用于过滤 slug（防止误处理非目标市场）
	if gc := config.Get(); gc != nil {
		if sp, err := gc.Market.Spec(); err == nil {
			s.marketSlugPrefix = strings.ToLower(sp.SlugPrefix())
		}
		s.minOrderSize = gc.MinOrderSize
		s.minShareSize = gc.MinShareSize
	}
	if s.marketSlugPrefix == "" {
		s.marketSlugPrefix = "btc-updown-15m-"
	}
	if s.minOrderSize <= 0 {
		s.minOrderSize = 1.1
	}
	if s.minShareSize <= 0 {
		s.minShareSize = 5.0
	}
	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	log.Infof("✅ [%s] 策略已订阅价格变化事件 (session=%s)", ID, session.Name)
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, _ *domain.Market) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = make(map[domain.TokenType][]sample)
	s.firstSeenAt = time.Now()
	s.tradedThisCycle = false
	// 不清 lastTriggerAt：避免周期切换瞬间重复触发
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}

	// 只处理目标市场（通过 prefix 匹配）
	if !strings.HasPrefix(strings.ToLower(e.Market.Slug), s.marketSlugPrefix) {
		return nil
	}

	now := e.Timestamp
	if now.IsZero() {
		now = time.Now()
	}

	s.mu.Lock()

	if s.firstSeenAt.IsZero() {
		s.firstSeenAt = now
	}
	if s.WarmupMs > 0 && now.Sub(s.firstSeenAt) < time.Duration(s.WarmupMs)*time.Millisecond {
		s.mu.Unlock()
		return nil
	}
	if s.OncePerCycle && s.tradedThisCycle {
		s.mu.Unlock()
		return nil
	}
	if !s.lastTriggerAt.IsZero() && now.Sub(s.lastTriggerAt) < time.Duration(s.CooldownMs)*time.Millisecond {
		s.mu.Unlock()
		return nil
	}

	// 更新样本
	priceCents := e.NewPrice.ToCents()
	if priceCents <= 0 || priceCents >= 100 {
		s.mu.Unlock()
		return nil
	}
	s.samples[e.TokenType] = append(s.samples[e.TokenType], sample{ts: now, priceCents: priceCents})
	s.pruneLocked(now)

	// 计算 UP/DOWN 指标，选择“上行更快”的一侧触发
	mUp := s.computeLocked(domain.TokenTypeUp)
	mDown := s.computeLocked(domain.TokenTypeDown)

	winner := domain.TokenType("")
	winMet := metrics{}
	if mUp.ok && mUp.delta >= s.MinMoveCents && mUp.velocity >= s.MinVelocityCentsPerSec {
		winner = domain.TokenTypeUp
		winMet = mUp
	}
	if mDown.ok && mDown.delta >= s.MinMoveCents && mDown.velocity >= s.MinVelocityCentsPerSec {
		if winner == "" || mDown.velocity > winMet.velocity {
			winner = domain.TokenTypeDown
			winMet = mDown
		}
	}
	if winner == "" {
		s.mu.Unlock()
		return nil
	}

	// 放锁外做 IO（下单/拉盘口）
	// 备注：这里用一个小技巧：先把必要字段拷贝出来
	market := e.Market
	hedgeOffset := s.HedgeOffsetCents
	maxEntry := s.MaxEntryPriceCents
	maxSpread := s.MaxSpreadCents
	orderSize := s.OrderSize
	hedgeSize := s.HedgeOrderSize
	minOrderSize := s.minOrderSize
	minShareSize := s.minShareSize
	s.mu.Unlock()

	if hedgeSize <= 0 {
		hedgeSize = orderSize
	}
	if hedgeOffset <= 0 {
		hedgeOffset = 3
	}

	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	entryAsset := market.YesAssetID
	hedgeAsset := market.NoAssetID
	if winner == domain.TokenTypeDown {
		entryAsset = market.NoAssetID
		hedgeAsset = market.YesAssetID
	}

	// 盘口健康检查（用 entry 侧 bestBid/bestAsk）
	bestBid, bestAsk, err := s.TradingService.GetBestPrice(orderCtx, entryAsset)
	if err != nil || bestBid <= 0 || bestAsk <= 0 {
		return nil
	}
	askCents := int(bestAsk*100 + 0.5)
	bidCents := int(bestBid*100 + 0.5)
	if askCents <= 0 || bidCents <= 0 || askCents >= 100 || bidCents >= 100 {
		return nil
	}
	if maxEntry > 0 && askCents > maxEntry {
		return nil
	}
	spread := askCents - bidCents
	if spread < 0 {
		spread = -spread
	}
	if maxSpread > 0 && spread > maxSpread {
		return nil
	}

	// 计算对侧挂单价格：互补价 - offset
	hedgeCents := 100 - askCents - hedgeOffset
	if hedgeCents < 1 {
		hedgeCents = 1
	}
	if hedgeCents > 99 {
		hedgeCents = 99
	}

	entryPrice := domain.Price{Pips: askCents * 100}   // 1 cent = 100 pips
	hedgePrice := domain.Price{Pips: hedgeCents * 100} // 1 cent = 100 pips

	entryAskDec := float64(askCents) / 100.0
	hedgeDec := float64(hedgeCents) / 100.0

	// size：确保满足最小金额/最小 shares（GTC）
	entryShares := ensureMinOrderSize(orderSize, entryAskDec, minOrderSize)
	hedgeShares := ensureMinOrderSize(hedgeSize, hedgeDec, minOrderSize)
	if hedgeShares < minShareSize {
		hedgeShares = minShareSize
	}

	req := execution.MultiLegRequest{
		Name:       "velocityfollow",
		MarketSlug: market.Slug,
		Legs: []execution.LegIntent{
			{
				Name:      "taker_buy_winner",
				AssetID:   entryAsset,
				TokenType: winner,
				Side:      types.SideBuy,
				Price:     entryPrice,
				Size:      entryShares,
				OrderType: types.OrderTypeFAK,
			},
			{
				Name:      "maker_buy_hedge",
				AssetID:   hedgeAsset,
				TokenType: opposite(winner),
				Side:      types.SideBuy,
				Price:     hedgePrice,
				Size:      hedgeShares,
				OrderType: types.OrderTypeGTC,
			},
		},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}

	_, execErr := s.TradingService.ExecuteMultiLeg(orderCtx, req)
	s.mu.Lock()
	if execErr == nil {
		s.lastTriggerAt = time.Now()
		s.tradedThisCycle = true
		log.Infof("⚡ [%s] 触发: side=%s ask=%dc hedge=%dc vel=%.3f(c/s) move=%dc/%0.1fs market=%s",
			ID, winner, askCents, hedgeCents, winMet.velocity, winMet.delta, winMet.seconds, market.Slug)
	} else {
		log.Warnf("⚠️ [%s] 下单失败: err=%v side=%s market=%s", ID, execErr, winner, market.Slug)
	}
	s.mu.Unlock()
	return nil
}

func (s *Strategy) pruneLocked(now time.Time) {
	window := time.Duration(s.WindowSeconds) * time.Second
	if window <= 0 {
		window = 10 * time.Second
	}
	cut := now.Add(-window)
	for tok, arr := range s.samples {
		// 找到第一个 >= cut 的索引
		i := 0
		for i < len(arr) && arr[i].ts.Before(cut) {
			i++
		}
		if i > 0 {
			arr = arr[i:]
		}
		// 防止极端情况下 slice 无限增长（保守上限）
		if len(arr) > 512 {
			arr = arr[len(arr)-512:]
		}
		s.samples[tok] = arr
	}
}

func (s *Strategy) computeLocked(tok domain.TokenType) metrics {
	arr := s.samples[tok]
	if len(arr) < 2 {
		return metrics{}
	}
	first := arr[0]
	last := arr[len(arr)-1]
	dt := last.ts.Sub(first.ts).Seconds()
	if dt <= 0.001 {
		return metrics{}
	}
	delta := last.priceCents - first.priceCents
	// 只做“上行”触发（你的描述是追涨买上涨的一方）
	if delta <= 0 {
		return metrics{}
	}
	vel := float64(delta) / dt
	if math.IsNaN(vel) || math.IsInf(vel, 0) {
		return metrics{}
	}
	return metrics{ok: true, delta: delta, seconds: dt, velocity: vel}
}

func opposite(t domain.TokenType) domain.TokenType {
	if t == domain.TokenTypeUp {
		return domain.TokenTypeDown
	}
	return domain.TokenTypeUp
}

func ensureMinOrderSize(desiredShares float64, price float64, minUSDC float64) float64 {
	if desiredShares <= 0 || price <= 0 {
		return desiredShares
	}
	if minUSDC <= 0 {
		minUSDC = 1.0
	}
	minShares := minUSDC / price
	if minShares > desiredShares {
		return minShares
	}
	return desiredShares
}

