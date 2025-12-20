package grid

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/internal/strategies/orderutil"
)

// planTick 在策略 loop 的 tick 中调用，负责超时自愈/重试/强对冲（补仓）等。
func (s *GridStrategy) planTick(ctx context.Context) {
	if s.plan == nil || s.config == nil || s.tradingService == nil {
		return
	}
	if s.Executor == nil {
		// Grid 下一阶段：强制所有交易 IO 走 Executor
		s.plan.State = PlanFailed
		s.plan.LastError = "Executor 未设置"
		s.plan = nil
		return
	}

	p := s.plan
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 3
	}
	now := time.Now()

	// 通用超时：Submitting 太久就失败并释放（避免卡死）
	if p.submittingTimedOut(now) {
		p.State = PlanFailed
		p.LastError = fmt.Sprintf("submit timeout: state=%s", p.State)
		// 允许该层级重试
		if p.LevelKey != "" && s.processedGridLevels != nil {
			delete(s.processedGridLevels, p.LevelKey)
		}
		s.plan = nil
		return
	}

	// 进入强对冲窗口/补仓：只在 plan 活跃时做（避免无仓位时瞎补）
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
			p.State = PlanHedgeCanceling
			p.StateAt = now
			_ = s.submitCancelOrderCmd(p.ID, p.HedgeOrderID)
		}
	case PlanHedgeCanceling:
		// 撤单长时间无反馈：进入重试窗口自愈
		if p.cancelTimedOut(now) {
			p.State = PlanRetryWait
			p.StateAt = now
			p.NextRetryAt = now.Add(2 * time.Second)
		}
	}

	// 重试窗口：到点就重试 hedge（entry 重试由网格触发控制）
	if p.State == PlanRetryWait && !p.NextRetryAt.IsZero() && now.After(p.NextRetryAt) {
		if p.HedgeTemplate == nil {
			p.State = PlanDone
			s.plan = nil
			return
		}
		if p.HedgeAttempts >= p.MaxAttempts {
			p.State = PlanFailed
			p.LastError = "hedge max attempts reached"
			s.plan = nil
			return
		}
		p.State = PlanHedgeSubmitting
		p.StateAt = now
		p.HedgeAttempts++
		// 重试前，按当前盘口/周期窗口刷新 hedge 价格（必要时放宽到 break-even）
		s.planRefreshHedgePrice(ctx)
		_ = s.submitPlaceOrderCmd(ctx, p.ID, gridCmdPlaceHedge, p.HedgeTemplate)
	}
}

func (s *GridStrategy) planOnOrderUpdate(ctx context.Context, order *domain.Order) {
	if s.plan == nil || order == nil {
		return
	}
	p := s.plan

	// entry 订单更新
	if p.EntryOrderID != "" && order.OrderID == p.EntryOrderID {
		p.EntryStatus = order.Status
		switch order.Status {
		case domain.OrderStatusCanceled, domain.OrderStatusFailed:
			p.State = PlanFailed
			p.LastError = "entry canceled/failed"
			// 允许该层级重试
			if p.LevelKey != "" && s.processedGridLevels != nil {
				delete(s.processedGridLevels, p.LevelKey)
			}
			s.plan = nil
			return
		case domain.OrderStatusFilled:
			// 入场完成 -> 对冲提交（如果需要）
			if p.HedgeTemplate == nil || !s.config.EnableDoubleSide {
				p.State = PlanDone
				s.plan = nil
				return
			}
			p.State = PlanHedgeSubmitting
			p.StateAt = time.Now()
			p.HedgeAttempts++
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
			p.State = PlanDone
			s.plan = nil
			return
		case domain.OrderStatusCanceled, domain.OrderStatusFailed:
			// 退避重试
			p.StateAt = time.Now()
			delay := time.Duration(1<<minInt(p.HedgeAttempts, 3)) * time.Second // 2s/4s/8s...
			p.NextRetryAt = time.Now().Add(delay)
			p.State = PlanRetryWait
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
	askPrice, err := orderutil.QuoteBuyPrice(ctx, s.tradingService, assetID, 0)
	if err != nil {
		// 取不到盘口时，退化为 maxHedgeCents（保证不破坏锁盈/不亏目标）
		p.HedgeTemplate.Price = domain.Price{Cents: maxHedgeCents}
		return
	}

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

	needUp := math.Max(0, target-upWin)
	needDown := math.Max(0, target-downWin)
	var tokenType domain.TokenType
	var assetID string
	var priceCents int
	var needed float64
	if needUp >= needDown {
		tokenType = domain.TokenTypeUp
		assetID = s.currentMarket.YesAssetID
		priceCents = s.currentPriceUp
		needed = needUp
	} else {
		tokenType = domain.TokenTypeDown
		assetID = s.currentMarket.NoAssetID
		priceCents = s.currentPriceDown
		needed = needDown
	}

	price := domain.Price{Cents: priceCents}
	priceDec := price.ToDecimal()
	if priceDec <= 0 || priceDec >= 1 {
		return
	}

	dQ := needed / (1.0 - priceDec)
	if dQ <= 0 || math.IsNaN(dQ) || math.IsInf(dQ, 0) {
		return
	}

	minOrderUSDC := s.config.MinOrderSize
	if minOrderUSDC <= 0 {
		minOrderUSDC = 1.1
	}
	if dQ*priceDec < minOrderUSDC {
		dQ = minOrderUSDC / priceDec
	}

	// 限制单次补仓
	maxDQ := 50.0
	if s.isInHedgeLockWindow(s.currentMarket) {
		maxDQ = math.Max(50.0, dQ)
	}
	if dQ > maxDQ {
		dQ = maxDQ
	}

	// 取 bestAsk，优先成交
	maxBuy := 0
	if s.config.SupplementMaxBuySlippageCents > 0 {
		maxBuy = price.Cents + s.config.SupplementMaxBuySlippageCents
	}
	bestPrice, err := orderutil.QuoteBuyPrice(ctx, s.tradingService, assetID, maxBuy)
	if err != nil {
		// 盘口不可用则用当前价格兜底（避免完全停摆）
		bestPrice = price
	}

	order := orderutil.NewOrder(s.currentMarket.Slug, assetID, types.SideBuy, bestPrice, dQ, tokenType, false, types.OrderTypeFAK)
	order.OrderID = fmt.Sprintf("plan-supp-%s-%d-%d", tokenType, bestPrice.Cents, time.Now().UnixNano())

	p.SupplementInFlight = true
	if p.SupplementDebouncer != nil {
		p.SupplementDebouncer.MarkNow()
	}
	p.StateAt = time.Now()
	_ = s.submitPlaceOrderCmd(ctx, p.ID, gridCmdSupplement, order)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
