package stream

import (
	"context"
	"sync"

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
	handlers []PriceChangeHandler
	mu       sync.RWMutex
}

// NewHandlerList 创建新的处理器列表
func NewHandlerList() *HandlerList {
	return &HandlerList{
		handlers: make([]PriceChangeHandler, 0),
	}
}

// Add 添加处理器
func (h *HandlerList) Add(handler PriceChangeHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handlers = append(h.handlers, handler)
}

// Snapshot 返回处理器快照（用于在无锁状态下遍历，避免长时间持锁）
func (h *HandlerList) Snapshot() []PriceChangeHandler {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]PriceChangeHandler, len(h.handlers))
	copy(out, h.handlers)
	return out
}

// Emit 触发所有处理器
func (h *HandlerList) Emit(ctx context.Context, event *events.PriceChangedEvent) {
	handlers := h.Snapshot()
	handlerCount := len(handlers)

	if handlerCount == 0 {
		log.Warnf("⚠️ [Emit] HandlerList 为空，没有处理器可触发！事件: %s @ %.4f", 
			event.TokenType, event.NewPrice.ToDecimal())
		return
	}

	log.Debugf("📤 [Emit] 触发 %d 个价格变化处理器: %s @ %.4f", 
		handlerCount, event.TokenType, event.NewPrice.ToDecimal())

	// 串行执行（确定性优先，避免并发导致的状态竞态）
	for i, handler := range handlers {
		if handler == nil {
			continue
		}
		func(idx int, h PriceChangeHandler) {
			defer func() {
				if r := recover(); r != nil {
					log.Errorf("价格变化处理器 %d panic: %v", idx, r)
				}
			}()
			if err := h.OnPriceChanged(ctx, event); err != nil {
				log.Errorf("价格变化处理器 %d 执行失败: %v", idx, err)
			} else {
				log.Debugf("✅ [Emit] 处理器 %d 执行成功", idx)
			}
		}(i, handler)
	}
}

// Count 返回处理器数量（用于调试）
func (h *HandlerList) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.handlers)
}

// Remove 移除处理器（通过比较指针地址）
func (h *HandlerList) Remove(handler PriceChangeHandler) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, hdl := range h.handlers {
		if hdl == handler {
			// 移除第 i 个元素
			h.handlers = append(h.handlers[:i], h.handlers[i+1:]...)
			return true
		}
	}
	return false
}

// Clear 清空所有处理器
func (h *HandlerList) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handlers = make([]PriceChangeHandler, 0)
}

