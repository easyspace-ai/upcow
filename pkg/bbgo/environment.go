package bbgo

import (
	"context"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/persistence"
	"github.com/betbot/gobet/pkg/shutdown"
)

// Environment 环境管理器，管理交易所会话和服务
type Environment struct {
	// 服务
	TradingService    *services.TradingService
	MarketDataService *services.MarketDataService
	PersistenceService persistence.Service
	Executor          CommandExecutor

	// 会话管理
	sessions map[string]*ExchangeSession
	sessionsMu sync.RWMutex

	// 关闭管理器
	shutdownManager *shutdown.Manager

	// 系统级配置
	DirectModeDebounce int    // 直接回调模式的防抖间隔（毫秒），默认100ms（BBGO风格：只支持直接模式）
}

// NewEnvironment 创建新的环境管理器
func NewEnvironment() *Environment {
	return &Environment{
		sessions:           make(map[string]*ExchangeSession),
		shutdownManager:    shutdown.NewManager(),
		DirectModeDebounce: 100,     // 默认100ms防抖（BBGO风格：只支持直接模式）
	}
}

// SetDirectModeDebounce 设置直接回调模式的防抖间隔（BBGO风格：只支持直接模式）
func (e *Environment) SetDirectModeDebounce(debounce int) {
	if debounce > 0 {
		e.DirectModeDebounce = debounce
	}
}

// SetTradingService 设置交易服务
func (e *Environment) SetTradingService(ts *services.TradingService) {
	e.TradingService = ts
}

// SetMarketDataService 设置市场数据服务
func (e *Environment) SetMarketDataService(mds *services.MarketDataService) {
	e.MarketDataService = mds
}

// SetPersistenceService 设置持久化服务
func (e *Environment) SetPersistenceService(ps persistence.Service) {
	e.PersistenceService = ps
}

// SetExecutor 设置全局命令执行器（用于串行执行网络/交易 IO）
func (e *Environment) SetExecutor(executor CommandExecutor) {
	e.Executor = executor
}

// AddSession 添加交易所会话
func (e *Environment) AddSession(name string, session *ExchangeSession) {
	e.sessionsMu.Lock()
	defer e.sessionsMu.Unlock()
	e.sessions[name] = session
}

// Session 获取交易所会话
func (e *Environment) Session(name string) (*ExchangeSession, bool) {
	e.sessionsMu.RLock()
	defer e.sessionsMu.RUnlock()
	session, ok := e.sessions[name]
	return session, ok
}

// Sessions 获取所有会话
func (e *Environment) Sessions() map[string]*ExchangeSession {
	e.sessionsMu.RLock()
	defer e.sessionsMu.RUnlock()
	
	result := make(map[string]*ExchangeSession)
	for k, v := range e.sessions {
		result[k] = v
	}
	return result
}

// ShutdownManager 获取关闭管理器
func (e *Environment) ShutdownManager() *shutdown.Manager {
	return e.shutdownManager
}

// Start 启动环境（准备数据，不进行网络交互）
func (e *Environment) Start(ctx context.Context) error {
	// 启动市场数据服务
	if e.MarketDataService != nil {
		e.MarketDataService.Start()
	}

	// 启动交易服务
	if e.TradingService != nil {
		if err := e.TradingService.Start(ctx); err != nil {
			return err
		}
	}

	// 启动命令执行器（如果配置）
	if e.Executor != nil {
		e.Executor.Start(ctx)
	}

	return nil
}

// Connect 连接到所有会话
func (e *Environment) Connect(ctx context.Context) error {
	e.sessionsMu.RLock()
	defer e.sessionsMu.RUnlock()

	for _, session := range e.sessions {
		if err := session.Connect(ctx); err != nil {
			return err
		}
	}

	return nil
}

// Close 关闭所有会话
func (e *Environment) Close() error {
	e.sessionsMu.RLock()
	defer e.sessionsMu.RUnlock()

	// 停止命令执行器（不阻塞关闭）
	if e.Executor != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = e.Executor.Stop(stopCtx)
		cancel()
	}

	for _, session := range e.sessions {
		if err := session.Close(); err != nil {
			// 记录错误但继续关闭其他会话
		}
	}

	return nil
}

