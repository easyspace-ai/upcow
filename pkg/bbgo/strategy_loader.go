package bbgo

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/sirupsen/logrus"
)

var strategyLoaderLog = logrus.WithField("component", "strategy_loader")

// TradingServiceSetter 交易服务设置接口
// 用于统一设置交易服务到策略
type TradingServiceSetter interface {
	SetTradingService(service interface{})
}

// StrategyLoader 策略加载器
// 负责从配置加载策略实例，执行统一的初始化流程
type StrategyLoader struct {
	tradingService interface{} // 交易服务（使用 interface{} 避免循环依赖）
}

// NewStrategyLoader 创建新的策略加载器
func NewStrategyLoader(tradingService interface{}) *StrategyLoader {
	return &StrategyLoader{
		tradingService: tradingService,
	}
}

// LoadStrategy 加载单个策略
// strategyName: 策略名称（必须已注册）
// config: 策略配置（已适配好的配置对象）
func (l *StrategyLoader) LoadStrategy(ctx context.Context, strategyName string, config interface{}) (interface{}, error) {
	// 获取已注册的策略类型
	strategyType, err := GetRegisteredStrategy(strategyName)
	if err != nil {
		return nil, fmt.Errorf("策略 %s 未注册: %w", strategyName, err)
	}

	// 通过反射创建策略实例
	strategyInstance, err := l.createStrategyInstance(strategyType)
	if err != nil {
		return nil, fmt.Errorf("创建策略 %s 实例失败: %w", strategyName, err)
	}

	// 设置交易服务（在初始化之前设置，因为某些策略可能在初始化时需要交易服务）
	if l.tradingService != nil {
		if err := l.setTradingService(strategyInstance); err != nil {
			strategyLoaderLog.Warnf("设置策略 %s 的交易服务失败: %v", strategyName, err)
		}
	}

	// 仅做“配置注入/绑定”，不做 Defaults/Validate/Initialize() 的生命周期调用。
	// 生命周期由 Trader 统一管理，避免重复初始化与上下文错乱。
	if err := l.initializeStrategy(ctx, strategyInstance, config); err != nil {
		return nil, fmt.Errorf("初始化策略 %s 失败: %w", strategyName, err)
	}

	return strategyInstance, nil
}

// createStrategyInstance 通过反射创建策略实例
func (l *StrategyLoader) createStrategyInstance(strategyType interface{}) (interface{}, error) {
	// 获取策略类型的反射类型
	strategyTypeValue := reflect.TypeOf(strategyType)
	if strategyTypeValue.Kind() == reflect.Ptr {
		strategyTypeValue = strategyTypeValue.Elem()
	}

	// 创建策略实例
	strategyValue := reflect.New(strategyTypeValue)
	strategyInstance := strategyValue.Interface()

	return strategyInstance, nil
}


// setTradingService 设置交易服务
func (l *StrategyLoader) setTradingService(strategy interface{}) error {
	// 尝试使用 SetTradingService 方法
	if setter, ok := strategy.(TradingServiceSetter); ok {
		setter.SetTradingService(l.tradingService)
		return nil
	}

	// 尝试使用反射调用 SetTradingService 方法
	strategyValue := reflect.ValueOf(strategy)
	setMethod := strategyValue.MethodByName("SetTradingService")
	if setMethod.IsValid() {
		results := setMethod.Call([]reflect.Value{reflect.ValueOf(l.tradingService)})
		if len(results) > 0 && !results[0].IsNil() {
			if err, ok := results[0].Interface().(error); ok && err != nil {
				return err
			}
		}
		return nil
	}

	// 尝试直接设置 tradingService 字段
	if strategyValue.Kind() == reflect.Ptr {
		strategyValue = strategyValue.Elem()
	}
	tradingServiceField := strategyValue.FieldByName("tradingService")
	if tradingServiceField.IsValid() && tradingServiceField.CanSet() {
		serviceValue := reflect.ValueOf(l.tradingService)
		if serviceValue.Type().AssignableTo(tradingServiceField.Type()) {
			tradingServiceField.Set(serviceValue)
			return nil
		}
	}

	return fmt.Errorf("无法设置交易服务：策略不支持 SetTradingService 方法或 tradingService 字段")
}

// initializeStrategy 将“适配后的配置”绑定到策略实例。
// 这里只负责把 config 交给策略（通常是设置 s.config 字段），不负责调用 Defaults/Validate/Initialize()。
func (l *StrategyLoader) initializeStrategy(ctx context.Context, strategy interface{}, config interface{}) error {
	// bbgo main 风格：优先把“配置 map/结构”直接反序列化到策略实例上。
	// 这样新增策略只需要：
	// - 在策略 struct 上定义 yaml/json tag
	// - init() RegisterStrategy(ID, &Strategy{})
	// 不需要额外的 config adapter / InitializeWithConfig 注入链路。
	if config != nil {
		if b, err := json.Marshal(config); err == nil {
			if err := json.Unmarshal(b, strategy); err == nil {
				return nil
			}
		}
	}

	strategyValue := reflect.ValueOf(strategy)
	configValue := reflect.ValueOf(config)

	// 1) 优先尝试 Initialize(ctx, config)（部分策略使用此方法）
	initMethod := strategyValue.MethodByName("Initialize")
	if initMethod.IsValid() {
		methodType := initMethod.Type()
		if methodType.NumIn() == 2 {
			results := initMethod.Call([]reflect.Value{reflect.ValueOf(ctx), configValue})
			if len(results) > 0 && !results[0].IsNil() {
				if err, ok := results[0].Interface().(error); ok && err != nil {
					return fmt.Errorf("Initialize 失败: %w", err)
				}
			}
			return nil
		}
	}

	// 2) 尝试 InitializeWithConfig(ctx, config)（多数策略使用此方法）
	initWithConfigMethod := strategyValue.MethodByName("InitializeWithConfig")
	if initWithConfigMethod.IsValid() {
		results := initWithConfigMethod.Call([]reflect.Value{reflect.ValueOf(ctx), configValue})
		if len(results) > 0 && !results[0].IsNil() {
			if err, ok := results[0].Interface().(error); ok && err != nil {
				return fmt.Errorf("InitializeWithConfig 失败: %w", err)
			}
		}
		return nil
	}

	return fmt.Errorf("无法注入策略配置：策略没有 Initialize(ctx, cfg) 或 InitializeWithConfig(ctx, cfg) 方法")
}
