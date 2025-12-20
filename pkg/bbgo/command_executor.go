package bbgo

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

var execLog = logrus.WithField("component", "command_executor")

// Command 表示一次需要串行执行的 IO/交易动作。
// 注意：Do 必须是幂等友好的（至少允许失败重试），且不得长时间阻塞不响应 ctx。
type Command struct {
	Name    string
	Timeout time.Duration
	Do      func(ctx context.Context)
}

// CommandExecutor 提供统一的命令队列/执行器接口。
// 目标：策略 loop 不直接做网络 IO，只投递命令并接收结果事件。
type CommandExecutor interface {
	Start(ctx context.Context)
	Stop(ctx context.Context) error
	Submit(cmd Command) bool
	QueueLen() int
}

// SerialCommandExecutor 单 worker 串行执行，保证确定性与限速。
type SerialCommandExecutor struct {
	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc

	ch   chan Command
	wg   sync.WaitGroup
	once sync.Once
}

func NewSerialCommandExecutor(buffer int) *SerialCommandExecutor {
	if buffer <= 0 {
		buffer = 1024
	}
	return &SerialCommandExecutor{
		ch: make(chan Command, buffer),
	}
}

func (e *SerialCommandExecutor) Start(ctx context.Context) {
	e.once.Do(func() {
		e.mu.Lock()
		e.ctx, e.cancel = context.WithCancel(ctx)
		e.mu.Unlock()

		e.wg.Add(1)
		go func() {
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
								execLog.Errorf("命令 panic: name=%s panic=%v", cmd.Name, r)
							}
						}()
						cmd.Do(runCtx)
					}()
				}
			}
		}()

		execLog.Infof("✅ CommandExecutor 已启动 (buffer=%d)", cap(e.ch))
	})
}

func (e *SerialCommandExecutor) Stop(ctx context.Context) error {
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
		execLog.Infof("✅ CommandExecutor 已停止")
		return nil
	case <-ctx.Done():
		return fmt.Errorf("停止 CommandExecutor 超时: %w", ctx.Err())
	}
}

func (e *SerialCommandExecutor) Submit(cmd Command) bool {
	select {
	case e.ch <- cmd:
		return true
	default:
		execLog.Warnf("⚠️ CommandExecutor 队列已满，丢弃命令: %s", cmd.Name)
		return false
	}
}

func (e *SerialCommandExecutor) QueueLen() int {
	return len(e.ch)
}

