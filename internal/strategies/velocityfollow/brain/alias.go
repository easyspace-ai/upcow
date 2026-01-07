package brain

import (
	"github.com/betbot/gobet/internal/services"
	core "github.com/betbot/gobet/internal/strategycore/brain"
)

// 说明：velocityfollow/brain 仅做薄封装，真实实现位于 internal/strategycore/brain。

type ConfigInterface = core.ConfigInterface

type Brain = core.Brain
type Decision = core.Decision
type VelocityInfo = core.VelocityInfo

type DecisionConditions = core.DecisionConditions
type StrategyInfo = core.StrategyInfo

type PositionState = core.PositionState
type PositionTracker = core.PositionTracker

type RiskProfitCalculator = core.RiskProfitCalculator
type PotentialTradeAnalysis = core.PotentialTradeAnalysis
type CurrentPositionAnalysis = core.CurrentPositionAnalysis

type ArbitrageBrain = core.ArbitrageBrain
type ArbitrageAnalysis = core.ArbitrageAnalysis

type PriceSample = core.PriceSample
type VelocitySample = core.VelocitySample
type VelocityState = core.VelocityState
type DecisionEngine = core.DecisionEngine

func New(ts *services.TradingService, cfg ConfigInterface) (*Brain, error) { return core.New(ts, cfg) }
func NewRiskProfitCalculator(ts *services.TradingService) *RiskProfitCalculator {
	return core.NewRiskProfitCalculator(ts)
}

