package pairedtrading

import (
	"context"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
)

// startLoop 启动策略内部单线程事件循环（只启动一次）
func (s *PairedTradingStrategy) startLoop(ctx context.Context) {
	s.loopOnce.Do(func() {
		loopCtx, cancel := context.WithCancel(ctx)
		s.loopCancel = cancel
		s.initLoopIfNeeded()
		go s.runLoop(loopCtx)
	})
}

// stopLoop 停止事件循环
func (s *PairedTradingStrategy) stopLoop() {
	if s.loopCancel != nil {
		s.loopCancel()
	}
}

// initLoopIfNeeded 初始化循环所需的 channel
func (s *PairedTradingStrategy) initLoopIfNeeded() {
	if s.priceSignalC == nil {
		s.priceSignalC = make(chan struct{}, 1)
	}
	if s.orderC == nil {
		s.orderC = make(chan *domain.Order, 4096)
	}
	if s.cmdResultC == nil {
		s.cmdResultC = make(chan pairedTradingCmdResult, 4096)
	}
	if s.latestPrices == nil {
		s.latestPrices = make(map[domain.TokenType]*events.PriceChangedEvent)
	}
}

// runLoop 单线程事件循环
func (s *PairedTradingStrategy) runLoop(ctx context.Context) {
	log.Infof("成对交易策略: 事件循环已启动")
	defer log.Infof("成对交易策略: 事件循环已停止")

	ticker := time.NewTicker(100 * time.Millisecond) // 定期检查状态
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-s.priceSignalC:
			// 价格变化信号
			s.priceMu.Lock()
			upEvent := s.latestPrices[domain.TokenTypeUp]
			downEvent := s.latestPrices[domain.TokenTypeDown]
			s.priceMu.Unlock()

			if err := s.onPricesChangedInternal(ctx, upEvent, downEvent); err != nil {
				log.Errorf("成对交易策略: 处理价格变化失败: %v", err)
			}

		case order := <-s.orderC:
			// 订单更新
			if err := s.onOrderUpdateInternal(ctx, order); err != nil {
				log.Errorf("成对交易策略: 处理订单更新失败: %v", err)
			}

		case result := <-s.cmdResultC:
			// 命令执行结果
			if s.inFlight > 0 {
				s.inFlight--
			}

			if result.err != nil {
				log.Errorf("成对交易策略: 命令执行失败 [%s/%s]: %v", result.tokenType, result.reason, result.err)
			} else if result.skipped {
				log.Debugf("成对交易策略: 订单跳过 [%s/%s]: 金额不足", result.tokenType, result.reason)
			} else if result.created != nil {
				log.Infof("成对交易策略: 订单已创建 [%s/%s]: ID=%s, 数量=%.2f, 价格=%.4f",
					result.tokenType, result.reason, result.created.OrderID,
					result.created.Size, result.created.Price.ToDecimal())
			}

		case <-ticker.C:
			// 定期检查（可用于日志输出等）
			// 暂时不需要额外处理
		}
	}
}
