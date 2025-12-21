package bbgo

import (
	"context"
	"fmt"
	"sync"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/ports"
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

	// 价格事件合并与串行分发（避免行情线程被策略阻塞，且保证确定性）
	priceSignalC chan struct{}
	priceMu      sync.Mutex
	latestPrices map[domain.TokenType]priceEvent
	loopOnce     sync.Once
	loopCancel   context.CancelFunc

	// 订阅管理
	subscriptions   []Subscription
	subscriptionsMu sync.RWMutex

	// 回调处理器列表
	priceChangeHandlers *stream.HandlerList
	orderHandlers       []OrderHandler
	tradeHandlers       []TradeHandler

	mu sync.RWMutex
}

type priceEvent struct {
	ctx   context.Context
	event *events.PriceChangedEvent
}

// Subscription 订阅信息
type Subscription struct {
	Channel string
	Symbol  string
	Options map[string]interface{}
}

// OrderHandler 订单处理器接口
//
// NOTE: aliased to ports.OrderUpdateHandler to avoid duplicated interface definitions
// across runtime/services/infrastructure and to keep handler types compatible.
type OrderHandler = ports.OrderUpdateHandler

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
		priceSignalC:        make(chan struct{}, 1),
		latestPrices:        make(map[domain.TokenType]priceEvent),
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
	s.startPriceLoop(ctx)

	if s.MarketDataStream != nil {
		// 先注册 handler：避免因为 market 尚未设置而“静默不注册”，导致后续完全收不到价格事件。
		// 注册本身不依赖 market；只有 Connect 才依赖 market。
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

		market := s.Market()
		if market == nil {
			// 这里以前会“静默跳过连接”，让人误以为 handler 没运行；改为直接报错更可诊断。
			return fmt.Errorf("session %s market is nil: call SetMarket() before Connect()", s.Name)
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

	if s.UserDataStream != nil {
		// UserDataStream 的连接逻辑在外部管理
		sessionLog.Infof("[Session %s] 用户数据流已就绪", s.Name)
	}

	return nil
}

func (s *ExchangeSession) startPriceLoop(ctx context.Context) {
	s.loopOnce.Do(func() {
		loopCtx, cancel := context.WithCancel(ctx)
		s.loopCancel = cancel

		go func() {
			for {
				select {
				case <-loopCtx.Done():
					return
				case <-s.priceSignalC:
					// 合并：每次只处理最新 UP/DOWN（或其他 tokenType）的事件
					s.priceMu.Lock()
					batch := make([]priceEvent, 0, len(s.latestPrices))
					// 为确定性：固定顺序处理
					if pe, ok := s.latestPrices[domain.TokenTypeUp]; ok && pe.event != nil {
						batch = append(batch, pe)
					}
					if pe, ok := s.latestPrices[domain.TokenTypeDown]; ok && pe.event != nil {
						batch = append(batch, pe)
					}
					// 处理完清空（下一轮继续合并）
					s.latestPrices = make(map[domain.TokenType]priceEvent)
					s.priceMu.Unlock()

					if len(batch) == 0 {
						continue
					}

					handlers := s.priceChangeHandlers.Snapshot()
					if len(handlers) == 0 {
						// 保留原有诊断日志
						last := batch[len(batch)-1]
						if last.event != nil {
							sessionLog.Warnf("⚠️ [Session %s] priceChangeHandlers 为空，价格更新将被丢弃！事件: %s @ %dc",
								s.Name, last.event.TokenType, last.event.NewPrice.Cents)
						}
						continue
					}

					// 串行分发（确定性优先）
					for _, pe := range batch {
						if pe.event == nil {
							continue
						}
						for i, h := range handlers {
							if h == nil {
								continue
							}
							func(idx int, handler stream.PriceChangeHandler, ev priceEvent) {
								defer func() {
									if r := recover(); r != nil {
										sessionLog.Errorf("价格变化处理器 %d panic: %v", idx, r)
									}
								}()
								if err := handler.OnPriceChanged(ev.ctx, ev.event); err != nil {
									sessionLog.Errorf("价格变化处理器 %d 执行失败: %v", idx, err)
								}
							}(i, h, pe)
						}
					}
				}
			}
		}()
	})
}

// sessionPriceHandler 将 MarketStream 的价格变化转发到 Session
type sessionPriceHandler struct {
	session *ExchangeSession
	once    sync.Once
}

func (h *sessionPriceHandler) OnPriceChanged(ctx context.Context, event *events.PriceChangedEvent) error {
	// 用 INFO 打一条“只出现一次”的确认日志，避免用户在 INFO 级别下误判“没运行”。
	h.once.Do(func() {
		if event == nil {
			sessionLog.Infof("📥 [sessionPriceHandler] 首次收到价格事件: <nil> (Session=%s)", h.session.Name)
			return
		}
		sessionLog.Infof("📥 [sessionPriceHandler] 首次收到价格事件: %s @ %dc (Session=%s)",
			event.TokenType, event.NewPrice.Cents, h.session.Name)
	})

	sessionLog.Debugf("📥 [sessionPriceHandler] 收到价格变化事件，转发到 Session: %s @ %dc (Session=%s)",
		event.TokenType, event.NewPrice.Cents, h.session.Name)
	h.session.EmitPriceChanged(ctx, event)
	return nil
}

// Close 关闭会话
func (s *ExchangeSession) Close() error {
	// 清空所有上层 handlers：避免 Close 期间仍有“延迟信号”触发旧周期策略
	// （例如 select 可能在 ctx.Done 已就绪时仍选中 priceSignalC 分支）
	if s.priceChangeHandlers != nil {
		s.priceChangeHandlers.Clear()
	}
	s.priceMu.Lock()
	s.latestPrices = make(map[domain.TokenType]priceEvent)
	s.priceMu.Unlock()

	// 停止价格事件分发 loop（不关闭 channel，避免并发发送 panic）
	if s.loopCancel != nil {
		s.loopCancel()
	}

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
	// 快路径：只做合并与信号，避免阻塞 MarketStream 的读循环
	if event == nil {
		return
	}

	s.priceMu.Lock()
	s.latestPrices[event.TokenType] = priceEvent{ctx: ctx, event: event}
	s.priceMu.Unlock()

	select {
	case s.priceSignalC <- struct{}{}:
	default:
		// 已经有信号在队列里，合并即可
	}
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

	// 串行执行（确定性优先，避免并发导致的状态竞态）
	for _, handler := range handlers {
		if handler == nil {
			continue
		}
		func(h OrderHandler) {
			defer func() {
				if r := recover(); r != nil {
					sessionLog.Errorf("订单更新处理器 panic: %v", r)
				}
			}()
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
