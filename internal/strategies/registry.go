package strategies

import (
	"fmt"
	"sync"

	"github.com/betbot/gobet/pkg/bbgo"
)

// Strategy 策略接口（兼容旧代码，使用 bbgo.StrategyID）
type Strategy interface {
	bbgo.StrategyID
	Name() string // 兼容旧接口
}

// Registry 策略注册表（兼容旧代码，建议使用 bbgo.RegisterStrategy）
type Registry struct {
	strategies map[string]Strategy
	mu         sync.RWMutex
}

// NewRegistry 创建新的策略注册表
func NewRegistry() *Registry {
	return &Registry{
		strategies: make(map[string]Strategy),
	}
}

// Register 注册策略（兼容旧代码，建议使用 bbgo.RegisterStrategy）
func (r *Registry) Register(strategy Strategy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	name := strategy.Name()
	if _, exists := r.strategies[name]; exists {
		return fmt.Errorf("策略 %s 已存在", name)
	}
	
	r.strategies[name] = strategy
	return nil
}

// Get 获取策略
func (r *Registry) Get(name string) (Strategy, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	strategy, exists := r.strategies[name]
	if !exists {
		return nil, fmt.Errorf("策略 %s 不存在", name)
	}
	
	return strategy, nil
}

// List 列出所有策略名称
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	names := make([]string, 0, len(r.strategies))
	for name := range r.strategies {
		names = append(names, name)
	}
	
	return names
}

// GetAll 获取所有策略
func (r *Registry) GetAll() []Strategy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	strategies := make([]Strategy, 0, len(r.strategies))
	for _, strategy := range r.strategies {
		strategies = append(strategies, strategy)
	}
	
	return strategies
}

// GlobalRegistry 全局策略注册表
var GlobalRegistry = NewRegistry()

