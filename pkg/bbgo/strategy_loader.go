package bbgo

import (
	"context"
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
func (l *StrategyLoader) LoadStrategy(strategyName string, config interface{}) (interface{}, error) {
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

	// 执行统一的初始化流程（会在内部设置配置）
	if err := l.initializeStrategy(strategyInstance, config); err != nil {
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

// initializeStrategy 执行统一的初始化流程
// 流程：Defaults -> Initialize/InitializeWithConfig (设置配置) -> Validate -> Initialize (BBGO风格)
func (l *StrategyLoader) initializeStrategy(strategy interface{}, config interface{}) error {
	// 1. Defaults - 设置默认值
	if defaulter, ok := strategy.(StrategyDefaulter); ok {
		if err := defaulter.Defaults(); err != nil {
			return fmt.Errorf("设置默认值失败: %w", err)
		}
	}

	strategyValue := reflect.ValueOf(strategy)
	configValue := reflect.ValueOf(config)

	// 2. 尝试使用 Initialize 方法（grid 策略使用此方法）
	initMethod := strategyValue.MethodByName("Initialize")
	if initMethod.IsValid() {
		// 检查方法签名：Initialize(ctx context.Context, config strategies.StrategyConfig)
		methodType := initMethod.Type()
		if methodType.NumIn() == 2 {
			// 调用 Initialize(ctx, config)
			results := initMethod.Call([]reflect.Value{
				reflect.ValueOf(context.Background()),
				configValue,
			})
			if len(results) > 0 && !results[0].IsNil() {
				if err, ok := results[0].Interface().(error); ok && err != nil {
					return fmt.Errorf("Initialize 失败: %w", err)
				}
			}
			// Initialize 成功，跳过后续步骤
			return nil
		}
	}

	// 3. 尝试使用 InitializeWithConfig 方法（其他策略使用此方法）
	initWithConfigMethod := strategyValue.MethodByName("InitializeWithConfig")
	if initWithConfigMethod.IsValid() {
		// 调用 InitializeWithConfig(ctx, config)
		results := initWithConfigMethod.Call([]reflect.Value{
			reflect.ValueOf(context.Background()),
			configValue,
		})
		if len(results) > 0 && !results[0].IsNil() {
			if err, ok := results[0].Interface().(error); ok && err != nil {
				return fmt.Errorf("InitializeWithConfig 失败: %w", err)
			}
		}
		// InitializeWithConfig 成功，跳过后续步骤
		return nil
	}

	// 4. 如果没有找到 Initialize 或 InitializeWithConfig 方法，尝试直接设置 config 字段
	// 注意：这只有在策略和加载器在同一个包内时才可能成功
	strategyValueForField := reflect.ValueOf(strategy)
	if strategyValueForField.Kind() == reflect.Ptr {
		strategyValueForField = strategyValueForField.Elem()
	}
	configField := strategyValueForField.FieldByName("config")
	if configField.IsValid() && configField.CanSet() {
		configField.Set(configValue)
		// 设置配置后，继续执行 BBGO 风格的初始化流程
	} else {
		return fmt.Errorf("无法设置策略配置：策略没有 Initialize 或 InitializeWithConfig 方法，且 config 字段不可设置")
	}

	// 5. Validate - 验证配置
	if validator, ok := strategy.(StrategyValidator); ok {
		if err := validator.Validate(); err != nil {
			return fmt.Errorf("配置验证失败: %w", err)
		}
	}

	// 6. Initialize - 初始化策略（BBGO 风格的 Initialize() 方法，无参数）
	if initializer, ok := strategy.(StrategyInitializer); ok {
		if err := initializer.Initialize(); err != nil {
			return fmt.Errorf("初始化失败: %w", err)
		}
	}

	return nil
}
