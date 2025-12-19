package datarecorder

import (
	"fmt"

	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
)

// DataRecorderConfigAdapter 数据记录策略配置适配器
type DataRecorderConfigAdapter struct{}

// AdaptConfig 从通用配置适配为数据记录策略配置
func (a *DataRecorderConfigAdapter) AdaptConfig(strategyConfig interface{}, proxyConfig interface{}) (interface{}, error) {
	cfg, ok := strategyConfig.(config.StrategyConfig)
	if !ok {
		return nil, fmt.Errorf("无效的策略配置类型: %T", strategyConfig)
	}

	if cfg.DataRecorder == nil {
		return nil, fmt.Errorf("数据记录策略已启用但配置为空")
	}

	proxyURL := ""
	if proxyConfig != nil {
		if proxy, ok := proxyConfig.(*config.ProxyConfig); ok && proxy != nil {
			proxyURL = fmt.Sprintf("http://%s:%d", proxy.Host, proxy.Port)
		}
	}

	return &DataRecorderStrategyConfig{
		OutputDir:       cfg.DataRecorder.OutputDir,
		UseRTDSFallback: cfg.DataRecorder.UseRTDSFallback,
		ProxyURL:        proxyURL,
	}, nil
}

// 确保 DataRecorderConfigAdapter 实现了 ConfigAdapter 接口
var _ bbgo.ConfigAdapter = (*DataRecorderConfigAdapter)(nil)

