package services

import (
	"context"
	"sync/atomic"
	"time"
)

// 说明：
// UserWebSocket 分发队列满时可能丢弃 order/trade 事件。量化系统不能静默失真；
// 因此这里提供“丢弃后的补偿对账”入口（严格、可审计、带节流）。

// dropCompensating 标记是否正在执行补偿对账
var dropCompensating atomic.Bool

// lastDropCompensateAtNs 用于节流（避免队列持续满导致频繁打 API）
var lastDropCompensateAtNs atomic.Int64

// CompensateAfterUserWSDrop 触发一次“丢弃补偿对账”（节流 + 去重）。
//
// 策略/运行时可在检测到 WS 队列丢弃时调用：
// - 强一致方式：对当前周期的活跃订单逐个调用 GetOrder（SyncOrderStatus），并在需要时合成 delta-trade 更新仓位
func (s *TradingService) CompensateAfterUserWSDrop(reason string) {
	if s == nil || s.syncer == nil {
		return
	}
	_ = reason

	// 节流：2 秒内最多触发一次
	now := time.Now().UnixNano()
	last := lastDropCompensateAtNs.Load()
	if last > 0 && now-last < int64(2*time.Second) {
		return
	}
	if !lastDropCompensateAtNs.CompareAndSwap(last, now) {
		return
	}

	// 去重：同一时间只允许一个补偿在跑
	if !dropCompensating.CompareAndSwap(false, true) {
		return
	}

	go func() {
		defer dropCompensating.Store(false)

		// 超时保护：避免网络问题导致挂死
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		currentMarket := s.GetCurrentMarket()
		orders := s.GetActiveOrders()
		if len(orders) == 0 {
			return
		}

		// 限制最大对账订单数：避免极端情况下打爆 API
		const maxOrders = 50
		n := 0
		for _, o := range orders {
			if o == nil || o.OrderID == "" {
				continue
			}
			// 只对账当前周期订单（严格）
			if currentMarket != "" {
				if o.MarketSlug == "" || o.MarketSlug != currentMarket {
					continue
				}
			}
			_ = s.SyncOrderStatus(ctx, o.OrderID)
			n++
			if n >= maxOrders {
				break
			}
			// 轻微间隔：尊重限流，避免瞬时 burst
			time.Sleep(40 * time.Millisecond)
		}
	}()
}

