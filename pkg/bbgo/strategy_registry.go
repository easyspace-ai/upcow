package bbgo

import (
	"fmt"
	"sync"
)

var (
	// LoadedStrategies 已注册的策略类型映射（使用 interface{} 避免循环依赖）
	LoadedStrategies = make(map[string]interface{})
	loadedStrategiesMu sync.RWMutex
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
