package brain

import (
	"github.com/betbot/gobet/internal/services"
	core "github.com/betbot/gobet/internal/strategycore/brain"
)

// 说明：goodluck/brain 作为 goodluck 的模块边界；实现复用 internal/strategycore/brain。

type ConfigInterface = core.ConfigInterface

type Brain = core.Brain
type Decision = core.Decision
type VelocityInfo = core.VelocityInfo

type DecisionConditions = core.DecisionConditions
type StrategyInfo = core.StrategyInfo

type PositionState = core.PositionState
type PositionAnalysis = core.PositionAnalysis

func New(ts *services.TradingService, cfg ConfigInterface) (*Brain, error) { return core.New(ts, cfg) }
