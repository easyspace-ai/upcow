package oms

import (
	"context"

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
}

// CapitalInterface Capital 模块接口，避免循环导入
type CapitalInterface interface {
	TryMergeCurrentCycle(ctx context.Context, market *domain.Market)
}

