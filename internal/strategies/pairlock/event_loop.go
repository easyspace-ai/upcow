package pairlock

import (
	"context"
	"time"
)

func (s *PairLockStrategy) startLoop(ctx context.Context) {
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

				case <-ticker.C:
					s.onTick(loopCtx)
				}
			}
		}()
	})
}

