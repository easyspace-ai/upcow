package datarecorder

import (
	"context"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
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
				case <-ticker.C:
					// 周期检测：即使没有新的 market 事件，也能触发切换
					_ = s.cycleCheckTick(loopCtx)
				}
			}
		}()
	})
}

func (s *DataRecorderStrategy) stopLoop() {
	if s.loopCancel != nil {
		s.loopCancel()
	}
}

