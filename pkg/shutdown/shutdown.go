package shutdown

import (
	"context"
	"sync"

	"github.com/betbot/gobet/pkg/logger"
)

// Handler 关闭处理函数
type Handler func(ctx context.Context, wg *sync.WaitGroup)

// Manager 优雅关闭管理器
type Manager struct {
	callbacks []Handler
	mu        sync.Mutex
}

// NewManager 创建新的关闭管理器
func NewManager() *Manager {
	return &Manager{
		callbacks: make([]Handler, 0),
	}
}

// OnShutdown 注册关闭回调
func (m *Manager) OnShutdown(handler Handler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callbacks = append(m.callbacks, handler)
}

// Shutdown 执行所有关闭回调（阻塞调用）
// ctx 应该是一个带超时的 context，避免无限等待
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	callbacks := m.callbacks
	m.mu.Unlock()

	if len(callbacks) == 0 {
		logger.Info("没有注册的关闭回调")
		return
	}

	logger.Infof("开始优雅关闭，共 %d 个回调", len(callbacks))

	var wg sync.WaitGroup
	wg.Add(len(callbacks))

	// 并发执行所有关闭回调
	for _, cb := range callbacks {
		go func(handler Handler) {
			defer wg.Done()
			handler(ctx, &wg)
		}(cb)
	}

	// 等待所有回调完成或超时
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Info("所有关闭回调已完成")
	case <-ctx.Done():
		logger.Warnf("关闭超时: %v", ctx.Err())
	}
}

