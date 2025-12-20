package arbitrage

import (
	"context"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
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
	s.loopOnce.Do(func() {
		loopCtx, cancel := context.WithCancel(ctx)
		s.loopCancel = cancel
		go func() {
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
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
					if up != nil {
						_ = s.onPriceChangedInternal(loopCtx, up)
					}
					if down != nil {
						_ = s.onPriceChangedInternal(loopCtx, down)
					}
				case o := <-s.orderC:
					if o == nil {
						continue
					}
					_ = s.handleOrderUpdateInternal(loopCtx, o)
				case res := <-s.cmdResultC:
					_ = s.handleCmdResultInternal(loopCtx, res)
				case <-ticker.C:
				}
			}
		}()
	})
}

func (s *ArbitrageStrategy) stopLoop() {
	if s.loopCancel != nil {
		s.loopCancel()
	}
}

