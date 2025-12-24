package all

// 统一导入所有内置策略以触发 init() 注册。
// 这样 cmd/bot/main.go 只需要导入这一处，新增策略时不再修改入口代码。

import (
	_ "github.com/betbot/gobet/internal/strategies/arbitrage"
	_ "github.com/betbot/gobet/internal/strategies/datarecorder"
	_ "github.com/betbot/gobet/internal/strategies/grid"
	_ "github.com/betbot/gobet/internal/strategies/momentum"
	_ "github.com/betbot/gobet/internal/strategies/orderlistener"
	_ "github.com/betbot/gobet/internal/strategies/pairedtrading"
	_ "github.com/betbot/gobet/internal/strategies/pairlock"
	_ "github.com/betbot/gobet/internal/strategies/threshold"
	_ "github.com/betbot/gobet/internal/strategies/unifiedarb"
	_ "github.com/betbot/gobet/internal/strategies/updown"
	_ "github.com/betbot/gobet/internal/strategies/velocityfollow"
)
