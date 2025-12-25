package stream

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
)

var log = logrus.WithField("component", "stream")

// PriceChangeHandler 价格变化处理器接口
type PriceChangeHandler interface {
	OnPriceChanged(ctx context.Context, event *events.PriceChangedEvent) error
}

// MarketDataStream 市场数据流接口
type MarketDataStream interface {
	// OnPriceChanged 注册价格变化回调
	OnPriceChanged(handler PriceChangeHandler)

	// Connect 连接到市场数据流
	Connect(ctx context.Context, market *domain.Market) error

	// Close 关闭连接
	Close() error
}

// HandlerList 处理器列表（用于存储多个处理器）
type HandlerList struct {
	mu sync.Mutex // 写锁：Add/Remove/Clear

	// handlersV 存放“不可变切片”的快照，读路径无锁、无分配（RCU 风格）。
	// 约定：外部只读，不允许修改返回的 slice 内容。
	handlersV atomic.Value // []PriceChangeHandler
}

// NewHandlerList 创建新的处理器列表
func NewHandlerList() *HandlerList {
	h := &HandlerList{}
	h.handlersV.Store([]PriceChangeHandler{})
	return h
}

// Add 添加处理器
func (h *HandlerList) Add(handler PriceChangeHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()

	cur := h.snapshotUnsafe()
	next := make([]PriceChangeHandler, 0, len(cur)+1)
	next = append(next, cur...)
	next = append(next, handler)
	h.handlersV.Store(next)
}

// Snapshot 返回处理器快照（用于在无锁状态下遍历，避免长时间持锁）
func (h *HandlerList) Snapshot() []PriceChangeHandler {
	return h.snapshotUnsafe()
}

func (h *HandlerList) snapshotUnsafe() []PriceChangeHandler {
	if h == nil {
		return nil
	}
	v := h.handlersV.Load()
	if v == nil {
		return nil
	}
	if s, ok := v.([]PriceChangeHandler); ok {
		return s
	}
	return nil
}

// Emit 触发所有处理器
func (h *HandlerList) Emit(ctx context.Context, event *events.PriceChangedEvent) {
	handlers := h.Snapshot()
	if len(handlers) == 0 {
		return
	}

	// 串行执行（确定性优先，避免并发导致的状态竞态）
	for i, handler := range handlers {
		if handler == nil {
			continue
		}
		callPriceHandler(i, handler, ctx, event)
	}
}

func callPriceHandler(idx int, h PriceChangeHandler, ctx context.Context, event *events.PriceChangedEvent) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("价格变化处理器 %d panic: %v", idx, r)
		}
	}()
	if err := h.OnPriceChanged(ctx, event); err != nil {
		log.Errorf("价格变化处理器 %d 执行失败: %v", idx, err)
	}
}

// Count 返回处理器数量（用于调试）
func (h *HandlerList) Count() int {
	return len(h.Snapshot())
}

// Remove 移除处理器（通过比较指针地址）
func (h *HandlerList) Remove(handler PriceChangeHandler) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	cur := h.snapshotUnsafe()
	for i, hdl := range cur {
		if hdl == handler {
			// 移除第 i 个元素（写时复制）
			next := make([]PriceChangeHandler, 0, len(cur)-1)
			next = append(next, cur[:i]...)
			next = append(next, cur[i+1:]...)
			h.handlersV.Store(next)
			return true
		}
	}
	return false
}

// Clear 清空所有处理器
func (h *HandlerList) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handlersV.Store([]PriceChangeHandler{})
}
