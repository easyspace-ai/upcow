package bbgo

import (
	"context"
	"sync"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/ports"
)

// SessionEventRouter 是“架构层”的事件路由/隔离器：
// - 统一把来自 UserWebSocket / TradingService 的订单/成交事件路由到“当前 session”
// - 并且在进入 session（进而进入策略）前做跨周期隔离（按当前 market 过滤）
//
// 设计目标：
// - 策略完全不需要关心“是否旧周期”
// - 应用层只需要在周期切换时调用 SetSession()
type SessionEventRouter struct {
	mu      sync.RWMutex
	session *ExchangeSession
}

var _ ports.OrderUpdateHandler = (*SessionEventRouter)(nil)
var _ ports.TradeUpdateHandler = (*SessionEventRouter)(nil)

func NewSessionEventRouter() *SessionEventRouter {
	return &SessionEventRouter{}
}

func (r *SessionEventRouter) SetSession(session *ExchangeSession) {
	r.mu.Lock()
	r.session = session
	r.mu.Unlock()
}

func (r *SessionEventRouter) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	r.mu.RLock()
	s := r.session
	r.mu.RUnlock()
	if s == nil {
		return nil
	}
	// 进一步的隔离与补齐由 session.EmitOrderUpdate 统一处理
	s.EmitOrderUpdate(ctx, order)
	return nil
}

func (r *SessionEventRouter) HandleTrade(ctx context.Context, trade *domain.Trade) {
	r.mu.RLock()
	s := r.session
	r.mu.RUnlock()
	if s == nil {
		return
	}
	// 进一步的隔离与补齐由 session.EmitTradeUpdate 统一处理
	s.EmitTradeUpdate(ctx, trade)
}

