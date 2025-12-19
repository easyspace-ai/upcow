package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/stream"
	"github.com/betbot/gobet/pkg/logger"
	"github.com/betbot/gobet/pkg/syncgroup"
	"github.com/gorilla/websocket"
)

// MarketWebSocket 市场价格 WebSocket 客户端（BBGO风格：使用直接回调，信号驱动重连）
type MarketWebSocket struct {
	conn           *websocket.Conn
	market         *domain.Market
	mu             sync.RWMutex
	closed         bool
	reconnectC      chan struct{} // 信号驱动的重连 channel
	reconnectMu    sync.Mutex
	reconnectCount int
	maxReconnects  int
	reconnectDelay time.Duration
	lastPong       time.Time
	healthCheckMu  sync.RWMutex
	proxyURL       string
	ctx            context.Context    // 保存 context，用于取消所有 goroutine
	cancel         context.CancelFunc // cancel 函数，用于取消 context
	sg             *syncgroup.SyncGroup // 使用 SyncGroup 管理 goroutine
	handlers       *stream.HandlerList  // 价格变化回调处理器列表
}

// NewMarketWebSocket 创建新的市场价格 WebSocket 客户端（BBGO风格：不需要 Publisher）
func NewMarketWebSocket() *MarketWebSocket {
	return &MarketWebSocket{
		reconnectC:     make(chan struct{}, 1), // 缓冲1，避免阻塞
		maxReconnects:  10,                     // 最多重连 10 次
		reconnectDelay: 5 * time.Second,        // 初始重连延迟 5 秒
		lastPong:       time.Now(),
		sg:             syncgroup.NewSyncGroup(),
		handlers:       stream.NewHandlerList(),
	}
}

// OnPriceChanged 注册价格变化回调（BBGO风格：直接回调）
func (m *MarketWebSocket) OnPriceChanged(handler stream.PriceChangeHandler) {
	m.handlers.Add(handler)
}

// Connect 连接到市场价格 WebSocket
func (m *MarketWebSocket) Connect(ctx context.Context, market *domain.Market, proxyURL string) error {
	m.mu.Lock()
	// 如果已有连接且未关闭，先关闭旧连接（避免重复连接）
	if m.conn != nil && !m.closed {
		m.conn.Close()
		m.conn = nil
		m.closed = true
	}
	// 取消旧的 context（如果存在）
	if m.cancel != nil {
		m.cancel()
	}
	// 创建新的 context 和 cancel 函数
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.market = market
	m.proxyURL = proxyURL
	m.mu.Unlock()

	// 构建 WebSocket URL
	wsURL := "wss://ws-subscriptions-clob.polymarket.com/ws/market"

	// 创建 dialer，支持代理
	dialer := websocket.Dialer{
		HandshakeTimeout: 30 * time.Second, // 增加超时时间
	}

	// 如果提供了代理 URL，配置代理
	if proxyURL != "" {
		proxyURLParsed, err := url.Parse(proxyURL)
		if err != nil {
			logger.Warnf("解析代理 URL 失败: %v，将尝试直接连接", err)
		} else {
			dialer.Proxy = http.ProxyURL(proxyURLParsed)
			logger.Infof("使用代理连接 WebSocket: %s", proxyURL)
		}
	} else {
		// 尝试从环境变量获取代理
		proxyEnv := getProxyFromEnv()
		if proxyEnv != "" {
			proxyURLParsed, err := url.Parse(proxyEnv)
			if err == nil {
				dialer.Proxy = http.ProxyURL(proxyURLParsed)
				logger.Infof("使用环境变量代理连接 WebSocket: %s", proxyEnv)
			}
		}
	}

	// 重试连接（最多 3 次）
	var conn *websocket.Conn
	var err error
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			logger.Infof("重试连接 WebSocket (第 %d/%d 次)...", i+1, maxRetries)
			time.Sleep(time.Duration(i) * 2 * time.Second) // 递增延迟
		}

		conn, _, err = dialer.Dial(wsURL, nil)
		if err == nil {
			break
		}
		logger.Warnf("连接 WebSocket 失败 (尝试 %d/%d): %v", i+1, maxRetries, err)
	}

	if err != nil {
		return fmt.Errorf("连接 WebSocket 失败（已重试 %d 次）: %w", maxRetries, err)
	}

	m.conn = conn
	m.closed = false

	// 订阅市场
	if err := m.subscribe(market); err != nil {
		conn.Close()
		return fmt.Errorf("订阅市场失败: %w", err)
	}

	// 启动重连器 goroutine（只启动一次，使用 SyncGroup）
	m.sg.Add(func() {
		m.reconnector(m.ctx)
	})
	m.sg.Run()

	// 启动消息处理、PING 循环和健康检查 goroutine（使用 SyncGroup）
	m.sg.Add(func() {
		m.handleMessages(m.ctx)
	})
	m.sg.Add(func() {
		m.startPingLoop(m.ctx)
	})
	m.sg.Add(func() {
		m.startHealthCheck(m.ctx)
	})
	m.sg.Run()

	// 重置重连计数
	m.reconnectMu.Lock()
	m.reconnectCount = 0
	m.reconnectMu.Unlock()

	logger.Infof("市场价格 WebSocket 已连接: %s", market.Slug)
	return nil
}

// reconnector 重连器 goroutine（信号驱动）
func (m *MarketWebSocket) reconnector(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.reconnectC:
			// 获取市场信息
			m.mu.RLock()
			market := m.market
			proxyURL := m.proxyURL
			m.mu.RUnlock()

			if market == nil {
				logger.Warnf("市场价格 WebSocket 重连：市场信息为空，跳过重连")
				continue
			}

			logger.Warnf("收到重连信号，冷却 %v...", m.reconnectDelay)
			time.Sleep(m.reconnectDelay)

			logger.Warnf("重新连接...")
			if err := m.Connect(ctx, market, proxyURL); err != nil {
				logger.Warnf("重连失败: %v，将再次尝试...", err)
				m.Reconnect() // 重新发送信号
			}
		}
	}
}

// Reconnect 触发重连（信号驱动）
func (m *MarketWebSocket) Reconnect() {
	select {
	case m.reconnectC <- struct{}{}:
		// 信号已发送
	default:
		// channel 已满，忽略（避免阻塞）
		logger.Debugf("重连信号 channel 已满，忽略")
	}
}

// startPingLoop 启动 PING 循环，保持连接活跃
func (m *MarketWebSocket) startPingLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Debugf("市场价格 WebSocket PING 循环收到取消信号，退出")
			return
		case <-ticker.C:
			// 检查 context 是否已取消
			select {
			case <-ctx.Done():
				logger.Debugf("市场价格 WebSocket PING 循环收到取消信号，退出")
				return
			default:
			}

			m.mu.RLock()
			conn := m.conn
			closed := m.closed
			m.mu.RUnlock()

			if closed || conn == nil {
				return
			}

			if err := conn.WriteMessage(websocket.TextMessage, []byte("PING")); err != nil {
				// 检查 context 是否已取消，如果已取消则不触发重连
				select {
				case <-ctx.Done():
					logger.Debugf("市场价格 WebSocket 发送 PING 失败但 context 已取消，退出")
					return
				default:
					logger.Warnf("发送 PING 失败: %v，将触发重连", err)
					// 触发重连（信号驱动）
					m.Reconnect()
					return
				}
			}
		}
	}
}

// startHealthCheck 启动健康检查
func (m *MarketWebSocket) startHealthCheck(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Debugf("市场价格 WebSocket 健康检查收到取消信号，退出")
			return
		case <-ticker.C:
			// 检查 context 是否已取消
			select {
			case <-ctx.Done():
				logger.Debugf("市场价格 WebSocket 健康检查收到取消信号，退出")
				return
			default:
			}

			m.healthCheckMu.RLock()
			lastPong := m.lastPong
			m.healthCheckMu.RUnlock()

			// 如果超过 60 秒没有收到 PONG，认为连接不健康
			if time.Since(lastPong) > 60*time.Second {
				// 再次检查 context 是否已取消，如果已取消则不触发重连
				select {
				case <-ctx.Done():
					logger.Debugf("市场价格 WebSocket 健康检查失败但 context 已取消，不触发重连")
					return
				default:
					logger.Warnf("WebSocket 健康检查失败：超过 60 秒未收到 PONG，将触发重连")
					m.Reconnect()
					return
				}
			}
		}
	}
}

// reconnect 方法已移除，现在使用信号驱动的 reconnector

// subscribe 订阅市场
func (m *MarketWebSocket) subscribe(market *domain.Market) error {
	subscribeMsg := map[string]interface{}{
		"assets_ids": []string{market.YesAssetID, market.NoAssetID},
		"type":       "market",
	}

	logger.Infof("📡 订阅市场资产: YES=%s, NO=%s", market.YesAssetID, market.NoAssetID)
	if err := m.conn.WriteJSON(subscribeMsg); err != nil {
		return err
	}
	logger.Infof("✅ 订阅消息已发送")
	return nil
}

// handleMessages 处理 WebSocket 消息
func (m *MarketWebSocket) handleMessages(ctx context.Context) {
	for {
		// 首先检查 context 是否已取消
		select {
		case <-ctx.Done():
			logger.Infof("WebSocket 消息处理收到取消信号，退出")
			return
		default:
		}

		// 获取连接引用
		m.mu.RLock()
		conn := m.conn
		closed := m.closed
		m.mu.RUnlock()

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
					logger.Errorf("市场价格 WebSocket 读取时发生 panic: %v，连接可能已失败", r)
					err = fmt.Errorf("panic: %v", r)
				}
			}()
			_, message, err = conn.ReadMessage()
		}()

		if err != nil {
			// 检查是否是超时错误（这是正常的，用于检查 context）
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				// 超时，继续循环检查 context
				continue
			}

			// 检查 context 是否已取消
			select {
			case <-ctx.Done():
				logger.Infof("市场价格 WebSocket 读取错误且 context 已取消，退出")
				return
			default:
			}

			// 检查是否是正常关闭（连接已被主动关闭）
			errStr := err.Error()
			isNormalClose := strings.Contains(errStr, "use of closed network connection") ||
				strings.Contains(errStr, "connection reset by peer")

			// 检查 context 是否已取消（正常关闭流程）
			select {
			case <-ctx.Done():
				// Context 已取消，这是正常关闭
				logger.Debugf("市场价格 WebSocket 正常关闭（context 已取消）")
				m.mu.Lock()
				m.closed = true
				m.mu.Unlock()
				return
			default:
			}

			// 检查连接是否已经被标记为关闭（正常关闭流程）
			m.mu.RLock()
			alreadyClosed := m.closed
			m.mu.RUnlock()

			if alreadyClosed || isNormalClose {
				// 正常关闭，记录为调试信息
				logger.Debugf("市场价格 WebSocket 正常关闭: %v", err)
				return
			}

			// 异常关闭，记录为警告
			logger.Warnf("市场价格 WebSocket 读取错误: %v，标记为已关闭并退出", err)

			// 标记为已关闭，避免重复读取
			m.mu.Lock()
			m.closed = true
			m.mu.Unlock()

			// 检查是否是连接关闭错误（用于决定是否重连）
			isCloseError := websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) ||
				websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure)

			if isCloseError {
				// 连接关闭，触发重连（信号驱动）
				logger.Infof("市场价格 WebSocket 连接已关闭，将触发重连")
				m.Reconnect()
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
			m.healthCheckMu.Lock()
			m.lastPong = time.Now()
			m.healthCheckMu.Unlock()
			logger.Debugf("收到 PONG 响应")
			continue
		}

		// 解析消息类型
		var msgType struct {
			EventType string `json:"event_type"`
		}
		if err := json.Unmarshal(message, &msgType); err != nil {
			logger.Debugf("解析消息类型失败: %v, 消息内容: %s", err, string(message))
			continue
		}

		logger.Infof("📨 收到 WebSocket 消息: event_type=%s", msgType.EventType)
		
		// 打印完整消息内容用于调试（限制长度）
		msgPreview := string(message)
		if len(msgPreview) > 500 {
			msgPreview = msgPreview[:500] + "..."
		}
		logger.Debugf("📨 完整消息内容: %s", msgPreview)

		// 根据事件类型处理不同的消息
		switch msgType.EventType {
		case "price_change":
			var priceMsg map[string]interface{}
			if err := json.Unmarshal(message, &priceMsg); err == nil {
				logger.Infof("📊 收到价格变化事件，消息内容: %+v", priceMsg)
				m.handlePriceChange(ctx, priceMsg)
			} else {
				logger.Warnf("解析价格变化消息失败: %v, 消息内容: %s", err, string(message))
			}
		case "book":
			// 订单簿快照（可选处理）
			logger.Infof("📚 收到订单簿快照消息，可能包含价格信息")
			// 订单簿消息可能包含价格信息，但通常价格变化会通过 price_change 事件发送
		case "tick_size_change":
			// Tick size 变化（可选处理）
			logger.Debugf("收到 tick size 变化消息")
		case "last_trade_price":
			// 最后交易价格（可选处理）
			logger.Infof("💰 收到最后交易价格消息，可能包含价格信息")
			// 可以尝试从最后交易价格中提取价格信息
			var tradeMsg map[string]interface{}
			if err := json.Unmarshal(message, &tradeMsg); err == nil {
				logger.Debugf("💰 最后交易价格消息内容: %+v", tradeMsg)
				// 暂时不处理，因为价格变化应该通过 price_change 事件发送
			}
		default:
			msgPreview := message
			if len(msgPreview) > 200 {
				msgPreview = msgPreview[:200]
			}
			logger.Infof("收到未知消息类型: %s (消息内容: %s)", msgType.EventType, string(msgPreview))
		}
	}
}

// handlePriceChange 处理价格变化
func (m *MarketWebSocket) handlePriceChange(ctx context.Context, msg map[string]interface{}) {
	priceChanges, ok := msg["price_changes"].([]interface{})
	if !ok {
		logger.Warnf("⚠️ 价格变化消息中没有 price_changes 字段，消息内容: %+v", msg)
		return
	}

	logger.Infof("📊 收到价格变化消息，包含 %d 个价格变化项", len(priceChanges))
	if len(priceChanges) == 0 {
		logger.Warnf("⚠️ 价格变化消息为空，没有价格更新")
		return
	}

	// 存储每个 token 的最新价格（用于方向判断和去重）
	tokenPrices := make(map[string]domain.Price)
	// 存储每个 asset_id 的最新价格和来源（用于去重和日志记录）
	latestPrices := make(map[string]struct {
		price  domain.Price
		source string
	})

	// 处理每个价格变化，收集所有价格更新
	for i, pc := range priceChanges {
		change, ok := pc.(map[string]interface{})
		if !ok {
			logger.Debugf("价格变化项格式错误")
			continue
		}

		assetID, _ := change["asset_id"].(string)
		if assetID == "" {
			logger.Warnf("⚠️ 价格变化项[%d]缺少 asset_id，跳过", i)
			continue
		}

		logger.Infof("📊 价格变化项[%d]: asset_id=%s (期望 YES=%s, NO=%s), 完整数据: %+v", i, assetID, m.market.YesAssetID, m.market.NoAssetID, change)

		// 确定 token 类型
		var tokenType domain.TokenType
		if assetID == m.market.YesAssetID {
			tokenType = domain.TokenTypeUp
			logger.Debugf("✅ 价格变化项[%d]: 匹配 UP 币", i)
		} else if assetID == m.market.NoAssetID {
			tokenType = domain.TokenTypeDown
			logger.Debugf("✅ 价格变化项[%d]: 匹配 DOWN 币", i)
		} else {
			// 如果不是当前市场的资产，跳过
			logger.Warnf("⚠️ 价格变化项[%d] asset_id 不匹配: %s (期望: YES=%s 或 NO=%s)，跳过", i, assetID, m.market.YesAssetID, m.market.NoAssetID)
			continue
		}

		// 尝试获取价格：优先使用 best_ask，如果为空则使用 best_bid，最后使用 price
		var priceStr string
		var priceSource string

		// 调试：打印所有可用的价格字段
		logger.Debugf("🔍 价格变化项[%d] 价格字段检查: best_ask=%v, best_bid=%v, price=%v", 
			i, change["best_ask"], change["best_bid"], change["price"])

		if bestAskStr, ok := change["best_ask"].(string); ok && bestAskStr != "" {
			priceStr = bestAskStr
			priceSource = "best_ask"
			logger.Debugf("✅ 使用 best_ask: %s", priceStr)
		} else if bestBidStr, ok := change["best_bid"].(string); ok && bestBidStr != "" {
			priceStr = bestBidStr
			priceSource = "best_bid"
			logger.Debugf("✅ 使用 best_bid: %s", priceStr)
		} else if priceVal, ok := change["price"].(string); ok && priceVal != "" {
			priceStr = priceVal
			priceSource = "price"
			logger.Debugf("✅ 使用 price: %s", priceStr)
		} else {
			logger.Warnf("⚠️ 价格变化项[%d]缺少价格字段 (asset_id=%s, tokenType=%s)，可用字段: %+v，跳过", i, assetID, tokenType, change)
			continue
		}

		// 解析价格
		newPrice, err := parsePriceString(priceStr)
		if err != nil {
			logger.Warnf("⚠️ 解析价格失败 (asset_id=%s, tokenType=%s, source=%s, value=%s): %v，跳过", assetID, tokenType, priceSource, priceStr, err)
			continue
		}

		// 更新价格缓存（保留最新的价格）
		tokenPrices[assetID] = newPrice
		latestPrices[assetID] = struct {
			price  domain.Price
			source string
		}{price: newPrice, source: priceSource}
	}

	// 处理完所有价格变化后，只记录每个 asset_id 的最新价格（去重）
	for assetID, latest := range latestPrices {
		// 确定 token 类型
		var tokenType domain.TokenType
		if assetID == m.market.YesAssetID {
			tokenType = domain.TokenTypeUp
		} else if assetID == m.market.NoAssetID {
			tokenType = domain.TokenTypeDown
		} else {
			continue
		}

		// 获取旧价格（用于判断是否真正变化）
		var oldPrice *domain.Price
		// 这里我们需要从策略中获取旧价格，但为了简化，我们只记录一次
		// 实际的价格比较会在策略层进行

		logger.Infof("📊 收到价格更新: %s (asset_id=%s, source=%s, price=%dc)", tokenType, assetID, latest.source, latest.price.Cents)

		// 创建价格变化事件（BBGO风格：直接回调）
		event := &events.PriceChangedEvent{
			Market:    m.market,
			TokenType: tokenType,
			OldPrice:  oldPrice,
			NewPrice:  latest.price,
			Timestamp: time.Now(),
		}

		// 触发所有注册的回调处理器（直接回调，不使用事件总线）
		m.handlers.Emit(ctx, event)
		logger.Debugf("📤 价格变化事件已触发回调: %s @ %dc (处理器数量: %d)", tokenType, latest.price.Cents, m.handlers.Count())
	}
}

// Close 关闭 WebSocket 连接
func (m *MarketWebSocket) Close() error {
	m.mu.Lock()
	// 先标记为已关闭，防止新的操作
	m.closed = true

	// 取消 context，通知所有 goroutine 停止
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}

	// 关闭连接，这会中断 ReadMessage 的阻塞
	var conn *websocket.Conn
	if m.conn != nil {
		conn = m.conn
		m.conn = nil
	}
	m.mu.Unlock()

	// 关闭连接（这会触发 ReadMessage 返回错误，让 handleMessages 退出）
	if conn != nil {
		conn.Close()
	}

	// 等待所有 goroutine 退出（使用 SyncGroup）
	m.sg.WaitAndClear()
	logger.Debugf("市场价格 WebSocket 所有 goroutine 已退出")

	return nil
}

// getProxyFromEnv 从环境变量获取代理 URL
func getProxyFromEnv() string {
	proxyVars := []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"}
	for _, v := range proxyVars {
		if proxy := os.Getenv(v); proxy != "" {
			return proxy
		}
	}
	return ""
}
