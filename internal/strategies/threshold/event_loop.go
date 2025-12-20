package threshold

import (
	"context"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/strategies/common"
)

type thresholdCmdResult struct {
	order   *domain.Order
	created *domain.Order
	err     error
}

func (s *ThresholdStrategy) initLoopIfNeeded() {
	if s.priceSignalC == nil {
		s.priceSignalC = make(chan struct{}, 1)
	}
	if s.orderC == nil {
		s.orderC = make(chan *domain.Order, 256)
	}
	if s.cmdResultC == nil {
		s.cmdResultC = make(chan thresholdCmdResult, 256)
	}
}

func (s *ThresholdStrategy) startLoop(ctx context.Context) {
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
					ev := s.latestPrice
					s.latestPrice = nil
					s.priceMu.Unlock()
					if ev != nil {
						_ = s.onPriceChangedInternal(loopCtx, ev)
					}

				case order := <-s.orderC:
					if order == nil {
						continue
					}
					_ = s.handleOrderUpdateInternal(loopCtx, order)

				case res := <-s.cmdResultC:
					_ = s.handleCmdResultInternal(loopCtx, res)

				case <-tickC:
					// 预留：做一些周期性检查/监控
				}
			}
		},
	)
}

func (s *ThresholdStrategy) stopLoop() {
	if s.loopCancel != nil {
		s.loopCancel()
	}
}
