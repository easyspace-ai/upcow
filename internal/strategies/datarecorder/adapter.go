package datarecorder

import (
	"github.com/betbot/gobet/internal/strategies/configadapter"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
)

// DataRecorderConfigAdapter 数据记录策略配置适配器
type DataRecorderConfigAdapter struct{}

// AdaptConfig 从通用配置适配为数据记录策略配置
func (a *DataRecorderConfigAdapter) AdaptConfig(strategyConfig interface{}, proxyConfig interface{}) (interface{}, error) {
	proxyURL := configadapter.ProxyURLFromAny(proxyConfig)
	return configadapter.AdaptRequired[config.DataRecorderConfig, DataRecorderStrategyConfig](
		strategyConfig,
		ID,
		func(cfg config.StrategyConfig) *config.DataRecorderConfig { return cfg.DataRecorder },
		func(c *config.DataRecorderConfig) (*DataRecorderStrategyConfig, error) {
			return &DataRecorderStrategyConfig{
				OutputDir:       c.OutputDir,
				UseRTDSFallback: c.UseRTDSFallback,
				ProxyURL:        proxyURL,
			}, nil
		},
	)
}

// 确保 DataRecorderConfigAdapter 实现了 ConfigAdapter 接口
var _ bbgo.ConfigAdapter = (*DataRecorderConfigAdapter)(nil)

