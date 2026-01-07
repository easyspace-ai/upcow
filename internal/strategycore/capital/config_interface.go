package capital

import "github.com/betbot/gobet/internal/common"

// ConfigInterface 配置接口，避免循环导入
type ConfigInterface interface {
	GetAutoMerge() common.AutoMergeConfig
}

