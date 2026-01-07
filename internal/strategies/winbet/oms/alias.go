package oms

import (
	"github.com/betbot/gobet/internal/services"
	core "github.com/betbot/gobet/internal/strategycore/oms"
)

// 说明：winbet/oms 作为 winbet 的模块边界；实现复用 internal/strategycore/oms。

type ConfigInterface = core.ConfigInterface
type CapitalInterface = core.CapitalInterface

type OMS = core.OMS
type RiskManagementStatus = core.RiskManagementStatus
type RiskExposureInfo = core.RiskExposureInfo

func New(ts *services.TradingService, cfg ConfigInterface) (*OMS, error) { return core.New(ts, cfg, "winbet") }

