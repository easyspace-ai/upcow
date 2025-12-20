package services

import (
	"context"
	"fmt"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

// CancelOrdersNotInMarket 只管理本周期：取消所有 MarketSlug != currentSlug 的活跃订单（MarketSlug 为空也会取消）
func (s *TradingService) CancelOrdersNotInMarket(ctx context.Context, currentSlug string) {
	orders := s.GetActiveOrders()
	for _, o := range orders {
		if o == nil || o.OrderID == "" {
			continue
		}
		if currentSlug == "" {
			_ = s.CancelOrder(ctx, o.OrderID)
			continue
		}
		if o.MarketSlug == "" || o.MarketSlug != currentSlug {
			_ = s.CancelOrder(ctx, o.OrderID)
		}
	}
}

// CancelOrdersForMarket 取消指定 marketSlug 的活跃订单
func (s *TradingService) CancelOrdersForMarket(ctx context.Context, marketSlug string) {
	if marketSlug == "" {
		return
	}
	orders := s.GetActiveOrders()
	for _, o := range orders {
		if o == nil || o.OrderID == "" {
			continue
		}
		if o.MarketSlug == marketSlug {
			_ = s.CancelOrder(ctx, o.OrderID)
		}
	}
}

// GetActiveOrders 获取活跃订单（通过 OrderEngine 查询）
func (s *TradingService) GetActiveOrders() []*domain.Order {
	reply := make(chan *StateSnapshot, 1)
	cmd := &QueryStateCommand{
		id:    fmt.Sprintf("query_open_orders_%d", time.Now().UnixNano()),
		Query: QueryOpenOrders,
		Reply: reply,
	}

	s.orderEngine.SubmitCommand(cmd)

	select {
	case snapshot := <-reply:
		return snapshot.OpenOrders
	case <-time.After(5 * time.Second):
		return []*domain.Order{} // 超时返回空列表
	}
}
