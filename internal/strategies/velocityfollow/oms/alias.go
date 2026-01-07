package oms

import (
	"github.com/betbot/gobet/internal/services"
	core "github.com/betbot/gobet/internal/strategycore/oms"
)

// 说明：velocityfollow/oms 仅做薄封装，真实实现位于 internal/strategycore/oms。

type ConfigInterface = core.ConfigInterface
type CapitalInterface = core.CapitalInterface

type OMS = core.OMS
type OpsMetrics = core.OpsMetrics
type OrderExecutor = core.OrderExecutor
type PositionManager = core.PositionManager
type RiskManager = core.RiskManager
type RiskExposure = core.RiskExposure
type HedgeReorder = core.HedgeReorder

type RiskManagementStatus = core.RiskManagementStatus
type RiskExposureInfo = core.RiskExposureInfo

func New(ts *services.TradingService, cfg ConfigInterface) (*OMS, error) {
	return core.New(ts, cfg, "velocityfollow")
}

