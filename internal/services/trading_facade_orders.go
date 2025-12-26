package services

import (
	"context"
	"fmt"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)

// Facade methods: keep TradingService public API stable.

func (s *TradingService) PlaceOrder(ctx context.Context, order *domain.Order) (*domain.Order, error) {
	if s.orders == nil {
		return nil, fmt.Errorf("orders service not initialized")
	}
	// 防御性校验：即使绕过 OrdersService 直接调用 TradingService.PlaceOrder，
	// 也必须遵守系统级 fail-safe（暂停模式 + 当前 market 一致性）。
	// 注意：OrdersService.PlaceOrder 内部也会做同样校验；这里是“更早失败 + 更清晰错误”。
	if s.isTradingPaused() {
		return nil, fmt.Errorf("trading paused: refusing to place order")
	}
	cur := s.GetCurrentMarket()
	if order == nil || order.MarketSlug == "" || cur == "" || order.MarketSlug != cur {
		return nil, fmt.Errorf("order market mismatch (refuse to trade): current=%s order=%v", cur, func() string {
			if order == nil {
				return "<nil>"
			}
			return order.MarketSlug
		}())
	}
	return s.orders.PlaceOrder(ctx, order)
}

func (s *TradingService) CancelOrder(ctx context.Context, orderID string) error {
	if s.orders == nil {
		return fmt.Errorf("orders service not initialized")
	}
	return s.orders.CancelOrder(ctx, orderID)
}

func (s *TradingService) GetBestPrice(ctx context.Context, assetID string) (bestBid float64, bestAsk float64, err error) {
	if s.orders == nil {
		return 0, 0, fmt.Errorf("orders service not initialized")
	}
	return s.orders.GetBestPrice(ctx, assetID)
}

func (s *TradingService) GetTopOfBook(ctx context.Context, market *domain.Market) (yesBid, yesAsk, noBid, noAsk domain.Price, source string, err error) {
	if s.orders == nil {
		return domain.Price{}, domain.Price{}, domain.Price{}, domain.Price{}, "", fmt.Errorf("orders service not initialized")
	}
	return s.orders.GetTopOfBook(ctx, market)
}

func (s *TradingService) GetMarketQuality(ctx context.Context, market *domain.Market, opt *MarketQualityOptions) (*MarketQuality, error) {
	if s.orders == nil {
		return nil, fmt.Errorf("orders service not initialized")
	}
	return s.orders.GetMarketQuality(ctx, market, opt)
}

// CheckOrderBookLiquidity 检查订单簿是否有足够的流动性来匹配订单
// 返回: (是否有流动性, 实际可用价格, 可用数量)
func (s *TradingService) CheckOrderBookLiquidity(ctx context.Context, assetID string, side types.Side, price float64, size float64) (bool, float64, float64) {
	if s.orders == nil {
		return false, 0, 0
	}
	return s.orders.CheckOrderBookLiquidity(ctx, assetID, side, price, size)
}

// GetOrderBook 获取订单簿完整信息（包括 tick_size 和 min_order_size）
func (s *TradingService) GetOrderBook(ctx context.Context, assetID string) (*types.OrderBookSummary, error) {
	if s.clobClient == nil {
		return nil, fmt.Errorf("clob client not initialized")
	}
	return s.clobClient.GetOrderBook(ctx, assetID, nil)
}

// GetSecondLevelPrice 获取订单簿的第二档价格（卖二价或买二价）
// 对于买入订单：返回卖二价（asks[1]），如果不存在则返回卖一价（asks[0]）
// 对于卖出订单：返回买二价（bids[1]），如果不存在则返回买一价（bids[0]）
// 返回: (价格, 是否存在第二档)
func (s *TradingService) GetSecondLevelPrice(ctx context.Context, assetID string, side types.Side) (float64, bool) {
	if s.orders == nil {
		return 0, false
	}
	return s.orders.GetSecondLevelPrice(ctx, assetID, side)
}
