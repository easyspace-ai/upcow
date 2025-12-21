package rtds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client represents a RTDS WebSocket client
type Client struct {
	conn                 *websocket.Conn
	url                  string
	proxyURL             string
	pingInterval         time.Duration
	writeTimeout         time.Duration
	readTimeout          time.Duration
	messageHandlers      map[string]MessageHandler
	handlersMutex        sync.RWMutex
	statsMutex           sync.RWMutex
	lastMessageAt        time.Time
	lastParseErrorAt     time.Time
	parseErrorCount      uint64
	lastSubscribeAckAt   time.Time
	lastUnsubscribeAckAt time.Time
	ctx                  context.Context
	cancel               context.CancelFunc
	wg                   sync.WaitGroup
	connected            bool
	connectedMutex       sync.RWMutex
	reconnect            bool
	reconnectDelay       time.Duration
	maxReconnect         int
	reconnectCount       int
	reconnectMutex       sync.Mutex
	isReconnecting       bool // 防止并发重连
	activeSubscriptions  []Subscription
	subscriptionsMutex   sync.RWMutex
	logger               Logger
}

// DebugSnapshot returns a concise snapshot for troubleshooting.
// It is safe to call concurrently.
func (c *Client) DebugSnapshot() string {
	c.connectedMutex.RLock()
	connected := c.connected
	c.connectedMutex.RUnlock()

	c.subscriptionsMutex.RLock()
	subs := make([]Subscription, 0, len(c.activeSubscriptions))
	subs = append(subs, c.activeSubscriptions...)
	c.subscriptionsMutex.RUnlock()

	c.handlersMutex.RLock()
	topics := make([]string, 0, len(c.messageHandlers))
	for topic := range c.messageHandlers {
		topics = append(topics, topic)
	}
	c.handlersMutex.RUnlock()

	c.statsMutex.RLock()
	lastMsgAt := c.lastMessageAt
	lastParseErrAt := c.lastParseErrorAt
	parseErrCnt := c.parseErrorCount
	lastSubAckAt := c.lastSubscribeAckAt
	lastUnsubAckAt := c.lastUnsubscribeAckAt
	c.statsMutex.RUnlock()

	return fmt.Sprintf(
		"connected=%v url=%s proxy=%s subs=%d handlers=%d lastMsgAt=%s parseErrCnt=%d lastParseErrAt=%s lastSubAckAt=%s lastUnsubAckAt=%s topics=%v subs=%v",
		connected,
		c.url,
		c.proxyURL,
		len(subs),
		len(topics),
		formatTimeOrEmpty(lastMsgAt),
		parseErrCnt,
		formatTimeOrEmpty(lastParseErrAt),
		formatTimeOrEmpty(lastSubAckAt),
		formatTimeOrEmpty(lastUnsubAckAt),
		topics,
		subs,
	)
}

func formatTimeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

func truncateForLog(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// ClientConfig represents configuration for the RTDS client
type ClientConfig struct {
	URL            string
	ProxyURL       string
	PingInterval   time.Duration
	WriteTimeout   time.Duration
	ReadTimeout    time.Duration
	Reconnect      bool
	ReconnectDelay time.Duration
	MaxReconnect   int
	Logger         Logger
}

// DefaultClientConfig returns a default client configuration
func DefaultClientConfig() *ClientConfig {
	return &ClientConfig{
		URL:            RTDSWebSocketURL,
		ProxyURL:       "http://127.0.0.1:15236",
		PingInterval:   5 * time.Second,
		WriteTimeout:   10 * time.Second,
		ReadTimeout:    60 * time.Second,
		Reconnect:      true,
		ReconnectDelay: 5 * time.Second,
		MaxReconnect:   10,
	}
}

// NewClient creates a new RTDS client with default configuration
func NewClient() *Client {
	return NewClientWithConfig(DefaultClientConfig())
}

// NewClientWithConfig creates a new RTDS client with custom configuration
func NewClientWithConfig(config *ClientConfig) *Client {
	if config == nil {
		config = DefaultClientConfig()
	}
	if config.URL == "" {
		config.URL = RTDSWebSocketURL
	}
	if config.PingInterval == 0 {
		config.PingInterval = 5 * time.Second
	}
	if config.WriteTimeout == 0 {
		config.WriteTimeout = 10 * time.Second
	}
	if config.ReadTimeout == 0 {
		config.ReadTimeout = 60 * time.Second
	}
	if config.ReconnectDelay == 0 {
		config.ReconnectDelay = 5 * time.Second
	}
	if config.MaxReconnect == 0 {
		config.MaxReconnect = 10
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Set up logger
	logger := config.Logger
	if logger == nil {
		logger = &DefaultLogger{}
	}

	return &Client{
		url:                 config.URL,
		proxyURL:            config.ProxyURL,
		pingInterval:        config.PingInterval,
		writeTimeout:        config.WriteTimeout,
		readTimeout:         config.ReadTimeout,
		messageHandlers:     make(map[string]MessageHandler),
		ctx:                 ctx,
		cancel:              cancel,
		reconnect:           config.Reconnect,
		reconnectDelay:      config.ReconnectDelay,
		maxReconnect:        config.MaxReconnect,
		activeSubscriptions: make([]Subscription, 0),
		logger:              logger,
	}
}

// Connect establishes a WebSocket connection to the RTDS server
func (c *Client) Connect() error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 30 * time.Second, // 增加超时时间到 30 秒
	}

	// Configure proxy if proxyURL is set
	if c.proxyURL != "" {
		proxyURL, err := url.Parse(c.proxyURL)
		if err != nil {
			return fmt.Errorf("invalid proxy URL: %w", err)
		}
		dialer.Proxy = http.ProxyURL(proxyURL)
		if c.logger != nil {
			c.logger.Printf("Connecting to RTDS via proxy: %s\n", c.proxyURL)
		}
	} else {
		if c.logger != nil {
			c.logger.Printf("Connecting to RTDS directly (no proxy)\n")
		}
	}

	conn, _, err := dialer.Dial(c.url, nil)
	if err != nil {
		if c.proxyURL != "" {
			return fmt.Errorf("failed to connect to RTDS via proxy %s: %w", c.proxyURL, err)
		}
		return fmt.Errorf("failed to connect to RTDS: %w", err)
	}

	c.conn = conn
	c.setConnected(true)
	c.reconnectCount = 0

	// Start message reader
	c.wg.Add(1)
	go c.readMessages()

	// Start ping sender
	c.wg.Add(1)
	go c.sendPings()

	// Re-subscribe to active subscriptions after reconnection
	c.resubscribe()

	return nil
}

// Disconnect closes the WebSocket connection
func (c *Client) Disconnect() error {
	// Disable reconnection when explicitly disconnecting
	c.reconnectMutex.Lock()
	c.reconnect = false
	c.reconnectMutex.Unlock()

	c.setConnected(false)

	// 先取消 context，让 goroutine 知道要退出
	c.cancel()

	// Clear active subscriptions on disconnect
	c.subscriptionsMutex.Lock()
	c.activeSubscriptions = make([]Subscription, 0)
	c.subscriptionsMutex.Unlock()

	// 关闭连接（这会触发 readMessages 和 sendPings 中的错误，让它们退出）
	if c.conn != nil {
		// 关闭连接，这会中断 ReadMessage 和 WriteMessage 的阻塞
		err := c.conn.Close()
		c.conn = nil

		// 等待 goroutine 退出，但设置超时避免无限期等待
		done := make(chan struct{})
		go func() {
			c.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// goroutine 已退出
		case <-time.After(3 * time.Second):
			// 超时，记录警告但继续
			if c.logger != nil {
				c.logger.Printf("等待 goroutine 退出超时（3秒），继续断开连接\n")
			}
		}

		return err
	}

	// 如果没有连接，仍然等待 goroutine 退出（带超时）
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// goroutine 已退出
	case <-time.After(3 * time.Second):
		// 超时，记录警告但继续
		if c.logger != nil {
			c.logger.Printf("等待 goroutine 退出超时（3秒），继续断开连接\n")
		}
	}

	return nil
}

// IsConnected returns whether the client is currently connected
func (c *Client) IsConnected() bool {
	c.connectedMutex.RLock()
	defer c.connectedMutex.RUnlock()
	return c.connected
}

func (c *Client) setConnected(connected bool) {
	c.connectedMutex.Lock()
	defer c.connectedMutex.Unlock()
	c.connected = connected
}

// RegisterHandler registers a message handler for a specific topic
func (c *Client) RegisterHandler(topic string, handler MessageHandler) {
	c.handlersMutex.Lock()
	defer c.handlersMutex.Unlock()
	c.messageHandlers[topic] = handler
}

// UnregisterHandler removes a message handler for a specific topic
func (c *Client) UnregisterHandler(topic string) {
	c.handlersMutex.Lock()
	defer c.handlersMutex.Unlock()
	delete(c.messageHandlers, topic)
}

// SendMessage sends a JSON message to the WebSocket server
func (c *Client) SendMessage(message interface{}) error {
	if !c.IsConnected() {
		return errors.New("client is not connected")
	}

	// 检查连接对象是否存在
	if c.conn == nil {
		return errors.New("connection is nil")
	}

	// Log the message being sent for debugging
	if msgBytes, err := json.Marshal(message); err == nil && c.logger != nil {
		c.logger.Printf("Sending RTDS message: %s\n", string(msgBytes))
	}

	c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout))
	if err := c.conn.WriteJSON(message); err != nil {
		// 如果写入失败，标记为未连接
		c.setConnected(false)
		return fmt.Errorf("failed to send message: %w", err)
	}
	return nil
}

// readMessages reads messages from the WebSocket connection
func (c *Client) readMessages() {
	defer c.wg.Done()

	// 使用 recover 捕获可能的 panic（例如 "repeated read on failed websocket connection"）
	defer func() {
		if r := recover(); r != nil {
			if c.logger != nil {
				c.logger.Printf("readMessages panic recovered: %v\n", r)
			}
			// 确保连接状态被正确清理
			c.setConnected(false)
			// 异步触发重连逻辑，避免在 defer 中阻塞
			go c.handleDisconnect()
		}
	}()

	for {
		// 首先检查 context 是否已取消
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		// 检查连接状态和连接对象
		if !c.IsConnected() || c.conn == nil {
			return
		}

		// 设置读取超时，使用较长的超时时间（30秒）以减少频繁的超时检查
		// 这样可以提高连接稳定性，避免频繁的超时导致连接状态检查问题
		// 超时主要用于定期检查 context，而不是真正的读取超时
		readTimeout := 30 * time.Second
		if c.readTimeout > 0 && c.readTimeout < readTimeout {
			readTimeout = c.readTimeout
		}
		c.conn.SetReadDeadline(time.Now().Add(readTimeout))
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			// 检查是否是超时错误（这是正常的，用于定期检查 context）
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				// 超时，检查 context 和连接状态，然后继续循环
				select {
				case <-c.ctx.Done():
					return
				default:
				}
				if !c.IsConnected() || c.conn == nil {
					return
				}
				// 超时是正常的，继续等待消息（不记录日志，避免刷屏）
				continue
			}

			// 检查 context 是否已取消
			select {
			case <-c.ctx.Done():
				return
			default:
			}

			// 连接已失败，立即标记为未连接，避免再次读取
			c.setConnected(false)

			// 记录错误（但不记录正常的关闭错误）
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				if c.logger != nil {
					c.logger.Printf("WebSocket read error: %v\n", err)
				}
			}

			// 处理断开连接（但不尝试再次读取）
			c.handleDisconnect()
			return
		}

		c.statsMutex.Lock()
		c.lastMessageAt = time.Now()
		c.statsMutex.Unlock()

		// RTDS 文档宣称 payload 是 JSON，但在真实链路（尤其是代理/网关）里可能出现：
		// - 空消息/纯空白（会导致 json.Unmarshal 报 EOF / unexpected end）
		// - 文本心跳 PING/PONG
		trimmed := strings.TrimSpace(string(data))
		if trimmed == "" {
			// 空消息：直接忽略（避免刷屏）
			continue
		}
		if trimmed == "PING" {
			// 文档建议发送 PING；这里兼容服务器文本心跳
			_ = c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout))
			_ = c.conn.WriteMessage(websocket.TextMessage, []byte("PONG"))
			continue
		}
		if trimmed == "PONG" {
			continue
		}

		var msg Message
		if err := json.Unmarshal([]byte(trimmed), &msg); err != nil {
			// 只记录高信号错误；EOF/意外截断多为代理/心跳噪音
			if c.logger != nil {
				c.statsMutex.Lock()
				c.parseErrorCount++
				shouldLog := c.lastParseErrorAt.IsZero() || time.Since(c.lastParseErrorAt) > 5*time.Second
				if shouldLog {
					c.lastParseErrorAt = time.Now()
				}
				c.statsMutex.Unlock()

				if shouldLog {
					c.logger.Printf("Failed to parse message: %v (len=%d preview=%q)\n", err, len(trimmed), truncateForLog(trimmed, 240))
				}
			}
			continue
		}

		// Check for error messages in the payload
		if msg.Topic == "error" || msg.Type == "error" {
			var errorPayload map[string]interface{}
			if err := json.Unmarshal(msg.Payload, &errorPayload); err == nil {
				errorMsg := fmt.Sprintf("Server error: %v", errorPayload)
				c.logger.Printf("%s\n", errorMsg)

				// Handle authentication errors specifically
				if errorCode, ok := errorPayload["code"].(string); ok {
					if errorCode == "AUTH_FAILED" || errorCode == "UNAUTHORIZED" {
						c.logger.Printf("Authentication failed. Connection may be closed.\n")
						c.handleDisconnect()
						return
					}
				}
			} else {
				c.logger.Printf("Error message received but failed to parse: %v\n", err)
			}
			continue
		}

		c.handleMessage(&msg)
	}
}

// sendPings sends periodic PING messages to keep the connection alive
func (c *Client) sendPings() {
	defer c.wg.Done()

	// 使用 recover 捕获可能的 panic
	defer func() {
		if r := recover(); r != nil {
			if c.logger != nil {
				c.logger.Printf("sendPings panic recovered: %v\n", r)
			}
			// 确保连接状态被正确清理
			c.setConnected(false)
		}
	}()

	ticker := time.NewTicker(c.pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			// 检查连接状态和连接对象
			if !c.IsConnected() || c.conn == nil {
				return
			}

			// 尝试发送 ping，如果失败则处理断开连接
			c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				if c.logger != nil {
					c.logger.Printf("Failed to send ping: %v\n", err)
				}
				// 连接已失败，立即标记为未连接
				c.setConnected(false)
				c.handleDisconnect()
				return
			}
		}
	}
}

// handleMessage processes an incoming message
func (c *Client) handleMessage(msg *Message) {
	// Log received message for debugging
	c.logger.Printf("Received RTDS message: topic=%s, type=%s\n", msg.Topic, msg.Type)

	// 订阅确认/管理消息：通常不需要业务 handler
	if msg.Type == "subscribe" || msg.Type == "unsubscribe" {
		preview := ""
		if len(msg.Payload) > 0 {
			preview = truncateForLog(strings.TrimSpace(string(msg.Payload)), 240)
		}
		c.statsMutex.Lock()
		if msg.Type == "subscribe" {
			c.lastSubscribeAckAt = time.Now()
		} else {
			c.lastUnsubscribeAckAt = time.Now()
		}
		c.statsMutex.Unlock()

		c.logger.Printf("RTDS subscription ack: topic=%s, type=%s payload_preview=%q\n", msg.Topic, msg.Type, preview)
		return
	}

	c.handlersMutex.RLock()
	handler, exists := c.messageHandlers[msg.Topic]
	wildcardHandler, wildcardExists := c.messageHandlers["*"]
	c.handlersMutex.RUnlock()

	if exists && handler != nil {
		// 对于 crypto_prices_chainlink，记录 payload 预览以便调试
		if msg.Topic == "crypto_prices_chainlink" {
			preview := ""
			if len(msg.Payload) > 0 {
				preview = truncateForLog(strings.TrimSpace(string(msg.Payload)), 200)
			}
			c.logger.Printf("Calling handler for crypto_prices_chainlink, payload_preview=%q\n", preview)
		}
		if err := handler(msg); err != nil {
			c.logger.Printf("Error handling message for topic %s: %v\n", msg.Topic, err)
		} else if msg.Topic == "crypto_prices_chainlink" {
			c.logger.Printf("Successfully handled crypto_prices_chainlink message\n")
		}
	} else {
		// 如果有 wildcard handler，不把“无 handler”当问题（避免无意义刷屏）
		if !wildcardExists || wildcardHandler == nil {
			// Log registered topics for debugging
			c.handlersMutex.RLock()
			topics := make([]string, 0, len(c.messageHandlers))
			for topic := range c.messageHandlers {
				topics = append(topics, topic)
			}
			c.handlersMutex.RUnlock()
			c.logger.Printf("No handler registered for topic %s (registered: %v)\n", msg.Topic, topics)
		}
	}

	// Also check for wildcard handler
	if wildcardExists && wildcardHandler != nil {
		if err := wildcardHandler(msg); err != nil {
			c.logger.Printf("Error handling message with wildcard handler: %v\n", err)
		}
	}
}

// handleDisconnect handles disconnection and optionally reconnects
func (c *Client) handleDisconnect() {
	// 确保连接状态被标记为未连接
	c.setConnected(false)

	// 清理连接对象（但不关闭，因为可能已经关闭了）
	// 注意：这里不设置 c.conn = nil，因为重连时需要创建新连接

	c.reconnectMutex.Lock()
	shouldReconnect := c.reconnect
	// 检查是否已经在重连中，避免并发重连
	if c.isReconnecting {
		c.reconnectMutex.Unlock()
		if c.logger != nil {
			c.logger.Printf("Reconnection already in progress, skipping\n")
		}
		return
	}
	c.reconnectMutex.Unlock()

	if !shouldReconnect {
		return
	}

	c.reconnectMutex.Lock()

	// Double-check reconnect flag after acquiring lock
	if !c.reconnect {
		c.reconnectMutex.Unlock()
		return
	}

	// 再次检查是否已经在重连中
	if c.isReconnecting {
		c.reconnectMutex.Unlock()
		return
	}

	if c.reconnectCount >= c.maxReconnect {
		c.reconnectMutex.Unlock()
		if c.logger != nil {
			c.logger.Printf("Max reconnection attempts reached\n")
		}
		return
	}

	c.reconnectCount++
	c.isReconnecting = true // 标记正在重连
	attemptNum := c.reconnectCount
	c.reconnectMutex.Unlock()

	if c.logger != nil {
		c.logger.Printf("Attempting to reconnect (%d/%d)...\n", attemptNum, c.maxReconnect)
	}

	// Use a ticker to check reconnect flag periodically during sleep
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	slept := time.Duration(0)
	for slept < c.reconnectDelay {
		select {
		case <-ticker.C:
			slept += 100 * time.Millisecond
			// Check if reconnect was disabled during sleep
			c.reconnectMutex.Lock()
			shouldReconnect := c.reconnect
			c.reconnectMutex.Unlock()
			if !shouldReconnect {
				if c.logger != nil {
					c.logger.Printf("Reconnection cancelled\n")
				}
				return
			}
		}
	}

	// Double-check reconnect flag before attempting connection
	c.reconnectMutex.Lock()
	shouldReconnect = c.reconnect
	c.reconnectMutex.Unlock()
	if !shouldReconnect {
		return
	}

	// 清理旧连接（如果存在）
	if c.conn != nil {
		// 不返回错误，因为连接可能已经关闭
		_ = c.conn.Close()
		c.conn = nil
	}

	// Create new context for reconnection
	c.ctx, c.cancel = context.WithCancel(context.Background())

	// 注意：锁已经在上面释放了，这里不需要再次释放
	// 直接尝试连接（Connect 内部可能会需要锁）

	if err := c.Connect(); err != nil {
		c.reconnectMutex.Lock()
		if c.logger != nil {
			c.logger.Printf("Reconnection failed: %v (attempt %d/%d)\n", err, c.reconnectCount, c.maxReconnect)
		}
		// 如果重连失败且未达到最大次数，继续尝试重连
		if c.reconnectCount < c.maxReconnect {
			c.isReconnecting = false // 重置标志，允许下次重连
			c.reconnectMutex.Unlock()
			// 延迟后继续重连，避免立即重试导致资源浪费
			go func() {
				time.Sleep(c.reconnectDelay)
				c.handleDisconnect()
			}()
		} else {
			c.isReconnecting = false // 重置标志
			c.reconnectMutex.Unlock()
			if c.logger != nil {
				c.logger.Printf("Max reconnection attempts reached, giving up\n")
			}
		}
	} else {
		c.reconnectMutex.Lock()
		if c.logger != nil {
			c.logger.Printf("Reconnected successfully, resubscribing to %d subscription(s)...\n", len(c.activeSubscriptions))
		}
		c.reconnectCount = 0
		c.isReconnecting = false // 重置标志
		c.reconnectMutex.Unlock()
		// Note: resubscribe is called automatically in Connect()
	}
}

// resubscribe re-subscribes to all active subscriptions
func (c *Client) resubscribe() {
	c.subscriptionsMutex.RLock()
	subscriptions := make([]Subscription, len(c.activeSubscriptions))
	copy(subscriptions, c.activeSubscriptions)
	c.subscriptionsMutex.RUnlock()

	if len(subscriptions) == 0 {
		return
	}

	// Wait a bit for the connection to stabilize
	time.Sleep(100 * time.Millisecond)

	req := SubscriptionRequest{
		Action:        ActionSubscribe,
		Subscriptions: subscriptions,
	}

	if err := c.SendMessage(req); err != nil {
		c.logger.Printf("Failed to resubscribe after reconnection: %v\n", err)
	} else {
		c.logger.Printf("Successfully resubscribed to %d subscription(s): %v\n", len(subscriptions), subscriptions)
	}
}
