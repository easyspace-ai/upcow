package updown

import (
	"context"
	"fmt"
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
	// BBGO风格：在init函数中注册策略及其配置适配器
	bbgo.RegisterStrategyWithAdapter(ID, &upDownStrategy{}, &ConfigAdapter{})
}

// Strategy is a standard single-exchange strategy implementation.
// It demonstrates:
// - typed tradingService ports
// - single goroutine loop via common.StartLoopOnce
// - non-blocking signals via common.TrySignal
// - basic in-flight limiting
type upDownStrategy struct {
	Executor bbgo.CommandExecutor

	mu             sync.RWMutex
	config         *Config
	tradingService strategyports.BasicTradingService
	currentMarket  *domain.Market
	marketGuard    strategycommon.MarketSlugGuard

	loopOnce   sync.Once
	loopCancel context.CancelFunc
	signalC    chan struct{}

	inFlight *strategycommon.InFlightLimiter
}

func (s *upDownStrategy) ID() string   { return ID }
func (s *upDownStrategy) Name() string { return ID }

func (s *upDownStrategy) Defaults() error { return nil }

func (s *upDownStrategy) Validate() error {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()
	if cfg == nil {
		return fmt.Errorf("config 未注入")
	}
	return cfg.Validate()
}

// InitializeWithConfig is used by StrategyLoader to inject the adapted config.
func (s *upDownStrategy) InitializeWithConfig(_ context.Context, cfg interface{}) error {
	c, ok := cfg.(*Config)
	if !ok {
		return fmt.Errorf("无效配置类型: %T", cfg)
	}
	s.mu.Lock()
	s.config = c
	if s.inFlight == nil {
		s.inFlight = strategycommon.NewInFlightLimiter(4)
	}
	s.mu.Unlock()
	return nil
}

func (s *upDownStrategy) SetTradingService(ts strategyports.BasicTradingService) {
	s.mu.Lock()
	s.tradingService = ts
	s.mu.Unlock()
}

func (s *upDownStrategy) Subscribe(session *bbgo.ExchangeSession) {
	// 标准订阅点：把 websocket callback 绑定到策略。
	session.OnOrderUpdate(s)
	session.OnPriceChanged(s)
}

func (s *upDownStrategy) Run(ctx context.Context, _ bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
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

func (s *upDownStrategy) Shutdown(ctx context.Context, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		if s.loopCancel != nil {
			s.loopCancel()
		}
		<-ctx.Done()
	}()
}

func (s *upDownStrategy) startLoop(ctx context.Context) {
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
func (s *upDownStrategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil {
		return nil
	}
	fmt.Println("====", e.OldPrice, e.NewPrice)
	s.startLoop(ctx)
	strategycommon.TrySignal(s.signalC)
	return nil
}

// OnOrderUpdate implements internal/ports.OrderUpdateHandler.
func (s *upDownStrategy) OnOrderUpdate(ctx context.Context, o *domain.Order) error {
	_ = ctx
	_ = o
	// 模板：如果你的策略需要订单更新驱动状态机，可在这里 TrySignal
	return nil
}

func (s *upDownStrategy) runLoop(ctx context.Context, tickC <-chan time.Time) {
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

func (s *upDownStrategy) tryDoOnce(ctx context.Context) {
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

func (s *upDownStrategy) placeFAK(ctx context.Context, ts strategyports.BasicTradingService, marketSlug, assetID string, size float64, maxCents int) {
	price, err := orderutil.QuoteBuyPrice(ctx, ts, assetID, maxCents)
	if err != nil {
		return
	}
	order := orderutil.NewOrder(marketSlug, assetID, types.SideBuy, price, size, domain.TokenTypeUp, true, types.OrderTypeFAK)
	_, _ = ts.PlaceOrder(ctx, order)
}
