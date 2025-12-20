package grid

import (
	"context"
	"fmt"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/internal/strategies/orderutil"
)

// planTick 在策略 loop 的 tick 中调用，负责超时自愈/重试/强对冲（补仓）等。
func (s *GridStrategy) planTick(ctx context.Context) {
	if s.config == nil || s.tradingService == nil {
		return
	}
	if s.Executor == nil {
		// Grid 下一阶段：强制所有交易 IO 走 Executor
		if s.plan != nil {
			s.planFailed("Executor 未设置", false)
		}
		return
	}

	// 实盘保障：无论是否存在 plan，都允许周期末强对冲（break-even 兜底）
	s.adhocStrongHedge(ctx)
	// 实盘保障：低频健康日志（可配置）
	s.logHealthTick()

	if s.plan == nil {
		return
	}

	p := s.plan
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 3
	}
	now := time.Now()

	// 通用超时：Submitting 太久就失败并释放（避免卡死）
	if p.submittingTimedOut(now) {
		s.planFailed(fmt.Sprintf("submit timeout: state=%s", p.State), true)
		return
	}

	// plan 活跃时：补仓/强对冲（更激进，允许 plan 内节流/状态跟踪）
	s.planStrongHedge(ctx)

	// 轻量自愈：如果 entry/hedge 长时间没有更新，触发一次 SyncOrderStatus
	switch p.State {
	case PlanEntryOpen:
		if p.shouldSyncEntry(now) {
			_ = s.submitSyncOrderCmd(p.ID, p.EntryOrderID)
		}
	case PlanHedgeOpen:
		if p.shouldSyncHedge(now) {
			_ = s.submitSyncOrderCmd(p.ID, p.HedgeOrderID)
		}
		// 如果对冲单长时间未成交，撤单并进入重试（撤单重挂）
		if p.shouldCancelStaleHedge(now) {
			p.enterHedgeCanceling(now)
			_ = s.submitCancelOrderCmd(p.ID, p.HedgeOrderID)
		}
	case PlanHedgeCanceling:
		// 撤单长时间无反馈：进入重试窗口自愈
		if p.cancelTimedOut(now) {
			p.enterRetryWaitFixed(now, planRetryAfterCancelTimeoutDelay)
		}
	}

	// 重试窗口：到点就重试 hedge（entry 重试由网格触发控制）
	if p.retryDue(now) {
		if p.HedgeTemplate == nil {
			s.planDone()
			return
		}
		if p.HedgeAttempts >= p.MaxAttempts {
			s.planFailed("hedge max attempts reached", false)
			return
		}
		p.enterHedgeSubmitting(now)
		// 重试前，按当前盘口/周期窗口刷新 hedge 价格（必要时放宽到 break-even）
		s.planRefreshHedgePrice(ctx)
		_ = s.submitPlaceOrderCmd(ctx, p.ID, gridCmdPlaceHedge, p.HedgeTemplate)
	}
}

// adhocStrongHedge allows end-of-cycle break-even locking even when no active plan exists.
// It is a safety net for live trading: prevents being stuck in a negative minProfit state
// due to plan lifecycle ending or missed events.
func (s *GridStrategy) adhocStrongHedge(ctx context.Context) {
	if s == nil || s.config == nil || s.tradingService == nil || s.currentMarket == nil {
		return
	}
	if !s.config.EnableAdhocStrongHedge {
		return
	}
	// 仅在周期末窗口触发，避免日常频繁“补仓刷单”
	if !s.isInHedgeLockWindow(s.currentMarket) {
		return
	}
	// 价格未就绪
	if s.currentPriceUp <= 0 || s.currentPriceDown <= 0 {
		return
	}
	if s.strongHedgeInFlight {
		return
	}
	if s.strongHedgeDebouncer == nil {
		s.strongHedgeDebouncer = common.NewDebouncer(2 * time.Second)
	}
	if s.config.StrongHedgeDebounceSeconds > 0 {
		s.strongHedgeDebouncer.SetInterval(time.Duration(s.config.StrongHedgeDebounceSeconds) * time.Second)
	}
	if ready, _ := s.strongHedgeDebouncer.ReadyNow(); !ready {
		return
	}

	upWin, downWin := s.profitsUSDC()
	// 周期末兜底：至少不亏
	target := 0.0
	if upWin >= target && downWin >= target {
		return
	}

	tokenType, assetID, fallbackPrice, dQ, maxBuy, ok := s.calcStrongHedgeOrderParams(target, upWin, downWin)
	if !ok {
		return
	}

	bestPrice, _ := orderutil.QuoteBuyPriceOr(ctx, s.tradingService, assetID, maxBuy, fallbackPrice)
	order := orderutil.NewOrder(s.currentMarket.Slug, assetID, types.SideBuy, bestPrice, dQ, tokenType, false, types.OrderTypeFAK)
	order.OrderID = fmt.Sprintf("adhoc-supp-%s-%d-%d", tokenType, bestPrice.Cents, time.Now().UnixNano())

	s.strongHedgeInFlight = true
	s.strongHedgeDebouncer.MarkNow()
	_ = s.submitPlaceOrderCmd(ctx, "adhoc", gridCmdSupplement, order)
}

func (s *GridStrategy) planOnOrderUpdate(ctx context.Context, order *domain.Order) {
	if s.plan == nil || order == nil {
		return
	}
	p := s.plan
	now := time.Now()

	// entry 订单更新
	if p.EntryOrderID != "" && order.OrderID == p.EntryOrderID {
		p.EntryStatus = order.Status
		switch order.Status {
		case domain.OrderStatusCanceled, domain.OrderStatusFailed:
			s.planFailed("entry canceled/failed", true)
			return
		case domain.OrderStatusFilled:
			// 入场完成 -> 对冲提交（如果需要）
			if p.HedgeTemplate == nil || !s.config.EnableDoubleSide {
				s.planDone()
				return
			}
			p.enterHedgeSubmitting(now)
			s.planRefreshHedgePrice(ctx)
			_ = s.submitPlaceOrderCmd(ctx, p.ID, gridCmdPlaceHedge, p.HedgeTemplate)
			return
		default:
			return
		}
	}

	// hedge 订单更新
	if p.HedgeOrderID != "" && order.OrderID == p.HedgeOrderID {
		p.HedgeStatus = order.Status
		switch order.Status {
		case domain.OrderStatusFilled:
			s.planDone()
			return
		case domain.OrderStatusCanceled, domain.OrderStatusFailed:
			// 退避重试
			p.enterRetryWait(now)
			return
		default:
			return
		}
	}
}

// planRefreshHedgePrice 基于当前盘口/周期窗口刷新 hedge price：
// - 常规：不突破 ProfitTarget（保证锁盈）
// - 周期末强对冲窗口：允许放宽到 break-even（target=0）
func (s *GridStrategy) planRefreshHedgePrice(ctx context.Context) {
	p := s.plan
	if p == nil || p.HedgeTemplate == nil || p.EntryTemplate == nil || s.currentMarket == nil || s.config == nil {
		return
	}

	entryCents := p.EntryTemplate.Price.Cents
	targetProfit := s.config.ProfitTarget
	if s.isInHedgeLockWindow(s.currentMarket) {
		targetProfit = 0
	}
	maxHedgeCents := 100 - entryCents - targetProfit
	if maxHedgeCents < 0 {
		maxHedgeCents = 0
	}

	assetID := p.HedgeTemplate.AssetID
	askPrice, _ := orderutil.QuoteBuyPriceOr(ctx, s.tradingService, assetID, 0, domain.Price{Cents: maxHedgeCents})

	// 锁盈约束：hedge 价格不能超过 maxHedgeCents，否则锁亏
	if askPrice.Cents > maxHedgeCents {
		askPrice.Cents = maxHedgeCents
	}
	if askPrice.Cents < 0 {
		askPrice.Cents = 0
	}
	p.HedgeTemplate.Price = askPrice
}

func (s *GridStrategy) planStrongHedge(ctx context.Context) {
	p := s.plan
	if p == nil || s.config == nil || s.tradingService == nil || s.currentMarket == nil {
		return
	}
	if p.SupplementInFlight {
		return
	}
	if p.SupplementDebouncer == nil {
		p.SupplementDebouncer = common.NewDebouncer(2 * time.Second)
	}
	if ready, _ := p.SupplementDebouncer.ReadyNow(); !ready {
		return
	}

	// 价格未就绪
	if s.currentPriceUp <= 0 || s.currentPriceDown <= 0 {
		return
	}

	upWin, downWin := s.profitsUSDC()
	target := s.minProfitTargetUSDC()
	// 周期末强对冲：至少不亏
	if s.isInHedgeLockWindow(s.currentMarket) {
		target = 0
	}

	if upWin >= target && downWin >= target {
		return
	}

	tokenType, assetID, price, dQ, maxBuy, ok := s.calcStrongHedgeOrderParams(target, upWin, downWin)
	if !ok {
		return
	}

	// 取 bestAsk，优先成交
	bestPrice, _ := orderutil.QuoteBuyPriceOr(ctx, s.tradingService, assetID, maxBuy, price)

	s.submitStrongHedgeSupplement(ctx, p, tokenType, assetID, bestPrice, dQ)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
