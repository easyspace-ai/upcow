package dashboard

import (
	"github.com/betbot/gobet/internal/services"
	core "github.com/betbot/gobet/internal/strategycore/dashboard"
)

// 说明：winbet/dashboard 作为 winbet 的模块边界；实现复用 internal/strategycore/dashboard。

type Dashboard = core.Dashboard
type Snapshot = core.Snapshot
type UpdateData = core.UpdateData
type PositionState = core.PositionState
type DecisionConditions = core.DecisionConditions
type RiskManagementStatus = core.RiskManagementStatus
type RiskExposureInfo = core.RiskExposureInfo

func New(ts *services.TradingService, useNativeTUI bool) *Dashboard {
	return core.New(ts, core.Options{
		StrategyID: "winbet",
		Title:      "WinBet Strategy Dashboard",
	}, useNativeTUI)
}

