package pairlock

import (
	"context"
	"time"

	"github.com/betbot/gobet/internal/strategies/common"
)

func (s *PairLockStrategy) startLoop(ctx context.Context) {
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
					up := s.latestPrice[upKey]
					down := s.latestPrice[downKey]
					s.latestPrice = make(map[tokenKey]*priceEvent)
					s.priceMu.Unlock()

					if up != nil {
						_ = s.onPriceChangedInternal(loopCtx, up.ctx, up.event)
					}
					if down != nil {
						_ = s.onPriceChangedInternal(loopCtx, down.ctx, down.event)
					}

				case upd := <-s.orderC:
					if upd.order == nil {
						continue
					}
					_ = s.onOrderUpdateInternal(loopCtx, upd.ctx, upd.order)

				case res := <-s.cmdResultC:
					_ = s.onCmdResultInternal(loopCtx, res)

				case <-tickC:
					s.onTick(loopCtx)
				}
			}
		},
	)
}
