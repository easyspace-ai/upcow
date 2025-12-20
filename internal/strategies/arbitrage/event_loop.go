package arbitrage

import (
	"context"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/strategies/common"
)

type arbitrageCmdResult struct {
	tokenType domain.TokenType
	reason    string
	created   *domain.Order
	err       error
	skipped   bool
}

func (s *ArbitrageStrategy) initLoopIfNeeded() {
	if s.priceSignalC == nil {
		s.priceSignalC = make(chan struct{}, 1)
	}
	if s.latestPrices == nil {
		s.latestPrices = make(map[domain.TokenType]*events.PriceChangedEvent)
	}
	if s.orderC == nil {
		s.orderC = make(chan *domain.Order, 512)
	}
	if s.cmdResultC == nil {
		s.cmdResultC = make(chan arbitrageCmdResult, 512)
	}
}

func (s *ArbitrageStrategy) startLoop(ctx context.Context) {
	s.initLoopIfNeeded()
	common.StartLoopOnce(
		ctx,
		&s.loopOnce,
		func(cancel context.CancelFunc) { s.loopCancel = cancel },
		250*time.Millisecond,
		func(loopCtx context.Context, tickC <-chan time.Time) {
			for {
				select {
				case <-loopCtx.Done():
					return
				case <-s.priceSignalC:
					s.priceMu.Lock()
					up := s.latestPrices[domain.TokenTypeUp]
					down := s.latestPrices[domain.TokenTypeDown]
					s.latestPrices = make(map[domain.TokenType]*events.PriceChangedEvent)
					s.priceMu.Unlock()
					_ = s.onPricesChangedInternal(loopCtx, up, down)
				case o := <-s.orderC:
					if o == nil {
						continue
					}
					_ = s.handleOrderUpdateInternal(loopCtx, o)
				case res := <-s.cmdResultC:
					_ = s.handleCmdResultInternal(loopCtx, res)
				case <-tickC:
					// 保留 tick：未来可用于“缺价时补偿检查/周期末强制锁定”等逻辑
				}
			}
		},
	)
}

func (s *ArbitrageStrategy) stopLoop() {
	if s.loopCancel != nil {
		s.loopCancel()
	}
}
