package bbgo

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/pkg/persistence"
	"github.com/betbot/gobet/pkg/shutdown"
)

var traderLog = logrus.WithField("component", "trader")

// StrategyID 策略ID接口（BBGO风格）
// 所有策略必须实现此接口
type StrategyID interface {
	ID() string
}

// SingleExchangeStrategy 单交易所策略接口（BBGO风格）
// 这是 BBGO 的核心策略接口，所有单交易所策略必须实现
type SingleExchangeStrategy interface {
	StrategyID
	Run(ctx context.Context, orderExecutor OrderExecutor, session *ExchangeSession) error
}

// StrategyInitializer 策略初始化接口（BBGO风格，可选）
// 在 Subscribe 之前调用，用于初始化策略
type StrategyInitializer interface {
	Initialize() error
}

// StrategyDefaulter 策略默认值接口（BBGO风格，可选）
// 在 Initialize 之前调用，用于设置默认值
type StrategyDefaulter interface {
	Defaults() error
}

// StrategyValidator 策略验证接口（BBGO风格，可选）
// 在 Initialize 之后调用，用于验证配置
type StrategyValidator interface {
	Validate() error
}

// StrategyShutdown 策略关闭接口（BBGO风格，可选）
// 在系统关闭时调用，用于优雅关闭
type StrategyShutdown interface {
	Shutdown(ctx context.Context, wg *sync.WaitGroup)
}

// ExchangeSessionSubscriber 交易所会话订阅接口（BBGO风格，可选）
// Subscribe 方法在连接建立前被调用，用于注册回调
type ExchangeSessionSubscriber interface {
	Subscribe(session *ExchangeSession)
}

// Trader 策略管理器，管理策略生命周期
type Trader struct {
	environment *Environment

	// 策略列表（使用 interface{} 避免循环依赖）
	strategies   []interface{}
	strategiesMu sync.RWMutex

	// 关闭管理器
	shutdownManager *shutdown.Manager

	// 运行期：用于周期切换时取消并重启策略 Run
	runMu           sync.Mutex
	strategyCancels map[string]context.CancelFunc // strategyID -> cancel
	activeSession   *ExchangeSession

	// 避免周期切换时重复注册 shutdown hook
	shutdownOnceMu        sync.Mutex
	shutdownRegisteredIDs map[string]bool
}

// NewTrader 创建新的策略管理器
func NewTrader(environ *Environment) *Trader {
	return &Trader{
		environment:           environ,
		strategies:            make([]interface{}, 0),
		shutdownManager:       environ.ShutdownManager(),
		strategyCancels:       make(map[string]context.CancelFunc),
		shutdownRegisteredIDs: make(map[string]bool),
	}
}

// AddStrategy 添加策略（使用 interface{} 避免循环依赖）
func (t *Trader) AddStrategy(strategy interface{}) {
	t.strategiesMu.Lock()
	defer t.strategiesMu.Unlock()
	t.strategies = append(t.strategies, strategy)
}

// Strategies 获取所有策略（返回 interface{} 切片避免循环依赖）
func (t *Trader) Strategies() []interface{} {
	t.strategiesMu.RLock()
	defer t.strategiesMu.RUnlock()

	result := make([]interface{}, len(t.strategies))
	copy(result, t.strategies)
	return result
}

// Initialize 初始化所有策略
// 调用策略的 Defaults、Validate 和 Initialize 方法
func (t *Trader) Initialize(ctx context.Context) error {
	t.strategiesMu.RLock()
	strategies := t.strategies
	t.strategiesMu.RUnlock()

	for _, s := range strategies {
		var strategyID string
		if sid, ok := s.(StrategyID); ok {
			strategyID = sid.ID()
		} else {
			// 兼容旧代码，尝试使用 Name() 方法
			if nameStrategy, ok := s.(interface{ Name() string }); ok {
				strategyID = nameStrategy.Name()
			} else {
				strategyID = "unknown"
			}
		}

		// 设置默认值
		if defaulter, ok := s.(StrategyDefaulter); ok {
			if err := defaulter.Defaults(); err != nil {
				return fmt.Errorf("strategy %s defaults error: %w", strategyID, err)
			}
		}

		// 验证配置
		if validator, ok := s.(StrategyValidator); ok {
			if err := validator.Validate(); err != nil {
				return fmt.Errorf("strategy %s validation error: %w", strategyID, err)
			}
		}

		// 初始化策略
		if initializer, ok := s.(StrategyInitializer); ok {
			if err := initializer.Initialize(); err != nil {
				return fmt.Errorf("strategy %s initialization error: %w", strategyID, err)
			}
		}
	}

	return nil
}

// InjectServices 注入服务到策略
func (t *Trader) InjectServices(ctx context.Context) error {
	t.strategiesMu.RLock()
	strategies := t.strategies
	t.strategiesMu.RUnlock()

	for _, s := range strategies {
		if err := t.injectServicesIntoStrategy(ctx, s); err != nil {
			strategyID := "unknown"
			if sid, ok := s.(StrategyID); ok {
				strategyID = sid.ID()
			} else if nameStrategy, ok := s.(interface{ Name() string }); ok {
				strategyID = nameStrategy.Name()
			}
			return fmt.Errorf("failed to inject services into strategy %s: %w", strategyID, err)
		}
	}

	return nil
}

// injectServicesIntoStrategy 注入服务到单个策略（使用 interface{} 避免循环依赖）
func (t *Trader) injectServicesIntoStrategy(ctx context.Context, strategy interface{}) error {
	strategyValue := reflect.ValueOf(strategy)
	if strategyValue.Kind() == reflect.Ptr {
		strategyValue = strategyValue.Elem()
	}

	if strategyValue.Kind() != reflect.Struct {
		return fmt.Errorf("strategy must be a struct or pointer to struct")
	}

	strategyID := "unknown"
	if sid, ok := strategy.(StrategyID); ok {
		strategyID = sid.ID()
	} else if nameStrategy, ok := strategy.(interface{ Name() string }); ok {
		strategyID = nameStrategy.Name()
	}

	// 注入 TradingService
	if t.environment.TradingService != nil {
		if err := t.injectField(strategy, "TradingService", t.environment.TradingService); err != nil {
			traderLog.Debugf("failed to inject TradingService into %s: %v", strategyID, err)
		}
	}

	// 注入 MarketDataService
	if t.environment.MarketDataService != nil {
		if err := t.injectField(strategy, "MarketDataService", t.environment.MarketDataService); err != nil {
			traderLog.Debugf("failed to inject MarketDataService into %s: %v", strategyID, err)
		}
	}

	// 注入系统级配置（直接回调模式防抖间隔，BBGO风格：只支持直接模式）
	if err := t.injectField(strategy, "directModeDebounce", t.environment.DirectModeDebounce); err != nil {
		traderLog.Debugf("failed to inject directModeDebounce into %s: %v", strategyID, err)
	}

	// 注入全局命令执行器（串行 IO）
	if t.environment != nil {
		exec := t.environment.Executor
		// 允许策略声明并发模式：注入并发执行器（如果配置了）
		if mp, ok := strategy.(ExecutionModeProvider); ok && mp.ExecutionMode() == ExecutionModeConcurrent {
			if t.environment.ConcurrentExecutor != nil {
				exec = t.environment.ConcurrentExecutor
			} else {
				traderLog.Warnf("⚠️ strategy %s 需要并发执行器，但 Environment.ConcurrentExecutor 未配置，回退到串行执行器", strategyID)
			}
		}
		if exec != nil {
			if err := t.injectField(strategy, "Executor", exec); err != nil {
				traderLog.Debugf("failed to inject Executor into %s: %v", strategyID, err)
			}
		}
	}

	return nil
}

// injectField 注入字段
func (t *Trader) injectField(obj interface{}, fieldName string, value interface{}) error {
	objValue := reflect.ValueOf(obj)
	if objValue.Kind() == reflect.Ptr {
		objValue = objValue.Elem()
	}

	field := objValue.FieldByName(fieldName)
	if !field.IsValid() {
		return fmt.Errorf("field %s not found", fieldName)
	}

	if !field.CanSet() {
		return fmt.Errorf("field %s cannot be set", fieldName)
	}

	valueValue := reflect.ValueOf(value)
	if field.Type() != valueValue.Type() {
		// 尝试接口匹配
		if field.Kind() == reflect.Interface {
			if valueValue.Type().Implements(field.Type()) {
				field.Set(valueValue)
				return nil
			}
		}
		return fmt.Errorf("type mismatch: field %s is %s, value is %s", fieldName, field.Type(), valueValue.Type())
	}

	field.Set(valueValue)
	return nil
}

// Subscribe 让策略订阅会话事件
func (t *Trader) Subscribe(ctx context.Context, session *ExchangeSession) error {
	t.strategiesMu.RLock()
	strategies := t.strategies
	t.strategiesMu.RUnlock()

	for _, s := range strategies {
		if subscriber, ok := s.(ExchangeSessionSubscriber); ok {
			strategyID := "unknown"
			if sid, ok := s.(StrategyID); ok {
				strategyID = sid.ID()
			} else if nameStrategy, ok := s.(interface{ Name() string }); ok {
				strategyID = nameStrategy.Name()
			}
			traderLog.Infof("🔄 [周期切换] 准备调用策略 %s 的 Subscribe 方法", strategyID)

			// 使用 defer recover 确保即使 Subscribe 出错也能继续
			var subscribeErr error
			func() {
				defer func() {
					if r := recover(); r != nil {
						traderLog.Errorf("❌ [周期切换] 策略 %s 的 Subscribe 方法 panic: %v", strategyID, r)
						subscribeErr = fmt.Errorf("panic: %v", r)
					}
				}()
				subscriber.Subscribe(session)
				traderLog.Infof("✅ [周期切换] 策略 %s 的 Subscribe 方法执行完成", strategyID)
			}()

			if subscribeErr != nil {
				traderLog.Errorf("❌ [周期切换] 策略 %s 订阅失败: %v", strategyID, subscribeErr)
			} else {
				traderLog.Infof("✅ [周期切换] 策略 %s 已订阅会话 %s", strategyID, session.Name)
			}
		} else {
			traderLog.Warnf("⚠️ 策略 %v 未实现 ExchangeSessionSubscriber 接口", s)
		}
	}

	return nil
}

type noopOrderExecutor struct{}

func (noopOrderExecutor) SubmitOrders(ctx context.Context, orders ...domain.Order) ([]*domain.Order, error) {
	_ = ctx
	_ = orders
	return nil, fmt.Errorf("no trading service: SubmitOrders is unavailable")
}

func (noopOrderExecutor) CancelOrders(ctx context.Context, orders ...*domain.Order) error {
	_ = ctx
	_ = orders
	return fmt.Errorf("no trading service: CancelOrders is unavailable")
}

func (t *Trader) makeOrderExecutor() OrderExecutor {
	if t.environment != nil && t.environment.TradingService != nil {
		return NewTradingServiceOrderExecutor(t.environment.TradingService)
	}
	traderLog.Warnf("⚠️ TradingService 不存在：策略将拿到 noop OrderExecutor（下单会报错）")
	return noopOrderExecutor{}
}

func (t *Trader) cancelAllRunsLocked() {
	for id, cancel := range t.strategyCancels {
		if cancel != nil {
			cancel()
		}
		delete(t.strategyCancels, id)
	}
	t.activeSession = nil
}

// StartWithSession 启动所有策略（每个策略单独 goroutine），并绑定到指定 session。
// 该方法会返回，不会阻塞主 goroutine；用于支持周期切换时重启策略 Run。
func (t *Trader) StartWithSession(ctx context.Context, session *ExchangeSession) error {
	t.runMu.Lock()
	defer t.runMu.Unlock()

	if session == nil {
		return fmt.Errorf("session is nil")
	}

	// 框架层周期钩子：首次启动也视作“进入一个新周期”
	t.invokeCycleHooks(ctx, nil, session)

	// 如果已经启动过，避免重复启动导致“同一策略多次 Run”
	if t.activeSession != nil {
		traderLog.Warnf("⚠️ Trader 已经启动过（session=%s），请使用 SwitchSession", t.activeSession.Name)
		return nil
	}

	// 订阅回调（价格/订单等）
	_ = t.Subscribe(ctx, session)

	orderExecutor := t.makeOrderExecutor()

	t.strategiesMu.RLock()
	strategies := t.strategies
	t.strategiesMu.RUnlock()

	for _, s := range strategies {
		// 注册关闭回调（保持原有语义）
		if shutdown, ok := s.(StrategyShutdown); ok && t.shutdownManager != nil {
			strategyID := "unknown"
			if sid, ok := s.(StrategyID); ok {
				strategyID = sid.ID()
			} else if nameStrategy, ok := s.(interface{ Name() string }); ok {
				strategyID = nameStrategy.Name()
			}

			t.shutdownOnceMu.Lock()
			already := t.shutdownRegisteredIDs[strategyID]
			if !already {
				t.shutdownRegisteredIDs[strategyID] = true
				t.shutdownManager.OnShutdown(func(ctx context.Context, wg *sync.WaitGroup) {
					shutdown.Shutdown(ctx, wg)
				})
			}
			t.shutdownOnceMu.Unlock()
		}

		single, ok := s.(SingleExchangeStrategy)
		if !ok {
			continue
		}

		strategyID := single.ID()
		runCtx, cancel := context.WithCancel(ctx)
		t.strategyCancels[strategyID] = cancel

		go func(st SingleExchangeStrategy, id string, runCtx context.Context) {
			if err := st.Run(runCtx, orderExecutor, session); err != nil && runCtx.Err() == nil {
				traderLog.Errorf("策略 %s Run 退出: %v", id, err)
			}
		}(single, strategyID, runCtx)

		traderLog.Infof("✅ 策略 %s 已启动（session=%s）", strategyID, session.Name)
	}

	t.activeSession = session
	traderLog.Infof("所有策略已启动，共 %d 个策略（session=%s）", len(strategies), session.Name)
	return nil
}

// SwitchSession 用于周期切换：取消上一周期所有策略 Run，并用新 session 重新 Subscribe+Run。
func (t *Trader) SwitchSession(ctx context.Context, session *ExchangeSession) error {
	t.runMu.Lock()
	defer t.runMu.Unlock()

	if session == nil {
		return fmt.Errorf("session is nil")
	}

	old := t.activeSession
	// 框架层周期钩子：在取消旧 Run 之前触发（让策略尽早清理旧周期状态）
	t.invokeCycleHooks(ctx, old, session)

	// 1) 先取消上一轮 Run（防止旧 market 状态继续运行）
	t.cancelAllRunsLocked()

	// 2) 订阅新 session 并重新 Run
	_ = t.Subscribe(ctx, session)
	orderExecutor := t.makeOrderExecutor()

	t.strategiesMu.RLock()
	strategies := t.strategies
	t.strategiesMu.RUnlock()

	for _, s := range strategies {
		if shutdown, ok := s.(StrategyShutdown); ok && t.shutdownManager != nil {
			strategyID := "unknown"
			if sid, ok := s.(StrategyID); ok {
				strategyID = sid.ID()
			} else if nameStrategy, ok := s.(interface{ Name() string }); ok {
				strategyID = nameStrategy.Name()
			}

			t.shutdownOnceMu.Lock()
			already := t.shutdownRegisteredIDs[strategyID]
			if !already {
				t.shutdownRegisteredIDs[strategyID] = true
				t.shutdownManager.OnShutdown(func(ctx context.Context, wg *sync.WaitGroup) {
					shutdown.Shutdown(ctx, wg)
				})
			}
			t.shutdownOnceMu.Unlock()
		}

		single, ok := s.(SingleExchangeStrategy)
		if !ok {
			continue
		}

		strategyID := single.ID()
		runCtx, cancel := context.WithCancel(ctx)
		t.strategyCancels[strategyID] = cancel

		go func(st SingleExchangeStrategy, id string, runCtx context.Context) {
			if err := st.Run(runCtx, orderExecutor, session); err != nil && runCtx.Err() == nil {
				traderLog.Errorf("策略 %s Run 退出: %v", id, err)
			}
		}(single, strategyID, runCtx)

		traderLog.Infof("🔄 [周期切换] 策略 %s 已切换到新 session=%s", strategyID, session.Name)
	}

	t.activeSession = session
	return nil
}

func (t *Trader) invokeCycleHooks(ctx context.Context, oldSession *ExchangeSession, newSession *ExchangeSession) {
	t.strategiesMu.RLock()
	strategies := t.strategies
	t.strategiesMu.RUnlock()

	var oldMarket *domain.Market
	if oldSession != nil {
		oldMarket = oldSession.Market()
	}
	var newMarket *domain.Market
	if newSession != nil {
		newMarket = newSession.Market()
	}

	for _, s := range strategies {
		ca, ok := s.(CycleAwareStrategy)
		if !ok {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					id := "unknown"
					if sid, ok := s.(StrategyID); ok {
						id = sid.ID()
					} else if ns, ok := s.(interface{ Name() string }); ok {
						id = ns.Name()
					}
					traderLog.Errorf("❌ [周期切换] strategy %s OnCycle panic: %v", id, r)
				}
			}()
			ca.OnCycle(ctx, oldMarket, newMarket)
		}()
	}
}

// Run 运行所有策略（BBGO风格）
func (t *Trader) Run(ctx context.Context) error {
	t.strategiesMu.RLock()
	strategies := t.strategies
	t.strategiesMu.RUnlock()

	// 创建订单执行器（如果有交易服务）
	var orderExecutor OrderExecutor
	if t.environment.TradingService != nil {
		orderExecutor = NewTradingServiceOrderExecutor(t.environment.TradingService)
	}

	// 获取会话（假设使用默认会话）
	var session *ExchangeSession
	if len(t.environment.sessions) > 0 {
		// 使用第一个会话
		for _, s := range t.environment.sessions {
			session = s
			break
		}
	}

	// 运行所有策略
	for _, s := range strategies {
		// 注册关闭回调
		if shutdown, ok := s.(StrategyShutdown); ok {
			t.shutdownManager.OnShutdown(func(ctx context.Context, wg *sync.WaitGroup) {
				// 注意：shutdown.Manager 已经在 goroutine 中处理了 wg.Done()
				// 策略的 Shutdown 方法不应该再调用 wg.Done()，除非它启动了新的 goroutine
				shutdown.Shutdown(ctx, wg)
			})
		}

		// 如果策略实现了 SingleExchangeStrategy，调用 Run 方法
		if singleStrategy, ok := s.(SingleExchangeStrategy); ok {
			if session == nil {
				traderLog.Warnf("策略 %s 需要会话，但未找到可用会话", singleStrategy.ID())
				continue
			}
			if orderExecutor == nil {
				traderLog.Warnf("策略 %s 需要订单执行器，但未找到交易服务", singleStrategy.ID())
				continue
			}
			if err := singleStrategy.Run(ctx, orderExecutor, session); err != nil {
				return fmt.Errorf("策略 %s 运行失败: %w", singleStrategy.ID(), err)
			}
			traderLog.Infof("策略 %s 已启动", singleStrategy.ID())
		}
	}

	traderLog.Infof("所有策略已启动，共 %d 个策略", len(strategies))
	return nil
}

// LoadState 加载策略状态
func (t *Trader) LoadState(ctx context.Context) error {
	if t.environment.PersistenceService == nil {
		return nil
	}

	t.strategiesMu.RLock()
	strategies := t.strategies
	t.strategiesMu.RUnlock()

	for _, s := range strategies {
		var id string
		if sid, ok := s.(StrategyID); ok {
			id = sid.ID()
		} else if nameStrategy, ok := s.(interface{ Name() string }); ok {
			id = nameStrategy.Name()
		} else {
			continue
		}
		if err := persistence.LoadFields(s, id, t.environment.PersistenceService); err != nil {
			traderLog.Warnf("加载策略 %s 状态失败: %v", id, err)
		}
	}

	return nil
}

// SaveState 保存策略状态
func (t *Trader) SaveState(ctx context.Context) error {
	if t.environment.PersistenceService == nil {
		return nil
	}

	t.strategiesMu.RLock()
	strategies := t.strategies
	t.strategiesMu.RUnlock()

	for _, s := range strategies {
		var id string
		if sid, ok := s.(StrategyID); ok {
			id = sid.ID()
		} else if nameStrategy, ok := s.(interface{ Name() string }); ok {
			id = nameStrategy.Name()
		} else {
			continue
		}
		if err := persistence.SaveFields(s, id, t.environment.PersistenceService); err != nil {
			traderLog.Warnf("保存策略 %s 状态失败: %v", id, err)
		}
	}

	return nil
}

// Shutdown 优雅关闭
func (t *Trader) Shutdown(ctx context.Context) {
	if t.shutdownManager != nil {
		t.shutdownManager.Shutdown(ctx)
	}
}
