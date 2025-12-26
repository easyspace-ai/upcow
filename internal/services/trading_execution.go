package services

import (
	"context"
	"fmt"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/execution"
)

// ExecuteMultiLeg 提供策略侧的统一入口：多腿并发执行 + 自动对冲。
// 返回：创建的订单（按 legs 顺序）与错误（如果任意腿失败）。
func (s *TradingService) ExecuteMultiLeg(ctx context.Context, req execution.MultiLegRequest) ([]*domain.Order, error) {
	if s == nil || s.execEngine == nil {
		return nil, fmt.Errorf("execution engine not available")
	}
	// 系统级硬防线：暂停模式下禁止多腿执行（fail-safe）
	if s.isTradingPaused() {
		return nil, fmt.Errorf("trading paused: refusing to execute multi-leg")
	}
	// 系统级硬防线：只允许对当前市场执行（避免跨周期串单）
	cur := s.GetCurrentMarket()
	if cur == "" || req.MarketSlug == "" || req.MarketSlug != cur {
		return nil, fmt.Errorf("market mismatch (refuse to trade): current=%s req=%s", cur, req.MarketSlug)
	}
	ticket, err := s.execEngine.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	select {
	case res := <-ticket.ResultC:
		return res.Created, res.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
