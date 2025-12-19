package bbgo

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"github.com/betbot/gobet/pkg/logger"
	"gopkg.in/yaml.v3"
)

// Config BBGO 风格配置
type Config struct {
	Strategies []StrategyConfigEntry `yaml:"strategies" json:"strategies"`
}

// StrategyConfigEntry 策略配置条目
type StrategyConfigEntry map[string]interface{}

// Load 从文件加载配置
func Load(configFile string) (*Config, error) {
	var config Config

	content, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}

	if err := yaml.Unmarshal(content, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// ReUnmarshal 将配置反序列化为策略实例（使用 interface{} 避免循环依赖）
func ReUnmarshal(conf interface{}, strategyType interface{}) (interface{}, error) {
	// 获取策略类型的反射类型
	strategyTypeValue := reflect.TypeOf(strategyType)
	if strategyTypeValue.Kind() == reflect.Ptr {
		strategyTypeValue = strategyTypeValue.Elem()
	}

	// 创建策略实例（通过反射）
	strategyValue := reflect.New(strategyTypeValue)
	strategyInstance := strategyValue.Interface()

	// 将配置转换为 JSON
	jsonData, err := json.Marshal(conf)
	if err != nil {
		return nil, fmt.Errorf("序列化配置失败: %w", err)
	}

	// 反序列化配置
	if err := json.Unmarshal(jsonData, strategyInstance); err != nil {
		return nil, fmt.Errorf("反序列化策略配置失败: %w", err)
	}

	return strategyInstance, nil
}

// LoadStrategies 从配置加载策略实例（返回 interface{} 切片避免循环依赖）
func LoadStrategies(config *Config) ([]interface{}, error) {
	var loadedStrategies []interface{}

	for _, entry := range config.Strategies {
		// entry 是一个 map，key 是策略 ID，value 是策略配置
		for strategyID, conf := range entry {
			// 获取策略类型
			strategyType, err := GetRegisteredStrategy(strategyID)
			if err != nil {
				return nil, fmt.Errorf("策略 %s 未注册: %w", strategyID, err)
			}

			// 创建策略实例
			strategyInstance, err := ReUnmarshal(conf, strategyType)
			if err != nil {
				return nil, fmt.Errorf("加载策略 %s 失败: %w", strategyID, err)
			}

			loadedStrategies = append(loadedStrategies, strategyInstance)
			logger.Infof("已加载策略: %s", strategyID)
		}
	}

	return loadedStrategies, nil
}

