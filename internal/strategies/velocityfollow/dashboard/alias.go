package dashboard

import (
	"github.com/betbot/gobet/internal/services"
	core "github.com/betbot/gobet/internal/strategycore/dashboard"
)

// 说明：velocityfollow/dashboard 仅做薄封装，真实实现位于 internal/strategycore/dashboard。

type Options = core.Options

type Dashboard = core.Dashboard
type Snapshot = core.Snapshot
type UpdateData = core.UpdateData
type PositionState = core.PositionState
type DecisionConditions = core.DecisionConditions
type RiskManagementStatus = core.RiskManagementStatus
type RiskExposureInfo = core.RiskExposureInfo
type UpdateMsg = core.UpdateMsg
type NativeTUI = core.NativeTUI

func New(ts *services.TradingService, useNativeTUI bool) *Dashboard {
	return core.New(ts, core.Options{
		StrategyID: "velocityfollow",
		Title:      "VelocityFollow Strategy Dashboard",
	}, useNativeTUI)
}

func NewNativeTUI() (*NativeTUI, error) { return core.NewNativeTUI() }

