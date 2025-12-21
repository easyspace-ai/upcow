package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/stream"
	"github.com/betbot/gobet/pkg/syncgroup"
)

var marketLog = logrus.WithField("component", "market_stream")

const (
	reconnectCoolDownPeriod = 15 * time.Second
	pingInterval            = 10 * time.Second
	readTimeout             = 30 * time.Second
	writeTimeout            = 10 * time.Second
)

// MarketStream 市场数据流实现（BBGO 风格）
type MarketStream struct {
	// 连接管理
	conn       *websocket.Conn
	connCtx    context.Context
	connCancel context.CancelFunc
	connMu     sync.Mutex

	// 重连管理
	reconnectC chan struct{} // 信号驱动的重连 channel
	closeC     chan struct{} // 关闭信号 channel

	// 市场信息
	market   *domain.Market
	proxyURL string

	// 回调处理器
	handlers *stream.HandlerList

	// Goroutine 管理
	sg     *syncgroup.SyncGroup // 长期运行的 goroutine（如 reconnector）
	connSg *syncgroup.SyncGroup // 连接相关的 goroutine（如 Read, ping）

	// 健康检查
	lastPong      time.Time
	healthCheckMu sync.RWMutex

	// 诊断：最近一次收到消息的时间（用于判断“订阅成功但没数据”）
	lastMessageAt time.Time
	lastMsgMu     sync.RWMutex
}

// NewMarketStream 创建新的市场数据流
func NewMarketStream() *MarketStream {
	return &MarketStream{
		reconnectC:    make(chan struct{}, 1),
		closeC:        make(chan struct{}),
		handlers:      stream.NewHandlerList(),
		sg:            syncgroup.NewSyncGroup(), // 长期运行的 goroutine
		connSg:        syncgroup.NewSyncGroup(), // 连接相关的 goroutine
		lastPong:      time.Now(),
		lastMessageAt: time.Now(),
	}
}

// markMessageReceived 记录最近收到消息的时间（用于诊断）
func (m *MarketStream) markMessageReceived() {
	m.lastMsgMu.Lock()
	m.lastMessageAt = time.Now()
	m.lastMsgMu.Unlock()
}

// writeTextMessage 向 WS 写入文本消息（用于兼容服务器的应用层 PING/PONG）
func (m *MarketStream) writeTextMessage(msg string) error {
	m.connMu.Lock()
	conn := m.conn
	m.connMu.Unlock()
	if conn == nil {
		return fmt.Errorf("连接未建立")
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return conn.WriteMessage(websocket.TextMessage, []byte(msg))
}

// OnPriceChanged 注册价格变化回调
func (m *MarketStream) OnPriceChanged(handler stream.PriceChangeHandler) {
	if handler == nil {
		marketLog.Errorf("❌ [注册] MarketStream.OnPriceChanged 收到 nil handler！")
		return
	}
	m.handlers.Add(handler)
	handlerCount := m.handlers.Count()
	marketSlug := ""
	if m.market != nil {
		marketSlug = m.market.Slug
	}
	marketLog.Infof("✅ [注册] MarketStream 注册价格变化处理器，当前 handlers 数量=%d，市场=%s",
		handlerCount, marketSlug)
	if handlerCount == 0 {
		marketLog.Errorf("❌ [注册] MarketStream handlers 仍为空！注册失败！")
	}
}

// HandlerCount 返回 handlers 数量（用于调试）
func (m *MarketStream) HandlerCount() int {
	return m.handlers.Count()
}

// Connect 连接到市场数据流
func (m *MarketStream) Connect(ctx context.Context, market *domain.Market) error {
	m.market = market

	// 启动重连器 goroutine（只启动一次）
	m.sg.Add(func() {
		m.reconnector(ctx)
	})
	m.sg.Run()

	// 立即尝试连接
	return m.DialAndConnect(ctx)
}

// DialAndConnect 拨号并连接
func (m *MarketStream) DialAndConnect(ctx context.Context) error {
	// 检查是否已关闭（防止周期切换后仍然重连）
	select {
	case <-m.closeC:
		return fmt.Errorf("MarketStream 已关闭，取消重连")
	default:
	}
	
	conn, err := m.Dial(ctx)
	if err != nil {
		return err
	}

	// 原子替换连接（这会取消旧连接的 context）
	connCtx, connCancel := m.SetConn(ctx, conn)

	// 在启动新 goroutine 之前，先等待旧的 goroutine 完成（带超时）
	// 这样可以避免多个 Read/ping goroutine 同时运行
	done := make(chan struct{})
	go func() {
		m.connSg.WaitAndClear()
		close(done)
	}()

	select {
	case <-done:
		// 旧的 goroutine 已完成
	case <-time.After(2 * time.Second):
		// 超时，继续启动新的 goroutine（旧的会通过 context 取消自然退出）
		marketLog.Debugf("等待旧连接 goroutine 完成超时（2秒），继续启动新连接")
	}

	// 启动读取和 ping goroutine（使用连接相关的 SyncGroup）
	m.connSg.Add(func() {
		m.Read(connCtx, conn, connCancel)
	})
	m.connSg.Add(func() {
		m.ping(connCtx, conn, connCancel)
	})
	m.connSg.Run()

	// 订阅市场（使用 m.market）
	if m.market == nil {
		conn.Close()
		return fmt.Errorf("market not set")
	}
	if err := m.subscribe(m.market); err != nil {
		conn.Close()
		return err
	}

	marketLog.Infof("市场价格 WebSocket 已连接: %s", m.market.Slug)
	return nil
}

// SetConn 原子替换连接
func (m *MarketStream) SetConn(ctx context.Context, conn *websocket.Conn) (context.Context, context.CancelFunc) {
	m.connMu.Lock()
	defer m.connMu.Unlock()

	// 取消旧连接（通过 context 取消，让 goroutine 自然退出）
	if m.connCancel != nil {
		m.connCancel()
		// 注意：不在这里等待 goroutine 完成，避免阻塞
		// goroutine 会通过 context.Done() 检测到取消并退出
		// 在 Close() 中统一等待所有 goroutine 完成
	}

	// 创建新连接的 context
	connCtx, connCancel := context.WithCancel(ctx)
	m.conn = conn
	m.connCtx = connCtx
	m.connCancel = connCancel

	return connCtx, connCancel
}

// Dial 拨号 WebSocket 连接
func (m *MarketStream) Dial(ctx context.Context) (*websocket.Conn, error) {
	wsURL := "wss://ws-subscriptions-clob.polymarket.com/ws/market"

	dialer := websocket.Dialer{
		HandshakeTimeout: 30 * time.Second,
	}

	// 配置代理
	if m.proxyURL != "" {
		proxyURL, err := url.Parse(m.proxyURL)
		if err == nil {
			dialer.Proxy = http.ProxyURL(proxyURL)
			marketLog.Infof("使用代理连接 WebSocket: %s", m.proxyURL)
		}
	}

	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return nil, err
	}

	// 设置 ping/pong handler
	conn.SetPingHandler(nil)
	conn.SetPongHandler(func(string) error {
		m.healthCheckMu.Lock()
		m.lastPong = time.Now()
		m.healthCheckMu.Unlock()
		if err := conn.SetReadDeadline(time.Now().Add(readTimeout * 2)); err != nil {
			marketLog.Errorf("设置读取超时失败: %v", err)
		}
		return nil
	})

	return conn, nil
}

// Reconnect 触发重连
func (m *MarketStream) Reconnect() {
	select {
	case m.reconnectC <- struct{}{}:
	default:
		// channel 已满，忽略
	}
}

// reconnector 重连器 goroutine（信号驱动）
func (m *MarketStream) reconnector(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.closeC:
			return
		case <-m.reconnectC:
			marketLog.Warnf("收到重连信号，冷却 %s...", reconnectCoolDownPeriod)
			
			// 冷却期间检查关闭状态（使用 select 非阻塞检查）
			select {
			case <-m.closeC:
				marketLog.Debugf("重连冷却期间收到关闭信号，取消重连")
				return
			case <-ctx.Done():
				return
			case <-time.After(reconnectCoolDownPeriod):
				// 冷却完成，继续重连
			}
			
			// 重连前再次检查关闭状态
			select {
			case <-m.closeC:
				marketLog.Debugf("重连前收到关闭信号，取消重连")
				return
			case <-ctx.Done():
				return
			default:
				// 继续重连
			}
			
			marketLog.Warnf("重新连接...")
			if err := m.DialAndConnect(ctx); err != nil {
				marketLog.Warnf("重连失败: %v，将再次尝试...", err)
				m.Reconnect() // 重新发送信号
			}
		}
	}
}

// Read 读取消息循环
func (m *MarketStream) Read(ctx context.Context, conn *websocket.Conn, cancel context.CancelFunc) {
	defer func() {
		cancel()
	}()

	for {
		// 检查 context 是否已取消（在阻塞操作之前）
		select {
		case <-ctx.Done():
			return
		case <-m.closeC:
			return
		default:
		}

		// 设置读取超时：用 deadline 让 ReadMessage 至多阻塞 readTimeout，
		// 这样无需每轮起 goroutine，避免长期运行下 goroutine churn。
		if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			marketLog.Errorf("设置读取超时失败: %v", err)
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			// 检查是否是关闭错误
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				marketLog.Debugf("WebSocket 正常关闭")
				return
			}

			// 超时：用于周期性检查 ctx
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				continue
			}

			// "use of closed network connection"：正常关闭
			if err.Error() == "use of closed network connection" {
				marketLog.Debugf("WebSocket 连接已关闭")
				return
			}

			// 网络错误，触发重连
			marketLog.Warnf("WebSocket 读取错误: %v，触发重连", err)
			_ = conn.Close()
			m.Reconnect()
			return
		}

		// 处理消息
		m.markMessageReceived()
		m.handleMessage(ctx, message)
	}
}

// ping ping 循环
func (m *MarketStream) ping(ctx context.Context, conn *websocket.Conn, cancel context.CancelFunc) {
	defer cancel()

	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.closeC:
			return
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				marketLog.Warnf("发送 PING 失败: %v，触发重连", err)
				m.Reconnect()
				return
			}
		}
	}
}

// subscribe 订阅市场
func (m *MarketStream) subscribe(market *domain.Market) error {
	subscribeMsg := map[string]interface{}{
		"assets_ids": []string{market.YesAssetID, market.NoAssetID},
		"type":       "market",
	}

	marketLog.Infof("📡 订阅市场资产: YES=%s, NO=%s", market.YesAssetID, market.NoAssetID)

	m.connMu.Lock()
	conn := m.conn
	m.connMu.Unlock()

	if conn == nil {
		return fmt.Errorf("连接未建立")
	}

	if err := conn.WriteJSON(subscribeMsg); err != nil {
		return err
	}
	marketLog.Infof("✅ 订阅消息已发送")
	return nil
}

// handleMessage 处理消息
func (m *MarketStream) handleMessage(ctx context.Context, message []byte) {
	// 兼容：服务器可能发送纯文本 PING/PONG（旧实现 MarketWebSocket 就是这么处理的）
	// 注意：这里不能假设一定是 JSON
	if len(message) > 0 {
		switch string(message) {
		case "PING":
			// 回复 PONG，保持连接
			if err := m.writeTextMessage("PONG"); err != nil {
				marketLog.Warnf("回复 PONG 失败: %v", err)
			}
			m.healthCheckMu.Lock()
			m.lastPong = time.Now()
			m.healthCheckMu.Unlock()
			return
		case "PONG":
			m.healthCheckMu.Lock()
			m.lastPong = time.Now()
			m.healthCheckMu.Unlock()
			return
		}
	}

	if len(message) > 0 && message[0] == '[' {
		var rawMsgs []json.RawMessage
		if err := json.Unmarshal(message, &rawMsgs); err == nil && len(rawMsgs) > 0 {
			for _, raw := range rawMsgs {
				if len(raw) == 0 {
					continue
				}
				m.handleMessage(ctx, raw)
			}
			return
		}
	}

	var msgType struct {
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal(message, &msgType); err != nil {
		// 非 JSON 消息：只在 debug 记录，避免刷屏
		msgPreview := message
		if len(msgPreview) > 200 {
			msgPreview = msgPreview[:200]
		}
		marketLog.Debugf("解析消息类型失败(可能是非JSON): %v, msg=%q", err, string(msgPreview))
		return
	}

	switch msgType.EventType {
	case "price_change":
		// 检查 handlers 数量（用于调试）
		handlerCount := m.handlers.Count()
		if handlerCount == 0 {
			marketLog.Warnf("⚠️ [消息处理] 收到 price_change 消息但 handlers 为空！市场=%s", m.market.Slug)
		} else {
			marketLog.Debugf("📨 [消息处理] 收到 price_change 消息，handlers 数量=%d，市场=%s", handlerCount, m.market.Slug)
		}
		var msg map[string]interface{}
		if err := json.Unmarshal(message, &msg); err != nil {
			marketLog.Warnf("解析价格变化消息失败: %v", err)
			return
		}
		m.handlePriceChange(ctx, msg)
	case "subscribed":
		marketLog.Infof("✅ MarketStream 收到订阅成功消息")
		// 订阅成功但长时间没任何数据时，给出更明确的诊断提示
		m.lastMsgMu.RLock()
		last := m.lastMessageAt
		m.lastMsgMu.RUnlock()
		_ = last // 预留：后续可在此处启动定时诊断（不在这里启动 goroutine，避免重复启动）
	case "pong":
		m.healthCheckMu.Lock()
		m.lastPong = time.Now()
		m.healthCheckMu.Unlock()
		marketLog.Debugf("收到 PONG 响应")
	case "book":
		// 兼容：某些情况下服务器只推 book（快照/增量），未推 price_change。
		// 为了不让策略“完全看不到实时 up/down”，这里从 book 中提取 best_ask/best_bid 并发出 PriceChangedEvent。
		m.handleBookAsPrice(ctx, message)
	case "tick_size_change":
		// Tick size 变化（可选处理）
		marketLog.Debugf("收到 tick size 变化消息")
	case "last_trade_price":
		// 最后交易价格（可选处理）
		marketLog.Debugf("💰 收到最后交易价格消息（价格变化应通过 price_change 事件发送）")
	default:
		msgPreview := message
		if len(msgPreview) > 200 {
			msgPreview = msgPreview[:200]
		}
		marketLog.Infof("收到未知消息类型: %s (消息内容: %s)", msgType.EventType, string(msgPreview))
	}
}

type orderLevel struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

// handleBookAsPrice 从 book 消息提取价格并触发 PriceChangedEvent（用于兼容“没有 price_change 但有 book”的情况）
func (m *MarketStream) handleBookAsPrice(ctx context.Context, message []byte) {
	if m.market == nil {
		return
	}

	type bookMessage struct {
		EventType string       `json:"event_type"`
		AssetID   string       `json:"asset_id"`
		BestBid   string       `json:"best_bid"`
		BestAsk   string       `json:"best_ask"`
		Price     string       `json:"price"`
		Bids      []orderLevel `json:"bids"`
		Asks      []orderLevel `json:"asks"`
	}

	var bm bookMessage
	if err := json.Unmarshal(message, &bm); err != nil {
		marketLog.Debugf("解析 book 消息失败: %v", err)
		return
	}
	if bm.AssetID == "" {
		return
	}

	// 选择价格来源：best_ask > best_bid > price > asks[0] > bids[0]
	priceStr := ""
	source := ""
	if bm.BestAsk != "" {
		priceStr = bm.BestAsk
		source = "book.best_ask"
	} else if bm.BestBid != "" {
		priceStr = bm.BestBid
		source = "book.best_bid"
	} else if bm.Price != "" {
		priceStr = bm.Price
		source = "book.price"
	} else if len(bm.Asks) > 0 && bm.Asks[0].Price != "" {
		priceStr = bm.Asks[0].Price
		source = "book.asks[0]"
	} else if len(bm.Bids) > 0 && bm.Bids[0].Price != "" {
		priceStr = bm.Bids[0].Price
		source = "book.bids[0]"
	} else {
		return
	}

	newPrice, err := parsePriceString(priceStr)
	if err != nil {
		marketLog.Debugf("解析 book 价格失败: source=%s value=%s err=%v", source, priceStr, err)
		return
	}

	// 调试日志：记录原始价格字符串和解析结果
	marketLog.Debugf("💰 [book价格解析] source=%s, priceStr=%s → %dc (decimal=%.4f)",
		source, priceStr, newPrice.Cents, newPrice.ToDecimal())

	var tokenType domain.TokenType
	if bm.AssetID == m.market.YesAssetID {
		tokenType = domain.TokenTypeUp
	} else if bm.AssetID == m.market.NoAssetID {
		tokenType = domain.TokenTypeDown
	} else {
		return
	}

	// 检查是否已关闭（避免处理关闭后的延迟消息）
	select {
	case <-m.closeC:
		marketLog.Debugf("⚠️ [book->price] MarketStream 已关闭，忽略价格事件: Token=%s, 价格=%dc", tokenType, newPrice.Cents)
		return
	default:
	}

	// 【关键修复】在发送事件前，检查 handlers 是否为空（防止在关闭过程中 handlers 被清空后仍然发送事件）
	if m.handlers.Count() == 0 {
		marketLog.Debugf("⚠️ [book->price] handlers 已清空，忽略价格事件: Token=%s, 价格=%dc", tokenType, newPrice.Cents)
		return
	}

	event := &events.PriceChangedEvent{
		Market:    m.market,
		TokenType: tokenType,
		OldPrice:  nil,
		NewPrice:  newPrice,
		Timestamp: time.Now(),
	}
	marketLog.Debugf("📤 [book->price] 触发价格变化回调: %s @ %dc (source=%s, 市场=%s)", tokenType, newPrice.Cents, source, m.market.Slug)
	m.handlers.Emit(ctx, event)
}

// handlePriceChange 处理价格变化（直接回调，不使用事件总线）
func (m *MarketStream) handlePriceChange(ctx context.Context, msg map[string]interface{}) {
	// 检查是否已关闭（避免处理关闭后的延迟消息）
	select {
	case <-m.closeC:
		marketLog.Debugf("⚠️ [价格处理] MarketStream 已关闭，忽略价格变化消息")
		return
	default:
	}

	// 检查 context 是否已取消
	select {
	case <-ctx.Done():
		marketLog.Debugf("⚠️ [价格处理] Context 已取消，忽略价格变化消息")
		return
	default:
	}

	priceChanges, ok := msg["price_changes"].([]interface{})
	if !ok {
		marketLog.Debugf("⚠️ [价格处理] 价格变化消息中没有 price_changes 字段")
		return
	}

	// 检查 handlers 数量
	handlerCount := m.handlers.Count()
	if handlerCount == 0 {
		marketLog.Debugf("⚠️ [价格处理] MarketStream.handlers 为空，价格更新将被丢弃！市场=%s", m.market.Slug)
		return
	}

	// 检查当前市场是否匹配（防止处理旧周期的消息）
	currentMarketSlug := ""
	if m.market != nil {
		currentMarketSlug = m.market.Slug
	}
	if currentMarketSlug == "" {
		marketLog.Debugf("⚠️ [价格处理] MarketStream.market 为空，忽略价格变化消息")
		return
	}

	marketLog.Debugf("📊 [价格处理] 收到价格变化消息，handlers 数量=%d，市场=%s", handlerCount, currentMarketSlug)

	latestPrices := make(map[string]struct {
		price  domain.Price
		source string
	})

	// 处理每个价格变化
	for _, pc := range priceChanges {
		change, ok := pc.(map[string]interface{})
		if !ok {
			continue
		}

		assetID, _ := change["asset_id"].(string)
		if assetID == "" {
			continue
		}

		// 获取价格
		var priceStr string
		var priceSource string
		if bestAskStr, ok := change["best_ask"].(string); ok && bestAskStr != "" {
			priceStr = bestAskStr
			priceSource = "best_ask"
		} else if bestBidStr, ok := change["best_bid"].(string); ok && bestBidStr != "" {
			priceStr = bestBidStr
			priceSource = "best_bid"
		} else if priceVal, ok := change["price"].(string); ok && priceVal != "" {
			priceStr = priceVal
			priceSource = "price"
		} else {
			continue
		}

		// 解析价格
		newPrice, err := parsePriceString(priceStr)
		if err != nil {
			marketLog.Debugf("⚠️ [价格解析] 解析失败: source=%s, priceStr=%s, err=%v", priceSource, priceStr, err)
			continue
		}

		// 调试日志：记录原始价格字符串和解析结果（INFO 级别，方便排查）
		marketSlug := ""
		if m.market != nil {
			marketSlug = m.market.Slug
		}
		marketLog.Infof("💰 [价格解析] 市场=%s, assetID=%s, source=%s, 原始字符串=%s → 解析结果=%dc (小数=%.4f)",
			marketSlug, assetID[:12]+"...", priceSource, priceStr, newPrice.Cents, newPrice.ToDecimal())

		latestPrices[assetID] = struct {
			price  domain.Price
			source string
		}{price: newPrice, source: priceSource}
	}

	// 触发回调
	for assetID, latest := range latestPrices {
		var tokenType domain.TokenType
		if assetID == m.market.YesAssetID {
			tokenType = domain.TokenTypeUp
		} else if assetID == m.market.NoAssetID {
			tokenType = domain.TokenTypeDown
		} else {
			continue
		}

		// 再次检查是否已关闭（双重保险）
		select {
		case <-m.closeC:
			marketLog.Debugf("⚠️ [价格事件] MarketStream 已关闭，忽略价格事件: 市场=%s, Token=%s, 价格=%dc",
				currentMarketSlug, tokenType, latest.price.Cents)
			continue
		default:
		}

		// 【关键修复】在发送事件前，检查 handlers 是否为空（防止在关闭过程中 handlers 被清空后仍然发送事件）
		if m.handlers.Count() == 0 {
			marketLog.Debugf("⚠️ [价格事件] handlers 已清空，忽略价格事件: 市场=%s, Token=%s, 价格=%dc",
				currentMarketSlug, tokenType, latest.price.Cents)
			continue
		}

		event := &events.PriceChangedEvent{
			Market:    m.market,
			TokenType: tokenType,
			OldPrice:  nil,
			NewPrice:  latest.price,
			Timestamp: time.Now(),
		}

		// 直接触发回调（不使用事件总线）
		// 注意：这里使用 handlerCount（在函数开头定义）
		marketLog.Infof("📤 [价格事件] 触发价格变化回调: 市场=%s, Token=%s, 价格=%dc (handlers=%d)",
			currentMarketSlug, tokenType, latest.price.Cents, handlerCount)
		m.handlers.Emit(ctx, event)
	}
}

// Close 关闭连接
func (m *MarketStream) Close() error {
	start := time.Now()
	// 检查是否已关闭（避免重复关闭）
	select {
	case <-m.closeC:
		// 已经关闭，直接返回
		return nil
	default:
	}

	// 【关键修复】先清空所有 handlers（阻止新事件被发送），再关闭 closeC
	// 这样可以确保在关闭过程中，即使有消息在处理，也不会发送事件到已清空的 handlers
	m.handlers.Clear()
	marketSlug := ""
	if m.market != nil {
		marketSlug = m.market.Slug
	}
	marketLog.Infof("🔄 [关闭] MarketStream 已清空所有 handlers，市场=%s", marketSlug)

	// 发送关闭信号（在清空 handlers 之后）
	close(m.closeC)

	m.connMu.Lock()
	if m.connCancel != nil {
		m.connCancel()
	}
	if m.conn != nil {
		m.conn.Close()
		m.conn = nil
	}
	m.connMu.Unlock()

	// 等待连接相关的 goroutine 完成（设置超时，避免无限等待）
	done1 := make(chan struct{})
	go func() {
		m.connSg.WaitAndClear()
		close(done1)
	}()

	select {
	case <-done1:
		// 正常完成
	case <-time.After(5 * time.Second):
		marketLog.Warnf("等待连接相关 goroutine 完成超时（5秒），继续关闭")
	}

	// 等待长期运行的 goroutine 完成（如 reconnector，设置超时）
	done2 := make(chan struct{})
	go func() {
		m.sg.WaitAndClear()
		close(done2)
	}()

	select {
	case <-done2:
		// 正常完成
	case <-time.After(5 * time.Second):
		marketLog.Warnf("等待长期运行 goroutine 完成超时（5秒），继续关闭")
	}

	// 明确标记：旧订阅已通过“关闭 WS + 清空 handlers”完成
	marketLog.Infof("✅ [unsubscribe] MarketStream 已关闭并完成退订：market=%s, elapsed=%s",
		marketSlug, time.Since(start))
	return nil
}

// SetProxyURL 设置代理 URL
func (m *MarketStream) SetProxyURL(proxyURL string) {
	m.proxyURL = proxyURL
}
