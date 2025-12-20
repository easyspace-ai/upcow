package grid

import (
	"context"
	"math"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

// hedgeLockWindowSeconds 周期末进入“强对冲”窗口：优先把 minProfit 拉回 >= 0
const hedgeLockWindowSeconds = 90

// ensureMinProfitLocked 基于 minProfit 目标驱动的动态对冲：
// 目标：min(P_up, P_down) >= target，其中
// P_up   = upHoldings - upTotalCost - downTotalCost
// P_down = downHoldings - upTotalCost - downTotalCost
func (s *GridStrategy) ensureMinProfitLocked(ctx context.Context, market *domain.Market) {
	// 已由 HedgePlan 状态机替代：见 planStrongHedge / planTick
	_ = ctx
	_ = market
}

func (s *GridStrategy) profitsUSDC() (upWin float64, downWin float64) {
	// 注意：这些字段未来会在单线程 loop 中维护，逐步移除锁
	upWin = s.upHoldings*1.0 - s.upTotalCost - s.downTotalCost
	downWin = s.downHoldings*1.0 - s.upTotalCost - s.downTotalCost
	return
}

// minProfitTargetUSDC 以“已成对的份额”为基准设置目标利润（避免一开始就激进补齐到很高利润）
// target = profitTargetPerShare * min(upHoldings, downHoldings)
func (s *GridStrategy) minProfitTargetUSDC() float64 {
	if s.config == nil {
		return 0
	}
	perShare := float64(s.config.ProfitTarget) / 100.0
	if perShare < 0 {
		perShare = 0
	}
	pairs := math.Min(s.upHoldings, s.downHoldings)
	if pairs <= 0 {
		return 0
	}
	return perShare * pairs
}

func (s *GridStrategy) isInHedgeLockWindow(market *domain.Market) bool {
	if market == nil || market.Timestamp <= 0 {
		return false
	}
	now := time.Now().Unix()
	end := market.Timestamp + 900
	return now >= end-hedgeLockWindowSeconds && now < end
}

func (s *GridStrategy) hasAnyPendingHedgeOrder() bool {
	// 1) pendingHedgeOrders（策略内部待提交）
	if len(s.pendingHedgeOrders) > 0 {
		return true
	}
	// 2) 交易所侧已挂的对冲单（open/pending）
	for _, o := range s.getActiveOrders() {
		if o == nil {
			continue
		}
		if !o.IsEntryOrder && (o.Status == domain.OrderStatusOpen || o.Status == domain.OrderStatusPending) {
			return true
		}
	}
	return false
}

