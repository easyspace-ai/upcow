package bbgo

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
)

type testPriceHandler struct {
	mu     sync.Mutex
	events []*events.PriceChangedEvent
	ch     chan struct{}
}

func (h *testPriceHandler) OnPriceChanged(_ context.Context, e *events.PriceChangedEvent) error {
	h.mu.Lock()
	h.events = append(h.events, e)
	h.mu.Unlock()
	select {
	case h.ch <- struct{}{}:
	default:
	}
	return nil
}

func (h *testPriceHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
}

func TestExchangeSession_PriceBufferedUntilFirstHandler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := NewExchangeSession("test")
	// 启动价格 loop（不需要 MarketStream）
	if err := s.Connect(ctx); err != nil {
		t.Fatalf("Connect() error: %v", err)
	}

	// 先发出一笔价格事件（模拟：WS 已连上但策略还没 Subscribe）
	s.EmitPriceChanged(ctx, &events.PriceChangedEvent{
		Market:    &domain.Market{Slug: "m1", Timestamp: 1},
		TokenType: domain.TokenTypeUp,
		NewPrice:  domain.Price{Cents: 46},
		Timestamp: time.Now(),
	})

	// 注册 handler 后，应能立刻收到“缓存的最新价”（无需等待下一笔行情）
	h := &testPriceHandler{ch: make(chan struct{}, 1)}
	s.OnPriceChanged(h)

	select {
	case <-h.ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected handler to receive buffered price event after registration")
	}

	if got := h.count(); got != 1 {
		t.Fatalf("expected 1 event, got %d", got)
	}
}
