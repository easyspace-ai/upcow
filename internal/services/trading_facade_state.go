package services

import (
	"fmt"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

// GetOrder 通过 OrderEngine 查询订单（包含已成交/已取消/失败）。
// 返回值:
// - (*domain.Order, true): 找到订单
// - (nil, false): 未找到或查询超时
func (s *TradingService) GetOrder(orderID string) (*domain.Order, bool) {
	if s == nil || s.orderEngine == nil || orderID == "" {
		return nil, false
	}
	reply := make(chan *StateSnapshot, 1)
	s.orderEngine.SubmitCommand(&QueryStateCommand{
		id:      fmt.Sprintf("query_order_%s_%d", orderID, time.Now().UnixNano()),
		Query:   QueryOrder,
		OrderID: orderID,
		Reply:   reply,
	})
	select {
	case snapshot := <-reply:
		if snapshot != nil && snapshot.Order != nil {
			return snapshot.Order, true
		}
		return nil, false
	case <-time.After(2 * time.Second):
		return nil, false
	}
}

