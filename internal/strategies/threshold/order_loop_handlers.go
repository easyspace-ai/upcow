package threshold

import (
	"context"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/pkg/logger"
)

// OnOrderUpdate 订单更新回调：入队到策略 loop。
func (s *ThresholdStrategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	if order == nil {
		return nil
	}
	s.startLoop(ctx)
	select {
	case s.orderC <- order:
	default:
		logger.Warnf("价格阈值策略: orderC 已满，丢弃订单更新: orderID=%s status=%s", order.OrderID, order.Status)
	}
	return nil
}

func (s *ThresholdStrategy) handleOrderUpdateInternal(ctx context.Context, order *domain.Order) error {
	// 如果我们已经知道当前 market，则只处理属于该 market 的资产订单，避免跨策略污染
	if s.currentMarket != nil {
		upID := s.currentMarket.GetAssetID(domain.TokenTypeUp)
		downID := s.currentMarket.GetAssetID(domain.TokenTypeDown)
		if order.AssetID != upID && order.AssetID != downID {
			return nil
		}
	}

	// 根据订单状态更新 pending 标记
	switch order.Status {
	case domain.OrderStatusCanceled, domain.OrderStatusFailed:
		if order.Side == types.SideBuy && order.IsEntryOrder {
			s.pendingEntry = false
		}
		if order.Side == types.SideSell {
			s.pendingExit = false
		}
		return nil
	case domain.OrderStatusFilled:
		// 先清理 pending，再触发成交事件
		if order.Side == types.SideBuy && order.IsEntryOrder {
			s.pendingEntry = false
		}
		if order.Side == types.SideSell {
			s.pendingExit = false
		}

		// 生成 OrderFilledEvent 并复用既有逻辑
		ev := &events.OrderFilledEvent{
			Order:  order,
			Market: s.currentMarket,
		}
		return s.OnOrderFilled(ctx, ev)
	default:
		// open/pending：保持 pending
		return nil
	}
}

func (s *ThresholdStrategy) handleCmdResultInternal(_ context.Context, res thresholdCmdResult) error {
	// 命令提交失败时，及时解除 pending，避免卡死
	if res.err != nil {
		if res.order != nil {
			if res.order.Side == types.SideBuy && res.order.IsEntryOrder {
				s.pendingEntry = false
			}
			if res.order.Side == types.SideSell {
				s.pendingExit = false
			}
		}
		return nil
	}
	// 成功创建订单：等待后续订单更新驱动状态转换
	return nil
}

