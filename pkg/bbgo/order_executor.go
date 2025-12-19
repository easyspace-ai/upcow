package bbgo

import (
	"context"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
)

// OrderExecutor 订单执行器接口（BBGO风格）
// 策略通过此接口提交和取消订单
type OrderExecutor interface {
	SubmitOrders(ctx context.Context, orders ...domain.Order) ([]*domain.Order, error)
	CancelOrders(ctx context.Context, orders ...*domain.Order) error
}

// TradingServiceOrderExecutor 基于 TradingService 的订单执行器实现
type TradingServiceOrderExecutor struct {
	tradingService *services.TradingService
}

// NewTradingServiceOrderExecutor 创建基于 TradingService 的订单执行器
func NewTradingServiceOrderExecutor(tradingService *services.TradingService) *TradingServiceOrderExecutor {
	return &TradingServiceOrderExecutor{
		tradingService: tradingService,
	}
}

// SubmitOrders 提交订单
func (e *TradingServiceOrderExecutor) SubmitOrders(ctx context.Context, orders ...domain.Order) ([]*domain.Order, error) {
	createdOrders := make([]*domain.Order, 0, len(orders))
	for _, order := range orders {
		// 创建订单副本（因为 PlaceOrder 需要指针）
		orderCopy := order
		createdOrder, err := e.tradingService.PlaceOrder(ctx, &orderCopy)
		if err != nil {
			return createdOrders, err
		}
		createdOrders = append(createdOrders, createdOrder)
	}
	return createdOrders, nil
}

// CancelOrders 取消订单
func (e *TradingServiceOrderExecutor) CancelOrders(ctx context.Context, orders ...*domain.Order) error {
	for _, order := range orders {
		if err := e.tradingService.CancelOrder(ctx, order.OrderID); err != nil {
			return err
		}
	}
	return nil
}

