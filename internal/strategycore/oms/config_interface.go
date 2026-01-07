package oms

import (
	"context"

	"github.com/betbot/gobet/internal/common"
	"github.com/betbot/gobet/internal/domain"
)

// ConfigInterface 配置接口，避免循环导入
type ConfigInterface interface {
	GetOrderExecutionMode() string
	GetSequentialCheckIntervalMs() int
	GetSequentialMaxWaitMs() int
	GetOrderSize() float64
	GetHedgeOrderSize() float64

	// RiskManager 配置
	GetRiskManagementEnabled() bool
	GetRiskManagementCheckIntervalMs() int
	GetAggressiveHedgeTimeoutSeconds() int
	GetMaxAcceptableLossCents() int

	// HedgeReorder 配置
	GetHedgeReorderTimeoutSeconds() int
	GetHedgeTimeoutFakSeconds() int
	GetAllowNegativeProfitOnHedgeReorder() bool
	GetMaxNegativeProfitCents() int
	GetHedgeOffsetCents() int

	// AutoMerge 配置（可选，用于获取 merge 触发延迟时间）
	GetAutoMerge() common.AutoMergeConfig

	// 最小订单金额配置（USDC）
	GetMinOrderUSDC() float64
}

// CapitalInterface Capital 模块接口，避免循环导入
type CapitalInterface interface {
	TryMergeCurrentCycle(ctx context.Context, market *domain.Market)
}
