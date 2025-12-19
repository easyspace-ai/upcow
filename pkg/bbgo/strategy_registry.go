package bbgo

import (
	"fmt"
	"sync"
)

// ConfigAdapter 配置适配器接口
// 每个策略可以实现此接口，从通用配置中提取自己的配置
type ConfigAdapter interface {
	// AdaptConfig 从通用配置适配为策略特定配置
	// strategyConfig: 通用策略配置（类型为 *config.StrategyConfig）
	// proxyConfig: 代理配置（类型为 *config.ProxyConfig，可选）
	AdaptConfig(strategyConfig interface{}, proxyConfig interface{}) (interface{}, error)
}

var (
	// LoadedStrategies 已注册的策略类型映射（使用 interface{} 避免循环依赖）
	LoadedStrategies = make(map[string]interface{})
	loadedStrategiesMu sync.RWMutex
	
	// ConfigAdapters 已注册的配置适配器映射
	ConfigAdapters = make(map[string]ConfigAdapter)
	configAdaptersMu sync.RWMutex
)

// RegisterStrategy 注册策略类型
// 策略应该在 init() 函数中调用此函数进行注册
func RegisterStrategy(id string, strategy interface{}) {
	loadedStrategiesMu.Lock()
	defer loadedStrategiesMu.Unlock()

	if _, exists := LoadedStrategies[id]; exists {
		panic(fmt.Errorf("strategy %s already registered", id))
	}

	LoadedStrategies[id] = strategy
}

// RegisterStrategyWithAdapter 注册策略及其配置适配器
// 这是推荐的注册方式，可以同时注册策略和配置适配器
func RegisterStrategyWithAdapter(id string, strategy interface{}, adapter ConfigAdapter) {
	RegisterStrategy(id, strategy)
	if adapter != nil {
		configAdaptersMu.Lock()
		defer configAdaptersMu.Unlock()
		
		if _, exists := ConfigAdapters[id]; exists {
			panic(fmt.Errorf("config adapter for strategy %s already registered", id))
		}
		
		ConfigAdapters[id] = adapter
	}
}

// GetRegisteredStrategy 获取已注册的策略类型（返回 interface{} 避免循环依赖）
func GetRegisteredStrategy(id string) (interface{}, error) {
	loadedStrategiesMu.RLock()
	defer loadedStrategiesMu.RUnlock()

	strategy, exists := LoadedStrategies[id]
	if !exists {
		return nil, fmt.Errorf("strategy %s not found", id)
	}

	return strategy, nil
}

// GetConfigAdapter 获取已注册的配置适配器
func GetConfigAdapter(id string) (ConfigAdapter, bool) {
	configAdaptersMu.RLock()
	defer configAdaptersMu.RUnlock()
	
	adapter, exists := ConfigAdapters[id]
	return adapter, exists
}

