package capital

import (
	"github.com/betbot/gobet/internal/services"
	core "github.com/betbot/gobet/internal/strategycore/capital"
)

// 说明：velocityfollow/capital 仅做薄封装，真实实现位于 internal/strategycore/capital。

type ConfigInterface = core.ConfigInterface

type Capital = core.Capital
type Merger = core.Merger
type Redeemer = core.Redeemer

func New(ts *services.TradingService, cfg ConfigInterface) (*Capital, error) { return core.New(ts, cfg) }
func NewMerger(ts *services.TradingService, cfg ConfigInterface) *Merger     { return core.NewMerger(ts, cfg) }
func NewRedeemer(ts *services.TradingService, cfg ConfigInterface) *Redeemer { return core.NewRedeemer(ts, cfg) }

