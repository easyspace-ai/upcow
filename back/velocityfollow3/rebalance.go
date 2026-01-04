package velocityfollow

import (
	"context"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)

// rebalancePositions 补齐缺失的 leg，确保持仓平衡
// diff: UP 持仓 - DOWN 持仓（正数表示 UP 多，负数表示 DOWN 多）
// upSizeInt: UP 持仓数量（整数）
// downSizeInt: DOWN 持仓数量（整数）
func (s *Strategy) rebalancePositions(ctx context.Context, market *domain.Market, diff int, upSizeInt int, downSizeInt int) {
	if s == nil || s.TradingService == nil || market == nil {
		return
	}

	// 如果差异为 0，不需要补齐
	if diff == 0 {
		return
	}

	// 确定需要补齐的方向和数量
	var missingToken domain.TokenType
	var missingSize int
	var missingAsset string

	if diff > 0 {
		// UP 持仓多，需要补齐 DOWN
		missingToken = domain.TokenTypeDown
		missingSize = diff
		missingAsset = market.NoAssetID
		log.Infof("🔧 [%s] 需要补齐 DOWN 持仓: 差异=%d shares (UP=%d, DOWN=%d)",
			ID, missingSize, upSizeInt, downSizeInt)
	} else {
		// DOWN 持仓多，需要补齐 UP
		missingToken = domain.TokenTypeUp
		missingSize = -diff // 转为正数
		missingAsset = market.YesAssetID
		log.Infof("🔧 [%s] 需要补齐 UP 持仓: 差异=%d shares (UP=%d, DOWN=%d)",
			ID, missingSize, upSizeInt, downSizeInt)
	}

	// 获取订单簿价格
	rebalanceCtx, rebalanceCancel := context.WithTimeout(ctx, 5*time.Second)
	defer rebalanceCancel()

	_, yesAsk, _, noAsk, source, err := s.TradingService.GetTopOfBook(rebalanceCtx, market)
	if err != nil {
		log.Errorf("❌ [%s] 获取订单簿价格失败，无法补齐持仓: err=%v", ID, err)
		return
	}

	// 确定价格（卖一价，用于 FAK 吃单）
	var rebalancePrice domain.Price
	if missingToken == domain.TokenTypeUp {
		rebalancePrice = yesAsk
	} else {
		rebalancePrice = noAsk
	}

	rebalancePriceCents := rebalancePrice.ToCents()
	log.Infof("💰 [%s] 补齐持仓: token=%s size=%d shares 价格=%dc (source=%s)",
		ID, missingToken, missingSize, rebalancePriceCents, source)

	// 获取市场精度信息
	var rebalanceTickSize types.TickSize
	var rebalanceNegRisk *bool
	if s.currentPrecision != nil {
		if parsed, err := ParseTickSize(s.currentPrecision.TickSize); err == nil {
			rebalanceTickSize = parsed
		}
		rebalanceNegRisk = boolPtr(s.currentPrecision.NegRisk)
	}

	// 以卖一价下 FAK 订单（吃单补齐）
	rebalanceOrder := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      missingAsset,
		TokenType:    missingToken,
		Side:         types.SideBuy,
		Price:        rebalancePrice,
		Size:         float64(missingSize),
		OrderType:    types.OrderTypeFAK, // FAK：立即成交或取消
		IsEntryOrder: false,              // 这是补齐订单，不是 Entry
		Status:       domain.OrderStatusPending,
		TickSize:     rebalanceTickSize,
		NegRisk:      rebalanceNegRisk,
		CreatedAt:    time.Now(),
	}

	rebalanceResult, err := s.TradingService.PlaceOrder(rebalanceCtx, rebalanceOrder)
	if err != nil {
		log.Errorf("❌ [%s] 补齐持仓失败: err=%v (token=%s size=%d)", ID, err, missingToken, missingSize)
	} else if rebalanceResult != nil && rebalanceResult.OrderID != "" {
		log.Infof("✅ [%s] 补齐持仓订单已提交: orderID=%s token=%s size=%d 价格=%dc",
			ID, rebalanceResult.OrderID, missingToken, missingSize, rebalancePriceCents)
	}
}
