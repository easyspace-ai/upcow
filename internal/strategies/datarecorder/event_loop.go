package datarecorder

import (
	"context"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/strategies/common"
)

func (s *DataRecorderStrategy) initLoopIfNeeded() {
	if s.priceSignalC == nil {
		s.priceSignalC = make(chan struct{}, 1)
	}
	if s.latestPrices == nil {
		s.latestPrices = make(map[domain.TokenType]*events.PriceChangedEvent)
	}
}

func (s *DataRecorderStrategy) startLoop(ctx context.Context) {
	s.initLoopIfNeeded()
	common.StartLoopOnce(
		ctx,
		&s.loopOnce,
		func(cancel context.CancelFunc) { s.loopCancel = cancel },
		1*time.Second,
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
					if up != nil {
						_ = s.onPriceChangedInternal(loopCtx, up)
					}
					if down != nil {
						_ = s.onPriceChangedInternal(loopCtx, down)
					}
				case <-tickC:
					// 周期检测：即使没有新的 market 事件，也能触发切换
					_ = s.cycleCheckTick(loopCtx)
				}
			}
		},
	)
}

func (s *DataRecorderStrategy) stopLoop() {
	if s.loopCancel != nil {
		s.loopCancel()
	}
}
