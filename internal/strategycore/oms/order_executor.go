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
	"github.com/betbot/gobet/internal/strategycore/brain"
	"github.com/sirupsen/logrus"
)

var oeLog = logrus.WithField("module", "order_executor")

type OrderExecutor struct {
	tradingService *services.TradingService
	config         ConfigInterface
	oms            *OMS

	strategyID string
}

func NewOrderExecutor(ts *services.TradingService, cfg ConfigInterface, strategyID string) *OrderExecutor {
	return &OrderExecutor{
		tradingService: ts,
		config:         cfg,
		strategyID:     strategyID,
	}
}

func (oe *OrderExecutor) SetOMS(oms *OMS) { oe.oms = oms }

func (oe *OrderExecutor) ExecuteSequential(ctx context.Context, market *domain.Market, decision *brain.Decision) error {
	if market == nil || decision == nil {
		return fmt.Errorf("参数无效")
	}

	entrySize, hedgeSize, entryPrice, hedgePrice, err := oe.calculateOrderParams(ctx, market, decision)
	if err != nil {
		return fmt.Errorf("计算订单参数失败: %w", err)
	}

	entryOrder, err := oe.placeEntryOrder(ctx, market, decision.Direction, entryPrice, entrySize)
	if err != nil {
		return fmt.Errorf("Entry 订单失败: %w", err)
	}
	if entryOrder == nil || entryOrder.OrderID == "" {
		return fmt.Errorf("Entry 订单创建失败")
	}

	oeLog.Debugf("✅ [OrderExecutor] Entry 订单已提交: orderID=%s direction=%s price=%.4f size=%.4f",
		entryOrder.OrderID, decision.Direction, entryPrice.ToDecimal(), entrySize)

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

	hedgeOrder, err := oe.placeHedgeOrder(ctx, market, decision.Direction, hedgePrice, hedgeSize)
	if err != nil {
		return fmt.Errorf("Hedge 订单失败: %w", err)
	}
	if hedgeOrder == nil || hedgeOrder.OrderID == "" {
		return fmt.Errorf("Hedge 订单创建失败")
	}

	if oe.oms != nil {
		oe.oms.RecordPendingHedge(entryOrder.OrderID, hedgeOrder.OrderID)
	}

	oeLog.Debugf("✅ [OrderExecutor] Hedge 订单已提交: orderID=%s direction=%s price=%.4f size=%.4f",
		hedgeOrder.OrderID, decision.Direction, hedgePrice.ToDecimal(), hedgeSize)

	if oe.oms != nil && oe.oms.hedgeReorder != nil {
		entryFilledTime := time.Now()
		if entryOrder.FilledAt != nil {
			entryFilledTime = *entryOrder.FilledAt
		}
		entryAskCents := entryOrder.Price.ToCents()
		if entryOrder.FilledPrice != nil {
			entryAskCents = entryOrder.FilledPrice.ToCents()
		}

		go oe.oms.hedgeReorder.MonitorAndReorderHedge(
			context.Background(),
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

func (oe *OrderExecutor) ExecuteParallel(ctx context.Context, market *domain.Market, decision *brain.Decision) error {
	if market == nil || decision == nil {
		return fmt.Errorf("参数无效")
	}

	entrySize, hedgeSize, entryPrice, hedgePrice, err := oe.calculateOrderParams(ctx, market, decision)
	if err != nil {
		return fmt.Errorf("计算订单参数失败: %w", err)
	}

	var entryAssetID, hedgeAssetID string
	if decision.Direction == domain.TokenTypeUp {
		entryAssetID = market.YesAssetID
		hedgeAssetID = market.NoAssetID
	} else {
		entryAssetID = market.NoAssetID
		hedgeAssetID = market.YesAssetID
	}

	namePrefix := oe.strategyID
	if namePrefix == "" {
		namePrefix = "strategy"
	}

	req := execution.MultiLegRequest{
		Name:       namePrefix + "_entry_hedge",
		MarketSlug: market.Slug,
		Legs: []execution.LegIntent{
			{
				Name:      "entry",
				AssetID:   entryAssetID,
				TokenType: decision.Direction,
				Side:      types.SideBuy,
				Price:     entryPrice,
				Size:      entrySize,
				OrderType: types.OrderTypeFAK,
			},
			{
				Name:      "hedge",
				AssetID:   hedgeAssetID,
				TokenType: getOppositeTokenType(decision.Direction),
				Side:      types.SideBuy,
				Price:     hedgePrice,
				Size:      hedgeSize,
				OrderType: types.OrderTypeGTC,
			},
		},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}

	var createdOrders []*domain.Order
	if oe.oms != nil {
		createdOrders, err = oe.oms.executeMultiLeg(ctx, req)
	} else {
		createdOrders, err = oe.tradingService.ExecuteMultiLeg(ctx, req)
	}
	if err != nil {
		return fmt.Errorf("执行多腿订单失败: %w", err)
	}
	if len(createdOrders) < 2 {
		return fmt.Errorf("订单创建不完整: 期望2个，实际%d个", len(createdOrders))
	}

	if oe.oms != nil && len(createdOrders) >= 2 {
		entryOrderID := createdOrders[0].OrderID
		hedgeOrderID := createdOrders[1].OrderID
		oe.oms.RecordPendingHedge(entryOrderID, hedgeOrderID)
	}

	oeLog.Debugf("✅ [OrderExecutor] 并发订单已提交: entryID=%s hedgeID=%s",
		createdOrders[0].OrderID, createdOrders[1].OrderID)
	return nil
}

func (oe *OrderExecutor) calculateOrderParams(
	ctx context.Context,
	market *domain.Market,
	decision *brain.Decision,
) (entrySize, hedgeSize float64, entryPrice, hedgePrice domain.Price, err error) {
	_ = ctx
	_ = market

	entryPrice = decision.EntryPrice
	hedgePrice = decision.HedgePrice
	entrySize = decision.EntrySize
	hedgeSize = decision.HedgeSize

	minSize := math.Min(entrySize, hedgeSize)
	entrySize = minSize
	hedgeSize = minSize
	return entrySize, hedgeSize, entryPrice, hedgePrice, nil
}

func (oe *OrderExecutor) placeEntryOrder(ctx context.Context, market *domain.Market, direction domain.TokenType, price domain.Price, size float64) (*domain.Order, error) {
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
	if oe.oms != nil {
		return oe.oms.placeOrder(ctx, order)
	}
	return oe.tradingService.PlaceOrder(ctx, order)
}

func (oe *OrderExecutor) placeHedgeOrder(ctx context.Context, market *domain.Market, direction domain.TokenType, price domain.Price, size float64) (*domain.Order, error) {
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
	if oe.oms != nil {
		return oe.oms.placeOrder(ctx, order)
	}
	return oe.tradingService.PlaceOrder(ctx, order)
}

func (oe *OrderExecutor) waitForOrderFill(ctx context.Context, orderID string, maxWait time.Duration) (bool, error) {
	checkInterval := time.Duration(oe.config.GetSequentialCheckIntervalMs()) * time.Millisecond
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}
		order, exists := oe.tradingService.GetOrder(orderID)
		if exists && order != nil && order.IsFilled() {
			return true, nil
		}
		time.Sleep(checkInterval)
	}
	return false, nil
}

func getOppositeTokenType(tokenType domain.TokenType) domain.TokenType {
	if tokenType == domain.TokenTypeUp {
		return domain.TokenTypeDown
	}
	return domain.TokenTypeUp
}

