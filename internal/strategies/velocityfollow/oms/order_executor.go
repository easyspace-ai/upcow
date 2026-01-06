package oms

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategies/velocityfollow/brain"
	"github.com/sirupsen/logrus"
)

var oeLog = logrus.WithField("module", "order_executor")

// OrderExecutor 订单执行器
type OrderExecutor struct {
	tradingService *services.TradingService
	config         ConfigInterface
	oms            *OMS // 反向引用，用于记录 pendingHedges
}

// NewOrderExecutor 创建新的订单执行器
func NewOrderExecutor(ts *services.TradingService, cfg ConfigInterface) *OrderExecutor {
	return &OrderExecutor{
		tradingService: ts,
		config:         cfg,
	}
}

// SetOMS 设置 OMS 引用（用于记录 pendingHedges）
func (oe *OrderExecutor) SetOMS(oms *OMS) {
	oe.oms = oms
}

// ExecuteSequential 顺序执行订单（先 Entry，后 Hedge）
func (oe *OrderExecutor) ExecuteSequential(ctx context.Context, market *domain.Market, decision *brain.Decision) error {
	if market == nil || decision == nil {
		return fmt.Errorf("参数无效")
	}

	// 1. 计算订单数量和价格
	entrySize, hedgeSize, entryPrice, hedgePrice, err := oe.calculateOrderParams(ctx, market, decision)
	if err != nil {
		return fmt.Errorf("计算订单参数失败: %w", err)
	}

	// 2. 下 Entry 订单（FAK）
	entryOrder, err := oe.placeEntryOrder(ctx, market, decision.Direction, entryPrice, entrySize)
	if err != nil {
		return fmt.Errorf("Entry 订单失败: %w", err)
	}

	if entryOrder == nil || entryOrder.OrderID == "" {
		return fmt.Errorf("Entry 订单创建失败")
	}

	oeLog.Debugf("✅ [OrderExecutor] Entry 订单已提交: orderID=%s direction=%s price=%.4f size=%.4f",
		entryOrder.OrderID, decision.Direction, entryPrice.ToDecimal(), entrySize)

	// 3. 等待 Entry 订单成交
	maxWaitMs := oe.config.GetSequentialMaxWaitMs()
	maxWait := time.Duration(maxWaitMs) * time.Millisecond
	entryFilled, err := oe.waitForOrderFill(ctx, entryOrder.OrderID, maxWait)
	if err != nil {
		return fmt.Errorf("等待 Entry 订单成交失败: %w", err)
	}

	if !entryFilled {
		oeLog.Warnf("⚠️ [OrderExecutor] Entry 订单未在指定时间内成交: orderID=%s", entryOrder.OrderID)
		return fmt.Errorf("Entry 订单未成交")
	}

	// 4. Entry 成交后，下 Hedge 订单（GTC）
	hedgeOrder, err := oe.placeHedgeOrder(ctx, market, decision.Direction, hedgePrice, hedgeSize)
	if err != nil {
		return fmt.Errorf("Hedge 订单失败: %w", err)
	}

	if hedgeOrder == nil || hedgeOrder.OrderID == "" {
		return fmt.Errorf("Hedge 订单创建失败")
	}

	// 记录 pendingHedge
	if oe.oms != nil {
		oe.oms.RecordPendingHedge(entryOrder.OrderID, hedgeOrder.OrderID)
	}

	oeLog.Debugf("✅ [OrderExecutor] Hedge 订单已提交: orderID=%s direction=%s price=%.4f size=%.4f",
		hedgeOrder.OrderID, decision.Direction, hedgePrice.ToDecimal(), hedgeSize)

	// 5. 启动对冲单重下监控（如果启用）
	if oe.oms != nil && oe.oms.hedgeReorder != nil {
		entryFilledTime := time.Now()
		if entryOrder.FilledAt != nil {
			entryFilledTime = *entryOrder.FilledAt
		}
		entryAskCents := entryOrder.Price.ToCents()
		if entryOrder.FilledPrice != nil {
			entryAskCents = entryOrder.FilledPrice.ToCents()
		}

		// 在 goroutine 中启动监控
		go oe.oms.hedgeReorder.MonitorAndReorderHedge(
			context.Background(), // 使用独立的 context
			market,
			entryOrder.OrderID,
			hedgeOrder.OrderID,
			hedgeOrder.AssetID,
			hedgePrice,
			hedgeSize,
			entryFilledTime,
			entryOrder.FilledSize,
			entryAskCents,
			decision.Direction,
		)
	}

	return nil
}

// ExecuteParallel 并发执行订单（同时提交 Entry 和 Hedge）
func (oe *OrderExecutor) ExecuteParallel(ctx context.Context, market *domain.Market, decision *brain.Decision) error {
	if market == nil || decision == nil {
		return fmt.Errorf("参数无效")
	}

	// 1. 计算订单数量和价格
	entrySize, hedgeSize, entryPrice, hedgePrice, err := oe.calculateOrderParams(ctx, market, decision)
	if err != nil {
		return fmt.Errorf("计算订单参数失败: %w", err)
	}

	// 2. 构建多腿请求
	var entryAssetID, hedgeAssetID string
	if decision.Direction == domain.TokenTypeUp {
		entryAssetID = market.YesAssetID
		hedgeAssetID = market.NoAssetID
	} else {
		entryAssetID = market.NoAssetID
		hedgeAssetID = market.YesAssetID
	}

	req := execution.MultiLegRequest{
		Name:       "velocityfollow_entry_hedge",
		MarketSlug: market.Slug,
		Legs: []execution.LegIntent{
			{
				Name:      "entry",
				AssetID:   entryAssetID,
				TokenType: decision.Direction,
				Side:      types.SideBuy,
				Price:     entryPrice,
				Size:      entrySize,
				OrderType: types.OrderTypeFAK, // Entry: FAK
			},
			{
				Name:      "hedge",
				AssetID:   hedgeAssetID,
				TokenType: getOppositeTokenType(decision.Direction),
				Side:      types.SideBuy,
				Price:     hedgePrice,
				Size:      hedgeSize,
				OrderType: types.OrderTypeGTC, // Hedge: GTC
			},
		},
		Hedge: execution.AutoHedgeConfig{Enabled: false}, // 不启用自动对冲
	}

	// 3. 执行多腿订单
	createdOrders, err := oe.tradingService.ExecuteMultiLeg(ctx, req)
	if err != nil {
		return fmt.Errorf("执行多腿订单失败: %w", err)
	}

	if len(createdOrders) < 2 {
		return fmt.Errorf("订单创建不完整: 期望2个，实际%d个", len(createdOrders))
	}

	// 记录 pendingHedge
	if oe.oms != nil && len(createdOrders) >= 2 {
		entryOrderID := createdOrders[0].OrderID
		hedgeOrderID := createdOrders[1].OrderID
		oe.oms.RecordPendingHedge(entryOrderID, hedgeOrderID)
	}

	oeLog.Debugf("✅ [OrderExecutor] 并发订单已提交: entryID=%s hedgeID=%s",
		createdOrders[0].OrderID, createdOrders[1].OrderID)

	return nil
}

// calculateOrderParams 计算订单参数（数量、价格）
func (oe *OrderExecutor) calculateOrderParams(
	ctx context.Context,
	market *domain.Market,
	decision *brain.Decision,
) (entrySize, hedgeSize float64, entryPrice, hedgePrice domain.Price, err error) {
	// 使用决策中的价格和数量
	entryPrice = decision.EntryPrice
	hedgePrice = decision.HedgePrice
	entrySize = decision.EntrySize
	hedgeSize = decision.HedgeSize

	// 确保 Entry 和 Hedge 数量相等（完全对冲）
	minSize := math.Min(entrySize, hedgeSize)
	entrySize = minSize
	hedgeSize = minSize

	// TODO: 考虑 minOrderSize、minShareSize 等限制
	// 这里简化处理，实际应该调用 TradingService 的相关方法进行调整

	return entrySize, hedgeSize, entryPrice, hedgePrice, nil
}

// placeEntryOrder 下 Entry 订单
func (oe *OrderExecutor) placeEntryOrder(
	ctx context.Context,
	market *domain.Market,
	direction domain.TokenType,
	price domain.Price,
	size float64,
) (*domain.Order, error) {
	var assetID string
	if direction == domain.TokenTypeUp {
		assetID = market.YesAssetID
	} else {
		assetID = market.NoAssetID
	}

	order := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      assetID,
		Side:         types.SideBuy,
		Price:        price,
		Size:         size,
		TokenType:    direction,
		IsEntryOrder: true,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
		OrderType:    types.OrderTypeFAK,
	}

	return oe.tradingService.PlaceOrder(ctx, order)
}

// placeHedgeOrder 下 Hedge 订单
func (oe *OrderExecutor) placeHedgeOrder(
	ctx context.Context,
	market *domain.Market,
	direction domain.TokenType,
	price domain.Price,
	size float64,
) (*domain.Order, error) {
	var assetID string
	hedgeDirection := getOppositeTokenType(direction)
	if hedgeDirection == domain.TokenTypeUp {
		assetID = market.YesAssetID
	} else {
		assetID = market.NoAssetID
	}

	order := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      assetID,
		Side:         types.SideBuy,
		Price:        price,
		Size:         size,
		TokenType:    hedgeDirection,
		IsEntryOrder: false,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
		OrderType:    types.OrderTypeGTC,
	}

	return oe.tradingService.PlaceOrder(ctx, order)
}

// waitForOrderFill 等待订单成交
func (oe *OrderExecutor) waitForOrderFill(ctx context.Context, orderID string, maxWait time.Duration) (bool, error) {
	checkInterval := time.Duration(oe.config.GetSequentialCheckIntervalMs()) * time.Millisecond
	deadline := time.Now().Add(maxWait)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}

		// 检查订单状态
		order, exists := oe.tradingService.GetOrder(orderID)
		if exists && order != nil && order.IsFilled() {
			return true, nil
		}

		time.Sleep(checkInterval)
	}

	return false, nil
}

// getOppositeTokenType 获取相反方向的 TokenType
func getOppositeTokenType(tokenType domain.TokenType) domain.TokenType {
	if tokenType == domain.TokenTypeUp {
		return domain.TokenTypeDown
	}
	return domain.TokenTypeUp
}
