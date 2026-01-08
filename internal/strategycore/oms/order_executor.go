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
	
	// 兜底检查：即使 waitForOrderFill 返回 false，也检查订单实际状态
	// 这可以处理订单在等待开始前就已经通过 WebSocket 快速成交的情况
	if !entryFilled {
		// 重新获取订单状态，确保使用最新数据
		latestOrder, exists := oe.tradingService.GetOrder(entryOrder.OrderID)
		if exists && latestOrder.IsFilled() {
			oeLog.Infof("✅ [OrderExecutor] Entry 订单已成交（兜底检查发现）: orderID=%s filledSize=%.4f", 
				entryOrder.OrderID, latestOrder.FilledSize)
			entryOrder = latestOrder // 使用最新的订单数据
			entryFilled = true
		} else {
			oeLog.Warnf("⚠️ [OrderExecutor] Entry 订单未在指定时间内成交: orderID=%s", entryOrder.OrderID)
			return fmt.Errorf("Entry 订单未成交")
		}
	}

	// 顺序模式：以“实际成交数量”对冲（更像职业交易：避免 FAK 部分成交导致过度对冲）
	filledSize := entryOrder.FilledSize
	if filledSize <= 0 {
		filledSize = entrySize
	}
	hedgeSize = filledSize

	// 顺序模式：根据当前盘口/允许负收益配置，调整初始 hedge 定价，提高对冲完成率
	hedgePrice = oe.calcInitialHedgePrice(ctx, market, decision.Direction, entryOrder, hedgePrice)

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

	// 并发模式：用当前盘口对初始 hedge 做一次更积极定价（仍由后续 HedgeReorder/RiskManager 兜底）
	hedgePrice = oe.calcInitialHedgePrice(ctx, market, decision.Direction, nil, hedgePrice)

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
				IsEntry:   true,
			},
			{
				Name:      "hedge",
				AssetID:   hedgeAssetID,
				TokenType: getOppositeTokenType(decision.Direction),
				Side:      types.SideBuy,
				Price:     hedgePrice,
				Size:      hedgeSize,
				// 由执行引擎兜底：若 size<5，会自动改用 FAK，避免强制放大到 5 shares
				OrderType: types.OrderTypeGTC,
				IsEntry:   false,
				BypassRiskOff: true,
				DisableSizeAdjust: true,
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

// calcInitialHedgePrice 依据当前订单簿与“允许负收益”配置，生成更“容易成交”的初始 hedge 限价。
// - entryOrder 可为 nil（并发模式）；顺序模式建议传入以使用实际成交价/成交数量。
func (oe *OrderExecutor) calcInitialHedgePrice(ctx context.Context, market *domain.Market, direction domain.TokenType, entryOrder *domain.Order, fallback domain.Price) domain.Price {
	if oe == nil || oe.tradingService == nil || oe.config == nil || market == nil {
		return fallback
	}

	// entry cost cents：优先用成交价
	entryAskCents := 0
	if entryOrder != nil {
		entryAskCents = entryOrder.Price.ToCents()
		if entryOrder.FilledPrice != nil {
			entryAskCents = entryOrder.FilledPrice.ToCents()
		}
	}
	if entryAskCents <= 0 {
		entryAskCents = fallback.ToCents()
	}
	if entryAskCents <= 0 {
		return fallback
	}

	// 当前 hedge side 的 ask
	_, yesAsk, _, noAsk, _, err := oe.tradingService.GetTopOfBook(ctx, market)
	if err != nil {
		return fallback
	}
	marketAskCents := 0
	if direction == domain.TokenTypeUp {
		// hedge 买 NO
		marketAskCents = noAsk.ToCents()
	} else {
		// hedge 买 YES
		marketAskCents = yesAsk.ToCents()
	}

	ideal := 100 - entryAskCents - oe.config.GetHedgeOffsetCents()
	if ideal < 1 {
		ideal = 1
	}
	if ideal > 99 {
		ideal = 99
	}

	newLimit := ideal
	if oe.config.GetAllowNegativeProfitOnHedgeReorder() {
		extra := 0
		if oe.oms != nil && market != nil {
			extra = oe.oms.hedgePriceExtraCents(market.Slug)
		}
		maxAllowed := ideal + oe.config.GetMaxNegativeProfitCents() + extra
		if maxAllowed < 1 {
			maxAllowed = 1
		}
		if maxAllowed > 99 {
			maxAllowed = 99
		}
		if marketAskCents > 0 && marketAskCents <= maxAllowed {
			newLimit = marketAskCents
		} else {
			newLimit = maxAllowed
		}
	} else {
		if marketAskCents > 0 && newLimit >= marketAskCents {
			newLimit = marketAskCents - 1
		}
		if newLimit < 1 {
			newLimit = 1
		}
	}

	return domain.Price{Pips: newLimit * 100}
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

	// Polymarket 对 GTC 限价单常见最小 size=5 shares。
	// 顺序模式下 hedgeSize 取自 entry 的实际成交量，可能小于 5；
	// 这里对小额 hedge 直接用 FAK，避免被系统/交易所强制放大到 5 引发过度对冲。
	orderType := types.OrderTypeGTC
	const minGTCShareSize = 5.0
	if size < minGTCShareSize {
		orderType = types.OrderTypeFAK
	}

	order := &domain.Order{
		MarketSlug:      market.Slug,
		AssetID:         assetID,
		Side:            types.SideBuy,
		Price:           price,
		Size:            size,
		TokenType:       hedgeDirection,
		IsEntryOrder:    false,
		Status:          domain.OrderStatusPending,
		CreatedAt:       time.Now(),
		OrderType:       orderType,
		DisableSizeAdjust: true, // ✅ 严格一对一：避免系统自动放大 size，确保对冲数量与 Entry 成交数量一致
		BypassRiskOff:   true,  // 风控动作：避免 risk-off 期间出不了场
	}
	if oe.oms != nil {
		return oe.oms.placeOrder(ctx, order)
	}
	return oe.tradingService.PlaceOrder(ctx, order)
}

func (oe *OrderExecutor) waitForOrderFill(ctx context.Context, orderID string, maxWait time.Duration) (bool, error) {
	// 在开始等待前先检查一次订单状态，处理订单已经快速成交的情况
	order, exists := oe.tradingService.GetOrder(orderID)
	if exists && order.IsFilled() {
		oeLog.Debugf("✅ [OrderExecutor] Entry 订单已成交（等待前检查）: orderID=%s", orderID)
		return true, nil
	}

	checkInterval := time.Duration(oe.config.GetSequentialCheckIntervalMs()) * time.Millisecond
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}
		order, exists := oe.tradingService.GetOrder(orderID)
		if exists && order.IsFilled() {
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

