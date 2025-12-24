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
	BinanceFuturesKlines *services.BinanceFuturesKlines
	Config         `yaml:",inline" json:",inline"`

	mu sync.Mutex

	samples map[domain.TokenType][]sample

	// cycle / throttle
	firstSeenAt     time.Time
	lastTriggerAt   time.Time
	tradedThisCycle bool

	// Binance-bias state (per cycle)
	cycleStartMs int64
	biasReady    bool
	biasToken    domain.TokenType
	biasReason   string

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
		} else {
			log.WithError(err).Warnf("⚠️ [%s] 读取 market 配置失败，将不做 marketSlugPrefix 过滤（可能会处理非目标市场）", ID)
		}
		s.minOrderSize = gc.MinOrderSize
		s.minShareSize = gc.MinShareSize
	} else {
		log.Warnf("⚠️ [%s] 全局配置未加载，将不做 marketSlugPrefix 过滤（可能会处理非目标市场）", ID)
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
	s.cycleStartMs = 0
	s.biasReady = false
	s.biasToken = ""
	s.biasReason = ""
	// 不清 lastTriggerAt：避免周期切换瞬间重复触发
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}

	// 只处理目标市场（通过 prefix 匹配）
	if s.marketSlugPrefix != "" && !strings.HasPrefix(strings.ToLower(e.Market.Slug), s.marketSlugPrefix) {
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

	// 尽量用 market.Timestamp 作为本周期起点（框架会从 slug 解析）
	if e.Market.Timestamp > 0 {
		st := e.Market.Timestamp * 1000
		if s.cycleStartMs == 0 || s.cycleStartMs != st {
			s.cycleStartMs = st
			s.biasReady = false
			s.biasToken = ""
			s.biasReason = ""
		}
	}

	// 可选：用“开盘第 1 根 1m K线阴阳”做 bias（hard/soft）
	if s.UseBinanceOpen1mBias {
		// 如果等太久还没有拿到那根 1m，就降级为“无 bias”继续跑
		if !s.biasReady && s.cycleStartMs > 0 && s.Open1mMaxWaitSeconds > 0 {
			if now.UnixMilli()-s.cycleStartMs > int64(s.Open1mMaxWaitSeconds)*1000 {
				s.biasReady = true
				s.biasToken = ""
				s.biasReason = "open1m_timeout"
			}
		}

		if !s.biasReady && s.BinanceFuturesKlines != nil && s.cycleStartMs > 0 {
			if k, ok := s.BinanceFuturesKlines.Get("1m", s.cycleStartMs); ok && k.IsClosed && k.Open > 0 {
				bodyBps, wickBps, dirTok := candleStatsBps(k, domain.TokenTypeUp, domain.TokenTypeDown)
				if bodyBps < s.Open1mMinBodyBps {
					s.biasReady = true
					s.biasToken = ""
					s.biasReason = "open1m_body_too_small"
				} else if wickBps > s.Open1mMaxWickBps {
					s.biasReady = true
					s.biasToken = ""
					s.biasReason = "open1m_wick_too_large"
				} else {
					s.biasReady = true
					s.biasToken = dirTok
					s.biasReason = "open1m_ok"
				}
			}
		}

		if s.RequireBiasReady && !s.biasReady {
			s.mu.Unlock()
			return nil
		}
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

	// 根据 bias 调整阈值（soft）或直接只允许 bias 方向（hard）
	reqMoveUp := s.MinMoveCents
	reqMoveDown := s.MinMoveCents
	reqVelUp := s.MinVelocityCentsPerSec
	reqVelDown := s.MinVelocityCentsPerSec

	if s.UseBinanceOpen1mBias && s.biasToken != "" && s.BiasMode == "soft" {
		if s.biasToken == domain.TokenTypeUp {
			reqMoveDown += s.OppositeBiasMinMoveExtraCents
			reqVelDown *= s.OppositeBiasVelocityMultiplier
		} else if s.biasToken == domain.TokenTypeDown {
			reqMoveUp += s.OppositeBiasMinMoveExtraCents
			reqVelUp *= s.OppositeBiasVelocityMultiplier
		}
	}

	winner := domain.TokenType("")
	winMet := metrics{}
	allowUp := true
	allowDown := true
	if s.UseBinanceOpen1mBias && s.biasToken != "" && s.BiasMode == "hard" {
		allowUp = s.biasToken == domain.TokenTypeUp
		allowDown = s.biasToken == domain.TokenTypeDown
	}

	if allowUp && mUp.ok && mUp.delta >= reqMoveUp && mUp.velocity >= reqVelUp {
		winner = domain.TokenTypeUp
		winMet = mUp
	}
	if allowDown && mDown.ok && mDown.delta >= reqMoveDown && mDown.velocity >= reqVelDown {
		if winner == "" || mDown.velocity > winMet.velocity {
			winner = domain.TokenTypeDown
			winMet = mDown
		}
	}
	if winner == "" {
		s.mu.Unlock()
		return nil
	}

	// 可选：用 Binance 1s “底层硬动”过滤（借鉴 momentum bot 的 move threshold 思路）
	if s.UseBinanceMoveConfirm {
		if s.BinanceFuturesKlines == nil {
			s.mu.Unlock()
			return nil
		}
		nowMs := now.UnixMilli()
		cur, okCur := s.BinanceFuturesKlines.Latest("1s")
		past, okPast := s.BinanceFuturesKlines.NearestAtOrBefore("1s", nowMs-int64(s.MoveConfirmWindowSeconds)*1000)
		if !okCur || !okPast || past.Close <= 0 {
			s.mu.Unlock()
			return nil
		}
		ret := (cur.Close - past.Close) / past.Close
		retBps := int(math.Abs(ret)*10000 + 0.5)
		dir := domain.TokenTypeDown
		if ret >= 0 {
			dir = domain.TokenTypeUp
		}
		if retBps < s.MinUnderlyingMoveBps || dir != winner {
			s.mu.Unlock()
			return nil
		}
	}

	// 放锁外做 IO（下单/拉盘口）
	// 备注：这里用一个小技巧：先把必要字段拷贝出来
	market := e.Market
	biasTok := s.biasToken
	biasReason := s.biasReason
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
		log.Infof("⚡ [%s] 触发: side=%s ask=%dc hedge=%dc vel=%.3f(c/s) move=%dc/%0.1fs bias=%s(%s) market=%s",
			ID, winner, askCents, hedgeCents, winMet.velocity, winMet.delta, winMet.seconds, biasTok, biasReason, market.Slug)
		if biasTok != "" || biasReason != "" {
			log.Infof("🧭 [%s] bias: token=%s reason=%s cycleStartMs=%d", ID, biasTok, biasReason, s.cycleStartMs)
		}

		// 额外：打印 Binance 1s/1m 最新 K 线（用于你观察“开盘 1 分钟”关系）
		if s.BinanceFuturesKlines != nil {
			if k1m, ok := s.BinanceFuturesKlines.Latest("1m"); ok {
				log.Infof("📊 [%s] Binance 1m kline: sym=%s o=%.2f c=%.2f h=%.2f l=%.2f closed=%v startMs=%d",
					ID, k1m.Symbol, k1m.Open, k1m.Close, k1m.High, k1m.Low, k1m.IsClosed, k1m.StartTimeMs)
			}
			if k1s, ok := s.BinanceFuturesKlines.Latest("1s"); ok {
				log.Infof("📊 [%s] Binance 1s kline: sym=%s o=%.2f c=%.2f closed=%v startMs=%d",
					ID, k1s.Symbol, k1s.Open, k1s.Close, k1s.IsClosed, k1s.StartTimeMs)
			}
		}
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

func candleStatsBps(k services.Kline, upTok domain.TokenType, downTok domain.TokenType) (bodyBps int, wickBps int, dirTok domain.TokenType) {
	// body: |c-o|/o
	body := math.Abs(k.Close-k.Open) / k.Open * 10000
	bodyBps = int(body + 0.5)

	hi := k.High
	lo := k.Low
	o := k.Open
	c := k.Close
	maxOC := math.Max(o, c)
	minOC := math.Min(o, c)
	upperWick := (hi - maxOC) / o * 10000
	lowerWick := (minOC - lo) / o * 10000
	w := math.Max(upperWick, lowerWick)
	if w < 0 {
		w = 0
	}
	wickBps = int(w + 0.5)

	dirTok = downTok
	if c >= o {
		dirTok = upTok
	}
	return
}

