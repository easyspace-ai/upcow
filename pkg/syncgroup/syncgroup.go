package syncgroup

import (
	"sync"
)

type syncGroupFunc func()

// SyncGroup 是 sync.WaitGroup 的包装器，简化 goroutine 生命周期管理
// 自动管理 Add() 和 Done()，减少遗漏 Done() 的风险
type SyncGroup struct {
	wg sync.WaitGroup

	sgFuncsMu   sync.Mutex
	sgFuncs     []syncGroupFunc
	hasRun      bool // 标记是否已经运行过
	runningCount int // 当前运行的 goroutine 数量
}

// NewSyncGroup 创建新的 SyncGroup
func NewSyncGroup() *SyncGroup {
	return &SyncGroup{}
}

// Add 添加一个 goroutine 函数
// 注意：Add() 应该在 Run() 之前调用，如果已经运行过，需要先调用 WaitAndClear()
func (w *SyncGroup) Add(fn syncGroupFunc) {
	if fn == nil {
		return
	}
	
	w.sgFuncsMu.Lock()
	defer w.sgFuncsMu.Unlock()

	// 如果已经运行过且还有 goroutine 在运行，不允许添加新的函数
	// 必须先调用 WaitAndClear() 等待所有 goroutine 完成
	if w.hasRun && w.runningCount > 0 {
		// 这种情况不应该发生，但为了安全，我们跳过
		// 注意：这可能导致函数没有被执行，但不会导致 WaitGroup 错误
		return
	}

	w.sgFuncs = append(w.sgFuncs, fn)
}

// Run 启动所有已添加的 goroutine
// 注意：Run() 应该在启动后清空函数列表，避免重复启动
// 如果已经运行过且还有 goroutine 在运行，会跳过本次调用
func (w *SyncGroup) Run() {
	w.sgFuncsMu.Lock()
	
	// 如果已经运行过且还有 goroutine 在运行，不允许再次运行
	// 必须先调用 WaitAndClear() 等待所有 goroutine 完成
	if w.hasRun && w.runningCount > 0 {
		w.sgFuncsMu.Unlock()
		return
	}
	
	fns := w.sgFuncs
	// 清空函数列表，避免重复启动
	w.sgFuncs = []syncGroupFunc{}
	w.hasRun = true
	w.runningCount = len(fns)
	w.sgFuncsMu.Unlock()

	// 为每个函数调用 wg.Add(1) 并启动 goroutine
	for _, fn := range fns {
		if fn == nil {
			continue
		}
		w.wg.Add(1)
		go func(doFunc syncGroupFunc) {
			defer func() {
				w.wg.Done()
				w.sgFuncsMu.Lock()
				w.runningCount--
				w.sgFuncsMu.Unlock()
			}()
			doFunc()
		}(fn)
	}
}

// WaitAndClear 等待所有 goroutine 完成并清空函数列表
func (w *SyncGroup) WaitAndClear() {
	w.wg.Wait()

	w.sgFuncsMu.Lock()
	w.sgFuncs = []syncGroupFunc{}
	w.hasRun = false
	w.runningCount = 0
	w.sgFuncsMu.Unlock()
}

// Wait 等待所有 goroutine 完成（不清空）
func (w *SyncGroup) Wait() {
	w.wg.Wait()
}

