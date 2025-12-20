package datarecorder

import (
	"fmt"
)

// DataRecorderStrategyConfig 数据记录策略配置
type DataRecorderStrategyConfig struct {
	OutputDir       string `json:"outputDir" yaml:"outputDir"`             // CSV 文件保存目录
	UseRTDSFallback *bool  `json:"useRTDSFallback" yaml:"useRTDSFallback"` // 是否使用 RTDS 作为目标价备选方案（默认 true）
	ProxyURL        string `json:"proxyURL" yaml:"proxyURL"`               // 代理 URL（格式：http://host:port）
}

// GetName 实现 StrategyConfig 接口
func (c *DataRecorderStrategyConfig) GetName() string {
	return "datarecorder"
}

// Validate 验证配置
func (c *DataRecorderStrategyConfig) Validate() error {
	if c.OutputDir == "" {
		return fmt.Errorf("输出目录不能为空")
	}
	return nil
}

// DefaultDataRecorderStrategyConfig 返回默认配置
func DefaultDataRecorderStrategyConfig() *DataRecorderStrategyConfig {
	defaultUse := true
	return &DataRecorderStrategyConfig{
		OutputDir:       "data/recordings",
		UseRTDSFallback: &defaultUse,
	}
}

