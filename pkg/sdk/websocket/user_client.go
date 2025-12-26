// Package websocket 提供用户数据 WebSocket 客户端
package websocket

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/betbot/gobet/pkg/sdk/api"
	"github.com/gorilla/websocket"
)

// UserClient 管理 Polymarket 用户数据 WebSocket 连接（需要认证）
// 提供用户相关的实时数据，包括交易、订单、持仓等
type UserClient struct {
	// 连接相关
	conn      *websocket.Conn
	connMu    sync.Mutex
	url       string
	config    *Config
	apiCreds  *api.APICreds
	running   bool
	runningMu sync.RWMutex

	// 订阅管理
	markets map[string]bool // condition_id -> 是否已订阅
	subMu   sync.RWMutex

	// 事件处理
	tradeHandler UserTradeHandler // 交易事件处理器（可选）

	// 消息通道
	msgChan chan interface{} // 用户消息通道
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

// NewUserClient 创建新的用户数据 WebSocket 客户端
// creds: API 凭证（必需）
// handler: 交易事件处理器（可选）
func NewUserClient(creds *api.APICreds, handler UserTradeHandler) *UserClient {
	return NewUserClientWithConfig(creds, handler, DefaultConfig())
}

// NewUserClientWithConfig 使用自定义配置创建用户数据 WebSocket 客户端
func NewUserClientWithConfig(creds *api.APICreds, handler UserTradeHandler, config *Config) *UserClient {
	if config == nil {
		config = DefaultConfig()
	}

	if creds == nil {
		panic("API 凭证不能为空")
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &UserClient{
		url:          wsUserURL,
		config:       config,
		apiCreds:     creds,
		markets:      make(map[string]bool),
		tradeHandler: handler,
		msgChan:      make(chan interface{}, config.MessageBufferSize),
		errChan:      make(chan error, config.ErrorBufferSize),
		ctx:          ctx,
		cancel:       cancel,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
		lastPong:     time.Now(),
	}
}

// Start 连接到 WebSocket 并开始监听
func (c *UserClient) Start(ctx context.Context) error {
	c.runningMu.Lock()
	if c.running {
		c.runningMu.Unlock()
		return fmt.Errorf("WebSocket 用户客户端已在运行")
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

	log.Printf("[WSUser] 已启动连接到 %s", c.url)
	return nil
}

// Stop 优雅地关闭 WebSocket 连接
func (c *UserClient) Stop() {
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
		log.Printf("[WSUser] 关闭超时")
	}

	log.Printf("[WSUser] 已停止")
}

// SubscribeMarkets 订阅特定市场的用户活动
// conditionIDs: 要订阅的市场条件 ID 列表
func (c *UserClient) SubscribeMarkets(conditionIDs ...string) error {
	c.subMu.Lock()
	newSubs := make([]string, 0)
	for _, id := range conditionIDs {
		if !c.markets[id] {
			c.markets[id] = true
			newSubs = append(newSubs, id)
		}
	}
	c.subMu.Unlock()

	if len(newSubs) == 0 {
		return nil
	}

	return c.sendSubscription(newSubs)
}

// UnsubscribeMarkets 取消订阅市场
// conditionIDs: 要取消订阅的市场条件 ID 列表
func (c *UserClient) UnsubscribeMarkets(conditionIDs ...string) error {
	c.subMu.Lock()
	toRemove := make([]string, 0)
	for _, id := range conditionIDs {
		if c.markets[id] {
			delete(c.markets, id)
			toRemove = append(toRemove, id)
		}
	}
	c.subMu.Unlock()

	if len(toRemove) == 0 {
		return nil
	}

	// 注意：Polymarket 可能不支持显式取消订阅
	// 这里我们只是从内部记录中移除
	log.Printf("[WSUser] 已取消订阅 %d 个市场", len(toRemove))
	return nil
}

// SubscriptionCount 返回活跃订阅数量
func (c *UserClient) SubscriptionCount() int {
	c.subMu.RLock()
	defer c.subMu.RUnlock()
	return len(c.markets)
}

// Messages 返回用户消息通道
func (c *UserClient) Messages() <-chan interface{} {
	return c.msgChan
}

// Errors 返回错误通道
func (c *UserClient) Errors() <-chan error {
	return c.errChan
}

// IsRunning 检查客户端是否正在运行
func (c *UserClient) IsRunning() bool {
	c.runningMu.RLock()
	defer c.runningMu.RUnlock()
	return c.running
}

// connect 建立 WebSocket 连接并认证
func (c *UserClient) connect() error {
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
		log.Printf("[WSUser] 使用代理: %s", c.config.ProxyURL)
	}

	// 设置 HTTP 头（包含认证信息）
	headers := make(http.Header)
	headers.Set("User-Agent", "polymarket-client/1.0")

	// 添加认证头
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	message := timestamp + "GET" + "/ws/user"
	signature := c.generateWSSignature(message, c.apiCreds.APISecret)

	headers.Set("POLY-API-KEY", c.apiCreds.APIKey)
	headers.Set("POLY-SIGNATURE", signature)
	headers.Set("POLY-TIMESTAMP", timestamp)
	headers.Set("POLY-PASSPHRASE", c.apiCreds.APIPassphrase)

	// 尝试连接（带重试）
	var conn *websocket.Conn
	var err error
	for i := 0; i < defaultMaxRetries; i++ {
		conn, _, err = dialer.Dial(c.url, headers)
		if err == nil {
			break
		}
		if i < defaultMaxRetries-1 {
			log.Printf("[WSUser] 连接尝试 %d/%d 失败: %v, 重试中...", i+1, defaultMaxRetries, err)
			time.Sleep(time.Duration(i+1) * time.Second)
		}
	}

	if err != nil {
		return fmt.Errorf("连接失败: %w", err)
	}

	c.conn = conn
	// 参考示例代码：不设置 SetPongHandler
	// 示例代码在 handleUserMessage 中检查 "PONG" 文本响应，而不是使用 WebSocket 标准的 PongHandler
	// 初始化 lastPong 为当前时间
	c.lastPongMu.Lock()
	c.lastPong = time.Now()
	c.lastPongMu.Unlock()

	// 发送认证消息
	authMsg := map[string]interface{}{
		"auth": map[string]string{
			"apiKey":     c.apiCreds.APIKey,
			"secret":     c.apiCreds.APISecret,
			"passphrase": c.apiCreds.APIPassphrase,
		},
		"type": "USER",
	}

	if err := conn.WriteJSON(authMsg); err != nil {
		conn.Close()
		return fmt.Errorf("认证失败: %w", err)
	}

	c.reconnectMu.Lock()
	c.reconnectAttempts = 0
	c.reconnectMu.Unlock()

	return nil
}

// sendSubscription 发送订阅消息
func (c *UserClient) sendSubscription(conditionIDs []string) error {
	if len(conditionIDs) == 0 {
		return nil
	}

	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("未连接")
	}

	msg := map[string]interface{}{
		"auth": map[string]string{
			"apiKey":     c.apiCreds.APIKey,
			"secret":     c.apiCreds.APISecret,
			"passphrase": c.apiCreds.APIPassphrase,
		},
		"markets": conditionIDs,
		"type":    "USER",
	}

	return c.conn.WriteJSON(msg)
}

// resubscribe 重新订阅所有市场（重连后使用）
func (c *UserClient) resubscribe() error {
	c.subMu.RLock()
	conditionIDs := make([]string, 0, len(c.markets))
	for id := range c.markets {
		conditionIDs = append(conditionIDs, id)
	}
	c.subMu.RUnlock()

	if len(conditionIDs) == 0 {
		return nil
	}

	return c.sendSubscription(conditionIDs)
}

// readLoop 读取循环，持续从 WebSocket 读取消息
func (c *UserClient) readLoop() {
	defer close(c.doneCh)
	log.Printf("[WSUser] [DEBUG] readLoop started")

	for {
		// 检查是否应该退出
		select {
		case <-c.ctx.Done():
			log.Printf("[WSUser] [DEBUG] readLoop exiting (context cancelled)")
			return
		case <-c.stopCh:
			log.Printf("[WSUser] [DEBUG] readLoop exiting (stop channel)")
			return
		default:
		}

		// 检查运行状态
		c.runningMu.RLock()
		running := c.running
		c.runningMu.RUnlock()
		if !running {
			log.Printf("[WSUser] [DEBUG] readLoop exiting (client stopped)")
			return
		}

		c.connMu.Lock()
		conn := c.conn
		c.connMu.Unlock()

		if conn == nil {
			if c.config.ReconnectEnabled {
				c.reconnect()
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// 参考示例代码：不设置读取超时（SetReadDeadline）
		// 示例代码没有设置 SetReadDeadline，这样可以避免因读取超时导致的误判
		// 我们使用 ping/pong 机制来检测连接状态，而不是依赖读取超时
		// 连接正常时，ReadMessage 会一直阻塞等待消息，这是正常行为
		// 如果连接失败，ReadMessage 会返回错误，我们处理错误并重连

		// 使用 panic recovery 捕获可能的 "repeated read on failed connection" panic
		// 这是 gorilla/websocket 库的一个已知问题：当连接失败后，再次调用 ReadMessage 会 panic
		var message []byte
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					// 捕获 panic，立即清理连接
					c.connMu.Lock()
					if c.conn != nil {
						// 不尝试关闭，直接设置为 nil（连接已经失败）
						c.conn = nil
						log.Printf("[WSUser] [DEBUG] 连接已清理（panic: %v）", r)
					}
					c.connMu.Unlock()
					err = fmt.Errorf("panic during ReadMessage: %v", r)
				}
			}()

			// 在读取前再次检查连接（防止在获取连接后、读取前连接被清理）
			c.connMu.Lock()
			currentConn := c.conn
			c.connMu.Unlock()

			if currentConn != conn || currentConn == nil {
				// 连接已经被替换或清理，跳过这次读取
				err = fmt.Errorf("connection changed before read")
				return
			}

			// 直接调用 ReadMessage，不设置超时（参考示例代码）
			// 如果连接失败，ReadMessage 会返回错误，而不是 panic（在大多数情况下）
			// 但如果连接在内部已经失败，可能会 panic，所以用 recovery 捕获
			_, message, err = conn.ReadMessage()
		}()

		if err != nil {
			// 连接出错，立即清理连接，避免重复读取失败的连接（这是导致 panic 的原因）
			// 注意：如果是从 panic recovery 来的错误，连接已经在 recovery 中清理了
			c.connMu.Lock()
			if c.conn != nil {
				// 尝试关闭连接（忽略错误，因为连接可能已经失败）
				c.conn.Close()
				c.conn = nil
				log.Printf("[WSUser] [DEBUG] 连接已清理（读取错误: %v）", err)
			}
			c.connMu.Unlock()

			// 简单的错误处理：如果是正常关闭，直接返回
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("[WSUser] [DEBUG] 连接正常关闭")
				return
			}
			// 检查是否应该退出
			c.runningMu.RLock()
			running = c.running
			c.runningMu.RUnlock()
			if !running {
				log.Printf("[WSUser] [DEBUG] readLoop exiting (client stopped, connection error: %v)", err)
				return
			}
			// 其他错误：记录并重连
			log.Printf("[WSUser] 读取错误: %v, 重连中...", err)
			if c.config.ReconnectEnabled {
				c.reconnect()
			} else {
				// 如果不允许重连，等待一段时间后继续（避免忙等待）
				time.Sleep(1 * time.Second)
			}
			continue
		}

		// 只有在没有错误且有消息时才处理
		if message != nil {
			c.handleUserMessage(message)
		}
	}
}

// pingLoop 心跳循环，定期发送 ping 消息
func (c *UserClient) pingLoop() {
	ticker := time.NewTicker(c.config.PingInterval)
	defer ticker.Stop()
	log.Printf("[WSUser] [DEBUG] pingLoop started")

	for {
		select {
		case <-c.ctx.Done():
			log.Printf("[WSUser] [DEBUG] pingLoop exiting (context cancelled)")
			return
		case <-c.stopCh:
			log.Printf("[WSUser] [DEBUG] pingLoop exiting (stop channel)")
			return
		case <-ticker.C:
			// 检查运行状态
			c.runningMu.RLock()
			running := c.running
			c.runningMu.RUnlock()
			if !running {
				log.Printf("[WSUser] [DEBUG] pingLoop exiting (client stopped)")
				return
			}

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
				// 检查是否应该退出
				c.runningMu.RLock()
				running = c.running
				c.runningMu.RUnlock()
				if !running {
					log.Printf("[WSUser] [DEBUG] pingLoop exiting (client stopped, PING error: %v)", err)
					return
				}
				log.Printf("[WSUser] PING 发送失败: %v", err)
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
func (c *UserClient) reconnect() {
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

	log.Printf("[WSUser] %v 后重连 (尝试 %d/%d)...", delay, attempts, c.config.MaxReconnectAttempts)

	select {
	case <-c.ctx.Done():
		return
	case <-c.stopCh:
		return
	case <-time.After(delay):
	}

	if err := c.connect(); err != nil {
		log.Printf("[WSUser] 重连失败: %v", err)
		return
	}

	// 重新订阅所有市场
	if err := c.resubscribe(); err != nil {
		log.Printf("[WSUser] 重新订阅失败: %v", err)
	}
}

// handleUserMessage 处理用户频道的消息
// 这是之前版本缺失的关键功能，现在已完整实现
func (c *UserClient) handleUserMessage(data []byte) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return
	}

	// 检查是否是纯文本消息（如 "PONG"）
	if len(trimmed) > 0 && trimmed[0] != '{' && trimmed[0] != '[' {
		// 处理文本消息（如 "PONG"）
		textMsg := string(trimmed)
		if textMsg == "PONG" || textMsg == "pong" {
			// 更新 pong 时间戳
			c.lastPongMu.Lock()
			c.lastPong = time.Now()
			c.lastPongMu.Unlock()
			log.Printf("[WSUser] 收到 PONG 响应")
		}
		return
	}

	// 尝试解析为 JSON
	var msg map[string]interface{}
	if err := json.Unmarshal(trimmed, &msg); err != nil {
		// 只记录 JSON 格式的错误
		if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			select {
			case c.errChan <- fmt.Errorf("解析用户消息失败: %w", err):
			default:
			}
		}
		return
	}

	// 将消息发送到消息通道
	select {
	case c.msgChan <- msg:
	default:
		eventType := "unknown"
		if et, ok := msg["event_type"].(string); ok {
			eventType = et
		}
		select {
		case c.errChan <- fmt.Errorf("用户消息通道已满，丢弃 %s 消息", eventType):
		default:
		}
	}

	// 处理交易事件
	if c.tradeHandler != nil {
		eventType, ok := msg["event_type"].(string)
		if !ok {
			return
		}

		// 只处理交易相关事件
		if eventType == "trade" || eventType == "TRADE" {
			c.processTradeMessage(msg)
		}
	}
}

// processTradeMessage 处理交易消息并调用处理器
func (c *UserClient) processTradeMessage(msg map[string]interface{}) {
	// 将消息解析为 DataTrade 结构
	var trade api.DataTrade

	// 解析基本字段
	if proxyWallet, ok := msg["proxyWallet"].(string); ok {
		trade.ProxyWallet = proxyWallet
	}
	if tradeType, ok := msg["type"].(string); ok {
		trade.Type = tradeType
	}
	if side, ok := msg["side"].(string); ok {
		trade.Side = side
	}
	if isMaker, ok := msg["isMaker"].(bool); ok {
		trade.IsMaker = isMaker
	}
	if asset, ok := msg["asset"].(string); ok {
		trade.Asset = asset
	}
	if conditionID, ok := msg["conditionId"].(string); ok {
		trade.ConditionID = conditionID
	}
	if timestamp, ok := msg["timestamp"].(float64); ok {
		trade.Timestamp = int64(timestamp)
	} else if timestamp, ok := msg["timestamp"].(int64); ok {
		trade.Timestamp = timestamp
	}

	// 解析 Numeric 类型字段（可能为字符串或数字）
	if size, ok := msg["size"]; ok {
		if err := parseNumeric(&trade.Size, size); err != nil {
			log.Printf("[WSUser] 解析 size 失败: %v", err)
		}
	}
	if usdcSize, ok := msg["usdcSize"]; ok {
		if err := parseNumeric(&trade.UsdcSize, usdcSize); err != nil {
			log.Printf("[WSUser] 解析 usdcSize 失败: %v", err)
		}
	}
	if price, ok := msg["price"]; ok {
		if err := parseNumeric(&trade.Price, price); err != nil {
			log.Printf("[WSUser] 解析 price 失败: %v", err)
		}
	}

	// 解析其他可选字段
	if title, ok := msg["title"].(string); ok {
		trade.Title = title
	}
	if slug, ok := msg["slug"].(string); ok {
		trade.Slug = slug
	}
	if icon, ok := msg["icon"].(string); ok {
		trade.Icon = icon
	}
	if eventSlug, ok := msg["eventSlug"].(string); ok {
		trade.EventSlug = eventSlug
	}
	if outcome, ok := msg["outcome"].(string); ok {
		trade.Outcome = outcome
	}
	if outcomeIndex, ok := msg["outcomeIndex"].(float64); ok {
		trade.OutcomeIndex = int(outcomeIndex)
	}
	if name, ok := msg["name"].(string); ok {
		trade.Name = name
	}
	if txHash, ok := msg["transactionHash"].(string); ok {
		trade.TransactionHash = txHash
	}

	// 调用交易处理器
	c.tradeHandler(trade)
}

// parseNumeric 解析 Numeric 类型字段（支持字符串或数字）
func parseNumeric(n *api.Numeric, value interface{}) error {
	switch v := value.(type) {
	case string:
		if v == "" {
			*n = 0
			return nil
		}
		var f float64
		if _, err := fmt.Sscanf(v, "%f", &f); err != nil {
			return err
		}
		*n = api.Numeric(f)
		return nil
	case float64:
		*n = api.Numeric(v)
		return nil
	case int64:
		*n = api.Numeric(v)
		return nil
	case int:
		*n = api.Numeric(v)
		return nil
	default:
		// 尝试 JSON 解析
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, n)
	}
}

// generateWSSignature 生成 HMAC-SHA256 签名用于 WebSocket 认证
func (c *UserClient) generateWSSignature(message, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(message))
	return hex.EncodeToString(h.Sum(nil))
}
