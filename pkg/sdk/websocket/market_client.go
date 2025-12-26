// Package websocket 提供市场数据 WebSocket 客户端
package websocket

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// MarketClient 管理 Polymarket 市场数据 WebSocket 连接
// 提供实时市场数据订阅，包括订单簿、价格变化、最新成交价等
type MarketClient struct {
	// 连接相关
	conn     *websocket.Conn
	connMu   sync.Mutex
	url      string
	config   *Config
	running  bool
	runningMu sync.RWMutex

	// 订阅管理
	subscriptions map[string]bool // asset_id -> 是否已订阅
	subMu         sync.RWMutex

	// 事件处理
	tradeHandler TradeHandler // 交易事件处理器（可选）

	// 消息通道
	msgChan chan interface{} // 市场消息通道
	errChan chan error       // 错误通道

	// 生命周期管理
	ctx    context.Context
	cancel context.CancelFunc
	stopCh chan struct{}
	doneCh chan struct{}

	// 重连状态
	reconnectAttempts int
	reconnectMu       sync.Mutex
	lastPong          time.Time
	lastPongMu        sync.RWMutex
}

// NewMarketClient 创建新的市场数据 WebSocket 客户端
// handler: 交易事件处理器（可选，如果为 nil 则不处理交易事件）
func NewMarketClient(handler TradeHandler) *MarketClient {
	return NewMarketClientWithConfig(handler, DefaultConfig())
}

// NewMarketClientWithConfig 使用自定义配置创建市场数据 WebSocket 客户端
func NewMarketClientWithConfig(handler TradeHandler, config *Config) *MarketClient {
	if config == nil {
		config = DefaultConfig()
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &MarketClient{
		url:           wsMarketURL,
		config:        config,
		subscriptions: make(map[string]bool),
		tradeHandler:  handler,
		msgChan:       make(chan interface{}, config.MessageBufferSize),
		errChan:       make(chan error, config.ErrorBufferSize),
		ctx:           ctx,
		cancel:        cancel,
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
		lastPong:      time.Now(),
	}
}

// Start 连接到 WebSocket 并开始监听
func (c *MarketClient) Start(ctx context.Context) error {
	c.runningMu.Lock()
	if c.running {
		c.runningMu.Unlock()
		return fmt.Errorf("WebSocket 客户端已在运行")
	}
	c.running = true
	c.runningMu.Unlock()

	// 使用传入的 context 或使用内部 context
	if ctx != nil {
		c.ctx = ctx
	}

	if err := c.connect(); err != nil {
		c.runningMu.Lock()
		c.running = false
		c.runningMu.Unlock()
		return fmt.Errorf("初始连接失败: %w", err)
	}

	// 启动读取循环和心跳循环
	go c.readLoop()
	go c.pingLoop()

	log.Printf("[WebSocket] 已启动连接到 %s", c.url)
	return nil
}

// Stop 优雅地关闭 WebSocket 连接
func (c *MarketClient) Stop() {
	c.runningMu.Lock()
	if !c.running {
		c.runningMu.Unlock()
		return
	}
	c.running = false
	c.runningMu.Unlock()

	c.cancel()
	close(c.stopCh)

	c.connMu.Lock()
	if c.conn != nil {
		// 发送关闭消息
		c.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		c.conn.Close()
		c.conn = nil
	}
	c.connMu.Unlock()

	// 等待 goroutine 完成
	select {
	case <-c.doneCh:
	case <-time.After(5 * time.Second):
		log.Printf("[WebSocket] 关闭超时")
	}

	log.Printf("[WebSocket] 已停止")
}

// Subscribe 订阅资产 ID 以监控交易事件
// assetIDs: 要订阅的资产 ID 列表
func (c *MarketClient) Subscribe(assetIDs ...string) error {
	c.subMu.Lock()
	newSubs := make([]string, 0)
	for _, id := range assetIDs {
		if !c.subscriptions[id] {
			c.subscriptions[id] = true
			newSubs = append(newSubs, id)
		}
	}
	c.subMu.Unlock()

	if len(newSubs) == 0 {
		return nil
	}

	return c.sendSubscription(newSubs)
}

// Unsubscribe 取消订阅资产 ID
// assetIDs: 要取消订阅的资产 ID 列表
func (c *MarketClient) Unsubscribe(assetIDs ...string) error {
	c.subMu.Lock()
	toRemove := make([]string, 0)
	for _, id := range assetIDs {
		if c.subscriptions[id] {
			delete(c.subscriptions, id)
			toRemove = append(toRemove, id)
		}
	}
	c.subMu.Unlock()

	if len(toRemove) == 0 {
		return nil
	}

	// 发送取消订阅消息
	msg := map[string]interface{}{
		"type":       "unsubscribe",
		"assets_ids": toRemove,
	}

	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("未连接")
	}

	return c.conn.WriteJSON(msg)
}

// SubscriptionCount 返回活跃订阅数量
func (c *MarketClient) SubscriptionCount() int {
	c.subMu.RLock()
	defer c.subMu.RUnlock()
	return len(c.subscriptions)
}

// Messages 返回市场消息通道
func (c *MarketClient) Messages() <-chan interface{} {
	return c.msgChan
}

// Errors 返回错误通道
func (c *MarketClient) Errors() <-chan error {
	return c.errChan
}

// IsRunning 检查客户端是否正在运行
func (c *MarketClient) IsRunning() bool {
	c.runningMu.RLock()
	defer c.runningMu.RUnlock()
	return c.running
}

// connect 建立 WebSocket 连接
func (c *MarketClient) connect() error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.conn != nil {
		c.conn.Close()
	}

	dialer := websocket.Dialer{
		ReadBufferSize:   c.config.ReadBufferSize,
		WriteBufferSize:  c.config.WriteBufferSize,
		HandshakeTimeout: c.config.HandshakeTimeout,
	}

	// 配置代理（如果提供）
	if c.config.ProxyURL != "" {
		proxyURL, err := url.Parse(c.config.ProxyURL)
		if err != nil {
			return fmt.Errorf("无效的代理 URL: %w", err)
		}
		dialer.Proxy = http.ProxyURL(proxyURL)
		log.Printf("[WebSocket] 使用代理: %s", c.config.ProxyURL)
	}

	// 设置 HTTP 头
	headers := make(http.Header)
	headers.Set("User-Agent", "polymarket-client/1.0")

	// 尝试连接（带重试）
	var conn *websocket.Conn
	var err error
	for i := 0; i < defaultMaxRetries; i++ {
		conn, _, err = dialer.Dial(c.url, headers)
		if err == nil {
			break
		}
		if i < defaultMaxRetries-1 {
			log.Printf("[WebSocket] 连接尝试 %d/%d 失败: %v, 重试中...", i+1, defaultMaxRetries, err)
			time.Sleep(time.Duration(i+1) * time.Second)
		}
	}

	if err != nil {
		return fmt.Errorf("连接失败: %w", err)
	}

	c.conn = conn
	// 参考示例代码：不设置 SetPongHandler
	// 示例代码在 handleMessage 中检查 "PONG" 文本响应，而不是使用 WebSocket 标准的 PongHandler
	// 初始化 lastPong 为当前时间
	c.lastPongMu.Lock()
	c.lastPong = time.Now()
	c.lastPongMu.Unlock()

	c.reconnectMu.Lock()
	c.reconnectAttempts = 0
	c.reconnectMu.Unlock()

	return nil
}

// sendSubscription 发送订阅消息
func (c *MarketClient) sendSubscription(assetIDs []string) error {
	if len(assetIDs) == 0 {
		return nil
	}

	// Polymarket 接受每批最多 100 个资产
	for i := 0; i < len(assetIDs); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(assetIDs) {
			end = len(assetIDs)
		}

		msg := map[string]interface{}{
			"type":       "market",
			"assets_ids": assetIDs[i:end],
		}

		c.connMu.Lock()
		if c.conn == nil {
			c.connMu.Unlock()
			return fmt.Errorf("未连接")
		}
		err := c.conn.WriteJSON(msg)
		c.connMu.Unlock()

		if err != nil {
			return fmt.Errorf("发送订阅失败: %w", err)
		}

		log.Printf("[WebSocket] 已订阅 %d 个资产 (批次 %d-%d)", end-i, i, end)
	}

	return nil
}

// resubscribe 重新订阅所有资产（重连后使用）
func (c *MarketClient) resubscribe() error {
	c.subMu.RLock()
	assetIDs := make([]string, 0, len(c.subscriptions))
	for id := range c.subscriptions {
		assetIDs = append(assetIDs, id)
	}
	c.subMu.RUnlock()

	if len(assetIDs) == 0 {
		return nil
	}

	return c.sendSubscription(assetIDs)
}

// readLoop 读取循环，持续从 WebSocket 读取消息
func (c *MarketClient) readLoop() {
	defer close(c.doneCh)
	log.Printf("[WebSocket] [DEBUG] readLoop started")

	messageCount := 0
	for {
		// 检查退出条件（非阻塞）
		select {
		case <-c.ctx.Done():
			log.Printf("[WebSocket] [DEBUG] readLoop stopped (context cancelled)")
			return
		case <-c.stopCh:
			log.Printf("[WebSocket] [DEBUG] readLoop stopped (stop signal)")
			return
		default:
		}

		c.connMu.Lock()
		conn := c.conn
		c.connMu.Unlock()

		if conn == nil {
			if c.config.ReconnectEnabled {
				c.reconnect()
			}
			// 如果连接为 nil，等待一段时间再重试，避免忙等待
			time.Sleep(1 * time.Second)
			continue
		}

		// 参考示例代码：不设置读取超时（SetReadDeadline）
		// 示例代码没有设置 SetReadDeadline，这样可以避免因读取超时导致的误判
		// 我们使用 ping/pong 机制来检测连接状态，而不是依赖读取超时
		// 连接正常时，ReadMessage 会一直阻塞等待消息，这是正常行为

		_, message, err := conn.ReadMessage()
		if err != nil {
			// 连接出错，立即清理连接，避免重复读取失败的连接
			c.connMu.Lock()
			if c.conn != nil {
				// 尝试关闭连接（忽略错误，因为连接可能已经失败）
				c.conn.Close()
				c.conn = nil
				log.Printf("[WebSocket] [DEBUG] 连接已清理（读取错误: %v）", err)
			}
			c.connMu.Unlock()
			
			// 简单的错误处理：如果是正常关闭，直接返回
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("[WebSocket] 连接正常关闭")
				return
			}
			// 其他错误：记录并重连
			log.Printf("[WebSocket] 读取错误: %v, 重连中...", err)
			if c.config.ReconnectEnabled {
				c.reconnect()
			} else {
				// 如果不允许重连，等待一段时间后继续（避免忙等待）
				time.Sleep(1 * time.Second)
			}
			continue
		}

		messageCount++
		log.Printf("[WebSocket] [DEBUG] readLoop received message #%d (length=%d)", messageCount, len(message))
		c.handleMessage(message)
	}
}

// pingLoop 心跳循环，定期发送 ping 消息
func (c *MarketClient) pingLoop() {
	ticker := time.NewTicker(c.config.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.connMu.Lock()
			conn := c.conn
			c.connMu.Unlock()

		if conn == nil {
			continue
		}

		// 参考示例代码：发送 "PING" 文本消息
		// 示例代码只发送 "PING" 文本消息，不发送 WebSocket 标准的 PingMessage
		// 也不进行复杂的超时检测，让连接自然运行
		// 如果连接断开，ReadMessage 会返回错误，readLoop 会处理重连
		if err := conn.WriteMessage(websocket.TextMessage, []byte("PING")); err != nil {
			log.Printf("[WebSocket] PING 发送失败: %v", err)
			// 如果发送失败，可能连接已断开，触发重连
			if c.config.ReconnectEnabled {
				c.reconnect()
			}
			continue
		}
	}
	}
}

// reconnect 重连逻辑（带指数退避）
func (c *MarketClient) reconnect() {
	c.reconnectMu.Lock()
	c.reconnectAttempts++
	attempts := c.reconnectAttempts
	c.reconnectMu.Unlock()

	if attempts > c.config.MaxReconnectAttempts {
		select {
		case c.errChan <- fmt.Errorf("达到最大重连次数 (%d)", c.config.MaxReconnectAttempts):
		default:
		}
		return
	}

	// 计算延迟（指数退避）
	delay := c.config.ReconnectDelay * time.Duration(attempts)
	if delay > c.config.MaxReconnectDelay {
		delay = c.config.MaxReconnectDelay
	}

	log.Printf("[WebSocket] %v 后重连 (尝试 %d/%d)...", delay, attempts, c.config.MaxReconnectAttempts)

	select {
	case <-c.ctx.Done():
		return
	case <-c.stopCh:
		return
	case <-time.After(delay):
	}

	if err := c.connect(); err != nil {
		log.Printf("[WebSocket] 重连失败: %v", err)
		return
	}

	// 重新订阅所有资产
	if err := c.resubscribe(); err != nil {
		log.Printf("[WebSocket] 重新订阅失败: %v", err)
	}
}

// handleMessage 处理接收到的消息
func (c *MarketClient) handleMessage(data []byte) {
	// 参考示例代码：首先检查是否是 PONG 响应
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] != '{' && trimmed[0] != '[' {
		// 处理文本消息（如 "PONG"）
		textMsg := string(trimmed)
		if textMsg == "PONG" || textMsg == "pong" {
			// 更新 pong 时间戳
			c.lastPongMu.Lock()
			c.lastPong = time.Now()
			c.lastPongMu.Unlock()
			log.Printf("[WebSocket] 收到 PONG 响应")
			return
		}
		log.Printf("[WebSocket] [DEBUG] Received non-JSON text message: %s", textMsg)
		return
	}
	
	msgPreview := string(data)
	if len(msgPreview) > 200 {
		msgPreview = msgPreview[:200] + "..."
	}
	log.Printf("[WebSocket] [DEBUG] Received message (length=%d): %s", len(data), msgPreview)

	// 先尝试解析为 map，这样可以处理 timestamp 为字符串的情况
	var msgMap map[string]interface{}
	if err := json.Unmarshal(data, &msgMap); err == nil {
		// 成功解析为 map，转换为 MarketMessage
		msg := c.parseMarketMessageFromMap(msgMap)
		if msg != nil {
			c.processMessage(*msg)
			return
		}
	}
	
	// 尝试直接解析为 MarketMessage（兼容旧格式）
	var msg MarketMessage
	if err := json.Unmarshal(data, &msg); err == nil {
		c.processMessage(msg)
		return
	}
	
	// 尝试解析为数组（某些消息以数组形式发送）
	var msgs []MarketMessage
	if err := json.Unmarshal(data, &msgs); err == nil {
		for _, m := range msgs {
			c.processMessage(m)
		}
		return
	}
	
	// 如果都失败了，尝试解析为 map 数组
	var msgMaps []map[string]interface{}
	if err := json.Unmarshal(data, &msgMaps); err == nil {
		for _, msgMap := range msgMaps {
			msg := c.parseMarketMessageFromMap(msgMap)
			if msg != nil {
				c.processMessage(*msg)
			}
		}
		return
	}
	
	// 所有解析都失败，记录错误（使用原始数据的前100个字符作为上下文）
	dataStr := string(data)
	if len(dataStr) > 100 {
		dataStr = dataStr[:100] + "..."
	}
	select {
	case c.errChan <- fmt.Errorf("解析消息失败，数据: %s", dataStr):
	default:
	}
}

// parseMarketMessageFromMap 从 map 解析 MarketMessage（支持官方格式）
func (c *MarketClient) parseMarketMessageFromMap(msgMap map[string]interface{}) *MarketMessage {
	msg := &MarketMessage{}
	
	// 解析 event_type
	if eventType, ok := msgMap["event_type"].(string); ok {
		msg.EventType = EventType(eventType)
	}
	
	// 解析 market
	if market, ok := msgMap["market"].(string); ok {
		msg.Market = market
	}
	
	// 解析 timestamp（支持字符串和数字，可能是毫秒级）
	if timestamp, ok := msgMap["timestamp"]; ok {
		switch v := timestamp.(type) {
		case string:
			// 可能是毫秒级时间戳
			if ts, err := strconv.ParseInt(v, 10, 64); err == nil {
				// 如果是毫秒级（> 1e12），转换为秒级
				if ts > 1e12 {
					ts = ts / 1000
				}
				msg.Timestamp = ts
			}
		case float64:
			ts := int64(v)
			// 如果是毫秒级，转换为秒级
			if ts > 1e12 {
				ts = ts / 1000
			}
			msg.Timestamp = ts
		case int64:
			ts := v
			// 如果是毫秒级，转换为秒级
			if ts > 1e12 {
				ts = ts / 1000
			}
			msg.Timestamp = ts
		case int:
			ts := int64(v)
			if ts > 1e12 {
				ts = ts / 1000
			}
			msg.Timestamp = ts
		}
	}
	
	// 处理 price_changes 数组（官方格式）
	if priceChanges, ok := msgMap["price_changes"].([]interface{}); ok {
		// 如果消息包含 price_changes 但没有 event_type，默认为 price_change
		if msg.EventType == "" {
			msg.EventType = EventPriceChange
			log.Printf("[WebSocket] [DEBUG] 消息包含 price_changes 但没有 event_type，设置为 price_change")
		}
		// 将 price_changes 转换为 changes 格式
		if changesBytes, err := json.Marshal(priceChanges); err == nil {
			msg.Changes = changesBytes
		}
		// 如果有 price_changes，提取第一个资产的信息
		if len(priceChanges) > 0 {
			if firstChange, ok := priceChanges[0].(map[string]interface{}); ok {
				if assetID, ok := firstChange["asset_id"].(string); ok {
					msg.AssetID = assetID
				}
				// 优先使用 best_ask 作为价格
				if bestAsk, ok := firstChange["best_ask"].(string); ok {
					msg.Price = bestAsk
				} else if price, ok := firstChange["price"].(string); ok {
					msg.Price = price
				}
			}
		}
	} else {
		// 兼容旧格式：直接解析 asset_id 和 price
		if assetID, ok := msgMap["asset_id"].(string); ok {
			msg.AssetID = assetID
		}
		if price, ok := msgMap["price"].(string); ok {
			msg.Price = price
		}
		// 解析 changes
		if changes, ok := msgMap["changes"]; ok {
			if changesBytes, err := json.Marshal(changes); err == nil {
				msg.Changes = changesBytes
			}
		}
	}
	
	// 解析 hash
	if hash, ok := msgMap["hash"].(string); ok {
		msg.Hash = hash
	}
	
	return msg
}

// processMessage 处理单个消息
func (c *MarketClient) processMessage(msg MarketMessage) {
	// 如果 EventType 为空，尝试从消息内容推断
	if msg.EventType == "" {
		if len(msg.Changes) > 0 {
			// 如果有 changes，可能是 price_change 或 book
			// 检查 changes 是否包含 price_changes 格式
			var testChanges []interface{}
			if err := json.Unmarshal(msg.Changes, &testChanges); err == nil {
				// 如果成功解析为数组，且包含 best_bid/best_ask，可能是 price_change
				if len(testChanges) > 0 {
					if first, ok := testChanges[0].(map[string]interface{}); ok {
						if _, hasBid := first["best_bid"]; hasBid {
							if _, hasAsk := first["best_ask"]; hasAsk {
								msg.EventType = EventPriceChange
								log.Printf("[WebSocket] [DEBUG] 推断消息类型为 price_change（基于 changes 格式）")
							}
						}
					}
				}
			}
		}
		// 如果仍然为空，且有 Price 字段，可能是 last_trade_price
		if msg.EventType == "" && msg.Price != "" {
			msg.EventType = EventLastTradePrice
			log.Printf("[WebSocket] [DEBUG] 推断消息类型为 last_trade_price（基于 price 字段）")
		}
	}
	
	// 将消息发送到消息通道
	select {
	case c.msgChan <- msg:
		log.Printf("[WebSocket] [DEBUG] 消息已发送到 channel: EventType=%s, AssetID=%s", msg.EventType, msg.AssetID)
	default:
		select {
		case c.errChan <- fmt.Errorf("消息通道已满，丢弃 %s 消息", msg.EventType):
		default:
		}
		log.Printf("[WebSocket] [DEBUG] 消息通道已满，丢弃消息: EventType=%s", msg.EventType)
	}

	// 处理交易事件
	switch msg.EventType {
	case EventLastTradePrice:
		// 这表示刚刚发生了一笔交易
		if c.tradeHandler != nil && msg.Price != "" {
			var price float64
			fmt.Sscanf(msg.Price, "%f", &price)

			event := TradeEvent{
				AssetID:   msg.AssetID,
				Price:     price,
				Timestamp: time.Now(), // 使用当前时间，因为交易刚刚发生
			}
			c.tradeHandler(event)
		}

	case EventPriceChange:
		// 价格变化 - 可能表示交易活动
		// 我们主要使用 last_trade_price 进行交易检测

	case EventBook:
		// 订单簿更新 - 对流动性监控有用
		// 但不直接表示交易

	case EventTickSizeChange:
		// 最小价格单位变化 - 与交易检测无关
	}
}

