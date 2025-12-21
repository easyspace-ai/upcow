package rtds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client represents a RTDS WebSocket client
type Client struct {
	conn                *websocket.Conn
	url                 string
	proxyURL            string
	pingInterval        time.Duration
	writeTimeout        time.Duration
	readTimeout         time.Duration
	messageHandlers     map[string]MessageHandler
	handlersMutex       sync.RWMutex
	ctx                 context.Context
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	connected           bool
	connectedMutex      sync.RWMutex
	reconnect           bool
	reconnectDelay      time.Duration
	maxReconnect        int
	reconnectCount      int
	reconnectMutex      sync.Mutex
	activeSubscriptions []Subscription
	subscriptionsMutex  sync.RWMutex
	logger              Logger
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

		// 设置较短的读取超时，以便能够及时响应 context 取消
		// 使用较小的超时值（1秒），这样即使 ReadMessage 阻塞，也能快速检查 context
		c.conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			// 检查是否是超时错误（这是正常的，用于检查 context）
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				// 超时，继续循环检查 context
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

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			c.logger.Printf("Failed to parse message: %v\n", err)
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

	c.handlersMutex.RLock()
	handler, exists := c.messageHandlers[msg.Topic]
	c.handlersMutex.RUnlock()

	if exists && handler != nil {
		if err := handler(msg); err != nil {
			c.logger.Printf("Error handling message for topic %s: %v\n", msg.Topic, err)
		}
	} else {
		// Log registered topics for debugging
		c.handlersMutex.RLock()
		topics := make([]string, 0, len(c.messageHandlers))
		for topic := range c.messageHandlers {
			topics = append(topics, topic)
		}
		c.handlersMutex.RUnlock()
		c.logger.Printf("No handler registered for topic %s (registered: %v)\n", msg.Topic, topics)
	}

	// Also check for wildcard handler
	c.handlersMutex.RLock()
	wildcardHandler, exists := c.messageHandlers["*"]
	c.handlersMutex.RUnlock()

	if exists && wildcardHandler != nil {
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
	c.reconnectMutex.Unlock()

	if !shouldReconnect {
		return
	}

	c.reconnectMutex.Lock()
	defer c.reconnectMutex.Unlock()

	// Double-check reconnect flag after acquiring lock
	if !c.reconnect {
		return
	}

	if c.reconnectCount >= c.maxReconnect {
		if c.logger != nil {
			c.logger.Printf("Max reconnection attempts reached\n")
		}
		return
	}

	c.reconnectCount++
	if c.logger != nil {
		c.logger.Printf("Attempting to reconnect (%d/%d)...\n", c.reconnectCount, c.maxReconnect)
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
				c.logger.Printf("Reconnection cancelled\n")
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

	if err := c.Connect(); err != nil {
		if c.logger != nil {
			c.logger.Printf("Reconnection failed: %v\n", err)
		}
	} else {
		if c.logger != nil {
			c.logger.Printf("Reconnected successfully\n")
		}
		c.reconnectCount = 0
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
		c.logger.Printf("Resubscribed to %d subscription(s)\n", len(subscriptions))
	}
}
