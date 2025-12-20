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
	sg          *syncgroup.SyncGroup // 长期运行的 goroutine（如 reconnector）
	connSg      *syncgroup.SyncGroup // 连接相关的 goroutine（如 Read, ping）

	// 健康检查
	lastPong      time.Time
	healthCheckMu sync.RWMutex

	// 诊断：消息/价格事件统计（用于排查“WS连接但没有价格更新”）
	diagMu       sync.Mutex
	lastMsgAt    time.Time
	lastPriceAt  time.Time
	msgCount     int64
	priceEvCount int64
}

// NewMarketStream 创建新的市场数据流
func NewMarketStream() *MarketStream {
	return &MarketStream{
		reconnectC: make(chan struct{}, 1),
		closeC:     make(chan struct{}),
		handlers:   stream.NewHandlerList(),
		sg:         syncgroup.NewSyncGroup(), // 长期运行的 goroutine
		connSg:     syncgroup.NewSyncGroup(), // 连接相关的 goroutine
		lastPong:   time.Now(),
		lastMsgAt:  time.Time{},
		lastPriceAt: time.Time{},
	}
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
	// 诊断：周期性输出“是否收到 WS 消息/价格事件”的汇总（INFO，低频）
	m.connSg.Add(func() {
		m.diagLoop(connCtx)
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

func (m *MarketStream) diagLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.closeC:
			return
		case <-ticker.C:
			m.diagMu.Lock()
			lastMsgAt := m.lastMsgAt
			lastPriceAt := m.lastPriceAt
			msgCount := m.msgCount
			priceEvCount := m.priceEvCount
			m.diagMu.Unlock()

			marketSlug := ""
			if m.market != nil {
				marketSlug = m.market.Slug
			}

			if lastMsgAt.IsZero() {
				marketLog.Infof("🛰️ [WS诊断] 尚未收到任何 WS 消息：market=%s msgCount=%d priceEvents=%d", marketSlug, msgCount, priceEvCount)
				continue
			}

			ageMsg := time.Since(lastMsgAt)
			agePrice := time.Duration(0)
			if !lastPriceAt.IsZero() {
				agePrice = time.Since(lastPriceAt)
			}

			// 只要出现“长时间无消息/无价格事件”，用 INFO 提醒（方便线上排查）
			if ageMsg > 45*time.Second {
				marketLog.Infof("🛰️ [WS诊断] 45s 内未收到任何 WS 消息：market=%s msgCount=%d priceEvents=%d lastMsgAgo=%v lastPriceAgo=%v",
					marketSlug, msgCount, priceEvCount, ageMsg, agePrice)
				continue
			}
			if lastPriceAt.IsZero() || agePrice > 45*time.Second {
				marketLog.Infof("🛰️ [WS诊断] 45s 内未产生价格事件（策略将无价格更新）：market=%s msgCount=%d priceEvents=%d lastMsgAgo=%v lastPriceAgo=%v",
					marketSlug, msgCount, priceEvCount, ageMsg, agePrice)
			}
		}
	}
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
			time.Sleep(reconnectCoolDownPeriod)

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

		// 设置读取超时
		if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			marketLog.Errorf("设置读取超时失败: %v", err)
			return
		}

		// 使用 goroutine 来执行阻塞的 ReadMessage，并通过 channel 传递结果
		type readResult struct {
			message []byte
			err     error
		}
		resultChan := make(chan readResult, 1)

		go func() {
			_, message, err := conn.ReadMessage()
			resultChan <- readResult{message: message, err: err}
		}()

		// 等待读取结果或 context 取消
		select {
		case <-ctx.Done():
			return
		case <-m.closeC:
			return
		case result := <-resultChan:
			if result.err != nil {
				// 检查是否是关闭错误
				if websocket.IsCloseError(result.err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					marketLog.Debugf("WebSocket 正常关闭")
					return
				}

				// 检查是否是超时错误（用于检查 context）
				if netErr, ok := result.err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
					// 超时，继续循环以检查 context
					continue
				}

				// 检查是否是 "use of closed network connection" 错误（正常关闭）
				errStr := result.err.Error()
				if errStr == "use of closed network connection" {
					marketLog.Debugf("WebSocket 连接已关闭")
					return
				}

				// 网络错误，触发重连
				marketLog.Warnf("WebSocket 读取错误: %v，触发重连", result.err)
				conn.Close()
				m.Reconnect()
				return
			}

			// 处理消息
			m.handleMessage(ctx, result.message)
		}
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
	var msgType struct {
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal(message, &msgType); err != nil {
		marketLog.Debugf("解析消息类型失败: %v", err)
		return
	}

	// 诊断：记录收到消息时间与计数（不打印每条，避免刷屏）
	m.diagMu.Lock()
	m.lastMsgAt = time.Now()
	m.msgCount++
	m.diagMu.Unlock()

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
	case "pong":
		m.healthCheckMu.Lock()
		m.lastPong = time.Now()
		m.healthCheckMu.Unlock()
		marketLog.Debugf("收到 PONG 响应")
	case "book":
		// 订单簿快照/增量：很多时候 WS 可能只推 book（而 price_change 很少/没有）
		// 这里将 book 的 best ask/bid 也转换为 PriceChangedEvent，保证策略能收到“价格变化”。
		m.handleBook(ctx, message)
	case "tick_size_change":
		// Tick size 变化（可选处理）
		marketLog.Debugf("收到 tick size 变化消息")
	case "last_trade_price":
		// 最后成交价：也转换为 PriceChangedEvent 作为兜底
		m.handleLastTradePrice(ctx, message)
	default:
		msgPreview := message
		if len(msgPreview) > 200 {
			msgPreview = msgPreview[:200]
		}
		marketLog.Infof("收到未知消息类型: %s (消息内容: %s)", msgType.EventType, string(msgPreview))
	}
}

type bookLevel struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

type bookMessage struct {
	EventType string      `json:"event_type"`
	Market    string      `json:"market"`
	AssetID   string      `json:"asset_id"`
	Bids      []bookLevel `json:"bids"`
	Asks      []bookLevel `json:"asks"`
	Timestamp string      `json:"timestamp"`
}

func (m *MarketStream) handleBook(ctx context.Context, raw []byte) {
	if m.market == nil {
		return
	}

	var bm bookMessage
	if err := json.Unmarshal(raw, &bm); err != nil {
		marketLog.Debugf("解析 book 消息失败: %v", err)
		return
	}
	if bm.AssetID == "" {
		return
	}

	// 选择“更适合做买入触发”的价格：优先 best ask，否则 best bid
	var priceStr string
	if len(bm.Asks) > 0 && bm.Asks[0].Price != "" {
		priceStr = bm.Asks[0].Price
	} else if len(bm.Bids) > 0 && bm.Bids[0].Price != "" {
		priceStr = bm.Bids[0].Price
	} else {
		return
	}

	newPrice, err := parsePriceString(priceStr)
	if err != nil {
		return
	}

	var tokenType domain.TokenType
	if bm.AssetID == m.market.YesAssetID {
		tokenType = domain.TokenTypeUp
	} else if bm.AssetID == m.market.NoAssetID {
		tokenType = domain.TokenTypeDown
	} else {
		return
	}

	ev := &events.PriceChangedEvent{
		Market:    m.market,
		TokenType: tokenType,
		OldPrice:  nil,
		NewPrice:  newPrice,
		Timestamp: time.Now(),
	}

	m.diagMu.Lock()
	m.lastPriceAt = time.Now()
	m.priceEvCount++
	m.diagMu.Unlock()

	m.handlers.Emit(ctx, ev)
}

type lastTradePriceMessage struct {
	EventType string `json:"event_type"`
	Market    string `json:"market"`
	AssetID   string `json:"asset_id"`
	Price     string `json:"price"`
	Timestamp string `json:"timestamp"`
}

func (m *MarketStream) handleLastTradePrice(ctx context.Context, raw []byte) {
	if m.market == nil {
		return
	}

	var tm lastTradePriceMessage
	if err := json.Unmarshal(raw, &tm); err != nil {
		marketLog.Debugf("解析 last_trade_price 消息失败: %v", err)
		return
	}
	if tm.AssetID == "" || tm.Price == "" {
		return
	}
	newPrice, err := parsePriceString(tm.Price)
	if err != nil {
		return
	}

	var tokenType domain.TokenType
	if tm.AssetID == m.market.YesAssetID {
		tokenType = domain.TokenTypeUp
	} else if tm.AssetID == m.market.NoAssetID {
		tokenType = domain.TokenTypeDown
	} else {
		return
	}

	ev := &events.PriceChangedEvent{
		Market:    m.market,
		TokenType: tokenType,
		OldPrice:  nil,
		NewPrice:  newPrice,
		Timestamp: time.Now(),
	}

	m.diagMu.Lock()
	m.lastPriceAt = time.Now()
	m.priceEvCount++
	m.diagMu.Unlock()

	m.handlers.Emit(ctx, ev)
}

// handlePriceChange 处理价格变化（直接回调，不使用事件总线）
func (m *MarketStream) handlePriceChange(ctx context.Context, msg map[string]interface{}) {
	priceChanges, ok := msg["price_changes"].([]interface{})
	if !ok {
		marketLog.Debugf("⚠️ [价格处理] 价格变化消息中没有 price_changes 字段")
		return
	}

	// 检查 handlers 数量
	handlerCount := m.handlers.Count()
	if handlerCount == 0 {
		marketLog.Warnf("⚠️ [价格处理] MarketStream.handlers 为空，价格更新将被丢弃！市场=%s", m.market.Slug)
	} else {
		marketLog.Debugf("📊 [价格处理] 收到价格变化消息，handlers 数量=%d，市场=%s", handlerCount, m.market.Slug)
	}

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
			continue
		}

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

		event := &events.PriceChangedEvent{
			Market:    m.market,
			TokenType: tokenType,
			OldPrice:  nil,
			NewPrice:  latest.price,
			Timestamp: time.Now(),
		}

		m.diagMu.Lock()
		m.lastPriceAt = time.Now()
		m.priceEvCount++
		m.diagMu.Unlock()

		// 直接触发回调（不使用事件总线）
		// 注意：这里使用 handlerCount（在函数开头定义）
		marketLog.Debugf("📤 [价格处理] 触发价格变化回调: %s @ %dc (handlers=%d, 市场=%s)", 
			tokenType, latest.price.Cents, handlerCount, m.market.Slug)
		m.handlers.Emit(ctx, event)
	}
}

// Close 关闭连接
func (m *MarketStream) Close() error {
	// 发送关闭信号（避免重复关闭）
	select {
	case <-m.closeC:
		// 已经关闭，直接返回
		return nil
	default:
		close(m.closeC)
	}

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

	return nil
}

// SetProxyURL 设置代理 URL
func (m *MarketStream) SetProxyURL(proxyURL string) {
	m.proxyURL = proxyURL
}
