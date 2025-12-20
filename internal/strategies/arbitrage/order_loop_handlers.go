package arbitrage

import (
	"context"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/pkg/logger"
)

func (s *ArbitrageStrategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	if order == nil {
		return nil
	}
	s.startLoop(ctx)
	select {
	case s.orderC <- order:
	default:
		logger.Warnf("套利策略: orderC 已满，丢弃订单更新: orderID=%s status=%s", order.OrderID, order.Status)
	}
	return nil
}

func (s *ArbitrageStrategy) handleOrderUpdateInternal(ctx context.Context, order *domain.Order) error {
	// 过滤：只处理当前市场相关的资产订单（避免多个策略共享 session 时互相污染）
	if s.currentMarket != nil {
		upID := s.currentMarket.GetAssetID(domain.TokenTypeUp)
		downID := s.currentMarket.GetAssetID(domain.TokenTypeDown)
		if order.AssetID != upID && order.AssetID != downID {
			return nil
		}
	}

	if order.IsFilled() {
		ev := &events.OrderFilledEvent{Order: order, Market: s.currentMarket}
		return s.OnOrderFilled(ctx, ev)
	}
	return nil
}

func (s *ArbitrageStrategy) handleCmdResultInternal(_ context.Context, res arbitrageCmdResult) error {
	// 无论成功/失败/跳过，都释放 in-flight 槽位（节流由上层控制）
	if s.inFlight > 0 {
		s.inFlight--
	}
	if res.err != nil {
		logger.Warnf("套利策略: 下单命令失败: reason=%s token=%s err=%v", res.reason, res.tokenType, res.err)
		return nil
	}
	if res.skipped {
		return nil
	}
	if res.created != nil {
		logger.Infof("套利策略: 下单命令成功: reason=%s token=%s orderID=%s", res.reason, res.tokenType, res.created.OrderID)
	}
	return nil
}

