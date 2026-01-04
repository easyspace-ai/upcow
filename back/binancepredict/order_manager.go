package binancepredict

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/sirupsen/logrus"
)

var orderManagerLog = logrus.WithField("component", "binancepredict_order_manager")

// PendingTrade 待处理的交易（Entry + Hedge）
type PendingTrade struct {
	EntryOrderID   string
	HedgeOrderID   string
	Direction      PredictionDirection
	EntryToken     domain.TokenType
	HedgeToken     domain.TokenType
	EntryPrice     domain.Price
	HedgePrice     domain.Price
	EntrySize      float64
	HedgeSize      float64
	CreatedAt      time.Time
	HedgeTimeoutAt time.Time
}

// OrderManager 订单管理器
type OrderManager struct {
	tradingService *services.TradingService
	config         Config

	// 待处理的交易
	pendingTrades map[string]*PendingTrade // key: entryOrderID
	mu            sync.Mutex

	// 监控 goroutine
	monitoring map[string]bool // key: marketSlug
	monitorMu  sync.Mutex
}

// NewOrderManager 创建新的订单管理器
func NewOrderManager(tradingService *services.TradingService, config Config) *OrderManager {
	return &OrderManager{
		tradingService: tradingService,
		config:         config,
		pendingTrades:  make(map[string]*PendingTrade),
		monitoring:     make(map[string]bool),
	}
}

// ExecuteTrade 执行交易（Entry + Hedge）
// direction: UP 表示预测上涨（买入 UP，卖出 DOWN），DOWN 表示预测下跌（买入 DOWN，卖出 UP）
func (om *OrderManager) ExecuteTrade(ctx context.Context, market *domain.Market, direction PredictionDirection, upBid, upAsk, downBid, downAsk domain.Price) error {
	if market == nil {
		return fmt.Errorf("market 不能为空")
	}

	// 验证镜像关系
	if !om.validateMirrorPrice(upAsk, downBid) {
		return fmt.Errorf("镜像价格验证失败: upAsk=%dc downBid=%dc", upAsk.ToCents(), downBid.ToCents())
	}

	var entryToken, hedgeToken domain.TokenType
	var entryPrice, hedgePrice domain.Price
	var entryAssetID, hedgeAssetID string

	if direction == DirectionUp {
		// 预测上涨：买入 UP（Taker），卖出 DOWN（Maker）
		entryToken = domain.TokenTypeUp
		hedgeToken = domain.TokenTypeDown
		entryAssetID = market.YesAssetID
		hedgeAssetID = market.NoAssetID

		// Entry: 在 UP Ask 价格吃单（加上偏移）
		entryPriceCents := upAsk.ToCents() + om.config.EntryPriceOffsetCents
		if entryPriceCents < 1 || entryPriceCents > 99 {
			return fmt.Errorf("Entry 价格超出范围: %dc", entryPriceCents)
		}
		entryPrice = domain.PriceFromDecimal(float64(entryPriceCents) / 100.0)

		// Hedge: 在 DOWN Bid 价格挂单（减去偏移，确保利润）
		hedgePriceCents := downBid.ToCents() + om.config.HedgePriceOffsetCents
		if hedgePriceCents < 1 || hedgePriceCents > 99 {
			return fmt.Errorf("Hedge 价格超出范围: %dc", hedgePriceCents)
		}
		hedgePrice = domain.PriceFromDecimal(float64(hedgePriceCents) / 100.0)

		// 验证利润
		totalCostCents := entryPriceCents + hedgePriceCents
		if totalCostCents >= 100-om.config.MinProfitCents {
			return fmt.Errorf("利润不足: totalCost=%dc minProfit=%dc", totalCostCents, om.config.MinProfitCents)
		}
	} else if direction == DirectionDown {
		// 预测下跌：买入 DOWN（Taker），卖出 UP（Maker）
		entryToken = domain.TokenTypeDown
		hedgeToken = domain.TokenTypeUp
		entryAssetID = market.NoAssetID
		hedgeAssetID = market.YesAssetID

		// Entry: 在 DOWN Ask 价格吃单
		entryPriceCents := downAsk.ToCents() + om.config.EntryPriceOffsetCents
		if entryPriceCents < 1 || entryPriceCents > 99 {
			return fmt.Errorf("Entry 价格超出范围: %dc", entryPriceCents)
		}
		entryPrice = domain.PriceFromDecimal(float64(entryPriceCents) / 100.0)

		// Hedge: 在 UP Bid 价格挂单
		hedgePriceCents := upBid.ToCents() + om.config.HedgePriceOffsetCents
		if hedgePriceCents < 1 || hedgePriceCents > 99 {
			return fmt.Errorf("Hedge 价格超出范围: %dc", hedgePriceCents)
		}
		hedgePrice = domain.PriceFromDecimal(float64(hedgePriceCents) / 100.0)

		// 验证利润
		totalCostCents := entryPriceCents + hedgePriceCents
		if totalCostCents >= 100-om.config.MinProfitCents {
			return fmt.Errorf("利润不足: totalCost=%dc minProfit=%dc", totalCostCents, om.config.MinProfitCents)
		}
	} else {
		return fmt.Errorf("无效的预测方向: %s", direction)
	}

	// 先挂 Hedge 单（Maker，GTC）
	hedgeOrder := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      hedgeAssetID,
		TokenType:    hedgeToken,
		Side:         types.SideSell,
		Price:        hedgePrice,
		Size:         om.config.OrderSize,
		OrderType:    types.OrderTypeGTC,
		IsEntryOrder: false,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
		SkipBalanceCheck: om.config.SkipBalanceCheck,
	}

	hedgeOrderResult, err := om.tradingService.PlaceOrder(ctx, hedgeOrder)
	if err != nil {
		return fmt.Errorf("挂 Hedge 单失败: %w", err)
	}

	// 计算预期利润
	totalCostCents := entryPrice.ToCents() + hedgePrice.ToCents()
	expectedProfitCents := 100 - totalCostCents

	orderManagerLog.Infof("✅ [%s] Hedge 单已挂: orderID=%s token=%s price=%dc size=%.4f",
		ID, hedgeOrderResult.OrderID, hedgeToken, hedgePrice.ToCents(), om.config.OrderSize)

	// 再下 Entry 单（Taker，FAK）
	entryOrder := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      entryAssetID,
		TokenType:    entryToken,
		Side:         types.SideBuy,
		Price:        entryPrice,
		Size:         om.config.OrderSize,
		OrderType:    types.OrderTypeFAK,
		IsEntryOrder: true,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
		SkipBalanceCheck: om.config.SkipBalanceCheck,
	}

	entryOrderResult, err := om.tradingService.PlaceOrder(ctx, entryOrder)
	if err != nil {
		// Entry 失败，取消 Hedge 单
		_ = om.tradingService.CancelOrder(ctx, hedgeOrderResult.OrderID)
		return fmt.Errorf("下 Entry 单失败: %w", err)
	}

	orderManagerLog.Infof("✅ [%s] Entry 单已下: orderID=%s token=%s price=%dc size=%.4f",
		ID, entryOrderResult.OrderID, entryToken, entryPrice.ToCents(), om.config.OrderSize)
	orderManagerLog.Infof("💰 [%s] 预期利润: entry=%dc + hedge=%dc = %dc, 利润=%dc (要求>=%dc)",
		ID, entryPrice.ToCents(), hedgePrice.ToCents(), totalCostCents, expectedProfitCents, om.config.MinProfitCents)

	// 记录待处理的交易
	pendingTrade := &PendingTrade{
		EntryOrderID:   entryOrderResult.OrderID,
		HedgeOrderID:   hedgeOrderResult.OrderID,
		Direction:      direction,
		EntryToken:     entryToken,
		HedgeToken:     hedgeToken,
		EntryPrice:     entryPrice,
		HedgePrice:     hedgePrice,
		EntrySize:      om.config.OrderSize,
		HedgeSize:      om.config.OrderSize,
		CreatedAt:      time.Now(),
		HedgeTimeoutAt: time.Now().Add(time.Duration(om.config.HedgeTimeoutSeconds) * time.Second),
	}

	om.mu.Lock()
	om.pendingTrades[entryOrderResult.OrderID] = pendingTrade
	om.mu.Unlock()

	// 启动监控（如果尚未启动）
	om.startMonitoringIfNeeded(ctx, market.Slug)

	return nil
}

// validateMirrorPrice 验证镜像价格关系
func (om *OrderManager) validateMirrorPrice(upAsk, downBid domain.Price) bool {
	mirrorSum := upAsk.ToCents() + downBid.ToCents()
	deviation := 100 - mirrorSum
	if deviation < 0 {
		deviation = -deviation
	}
	return deviation <= om.config.MaxMirrorDeviationCents
}

// startMonitoringIfNeeded 如果需要，启动监控 goroutine
func (om *OrderManager) startMonitoringIfNeeded(ctx context.Context, marketSlug string) {
	om.monitorMu.Lock()
	defer om.monitorMu.Unlock()

	if om.monitoring[marketSlug] {
		return
	}
	om.monitoring[marketSlug] = true

	go om.monitorPendingTrades(ctx, marketSlug)
}

// monitorPendingTrades 监控待处理的交易
func (om *OrderManager) monitorPendingTrades(ctx context.Context, marketSlug string) {
	ticker := time.NewTicker(time.Duration(om.config.HedgeTimeoutSeconds/2) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			om.checkPendingTrades(ctx, marketSlug)
		}
	}
}

// checkPendingTrades 检查待处理的交易
func (om *OrderManager) checkPendingTrades(ctx context.Context, marketSlug string) {
	om.mu.Lock()
	trades := make([]*PendingTrade, 0)
	for _, trade := range om.pendingTrades {
		// 只检查当前市场的交易
		// 这里简化处理，实际应该通过 marketSlug 过滤
		if time.Now().After(trade.HedgeTimeoutAt) {
			trades = append(trades, trade)
		}
	}
	om.mu.Unlock()

	for _, trade := range trades {
		om.handleHedgeTimeout(ctx, trade)
	}
}

// handleHedgeTimeout 处理 Hedge 超时
func (om *OrderManager) handleHedgeTimeout(ctx context.Context, trade *PendingTrade) {
	if !om.config.EnableStopLoss {
		orderManagerLog.Debugf("⏸️ [%s] Hedge 超时但止损未启用: entryOrderID=%s hedgeOrderID=%s",
			ID, trade.EntryOrderID, trade.HedgeOrderID)
		return
	}

	timeoutDuration := time.Since(trade.CreatedAt)
	orderManagerLog.Warnf("⚠️ [%s] Hedge 超时: entryOrderID=%s hedgeOrderID=%s timeout=%v (限制=%ds)",
		ID, trade.EntryOrderID, trade.HedgeOrderID, timeoutDuration, om.config.HedgeTimeoutSeconds)

	// 取消 Hedge 单
	if err := om.tradingService.CancelOrder(ctx, trade.HedgeOrderID); err != nil {
		orderManagerLog.Errorf("❌ [%s] 取消 Hedge 单失败: orderID=%s err=%v",
			ID, trade.HedgeOrderID, err)
	} else {
		orderManagerLog.Infof("✅ [%s] Hedge 单已取消: orderID=%s", ID, trade.HedgeOrderID)
	}

	// 如果 Entry 已成交，需要平仓止损
	// 这里简化处理，实际应该检查订单状态
	// TODO: 实现完整的止损逻辑（检查 Entry 订单状态，如果已成交则平仓）

	// 从待处理列表中移除
	om.mu.Lock()
	delete(om.pendingTrades, trade.EntryOrderID)
	om.mu.Unlock()
}

// OnOrderUpdate 订单更新回调
func (om *OrderManager) OnOrderUpdate(order *domain.Order) {
	if order == nil {
		return
	}

	om.mu.Lock()
	trade, exists := om.pendingTrades[order.OrderID]
	if !exists {
		// 可能是 Hedge 单更新，需要反向查找
		for _, t := range om.pendingTrades {
			if t.HedgeOrderID == order.OrderID {
				trade = t
				exists = true
				break
			}
		}
	}
	om.mu.Unlock()

	if !exists {
		return
	}

	// 检查是否完全对冲
	if order.Status == domain.OrderStatusFilled {
		om.checkHedgedStatus(trade)
	}
}

// checkHedgedStatus 检查对冲状态
func (om *OrderManager) checkHedgedStatus(trade *PendingTrade) {
	// TODO: 实现完整的对冲状态检查
	// 如果 Entry 和 Hedge 都已成交，从待处理列表中移除
	
	// 计算实际利润
	totalCostCents := trade.EntryPrice.ToCents() + trade.HedgePrice.ToCents()
	actualProfitCents := 100 - totalCostCents
	profitPercent := float64(actualProfitCents) / 100.0 * 100.0

	orderManagerLog.Infof("✅ [%s] 交易完成对冲: entryOrderID=%s hedgeOrderID=%s direction=%s profit=%dc (%.2f%%)",
		ID, trade.EntryOrderID, trade.HedgeOrderID, trade.Direction, actualProfitCents, profitPercent)

	om.mu.Lock()
	delete(om.pendingTrades, trade.EntryOrderID)
	om.mu.Unlock()
}
