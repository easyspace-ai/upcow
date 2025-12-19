package orderbook

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/pkg/logger"
	"github.com/betbot/gobet/pkg/sigchan"
)

const (
	DefaultCancelOrderWaitTime = 50 * time.Millisecond
	DefaultOrderCancelTimeout  = 15 * time.Second
)

// ActiveOrderBook 管理本地活跃订单簿
type ActiveOrderBook struct {
	Symbol string

	orders              map[string]*domain.Order // 订单 ID -> 订单
	pendingOrderUpdates map[string]*domain.Order // 待处理的订单更新
	mu                  sync.RWMutex

	// 回调
	newCallbacks      []func(order *domain.Order)
	filledCallbacks   []func(order *domain.Order)
	canceledCallbacks []func(order *domain.Order)

	// 信号 channel
	C *sigchan.Chan

	// 取消订单配置
	cancelOrderWaitTime time.Duration
	cancelOrderTimeout  time.Duration
}

// NewActiveOrderBook 创建新的活跃订单簿
func NewActiveOrderBook(symbol string) *ActiveOrderBook {
	return &ActiveOrderBook{
		Symbol:              symbol,
		orders:              make(map[string]*domain.Order),
		pendingOrderUpdates: make(map[string]*domain.Order),
		C:                   sigchan.New(1),
		cancelOrderWaitTime: DefaultCancelOrderWaitTime,
		cancelOrderTimeout:  DefaultOrderCancelTimeout,
	}
}

// Add 添加订单
func (b *ActiveOrderBook) Add(order *domain.Order) {
	b.mu.Lock()

	// 检查是否有待处理的更新
	if pendingOrder, ok := b.pendingOrderUpdates[order.OrderID]; ok {
		if isNewerOrderUpdate(order, pendingOrder) {
			order = pendingOrder
		}
		delete(b.pendingOrderUpdates, order.OrderID)
	}

	b.orders[order.OrderID] = order
	b.mu.Unlock()

	// 触发回调（在锁外执行，避免死锁）
	for _, cb := range b.newCallbacks {
		cb(order)
	}
	b.C.Emit()
}

// Update 更新订单
func (b *ActiveOrderBook) Update(order *domain.Order) {
	b.mu.Lock()

	// 检查订单是否存在
	if _, exists := b.orders[order.OrderID]; !exists {
		// 如果订单不存在，保存为待处理更新
		b.pendingOrderUpdates[order.OrderID] = order
		b.mu.Unlock()
		return
	}

	// 更新订单
	b.orders[order.OrderID] = order

	// 根据状态处理
	switch order.Status {
	case domain.OrderStatusFilled:
		delete(b.orders, order.OrderID)
		b.mu.Unlock()
		// 触发回调（在锁外执行，避免死锁）
		for _, cb := range b.filledCallbacks {
			cb(order)
		}
		b.C.Emit()

	case domain.OrderStatusCanceled:
		delete(b.orders, order.OrderID)
		b.mu.Unlock()
		// 触发回调（在锁外执行，避免死锁）
		for _, cb := range b.canceledCallbacks {
			cb(order)
		}
		b.C.Emit()

	default:
		b.mu.Unlock()
		b.C.Emit()
	}
}

// Remove 移除订单
func (b *ActiveOrderBook) Remove(orderID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, exists := b.orders[orderID]; exists {
		delete(b.orders, orderID)
		return true
	}
	return false
}

// Get 获取订单
func (b *ActiveOrderBook) Get(orderID string) (*domain.Order, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	order, ok := b.orders[orderID]
	return order, ok
}

// Exists 检查订单是否存在
func (b *ActiveOrderBook) Exists(orderID string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, exists := b.orders[orderID]
	return exists
}

// NumOfOrders 获取订单数量
func (b *ActiveOrderBook) NumOfOrders() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.orders)
}

// Orders 获取所有订单
func (b *ActiveOrderBook) Orders() []*domain.Order {
	b.mu.RLock()
	defer b.mu.RUnlock()

	orders := make([]*domain.Order, 0, len(b.orders))
	for _, order := range b.orders {
		orders = append(orders, order)
	}
	return orders
}

// OnNew 注册新订单回调
func (b *ActiveOrderBook) OnNew(cb func(order *domain.Order)) {
	b.newCallbacks = append(b.newCallbacks, cb)
}

// OnFilled 注册订单成交回调
func (b *ActiveOrderBook) OnFilled(cb func(order *domain.Order)) {
	b.filledCallbacks = append(b.filledCallbacks, cb)
}

// OnCanceled 注册订单取消回调
func (b *ActiveOrderBook) OnCanceled(cb func(order *domain.Order)) {
	b.canceledCallbacks = append(b.canceledCallbacks, cb)
}

// GracefulCancel 优雅取消所有订单
func (b *ActiveOrderBook) GracefulCancel(ctx context.Context, cancelFunc func(ctx context.Context, orderID string) error) error {
	orders := b.Orders()

	// 批量取消
	for _, order := range orders {
		if err := cancelFunc(ctx, order.OrderID); err != nil {
			logger.Warnf("取消订单 %s 失败: %v", order.OrderID, err)
		}
	}

	// 等待订单清除
	return b.waitOrderClear(ctx)
}

// waitOrderClear 等待订单清除
func (b *ActiveOrderBook) waitOrderClear(ctx context.Context) error {
	ticker := time.NewTicker(b.cancelOrderWaitTime)
	defer ticker.Stop()

	timeout := time.NewTimer(b.cancelOrderTimeout)
	defer timeout.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return fmt.Errorf("order cancel timeout")
		case <-ticker.C:
			if b.NumOfOrders() == 0 {
				return nil
			}
		case <-b.C.C():
			if b.NumOfOrders() == 0 {
				return nil
			}
		}
	}
}

// isNewerOrderUpdate 检查订单 a 是否比订单 b 更新
func isNewerOrderUpdate(a, b *domain.Order) bool {
	// 简单的比较：如果 a 的创建时间更晚，则认为 a 更新
	return a.CreatedAt.After(b.CreatedAt)
}

