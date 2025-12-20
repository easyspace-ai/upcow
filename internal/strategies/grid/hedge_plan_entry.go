package grid

import (
	"context"
	"fmt"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
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
		s.processedGridLevels = make(map[string]time.Time)
	}
	if last, ok := s.processedGridLevels[levelKey]; ok {
		if time.Since(last) < 30*time.Second {
			return nil
		}
	}
	s.processedGridLevels[levelKey] = time.Now()

	orderCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	entryPrice := domain.Price{Cents: gridLevel}
	hedgePrice := domain.Price{Cents: 0}

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
		entryOrder = &domain.Order{
			OrderID:      fmt.Sprintf("plan-entry-up-%d-%d", gridLevel, now.UnixNano()),
			AssetID:      market.YesAssetID,
			Side:         types.SideBuy,
			Price:        entryPrice,
			Size:         entryShare,
			GridLevel:    gridLevel,
			TokenType:    domain.TokenTypeUp,
			IsEntryOrder: true,
			Status:       domain.OrderStatusPending,
			CreatedAt:    now,
			OrderType:    types.OrderTypeFAK,
		}
		if s.config.EnableDoubleSide {
			_, hedgeShare := s.calculateOrderSize(hedgePrice)
			hedgeOrder = &domain.Order{
				OrderID:      fmt.Sprintf("plan-hedge-down-%d-%d", gridLevel, now.UnixNano()),
				AssetID:      market.NoAssetID,
				Side:         types.SideBuy,
				Price:        hedgePrice,
				Size:         hedgeShare,
				GridLevel:    gridLevel,
				TokenType:    domain.TokenTypeDown,
				IsEntryOrder: false,
				Status:       domain.OrderStatusPending,
				CreatedAt:    now,
				OrderType:    types.OrderTypeFAK,
			}
		}
	} else if tokenType == domain.TokenTypeDown {
		entryOrder = &domain.Order{
			OrderID:      fmt.Sprintf("plan-entry-down-%d-%d", gridLevel, now.UnixNano()),
			AssetID:      market.NoAssetID,
			Side:         types.SideBuy,
			Price:        entryPrice,
			Size:         entryShare,
			GridLevel:    hedgePriceCents, // 维持原有语义：记录对冲层级
			TokenType:    domain.TokenTypeDown,
			IsEntryOrder: true,
			Status:       domain.OrderStatusPending,
			CreatedAt:    now,
			OrderType:    types.OrderTypeFAK,
		}
		if s.config.EnableDoubleSide {
			_, hedgeShare := s.calculateOrderSize(hedgePrice)
			hedgeOrder = &domain.Order{
				OrderID:      fmt.Sprintf("plan-hedge-up-%d-%d", gridLevel, now.UnixNano()),
				AssetID:      market.YesAssetID,
				Side:         types.SideBuy,
				Price:        hedgePrice,
				Size:         hedgeShare,
				GridLevel:    hedgePriceCents,
				TokenType:    domain.TokenTypeUp,
				IsEntryOrder: false,
				Status:       domain.OrderStatusPending,
				CreatedAt:    now,
				OrderType:    types.OrderTypeFAK,
			}
		}
	} else {
		return nil
	}

	planID := fmt.Sprintf("%s-%s-%d-%d", market.Slug, tokenType, gridLevel, now.UnixNano())
	s.plan = &HedgePlan{
		ID:            planID,
		LevelKey:       levelKey,
		State:          PlanEntrySubmitting,
		CreatedAt:      now,
		EntryTemplate:  entryOrder,
		HedgeTemplate:  hedgeOrder,
		EntryCreated:   nil,
		HedgeCreated:   nil,
		LastError:      "",
	}

	// 标记正在下单（用于诊断/兼容旧逻辑）
	s.placeOrderMu.Lock()
	s.isPlacingOrder = true
	s.isPlacingOrderSetTime = time.Now()
	s.placeOrderMu.Unlock()

	_ = currentPrice
	return s.submitPlaceOrderCmd(orderCtx, planID, gridCmdPlaceEntry, entryOrder)
}

