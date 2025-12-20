package updown

import (
	"context"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	strategycommon "github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/internal/strategies/orderutil"
	strategyports "github.com/betbot/gobet/internal/strategies/ports"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", "updown")

func init() {
	// bbgo main 风格：注册策略 struct，用于直接从 YAML/JSON 反序列化配置
	bbgo.RegisterStrategy(ID, &Strategy{})
}

// Strategy is a standard single-exchange strategy implementation.
// It demonstrates:
// - typed tradingService ports
// - single goroutine loop via common.StartLoopOnce
// - non-blocking signals via common.TrySignal
// - basic in-flight limiting
type Strategy struct {
	Executor bbgo.CommandExecutor

	mu             sync.RWMutex
	Config         `yaml:",inline" json:",inline"`
	config         *Config `json:"-" yaml:"-"`
	tradingService strategyports.BasicTradingService
	currentMarket  *domain.Market
	marketGuard    strategycommon.MarketSlugGuard

	loopOnce   sync.Once
	loopCancel context.CancelFunc
	signalC    chan struct{}

	inFlight *strategycommon.InFlightLimiter
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }

func (s *Strategy) Defaults() error { return nil }

func (s *Strategy) Validate() error {
	s.mu.Lock()
	s.config = &s.Config
	cfg := s.config
	s.mu.Unlock()
	return cfg.Validate()
}

func (s *Strategy) Initialize() error {
	s.mu.Lock()
	s.config = &s.Config
	if s.inFlight == nil {
		s.inFlight = strategycommon.NewInFlightLimiter(4)
	}
	s.mu.Unlock()
	return nil
}

func (s *Strategy) SetTradingService(ts strategyports.BasicTradingService) {
	s.mu.Lock()
	s.tradingService = ts
	s.mu.Unlock()
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	// 标准订阅点：把 websocket callback 绑定到策略。
	session.OnOrderUpdate(s)
	session.OnPriceChanged(s)
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	// 标准 Run：记录 market 并启动 loop
	s.mu.Lock()
	s.currentMarket = session.Market()
	if s.currentMarket != nil {
		s.marketGuard.Update(s.currentMarket.Slug)
	}
	s.mu.Unlock()

	s.startLoop(ctx)
	<-ctx.Done()
	return ctx.Err()
}

func (s *Strategy) Shutdown(ctx context.Context, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		if s.loopCancel != nil {
			s.loopCancel()
		}
		<-ctx.Done()
	}()
}

func (s *Strategy) startLoop(ctx context.Context) {
	if s.signalC == nil {
		s.signalC = make(chan struct{}, 1)
	}

	strategycommon.StartLoopOnce(
		ctx,
		&s.loopOnce,
		func(cancel context.CancelFunc) { s.loopCancel = cancel },
		300*time.Millisecond,
		func(loopCtx context.Context, tickC <-chan time.Time) {
			s.runLoop(loopCtx, tickC)
		},
	)
}

// OnPriceChanged implements internal/stream.PriceChangeHandler.
func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil {
		return nil
	}
	s.startLoop(ctx)
	strategycommon.TrySignal(s.signalC)
	return nil
}

// OnOrderUpdate implements internal/ports.OrderUpdateHandler.
func (s *Strategy) OnOrderUpdate(ctx context.Context, o *domain.Order) error {
	_ = ctx
	_ = o
	// 模板：如果你的策略需要订单更新驱动状态机，可在这里 TrySignal
	return nil
}

func (s *Strategy) runLoop(ctx context.Context, tickC <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.signalC:
			// 合并信号：避免高频事件下堆积
			s.tryDoOnce(ctx)
		case <-tickC:
			// 标准 tick：定时驱动状态机
			s.tryDoOnce(ctx)
		}
	}
}

func (s *Strategy) tryDoOnce(ctx context.Context) {
	s.mu.RLock()
	cfg := s.config
	market := s.currentMarket
	ts := s.tradingService
	s.mu.RUnlock()

	if cfg == nil || !cfg.Enabled || market == nil || ts == nil {
		return
	}

	if s.inFlight != nil && s.inFlight.AtLimit() {
		return
	}

	assetID := market.YesAssetID // 示例：买 YES
	maxCents := 0
	if cfg.MaxBuySlippageCents > 0 {
		// 模板：通常这里需要用“观测价”来加滑点上限；这里只示例留空
		maxCents = 0
	}

	if s.Executor == nil {
		// 不建议：会阻塞 loop（但保持兼容）
		s.placeFAK(ctx, ts, market.Slug, assetID, cfg.OrderSize, maxCents)
		return
	}

	if s.inFlight != nil && !s.inFlight.TryAcquire() {
		return
	}

	ok := s.Executor.Submit(bbgo.Command{
		Name:    "template_place",
		Timeout: 25 * time.Second,
		Do: func(runCtx context.Context) {
			defer func() {
				if s.inFlight != nil {
					s.inFlight.Release()
				}
			}()
			s.placeFAK(runCtx, ts, market.Slug, assetID, cfg.OrderSize, maxCents)
		},
	})
	if !ok && s.inFlight != nil {
		s.inFlight.Release()
	}
}

func (s *Strategy) placeFAK(ctx context.Context, ts strategyports.BasicTradingService, marketSlug, assetID string, size float64, maxCents int) {
	price, err := orderutil.QuoteBuyPrice(ctx, ts, assetID, maxCents)
	if err != nil {
		return
	}
	order := orderutil.NewOrder(marketSlug, assetID, types.SideBuy, price, size, domain.TokenTypeUp, true, types.OrderTypeFAK)
	_, _ = ts.PlaceOrder(ctx, order)
}
