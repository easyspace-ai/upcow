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

func (s *GridStrategy) handleGridLevelReachedWithPlan(
	ctx context.Context,
	market *domain.Market,
	tokenType domain.TokenType,
	gridLevel int,
	currentPrice domain.Price,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.tradingService == nil || s.config == nil || market == nil {
		return nil
	}
	if s.Executor == nil {
		// 下一阶段彻底工程化：网格策略强制所有交易 IO 走 Executor
		return fmt.Errorf("grid: Executor 未设置")
	}

	// 若已有 plan 在途，则不重复触发
	if s.plan != nil && (s.plan.State == PlanEntrySubmitting || s.plan.State == PlanEntryOpen || s.plan.State == PlanHedgeSubmitting || s.plan.State == PlanHedgeOpen) {
		return nil
	}

	// 简化防重复：单线程 loop 下无需锁；保留时间窗口避免抖动重复触发
	levelKey := fmt.Sprintf("%s:%d", tokenType, gridLevel)
	if s.processedGridLevels == nil {
		s.processedGridLevels = make(map[string]*common.Debouncer)
	}
	deb := s.processedGridLevels[levelKey]
	if deb == nil {
		deb = common.NewDebouncer(30 * time.Second)
		s.processedGridLevels[levelKey] = deb
	}
	if ready, _ := deb.ReadyNow(); !ready {
		return nil
	}
	deb.MarkNow()

	orderCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 入场价格：优先取 bestAsk，但不允许超过 gridLevel + slippage
	entryPrice := domain.Price{Cents: gridLevel}
	hedgePrice := domain.Price{Cents: 0}

	entryMax := 0
	if s.config.EntryMaxBuySlippageCents > 0 {
		entryMax = gridLevel + s.config.EntryMaxBuySlippageCents
	}
	if tokenType == domain.TokenTypeUp {
		p, _ := orderutil.QuoteBuyPriceOr(orderCtx, s.tradingService, market.YesAssetID, entryMax, entryPrice)
		entryPrice = p
	} else if tokenType == domain.TokenTypeDown {
		p, _ := orderutil.QuoteBuyPriceOr(orderCtx, s.tradingService, market.NoAssetID, entryMax, entryPrice)
		entryPrice = p
	}

	// 仍沿用既有“锁定利润目标”规则：hedgePrice <= 100 - entryPrice - ProfitTarget
	hedgePriceCents := 100 - entryPrice.Cents - s.config.ProfitTarget
	if hedgePriceCents < 0 {
		hedgePriceCents = 0
	}
	hedgePrice = domain.Price{Cents: hedgePriceCents}

	// 计算入场订单金额和share数量
	_, entryShare := s.calculateOrderSize(entryPrice)

	var entryOrder *domain.Order
	var hedgeOrder *domain.Order

	now := time.Now()
	if tokenType == domain.TokenTypeUp {
		entryOrder = orderutil.NewOrder(market.Slug, market.YesAssetID, types.SideBuy, entryPrice, entryShare, domain.TokenTypeUp, true, types.OrderTypeFAK)
		entryOrder.OrderID = fmt.Sprintf("plan-entry-up-%d-%d", gridLevel, now.UnixNano())
		entryOrder.GridLevel = gridLevel
		if s.config.EnableDoubleSide {
			_, hedgeShare := s.calculateOrderSize(hedgePrice)
			// 对冲价格：优先取 bestAsk，但不超过 lock-in 上限（hedgePriceCents）
			hp, _ := orderutil.QuoteBuyPriceOr(orderCtx, s.tradingService, market.NoAssetID, hedgePriceCents, hedgePrice)
			if hp.Cents > hedgePriceCents {
				hp.Cents = hedgePriceCents
			}
			hedgeOrder = orderutil.NewOrder(market.Slug, market.NoAssetID, types.SideBuy, hp, hedgeShare, domain.TokenTypeDown, false, types.OrderTypeFAK)
			hedgeOrder.OrderID = fmt.Sprintf("plan-hedge-down-%d-%d", gridLevel, now.UnixNano())
			hedgeOrder.GridLevel = gridLevel
		}
	} else if tokenType == domain.TokenTypeDown {
		entryOrder = orderutil.NewOrder(market.Slug, market.NoAssetID, types.SideBuy, entryPrice, entryShare, domain.TokenTypeDown, true, types.OrderTypeFAK)
		entryOrder.OrderID = fmt.Sprintf("plan-entry-down-%d-%d", gridLevel, now.UnixNano())
		entryOrder.GridLevel = hedgePriceCents // 维持原有语义：记录对冲层级
		if s.config.EnableDoubleSide {
			_, hedgeShare := s.calculateOrderSize(hedgePrice)
			hp, _ := orderutil.QuoteBuyPriceOr(orderCtx, s.tradingService, market.YesAssetID, hedgePriceCents, hedgePrice)
			if hp.Cents > hedgePriceCents {
				hp.Cents = hedgePriceCents
			}
			hedgeOrder = orderutil.NewOrder(market.Slug, market.YesAssetID, types.SideBuy, hp, hedgeShare, domain.TokenTypeUp, false, types.OrderTypeFAK)
			hedgeOrder.OrderID = fmt.Sprintf("plan-hedge-up-%d-%d", gridLevel, now.UnixNano())
			hedgeOrder.GridLevel = hedgePriceCents
		}
	} else {
		return nil
	}

	planID := fmt.Sprintf("%s-%s-%d-%d", market.Slug, tokenType, gridLevel, now.UnixNano())
	s.plan = &HedgePlan{
		ID:            planID,
		LevelKey:      levelKey,
		State:         PlanEntrySubmitting,
		CreatedAt:     now,
		StateAt:       now,
		EntryAttempts: 1,
		HedgeAttempts: 0,
		MaxAttempts:   3,
		EntryTemplate: entryOrder,
		HedgeTemplate: hedgeOrder,
		EntryCreated:  nil,
		HedgeCreated:  nil,
		LastError:     "",
		// 补仓/强对冲节流：避免短时间内连续补仓刷单
		SupplementDebouncer: common.NewDebouncer(2 * time.Second),
	}

	// 标记正在下单（用于诊断/兼容旧逻辑）
	s.placeOrderMu.Lock()
	s.isPlacingOrder = true
	s.isPlacingOrderSetTime = time.Now()
	s.placeOrderMu.Unlock()

	_ = currentPrice
	if err := s.submitPlaceOrderCmd(orderCtx, planID, gridCmdPlaceEntry, entryOrder); err != nil {
		// Executor 队列满等提交失败：立即释放 plan（允许重试该层级），避免卡死
		s.placeOrderMu.Lock()
		s.isPlacingOrder = false
		s.isPlacingOrderSetTime = time.Time{}
		s.placeOrderMu.Unlock()

		s.planFailed(fmt.Sprintf("submit place entry failed: %v", err), true)
		return nil
	}
	return nil
}
