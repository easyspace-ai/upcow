package capital

import (
	"github.com/betbot/gobet/internal/services"
	core "github.com/betbot/gobet/internal/strategycore/capital"
)

// 说明：winbet/capital 作为 winbet 的模块边界；实现复用 internal/strategycore/capital。

type ConfigInterface = core.ConfigInterface
type Capital = core.Capital

func New(ts *services.TradingService, cfg ConfigInterface) (*Capital, error) { return core.New(ts, cfg) }

