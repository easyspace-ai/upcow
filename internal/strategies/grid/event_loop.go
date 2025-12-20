package grid

import (
	"context"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
)

// startLoop 启动策略内部单线程事件循环（只启动一次）
// 目标：策略状态只在一个 goroutine 中变更，避免并发竞态与过度加锁。
func (s *GridStrategy) startLoop(ctx context.Context) {
	s.loopOnce.Do(func() {
		log.Infof("🔄 [GridLoop] 正在启动策略事件循环...")
		loopCtx, cancel := context.WithCancel(ctx)
		s.loopCancel = cancel

		go func() {
			log.Infof("✅ [GridLoop] 策略事件循环已启动")
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
			
			defer func() {
				log.Infof("🛑 [GridLoop] 策略事件循环已退出")
			}()

			for {
				select {
				case <-loopCtx.Done():
					log.Warnf("🛑 [GridLoop] Context已取消，正在退出循环: %v", loopCtx.Err())
					return

				case <-s.priceSignalC:
					// 合并：每次只处理最新 UP/DOWN
					s.priceMu.Lock()
					up := s.latestPrice[domain.TokenTypeUp]
					down := s.latestPrice[domain.TokenTypeDown]
					// 清空，继续接收下一批
					s.latestPrice = make(map[domain.TokenType]*events.PriceChangedEvent)
					s.priceMu.Unlock()

					// 串行处理（确定性优先）
					if up != nil {
						_ = s.onPriceChangedInternal(loopCtx, up)
					}
					if down != nil {
						_ = s.onPriceChangedInternal(loopCtx, down)
					}

				case upd := <-s.orderC:
					if upd.order == nil {
						continue
					}
					// 先让 plan 吸收订单更新（用来驱动状态机）
					s.planOnOrderUpdate(loopCtx, upd.order)
					_ = s.handleOrderUpdateInternal(loopCtx, upd.ctx, upd.order)

				case res := <-s.cmdResultC:
					_ = s.handleCmdResultInternal(loopCtx, res)

				case <-ticker.C:
					// 周期性 tick：HedgePlan 超时/自愈、重试、周期末强对冲窗口等
					s.planTick(loopCtx)
				}
			}
		}()
	})
}

