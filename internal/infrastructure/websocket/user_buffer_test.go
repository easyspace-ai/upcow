package websocket

import (
	"context"
	"testing"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/ports"
)

type testOrderHandler struct {
	ch chan *domain.Order
}

func (h *testOrderHandler) OnOrderUpdate(_ context.Context, o *domain.Order) error {
	select {
	case h.ch <- o:
	default:
	}
	return nil
}

var _ ports.OrderUpdateHandler = (*testOrderHandler)(nil)

func TestUserWebSocket_BuffersOrderUpdatesUntilHandlerRegistered(t *testing.T) {
	u := NewUserWebSocket()
	u.startDispatchLoops()

	ctx := context.Background()
	order := &domain.Order{
		OrderID:    "order_1",
		Status:     domain.OrderStatusOpen,
		FilledSize: 0,
		MarketSlug: "m1",
	}

	// 先投递（此时没有 handler）：应进入 pending，而不是被“空 handlers”静默消费掉
	u.orderUpdateC <- orderUpdateJob{ctx: ctx, order: order}

	// 确认短时间内不会被处理（因为没有 handler）
	h := &testOrderHandler{ch: make(chan *domain.Order, 1)}
	select {
	case <-h.ch:
		t.Fatalf("unexpected order delivery before handler registration")
	case <-time.After(50 * time.Millisecond):
	}

	// 注册 handler：应触发 flush，并最终收到该订单更新
	u.OnOrderUpdate(h)
	select {
	case got := <-h.ch:
		if got == nil || got.OrderID != "order_1" {
			t.Fatalf("unexpected order: %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected buffered order update to be flushed after handler registration")
	}
}
