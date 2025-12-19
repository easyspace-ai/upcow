package threshold

import (
	"fmt"

	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
)

// ThresholdConfigAdapter 价格阈值策略配置适配器
type ThresholdConfigAdapter struct{}

// AdaptConfig 从通用配置适配为价格阈值策略配置
func (a *ThresholdConfigAdapter) AdaptConfig(strategyConfig interface{}, proxyConfig interface{}) (interface{}, error) {
	cfg, ok := strategyConfig.(config.StrategyConfig)
	if !ok {
		return nil, fmt.Errorf("无效的策略配置类型: %T", strategyConfig)
	}

	if cfg.Threshold == nil {
		return nil, fmt.Errorf("价格阈值策略已启用但配置为空")
	}

	return &ThresholdStrategyConfig{
		BuyThreshold:      cfg.Threshold.BuyThreshold,
		SellThreshold:     cfg.Threshold.SellThreshold,
		OrderSize:         cfg.Threshold.OrderSize,
		TokenType:         cfg.Threshold.TokenType,
		ProfitTargetCents: cfg.Threshold.ProfitTargetCents,
		StopLossCents:     cfg.Threshold.StopLossCents,
	}, nil
}

// 确保 ThresholdConfigAdapter 实现了 ConfigAdapter 接口
var _ bbgo.ConfigAdapter = (*ThresholdConfigAdapter)(nil)

