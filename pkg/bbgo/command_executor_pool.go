package bbgo

import (
	"context"
	"fmt"
	"sync"

	"github.com/sirupsen/logrus"
)

var poolExecLog = logrus.WithField("component", "command_executor_pool")

// WorkerPoolCommandExecutor 多 worker 并发执行命令（有界队列 + 固定 worker）。
// 适用：套利等允许并发、追求吞吐的策略。
type WorkerPoolCommandExecutor struct {
	workers int
	buffer  int

	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc

	ch   chan Command
	wg   sync.WaitGroup
	once sync.Once
}

func NewWorkerPoolCommandExecutor(buffer int, workers int) *WorkerPoolCommandExecutor {
	if buffer <= 0 {
		buffer = 1024
	}
	if workers <= 0 {
		workers = 8
	}
	return &WorkerPoolCommandExecutor{
		workers: workers,
		buffer:  buffer,
		ch:      make(chan Command, buffer),
	}
}

func (e *WorkerPoolCommandExecutor) Start(ctx context.Context) {
	e.once.Do(func() {
		e.mu.Lock()
		e.ctx, e.cancel = context.WithCancel(ctx)
		e.mu.Unlock()

		for i := 0; i < e.workers; i++ {
			e.wg.Add(1)
			go func(workerID int) {
				defer e.wg.Done()
				for {
					select {
					case <-e.ctx.Done():
						return
					case cmd := <-e.ch:
						if cmd.Do == nil {
							continue
						}
						runCtx := e.ctx
						cancel := func() {}
						if cmd.Timeout > 0 {
							runCtx, cancel = context.WithTimeout(e.ctx, cmd.Timeout)
						}
						func() {
							defer cancel()
							defer func() {
								if r := recover(); r != nil {
									poolExecLog.Errorf("命令 panic: worker=%d name=%s panic=%v", workerID, cmd.Name, r)
								}
							}()
							cmd.Do(runCtx)
						}()
					}
				}
			}(i)
		}

		poolExecLog.Infof("✅ WorkerPoolCommandExecutor 已启动 (workers=%d buffer=%d)", e.workers, cap(e.ch))
	})
}

func (e *WorkerPoolCommandExecutor) Stop(ctx context.Context) error {
	e.mu.Lock()
	if e.cancel != nil {
		e.cancel()
	}
	e.mu.Unlock()

	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		poolExecLog.Infof("✅ WorkerPoolCommandExecutor 已停止")
		return nil
	case <-ctx.Done():
		return fmt.Errorf("停止 WorkerPoolCommandExecutor 超时: %w", ctx.Err())
	}
}

func (e *WorkerPoolCommandExecutor) Submit(cmd Command) bool {
	select {
	case e.ch <- cmd:
		return true
	default:
		poolExecLog.Warnf("⚠️ WorkerPoolCommandExecutor 队列已满，丢弃命令: %s", cmd.Name)
		return false
	}
}

func (e *WorkerPoolCommandExecutor) QueueLen() int {
	return len(e.ch)
}
