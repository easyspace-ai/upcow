package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/ports"
)

var userLog = logrus.WithField("component", "user_websocket")

// UserWebSocket 用户订单 WebSocket 客户端（BBGO风格：使用直接回调，不使用事件总线）
type UserWebSocket struct {
	conn           *websocket.Conn
	orderHandlers  []ports.OrderUpdateHandler // BBGO风格：直接回调列表
	tradeHandlers  []ports.TradeUpdateHandler // BBGO风格：交易回调列表
	creds          *UserCredentials
	mu             sync.RWMutex
	closed         bool
	reconnectMu    sync.Mutex
	reconnectCount int
	maxReconnects  int
	reconnectDelay time.Duration
	lastPong       time.Time
	healthCheckMu  sync.RWMutex
	proxyURL       string
	ctx            context.Context // 保存 context，用于取消所有 goroutine
	cancel         context.CancelFunc // cancel 函数，用于取消 context
	wg             sync.WaitGroup     // 用于等待所有 goroutine 退出
}

// UserCredentials 用户凭证
type UserCredentials struct {
	APIKey    string
	Secret    string
	Passphrase string
}

// NewUserWebSocket 创建新的用户订单 WebSocket 客户端（BBGO风格）
func NewUserWebSocket() *UserWebSocket {
	return &UserWebSocket{
		orderHandlers:  make([]ports.OrderUpdateHandler, 0),
		tradeHandlers:  make([]ports.TradeUpdateHandler, 0),
		maxReconnects:  10,                    // 最多重连 10 次
		reconnectDelay: 5 * time.Second,       // 初始重连延迟 5 秒
		lastPong:       time.Now(),
	}
}

// OnOrderUpdate 注册订单更新回调（BBGO风格）
func (u *UserWebSocket) OnOrderUpdate(handler ports.OrderUpdateHandler) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.orderHandlers = append(u.orderHandlers, handler)
}

// OnTradeUpdate 注册交易更新回调（BBGO风格）
func (u *UserWebSocket) OnTradeUpdate(handler ports.TradeUpdateHandler) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.tradeHandlers = append(u.tradeHandlers, handler)
}

// Connect 连接到用户订单 WebSocket
func (u *UserWebSocket) Connect(ctx context.Context, creds *UserCredentials, proxyURL string) error {
	u.mu.Lock()
	// 如果已有连接且未关闭，先关闭旧连接（避免重复连接）
	if u.conn != nil && !u.closed {
		u.conn.Close()
		u.conn = nil
		u.closed = true
	}
	// 取消旧的 context（如果存在）
	if u.cancel != nil {
		u.cancel()
	}
	// 创建新的 context 和 cancel 函数
	u.ctx, u.cancel = context.WithCancel(ctx)
	u.creds = creds
	u.proxyURL = proxyURL
	u.mu.Unlock()

	// 构建 WebSocket URL
	wsURL := "wss://ws-subscriptions-clob.polymarket.com/ws/user"

	// 创建 dialer，支持代理
	dialer := websocket.Dialer{
		HandshakeTimeout: 30 * time.Second, // 增加超时时间
	}

	// 如果提供了代理 URL，配置代理
	if proxyURL != "" {
		proxyURLParsed, err := url.Parse(proxyURL)
		if err != nil {
			userLog.Warnf("解析代理 URL 失败: %v，将尝试直接连接", err)
		} else {
			dialer.Proxy = http.ProxyURL(proxyURLParsed)
			userLog.Infof("使用代理连接用户订单 WebSocket: %s", proxyURL)
		}
	} else {
		// 尝试从环境变量获取代理
		proxyEnv := getProxyFromEnv()
		if proxyEnv != "" {
			proxyURLParsed, err := url.Parse(proxyEnv)
			if err == nil {
				dialer.Proxy = http.ProxyURL(proxyURLParsed)
				userLog.Infof("使用环境变量代理连接用户订单 WebSocket: %s", proxyEnv)
			}
		}
	}

	// 重试连接（最多 3 次）
	var conn *websocket.Conn
	var err error
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			userLog.Infof("重试连接用户订单 WebSocket (第 %d/%d 次)...", i+1, maxRetries)
			time.Sleep(time.Duration(i) * 2 * time.Second) // 递增延迟
		}

		conn, _, err = dialer.Dial(wsURL, nil)
		if err == nil {
			break
		}
		userLog.Warnf("连接用户订单 WebSocket 失败 (尝试 %d/%d): %v", i+1, maxRetries, err)
	}

	if err != nil {
		return fmt.Errorf("连接 WebSocket 失败（已重试 %d 次）: %w", maxRetries, err)
	}

	u.conn = conn
	u.closed = false

	// 认证
	if err := u.authenticate(); err != nil {
		conn.Close()
		return fmt.Errorf("认证失败: %w", err)
	}

	// 启动消息处理 goroutine（使用保存的 context）
	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		u.handleMessages(u.ctx)
	}()

	// 启动 PING 循环（使用保存的 context）
	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		u.startPingLoop(u.ctx)
	}()

	// 启动健康检查（使用保存的 context）
	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		u.startHealthCheck(u.ctx)
	}()

	// 重置重连计数
	u.reconnectMu.Lock()
	u.reconnectCount = 0
	u.reconnectMu.Unlock()

	userLog.Info("用户订单 WebSocket 已连接")
	return nil
}

// startPingLoop 启动 PING 循环，保持连接活跃
func (u *UserWebSocket) startPingLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 检查 context 是否已取消
			select {
			case <-ctx.Done():
				userLog.Debugf("用户订单 WebSocket PING 循环收到取消信号，退出")
				return
			default:
			}

			u.mu.RLock()
			conn := u.conn
			closed := u.closed
			u.mu.RUnlock()

			if closed || conn == nil {
				return
			}

			if err := conn.WriteMessage(websocket.TextMessage, []byte("PING")); err != nil {
				// 检查 context 是否已取消，如果已取消则不触发重连
				select {
				case <-ctx.Done():
					userLog.Debugf("用户订单 WebSocket 发送 PING 失败但 context 已取消，退出")
					return
				default:
					userLog.Warnf("发送 PING 失败: %v，将触发重连", err)
					// 触发重连
					go u.reconnect(ctx)
					return
				}
			}
		}
	}
}

// startHealthCheck 启动健康检查
func (u *UserWebSocket) startHealthCheck(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			userLog.Debugf("用户订单 WebSocket 健康检查收到取消信号，退出")
			return
		case <-ticker.C:
			// 检查 context 是否已取消
			select {
			case <-ctx.Done():
				userLog.Debugf("用户订单 WebSocket 健康检查收到取消信号，退出")
				return
			default:
			}

			u.healthCheckMu.RLock()
			lastPong := u.lastPong
			u.healthCheckMu.RUnlock()

			// 如果超过 60 秒没有收到 PONG，认为连接不健康
			if time.Since(lastPong) > 60*time.Second {
				// 再次检查 context 是否已取消，如果已取消则不触发重连
				select {
				case <-ctx.Done():
					userLog.Debugf("用户订单 WebSocket 健康检查失败但 context 已取消，不触发重连")
					return
				default:
					userLog.Warnf("WebSocket 健康检查失败：超过 60 秒未收到 PONG，将触发重连")
					go u.reconnect(ctx)
					return
				}
			}
		}
	}
}

// reconnect 自动重连
func (u *UserWebSocket) reconnect(ctx context.Context) {
	u.reconnectMu.Lock()
	defer u.reconnectMu.Unlock()

	// 获取保存的 context（如果存在）
	u.mu.RLock()
	wsCtx := u.ctx
	u.mu.RUnlock()

	// 使用保存的 context 或传入的 context
	if wsCtx == nil {
		wsCtx = ctx
	}

	// 首先检查 context 是否已取消
	select {
	case <-wsCtx.Done():
		userLog.Debugf("用户订单 WebSocket 重连收到取消信号，退出")
		return
	case <-ctx.Done():
		userLog.Debugf("用户订单 WebSocket 重连收到取消信号，退出")
		return
	default:
	}

	// 检查是否超过最大重连次数
	if u.reconnectCount >= u.maxReconnects {
		userLog.Errorf("WebSocket 重连失败：已达到最大重连次数 (%d)", u.maxReconnects)
		return
	}

	u.reconnectCount++
	delay := u.reconnectDelay * time.Duration(u.reconnectCount) // 指数退避

	userLog.Infof("WebSocket 将在 %v 后尝试重连 (第 %d/%d 次)", delay, u.reconnectCount, u.maxReconnects)

	// 关闭当前连接
	u.mu.Lock()
	if u.conn != nil {
		u.conn.Close()
		u.conn = nil
	}
	u.closed = true
	u.mu.Unlock()

	// 等待后重连（使用 select 响应 context 取消）
	select {
	case <-wsCtx.Done():
		userLog.Debugf("用户订单 WebSocket 重连等待期间收到取消信号，退出")
		return
	case <-ctx.Done():
		userLog.Debugf("用户订单 WebSocket 重连等待期间收到取消信号，退出")
		return
	case <-time.After(delay):
		// 继续重连
	}

	// 再次检查 context 是否已取消
	select {
	case <-wsCtx.Done():
		userLog.Debugf("用户订单 WebSocket 重连前收到取消信号，退出")
		return
	case <-ctx.Done():
		userLog.Debugf("用户订单 WebSocket 重连前收到取消信号，退出")
		return
	default:
	}

	// 尝试重连（Connect 会创建新的 context，所以使用原始 context）
	if u.creds != nil {
		if err := u.Connect(ctx, u.creds, u.proxyURL); err != nil {
			userLog.Errorf("WebSocket 重连失败: %v", err)
			// 如果重连失败，继续尝试（但先检查 context）
			select {
			case <-wsCtx.Done():
				userLog.Debugf("用户订单 WebSocket 重连失败且 context 已取消，退出")
				return
			case <-ctx.Done():
				userLog.Debugf("用户订单 WebSocket 重连失败且 context 已取消，退出")
				return
			default:
				go u.reconnect(ctx)
			}
		} else {
			userLog.Infof("WebSocket 重连成功 (第 %d 次)", u.reconnectCount)
			u.reconnectCount = 0 // 重置计数
		}
	}
}

// authenticate 认证
func (u *UserWebSocket) authenticate() error {
	authMsg := map[string]interface{}{
		"auth": map[string]string{
			"apikey":     u.creds.APIKey,
			"secret":     u.creds.Secret,
			"passphrase": u.creds.Passphrase,
		},
		"type": "user",
	}

	return u.conn.WriteJSON(authMsg)
}

// handleMessages 处理 WebSocket 消息
func (u *UserWebSocket) handleMessages(ctx context.Context) {
	for {
		// 首先检查 context 是否已取消
		select {
		case <-ctx.Done():
			userLog.Infof("用户订单 WebSocket 消息处理收到取消信号，退出")
			return
		default:
		}

		// 获取连接引用
		u.mu.RLock()
		conn := u.conn
		closed := u.closed
		u.mu.RUnlock()
		
		if conn == nil || closed {
			return
		}

		// 设置读取超时（30秒），既能及时响应 context 取消，又不会因为正常延迟而误判
		// 使用较长的超时时间，避免正常的网络延迟被误判为连接失败
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		
		// 使用 recover 捕获可能的 panic（连接失败后重复读取会导致 panic）
		var message []byte
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					// 捕获 panic，特别是 "repeated read on failed websocket connection"
					userLog.Errorf("用户订单 WebSocket 读取时发生 panic: %v，连接可能已失败", r)
					// 标记连接为已关闭，避免后续重复读取
					u.mu.Lock()
					u.closed = true
					u.mu.Unlock()
					err = fmt.Errorf("panic: %v", r)
				}
			}()
			// 再次检查连接状态（防止在 recover 和实际读取之间的竞态条件）
			u.mu.RLock()
			if u.closed || u.conn == nil || u.conn != conn {
				u.mu.RUnlock()
				err = fmt.Errorf("连接已关闭")
				return
			}
			u.mu.RUnlock()
			_, message, err = conn.ReadMessage()
		}()
		
		if err != nil {
			// 检查是否是 panic 错误（连接失败后重复读取）
			errStr := err.Error()
			isPanicError := strings.Contains(errStr, "panic:") || 
				strings.Contains(errStr, "repeated read on failed websocket connection")
			
			// 如果是 panic 错误，立即标记为关闭并退出
			if isPanicError {
				userLog.Warnf("用户订单 WebSocket 读取时发生 panic 错误: %v，标记为已关闭并退出", err)
				u.mu.Lock()
				u.closed = true
				u.mu.Unlock()
				return
			}
			
			// 检查是否是超时错误（这是正常的，用于检查 context）
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				// 超时，继续循环检查 context
				continue
			}

			// 检查 context 是否已取消
			select {
			case <-ctx.Done():
				userLog.Infof("用户订单 WebSocket 读取错误且 context 已取消，退出")
				return
			default:
			}

			// 检查是否是正常关闭（连接已被主动关闭）
			isNormalClose := strings.Contains(errStr, "use of closed network connection") ||
				strings.Contains(errStr, "connection reset by peer")
			
			// 检查 context 是否已取消（正常关闭流程）
			select {
			case <-ctx.Done():
				// Context 已取消，这是正常关闭
				userLog.Debugf("用户订单 WebSocket 正常关闭（context 已取消）")
				u.mu.Lock()
				u.closed = true
				u.mu.Unlock()
				return
			default:
			}
			
			// 检查连接是否已经被标记为关闭（正常关闭流程）
			u.mu.RLock()
			alreadyClosed := u.closed
			u.mu.RUnlock()
			
			if alreadyClosed || isNormalClose {
				// 正常关闭，记录为调试信息
				userLog.Debugf("用户订单 WebSocket 正常关闭: %v", err)
				return
			}
			
			// 异常关闭，记录为警告
			userLog.Warnf("用户订单 WebSocket 读取错误: %v，标记为已关闭并退出", err)
			
			// 标记为已关闭，避免重复读取
			u.mu.Lock()
			u.closed = true
			u.mu.Unlock()
			
			// 检查是否是连接关闭错误（用于决定是否重连）
			isCloseError := websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) ||
				websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure)
			
			if isCloseError {
				// 连接关闭，触发重连
				userLog.Infof("用户订单 WebSocket 连接已关闭，将触发重连")
				go u.reconnect(ctx)
			}
			
			return
		}

		// 处理 PING/PONG
		msgStr := string(message)
		if msgStr == "PING" {
			conn.WriteMessage(websocket.TextMessage, []byte("PONG"))
			continue
		}
		if msgStr == "PONG" {
			u.healthCheckMu.Lock()
			u.lastPong = time.Now()
			u.healthCheckMu.Unlock()
			userLog.Debugf("收到 PONG 响应")
			continue
		}

		// 解析消息
		var msg map[string]interface{}
		if err := json.Unmarshal(message, &msg); err != nil {
			userLog.Debugf("解析消息失败: %v", err)
			continue
		}

		eventType, _ := msg["event_type"].(string)
		switch eventType {
		case "order":
			u.handleOrderMessage(ctx, msg)
		case "trade":
			u.handleTradeMessage(ctx, msg)
		}
	}
}

// handleOrderMessage 处理订单消息
func (u *UserWebSocket) handleOrderMessage(ctx context.Context, msg map[string]interface{}) {
	// 解析订单消息
	orderID, _ := msg["id"].(string)
	assetID, _ := msg["asset_id"].(string)
	sideStr, _ := msg["side"].(string)
	priceStr, _ := msg["price"].(string)
	originalSizeStr, _ := msg["original_size"].(string)
	sizeMatchedStr, _ := msg["size_matched"].(string)
	orderTypeStr, _ := msg["type"].(string) // PLACEMENT, UPDATE, CANCELLATION

	// 解析价格和数量
	price, err := parsePriceString(priceStr)
	if err != nil {
		userLog.Debugf("解析订单价格失败: %v", err)
		return
	}

	originalSize, _ := strconv.ParseFloat(originalSizeStr, 64)
	sizeMatched, _ := strconv.ParseFloat(sizeMatchedStr, 64)

	// 确定订单方向
	var side types.Side
	if sideStr == "BUY" {
		side = types.SideBuy
	} else {
		side = types.SideSell
	}

	// 确定订单状态
	var status domain.OrderStatus
	switch orderTypeStr {
	case "PLACEMENT":
		status = domain.OrderStatusOpen
	case "UPDATE":
		if sizeMatched >= originalSize {
			status = domain.OrderStatusFilled
		} else {
			status = domain.OrderStatusOpen
		}
	case "CANCELLATION":
		status = domain.OrderStatusCanceled
	default:
		status = domain.OrderStatusPending
	}

	// 构建订单领域对象
	order := &domain.Order{
		OrderID:   orderID,
		AssetID:   assetID,
		Side:      side,
		Price:     price,
		Size:      sizeMatched,
		Status:    status,
		CreatedAt: time.Now(),
	}

	// BBGO风格：直接触发回调，不使用事件总线
	u.mu.RLock()
	handlers := u.orderHandlers
	u.mu.RUnlock()

	// 根据订单状态更新订单对象
	if orderTypeStr == "UPDATE" && sizeMatched >= originalSize {
		// 订单已成交
		filledAt := time.Now()
		order.FilledAt = &filledAt
		order.Status = domain.OrderStatusFilled
	} else if orderTypeStr == "CANCELLATION" {
		order.Status = domain.OrderStatusCanceled
	} else if orderTypeStr == "PLACEMENT" {
		order.Status = domain.OrderStatusOpen
	}

	// 触发所有注册的回调处理器
	for _, handler := range handlers {
		go func(h ports.OrderUpdateHandler) {
			if err := h.OnOrderUpdate(ctx, order); err != nil {
				userLog.Errorf("订单更新处理器执行失败: %v", err)
			}
		}(handler)
	}

	userLog.Debugf("处理订单消息: orderID=%s, type=%s, status=%s", orderID, orderTypeStr, status)
}

// handleTradeMessage 处理交易消息（BBGO风格：解析交易事件，调用 TradeCollector）
func (u *UserWebSocket) handleTradeMessage(ctx context.Context, msg map[string]interface{}) {
	// 解析交易消息
	tradeID, _ := msg["id"].(string)
	assetID, _ := msg["asset_id"].(string)
	priceStr, _ := msg["price"].(string)
	sizeStr, _ := msg["size"].(string)
	sideStr, _ := msg["side"].(string)
	status, _ := msg["status"].(string) // MATCHED, MINED, CONFIRMED, RETRYING, FAILED
	takerOrderID, _ := msg["taker_order_id"].(string)
	makerOrders, _ := msg["maker_orders"].([]interface{})
	
	// 只处理 MATCHED 状态的交易（实际成交的交易）
	if status != "MATCHED" {
		userLog.Debugf("收到交易消息（非 MATCHED 状态，跳过）: tradeID=%s, status=%s", tradeID, status)
		return
	}
	
	// 解析价格和数量
	price, err := parsePriceString(priceStr)
	if err != nil {
		userLog.Debugf("解析交易价格失败: %v", err)
		return
	}
	
	size, err := strconv.ParseFloat(sizeStr, 64)
	if err != nil {
		userLog.Debugf("解析交易数量失败: %v", err)
		return
	}
	
	// 确定交易方向
	var side types.Side
	if sideStr == "BUY" {
		side = types.SideBuy
	} else {
		side = types.SideSell
	}
	
	// 确定订单 ID（优先使用 taker_order_id，如果没有则从 maker_orders 中获取）
	orderID := takerOrderID
	if orderID == "" && len(makerOrders) > 0 {
		// 从 maker_orders 中获取第一个订单 ID
		if makerOrder, ok := makerOrders[0].(map[string]interface{}); ok {
			if id, ok := makerOrder["order_id"].(string); ok {
				orderID = id
			}
		}
	}
	
	if orderID == "" {
		userLog.Debugf("交易消息中未找到订单 ID，跳过: tradeID=%s", tradeID)
		return
	}
	
	// 构建交易领域对象
	trade := &domain.Trade{
		ID:        tradeID,
		OrderID:   orderID,
		AssetID:   assetID,
		Side:      side,
		Price:     price,
		Size:      size,
		Time:      time.Now(),
	}
	
	// 确定 TokenType（从 outcome 字段，如果有）
	if outcome, ok := msg["outcome"].(string); ok {
		if outcome == "YES" {
			trade.TokenType = domain.TokenTypeUp
		} else {
			trade.TokenType = domain.TokenTypeDown
		}
	}
	
	userLog.Debugf("收到交易消息: tradeID=%s, orderID=%s, assetID=%s, side=%s, price=%.4f, size=%.2f",
		tradeID, orderID, assetID, sideStr, price.ToDecimal(), size)
	
	// BBGO风格：直接调用交易处理器
	u.mu.RLock()
	handlers := u.tradeHandlers
	u.mu.RUnlock()
	
	for _, handler := range handlers {
		handler.HandleTrade(ctx, trade)
	}
}


// Close 关闭 WebSocket 连接
func (u *UserWebSocket) Close() error {
	u.mu.Lock()
	// 先标记为已关闭，防止新的操作
	u.closed = true
	
	// 取消 context，通知所有 goroutine 停止
	if u.cancel != nil {
		u.cancel()
		u.cancel = nil
	}
	
	// 关闭连接，这会中断 ReadMessage 的阻塞
	var conn *websocket.Conn
	if u.conn != nil {
		conn = u.conn
		u.conn = nil
	}
	u.mu.Unlock()

	// 关闭连接（这会触发 ReadMessage 返回错误，让 handleMessages 退出）
	if conn != nil {
		conn.Close()
	}

	// 等待所有 goroutine 退出（最多等待3秒）
	done := make(chan struct{})
	go func() {
		u.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// 所有 goroutine 已退出
		userLog.Debugf("用户订单 WebSocket 所有 goroutine 已退出")
	case <-time.After(3 * time.Second):
		// 超时，记录警告但继续
		userLog.Warnf("等待用户订单 WebSocket goroutine 退出超时（3秒），继续关闭")
	}

	return nil
}
