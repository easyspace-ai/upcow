package bbgo

import (
	"context"
	"sync"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/internal/infrastructure/websocket"
	"github.com/betbot/gobet/internal/stream"
)

var sessionLog = logrus.WithField("component", "session")

// ExchangeSession 交易所会话，封装市场数据流和用户数据流
type ExchangeSession struct {
	Name string

	// WebSocket 连接
	MarketDataStream stream.MarketDataStream // 使用新的 MarketStream 接口
	UserDataStream   *websocket.UserWebSocket

	// 市场信息
	market *domain.Market

	// 订阅管理
	subscriptions []Subscription
	subscriptionsMu sync.RWMutex

	// 回调处理器列表
	priceChangeHandlers *stream.HandlerList
	orderHandlers       []OrderHandler
	tradeHandlers       []TradeHandler

	mu sync.RWMutex
}

// Subscription 订阅信息
type Subscription struct {
	Channel string
	Symbol  string
	Options map[string]interface{}
}

// OrderHandler 订单处理器接口
type OrderHandler interface {
	OnOrderUpdate(ctx context.Context, order *domain.Order) error
}

// TradeHandler 交易处理器接口（暂时使用 Order，因为当前项目没有独立的 Trade 类型）
type TradeHandler interface {
	OnTradeUpdate(ctx context.Context, order *domain.Order) error
}

// NewExchangeSession 创建新的交易所会话
func NewExchangeSession(name string) *ExchangeSession {
	return &ExchangeSession{
		Name:                name,
		subscriptions:       make([]Subscription, 0),
		priceChangeHandlers: stream.NewHandlerList(),
		orderHandlers:       make([]OrderHandler, 0),
		tradeHandlers:       make([]TradeHandler, 0),
	}
}

// SetMarket 设置市场信息
func (s *ExchangeSession) SetMarket(market *domain.Market) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.market = market
}

// Market 获取市场信息
func (s *ExchangeSession) Market() *domain.Market {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.market
}

// SetMarketDataStream 设置市场数据流
func (s *ExchangeSession) SetMarketDataStream(stream stream.MarketDataStream) {
	s.MarketDataStream = stream
}

// SetUserDataStream 设置用户数据流
func (s *ExchangeSession) SetUserDataStream(stream *websocket.UserWebSocket) {
	s.UserDataStream = stream
}

// Subscribe 添加订阅
func (s *ExchangeSession) Subscribe(channel, symbol string, options map[string]interface{}) {
	s.subscriptionsMu.Lock()
	defer s.subscriptionsMu.Unlock()
	s.subscriptions = append(s.subscriptions, Subscription{
		Channel: channel,
		Symbol:  symbol,
		Options: options,
	})
}

// Connect 连接到交易所
func (s *ExchangeSession) Connect(ctx context.Context) error {
	if s.MarketDataStream != nil {
		market := s.Market()
		if market != nil {
			// 将 Session 的价格变化处理器注册到 MarketStream
			// 这样 MarketStream 收到价格变化时会触发 Session 的处理器
			sessionLog.Infof("🔗 [Session %s] 注册 sessionPriceHandler 到 MarketStream", s.Name)
			s.MarketDataStream.OnPriceChanged(&sessionPriceHandler{session: s})
			
			// 检查 handlers 数量（用于调试）
			if ms, ok := s.MarketDataStream.(*websocket.MarketStream); ok {
				handlerCount := ms.HandlerCount()
				sessionLog.Infof("✅ [Session %s] MarketStream handlers 数量=%d (注册后)", s.Name, handlerCount)
				if handlerCount == 0 {
					sessionLog.Errorf("❌ [Session %s] 错误：MarketStream handlers 为空！sessionPriceHandler 注册失败！", s.Name)
				}
			}
			
			if err := s.MarketDataStream.Connect(ctx, market); err != nil {
				return err
			}
			
			// 连接后再次检查 handlers 数量
			if ms, ok := s.MarketDataStream.(*websocket.MarketStream); ok {
				handlerCount := ms.HandlerCount()
				sessionLog.Infof("✅ [Session %s] MarketStream handlers 数量=%d (连接后)", s.Name, handlerCount)
				if handlerCount == 0 {
					sessionLog.Errorf("❌ [Session %s] 错误：连接后 MarketStream handlers 为空！", s.Name)
				}
			}
			
			sessionLog.Infof("[Session %s] 市场数据流已连接", s.Name)
		}
	}

	if s.UserDataStream != nil {
		// UserDataStream 的连接逻辑在外部管理
		sessionLog.Infof("[Session %s] 用户数据流已就绪", s.Name)
	}

	return nil
}

// sessionPriceHandler 将 MarketStream 的价格变化转发到 Session
type sessionPriceHandler struct {
	session *ExchangeSession
}

func (h *sessionPriceHandler) OnPriceChanged(ctx context.Context, event *events.PriceChangedEvent) error {
	sessionLog.Debugf("📥 [sessionPriceHandler] 收到价格变化事件，转发到 Session: %s @ %dc (Session=%s)", 
		event.TokenType, event.NewPrice.Cents, h.session.Name)
	h.session.EmitPriceChanged(ctx, event)
	return nil
}

// Close 关闭会话
func (s *ExchangeSession) Close() error {
	if s.MarketDataStream != nil {
		if err := s.MarketDataStream.Close(); err != nil {
			return err
		}
	}

	if s.UserDataStream != nil {
		// UserDataStream 的关闭逻辑在外部管理
	}

	return nil
}

// OnPriceChanged 注册价格变化处理器
func (s *ExchangeSession) OnPriceChanged(handler stream.PriceChangeHandler) {
	s.priceChangeHandlers.Add(handler)
	handlerCount := s.priceChangeHandlers.Count()
	sessionLog.Debugf("✅ [Session %s] 注册价格变化处理器，当前 handlers 数量=%d", s.Name, handlerCount)
}

// EmitPriceChanged 触发价格变化事件
func (s *ExchangeSession) EmitPriceChanged(ctx context.Context, event *events.PriceChangedEvent) {
	handlerCount := s.priceChangeHandlers.Count()
	if handlerCount == 0 {
		sessionLog.Warnf("⚠️ [Session %s] priceChangeHandlers 为空，价格更新将被丢弃！事件: %s @ %dc", 
			s.Name, event.TokenType, event.NewPrice.Cents)
	} else {
		sessionLog.Debugf("📊 [Session %s] 触发价格变化事件: %s @ %dc (handlers=%d)", 
			s.Name, event.TokenType, event.NewPrice.Cents, handlerCount)
	}
	s.priceChangeHandlers.Emit(ctx, event)
}

// OnOrderUpdate 注册订单更新处理器
func (s *ExchangeSession) OnOrderUpdate(handler OrderHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orderHandlers = append(s.orderHandlers, handler)
}

// EmitOrderUpdate 触发订单更新事件（BBGO风格：直接回调）
func (s *ExchangeSession) EmitOrderUpdate(ctx context.Context, order *domain.Order) {
	s.mu.RLock()
	handlers := s.orderHandlers
	s.mu.RUnlock()

	sessionLog.Debugf("📊 Session %s 触发订单更新事件: orderID=%s, status=%s", s.Name, order.OrderID, order.Status)
	
	// 异步执行，避免阻塞
	for _, handler := range handlers {
		go func(h OrderHandler) {
			if err := h.OnOrderUpdate(ctx, order); err != nil {
				sessionLog.Errorf("订单更新处理器执行失败: %v", err)
			}
		}(handler)
	}
}

// OnTradeUpdate 注册交易更新处理器
func (s *ExchangeSession) OnTradeUpdate(handler TradeHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tradeHandlers = append(s.tradeHandlers, handler)
}

// PriceChangeHandlerCount 返回价格变化处理器数量（用于调试）
func (s *ExchangeSession) PriceChangeHandlerCount() int {
	return s.priceChangeHandlers.Count()
}

