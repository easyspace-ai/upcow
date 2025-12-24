package bbgo

import (
	"context"

	"github.com/betbot/gobet/internal/domain"
)

// CycleAwareStrategy 是一个可选接口：策略若实现它，框架会在“周期切换（session 切换）”时回调。
//
// 目的：
// - 把“每个策略里都写一遍 marketSlug 对比 + 重置状态”的样板代码上移到框架层
// - 让策略只关注交易逻辑，不关注周期切换检测
//
// 调用时机：
// - 首次启动策略 Run 之前：oldMarket=nil, newMarket=current
// - MarketScheduler 触发 session 切换后，Trader.SwitchSession 会调用：oldMarket=上一周期, newMarket=新周期
type CycleAwareStrategy interface {
	OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market)
}

