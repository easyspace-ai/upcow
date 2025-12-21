package datarecorder

import (
	"context"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/pkg/logger"
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
					
					// 验证 UP 和 DOWN 事件是否属于同一周期（防止混合不同周期的数据）
					if up != nil && down != nil && up.Market != nil && down.Market != nil {
						if up.Market.Slug != down.Market.Slug {
							logger.Warnf("数据记录策略: ⚠️ UP 和 DOWN 事件来自不同周期，跳过处理 - UP周期=%s, DOWN周期=%s",
								up.Market.Slug, down.Market.Slug)
							// 只处理属于当前周期的事件
							s.mu.RLock()
							currentSlug := ""
							if s.currentMarket != nil {
								currentSlug = s.currentMarket.Slug
							}
							s.mu.RUnlock()
							
							if up.Market.Slug == currentSlug {
								_ = s.onPriceChangedInternal(loopCtx, up)
							}
							if down.Market.Slug == currentSlug {
								_ = s.onPriceChangedInternal(loopCtx, down)
							}
							continue
						}
					}
					
					// 正常处理：UP 和 DOWN 属于同一周期，或只有一个事件
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
