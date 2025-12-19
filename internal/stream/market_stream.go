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

// Emit 触发所有处理器
func (h *HandlerList) Emit(ctx context.Context, event *events.PriceChangedEvent) {
	h.mu.RLock()
	handlers := h.handlers
	handlerCount := len(handlers)
	h.mu.RUnlock()

	if handlerCount == 0 {
		log.Warnf("⚠️ [Emit] HandlerList 为空，没有处理器可触发！事件: %s @ %dc", 
			event.TokenType, event.NewPrice.Cents)
		return
	}

	log.Debugf("📤 [Emit] 触发 %d 个价格变化处理器: %s @ %dc", 
		handlerCount, event.TokenType, event.NewPrice.Cents)

	// 异步执行，避免阻塞
	for i, handler := range handlers {
		go func(idx int, h PriceChangeHandler) {
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

