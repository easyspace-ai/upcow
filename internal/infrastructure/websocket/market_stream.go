package websocket

import (
	"bytes"
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

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/marketstate"
	"github.com/betbot/gobet/internal/stream"
	"github.com/betbot/gobet/pkg/syncgroup"
)

var marketLog = logrus.WithField("component", "market_stream")

const (
	reconnectCoolDownPeriod = 15 * time.Second
	pingInterval            = 10 * time.Second
	readTimeout             = 30 * time.Second
	writeTimeout            = 10 * time.Second
	// marketDataMaxSpreadCents: 盘口质量 gate（ask-bid 超过该值则认为“不适合做决策/触发策略”）
	// 目的：避免初始快照/断档盘口把 best_ask=0.99 这种极端值当作“市场价格”
	marketDataMaxSpreadCents = 10
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

	// 原子快照：top-of-book（供策略/执行快速读取）
	bestBook *marketstate.AtomicBestBook

	// 热路径降噪：对"盘口价差过大"的 WARN 做限频，避免刷屏拖慢主循环
	spreadWarnMu sync.Mutex
	spreadWarnAt map[string]time.Time // key: assetID

	// 订阅状态跟踪：支持动态订阅/退订
	subscribedAssets   map[string]bool // key: assetID, value: 是否已订阅
	subscribedAssetsMu sync.RWMutex    // 保护订阅列表
}

func validateMarketForStream(market *domain.Market) error {
	if market == nil {
		return fmt.Errorf("market 为 nil")
	}
	if market.Slug == "" {
		return fmt.Errorf("market slug 为空")
	}
	if market.YesAssetID == "" || market.NoAssetID == "" {
		return fmt.Errorf("market asset IDs not set: YesAssetID=%s NoAssetID=%s", market.YesAssetID, market.NoAssetID)
	}
	// 【系统级硬约束】ConditionID 必须存在，否则无法可靠做 market 过滤（会导致跨周期污染进入策略）
	if strings.TrimSpace(market.ConditionID) == "" {
		return fmt.Errorf("market ConditionID 为空（拒绝连接/切换，避免跨周期数据污染）: market=%s", market.Slug)
	}
	return nil
}

// NewMarketStream 创建新的市场数据流
func NewMarketStream() *MarketStream {
	return &MarketStream{
		reconnectC:       make(chan struct{}, 1),
		closeC:           make(chan struct{}),
		handlers:         stream.NewHandlerList(),
		sg:               syncgroup.NewSyncGroup(), // 长期运行的 goroutine
		connSg:           syncgroup.NewSyncGroup(), // 连接相关的 goroutine
		lastPong:         time.Now(),
		lastMessageAt:    time.Now(),
		bestBook:         marketstate.NewAtomicBestBook(),
		spreadWarnAt:     make(map[string]time.Time),
		subscribedAssets: make(map[string]bool),
	}
}

// shouldLogWideSpreadWarn 对同一 asset 的 “wide spread” 警告做限频（默认每 2 秒最多一条）。
func (m *MarketStream) shouldLogWideSpreadWarn(assetID string) bool {
	if m == nil || assetID == "" {
		return false
	}
	now := time.Now()
	m.spreadWarnMu.Lock()
	defer m.spreadWarnMu.Unlock()
	if last, ok := m.spreadWarnAt[assetID]; ok && now.Sub(last) < 2*time.Second {
		return false
	}
	m.spreadWarnAt[assetID] = now
	return true
}

// BestBook 返回当前 MarketStream 的原子 top-of-book 快照（可能为 nil）。
func (m *MarketStream) BestBook() *marketstate.AtomicBestBook {
	if m == nil {
		return nil
	}
	return m.bestBook
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

// SwitchMarket 切换市场（动态订阅/退订，不关闭连接）
// oldMarket: 旧市场（如果为 nil，则只订阅新市场）
// newMarket: 新市场（如果为 nil，则只退订旧市场）
func (m *MarketStream) SwitchMarket(ctx context.Context, oldMarket, newMarket *domain.Market) error {
	if newMarket != nil {
		if err := validateMarketForStream(newMarket); err != nil {
			return err
		}
	}
	// 检查连接状态
	m.connMu.Lock()
	conn := m.conn
	m.connMu.Unlock()

	// 如果连接未建立，先建立连接
	if conn == nil {
		if newMarket == nil {
			return fmt.Errorf("连接未建立且新市场为 nil，无法切换")
		}
		marketLog.Infof("🔄 [切换市场] 连接未建立，先建立连接: %s", newMarket.Slug)
		if err := m.Connect(ctx, newMarket); err != nil {
			return fmt.Errorf("建立连接失败: %w", err)
		}
		return nil
	}

	// 【关键修复】先更新市场信息，确保后续消息过滤使用正确的市场信息
	// 同时将“订阅报文（wire）”与“允许处理资产集合（logical）”解耦：
	// - wire：只发送 YES(UP) asset_id（服务器会回推 UP/DOWN 两个 token 的 price_change）
	// - logical：本地允许处理 YES+NO，避免把 DOWN 数据丢掉
	if newMarket != nil {
		m.market = newMarket
		marketLog.Infof("🔄 [切换市场] 已更新市场信息: %s", newMarket.Slug)
	}

	// 退订旧市场（wire 只退订 YES/UP；logical 订阅集合会在下面重置，防止旧周期残留被重连恢复）
	if oldMarket != nil {
		if oldMarket.YesAssetID != "" {
			if err := m.sendMarketSubscription([]string{oldMarket.YesAssetID}, "unsubscribe"); err != nil {
				marketLog.Warnf("⚠️ [切换市场] 退订旧市场(UP)失败: %v", err)
			} else {
				marketLog.Infof("✅ [切换市场] 已发送旧市场退订(UP): %s", oldMarket.Slug)
			}
		}
		// 等待一小段时间，尽量让退订先落地，降低旧数据继续推送窗口
		time.Sleep(50 * time.Millisecond)
	}

	// 订阅新市场（wire 只订阅 YES/UP；logical 重置为当前 market 的 YES+NO）
	if newMarket != nil {
		m.resetLogicalSubscriptionsForMarket(newMarket)
		if newMarket.YesAssetID == "" {
			return fmt.Errorf("订阅新市场失败: YesAssetID 为空 market=%s", newMarket.Slug)
		}
		if err := m.sendMarketSubscription([]string{newMarket.YesAssetID}, "subscribe"); err != nil {
			return fmt.Errorf("订阅新市场失败: %w", err)
		}
		marketLog.Infof("✅ [切换市场] 已订阅新市场(UP-only): %s", newMarket.Slug)

		// 重置 bestBook（新市场需要重新构建订单簿）
		// 注意：不能替换 bestBook 指针，否则 Session/策略若缓存了旧指针，会继续读到旧盘口（数据污染）。
		// 必须原地 Reset。
		if m.bestBook != nil {
			m.bestBook.Reset()
		} else {
			m.bestBook = marketstate.NewAtomicBestBook()
		}

		// 【修复】验证订阅状态
		m.subscribedAssetsMu.RLock()
		subscribedCount := len(m.subscribedAssets)
		subscribedList := make([]string, 0, len(m.subscribedAssets))
		for assetID := range m.subscribedAssets {
			subscribedList = append(subscribedList, assetID)
		}
		m.subscribedAssetsMu.RUnlock()
		marketLog.Infof("📊 [切换市场] 订阅状态验证: 已订阅资产数量=%d, 期望资产=[%s, %s]",
			subscribedCount, newMarket.YesAssetID[:12]+"...", newMarket.NoAssetID[:12]+"...")

		if subscribedCount == 0 {
			marketLog.Warnf("⚠️ [切换市场] 订阅状态异常：没有已订阅的资产！")
		}

		// 【修复】记录切换前的最后消息时间，用于后续超时检测
		m.lastMsgMu.RLock()
		switchStartTime := m.lastMessageAt
		m.lastMsgMu.RUnlock()

		// 【修复】启动价格数据超时检测（30秒后检查是否收到价格数据）
		go func() {
			time.Sleep(30 * time.Second)
			m.lastMsgMu.RLock()
			lastMsg := m.lastMessageAt
			m.lastMsgMu.RUnlock()

			// 如果切换后30秒内没有收到任何消息，记录警告
			if lastMsg.IsZero() || lastMsg.Equal(switchStartTime) || time.Since(lastMsg) > 30*time.Second {
				handlerCount := m.handlers.Count()
				marketLog.Warnf("⚠️ [切换市场] 周期切换后30秒内未收到价格数据: market=%s lastMsg=%v handlers=%d",
					newMarket.Slug, lastMsg, handlerCount)

				// 如果 handlers 已注册但没有数据，尝试重新订阅
				if handlerCount > 0 {
					marketLog.Warnf("🔄 [切换市场] 尝试重新订阅: market=%s", newMarket.Slug)
					// wire 仍然只发 UP；logical 不动（仍为 YES+NO）
					if err := m.sendMarketSubscription([]string{newMarket.YesAssetID}, "subscribe"); err != nil {
						marketLog.Errorf("❌ [切换市场] 重新订阅失败: %v", err)
					} else {
						marketLog.Infof("✅ [切换市场] 重新订阅成功: market=%s", newMarket.Slug)
					}
				}
			} else {
				marketLog.Debugf("✅ [切换市场] 周期切换后已收到价格数据: market=%s lastMsg=%v",
					newMarket.Slug, lastMsg)
			}
		}()
	}

	return nil
}

// Connect 连接到市场数据流（支持连接复用）
// 如果连接已建立，只订阅新市场；如果连接未建立，建立连接并订阅
func (m *MarketStream) Connect(ctx context.Context, market *domain.Market) error {
	if err := validateMarketForStream(market); err != nil {
		return err
	}

	m.connMu.Lock()
	conn := m.conn
	m.connMu.Unlock()

	// 如果连接已建立，只订阅新市场（动态订阅）
	if conn != nil {
		marketLog.Infof("🔄 [Connect] 连接已建立，使用动态订阅: %s", market.Slug)
		m.market = market
		m.resetLogicalSubscriptionsForMarket(market)
		if market.YesAssetID == "" {
			return fmt.Errorf("market YesAssetID not set: market=%s", market.Slug)
		}
		return m.sendMarketSubscription([]string{market.YesAssetID}, "subscribe")
	}

	// 连接未建立，建立新连接
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
	// 使用额外的 recover 保护，防止 panic 导致整个程序崩溃
	m.connSg.Add(func() {
		defer func() {
			if r := recover(); r != nil {
				marketLog.Errorf("MarketStream Read goroutine panic recovered: %v", r)
				_ = conn.Close()
				connCancel()
			}
		}()
		m.Read(connCtx, conn, connCancel)
	})
	m.connSg.Add(func() {
		defer func() {
			if r := recover(); r != nil {
				marketLog.Errorf("MarketStream ping goroutine panic recovered: %v", r)
				_ = conn.Close()
				connCancel()
			}
		}()
		m.ping(connCtx, conn, connCancel)
	})
	m.connSg.Run()

	// 订阅市场（使用 m.market）
	if m.market == nil {
		conn.Close()
		return fmt.Errorf("market not set")
	}
	if err := validateMarketForStream(m.market); err != nil {
		conn.Close()
		return err
	}

	// 健康检查：验证重连后的状态
	handlerCount := m.handlers.Count()
	if handlerCount == 0 {
		marketLog.Warnf("⚠️ [重连健康检查] Handlers 为空，但继续连接: market=%s", m.market.Slug)
	} else {
		marketLog.Infof("✅ [重连健康检查] Handlers 数量=%d: market=%s", handlerCount, m.market.Slug)
	}

	// 订阅当前市场（wire: UP-only；logical: YES+NO）
	m.resetLogicalSubscriptionsForMarket(m.market)
	if err := m.sendMarketSubscription([]string{m.market.YesAssetID}, "subscribe"); err != nil {
		conn.Close()
		return err
	}

	marketLog.Infof("市场价格 WebSocket 已连接: %s (handlers=%d)", m.market.Slug, handlerCount)
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
			} else {
				// 重连成功后的状态验证
				handlerCount := m.handlers.Count()
				marketSlug := ""
				marketConditionID := ""
				if m.market != nil {
					marketSlug = m.market.Slug
					marketConditionID = m.market.ConditionID
				}

				marketLog.Infof("✅ [重连成功] 连接已恢复: market=%s conditionID=%s handlers=%d",
					marketSlug, marketConditionID, handlerCount)

				if m.market == nil {
					marketLog.Errorf("❌ [重连验证] Market 未设置！")
				} else if m.market.YesAssetID == "" || m.market.NoAssetID == "" {
					marketLog.Errorf("❌ [重连验证] Market Asset IDs 未设置: YesAssetID=%s NoAssetID=%s",
						m.market.YesAssetID, m.market.NoAssetID)
				}

				if handlerCount == 0 {
					marketLog.Errorf("❌ [重连验证] Handlers 为空！价格事件将无法触发策略！")
				}

				// 重连成功后：强制恢复“当前周期 market”的订阅，避免把旧周期的资产带回来（数据源污染根因之一）
				// wire: 只订阅 YES/UP；logical: 只允许当前 market 的 YES+NO 进入策略
				if m.market == nil || m.market.YesAssetID == "" || m.market.NoAssetID == "" {
					marketLog.Errorf("❌ [重连恢复] Market 未就绪，无法恢复订阅: market=%v", m.market != nil)
					continue
				}
				m.resetLogicalSubscriptionsForMarket(m.market)
				marketLog.Infof("🔄 [重连恢复] 恢复当前市场订阅(UP-only wire, YES+NO logical): market=%s", marketSlug)
				resubscribeStartTime := time.Now()
				if err := m.sendMarketSubscription([]string{m.market.YesAssetID}, "subscribe"); err != nil {
					marketLog.Warnf("⚠️ [重连恢复] 恢复订阅失败: %v", err)
				} else {
					marketLog.Infof("✅ [重连恢复] 订阅消息已发送(UP-only): market=%s handlers=%d", marketSlug, handlerCount)
					// 启动监控：如果重连后 30 秒内没有收到任何消息，自动重新订阅（仍然只发 UP）
					go func() {
						time.Sleep(30 * time.Second)
						m.lastMsgMu.RLock()
						lastMsg := m.lastMessageAt
						m.lastMsgMu.RUnlock()
						if lastMsg.Before(resubscribeStartTime) || time.Since(lastMsg) > 30*time.Second {
							marketLog.Warnf("⚠️ [重连监控] 重连后 30 秒内未收到任何消息，尝试自动重新订阅: market=%s lastMsg=%v",
								marketSlug, lastMsg)
							if handlerCount > 0 {
								if err := m.sendMarketSubscription([]string{m.market.YesAssetID}, "subscribe"); err != nil {
									marketLog.Errorf("❌ [重连监控] 自动重新订阅失败: %v", err)
								} else {
									marketLog.Infof("✅ [重连监控] 自动重新订阅成功: market=%s", marketSlug)
								}
							}
						} else {
							marketLog.Debugf("✅ [重连监控] 重连后已收到消息: market=%s lastMsg=%v", marketSlug, lastMsg)
						}
					}()
				}
			}
		}
	}
}

// resetLogicalSubscriptionsForMarket 将“允许处理的资产集合（logical）”重置为指定 market 的 YES+NO。
// 目的：
// - 周期切换/重连成功后，强制清掉旧周期资产，避免旧资产在后续重连时被恢复（数据源污染）
// - 支持“wire 只订阅 UP，但本地仍允许处理 UP+DOWN”
func (m *MarketStream) resetLogicalSubscriptionsForMarket(market *domain.Market) {
	if market == nil {
		return
	}
	newMap := make(map[string]bool, 2)
	if market.YesAssetID != "" {
		newMap[market.YesAssetID] = true
	}
	if market.NoAssetID != "" {
		newMap[market.NoAssetID] = true
	}
	m.subscribedAssetsMu.Lock()
	m.subscribedAssets = newMap
	m.subscribedAssetsMu.Unlock()
}

// sendMarketSubscription 发送 market-channel 的订阅/退订消息（wire 层）。
// 说明：
// - 为对齐服务端行为与“只需订阅 UP”的约束，我们这里允许只发送 1 个 asset_id。
// - logical 层（本地过滤允许处理的资产）由 resetLogicalSubscriptionsForMarket 负责维护。
func (m *MarketStream) sendMarketSubscription(assetIDs []string, operation string) error {
	if len(assetIDs) == 0 {
		return fmt.Errorf("资产 ID 列表为空")
	}

	// 订阅报文
	msg := map[string]interface{}{
		"assets_ids": assetIDs,
		"type":       "market",
	}
	if operation != "" {
		msg["operation"] = operation
	}

	m.connMu.Lock()
	conn := m.conn
	m.connMu.Unlock()
	if conn == nil {
		return fmt.Errorf("连接未建立")
	}

	// 记录订阅消息内容（用于调试）
	if msgBytes, err := json.Marshal(msg); err == nil {
		marketLog.Debugf("📤 [订阅发送] market msg: %s", string(msgBytes))
	}
	return conn.WriteJSON(msg)
}

// Read 读取消息循环
func (m *MarketStream) Read(ctx context.Context, conn *websocket.Conn, cancel context.CancelFunc) {
	defer func() {
		cancel()
	}()

	// 使用 recover 捕获可能的 panic（连接失败后重复读取会导致 panic）
	defer func() {
		if r := recover(); r != nil {
			// 捕获 panic，特别是 "repeated read on failed websocket connection"
			marketLog.Errorf("WebSocket 读取时发生 panic: %v，连接可能已失败", r)
			// 标记连接为已关闭，避免后续重复读取
			_ = conn.Close()
			m.Reconnect()
		}
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

		// 在设置 deadline 之前，快速检查连接是否仍然有效
		// 如果连接已经被替换，context 应该已经被取消，但为了安全起见，我们再次检查
		m.connMu.Lock()
		currentConn := m.conn
		currentCtx := m.connCtx
		m.connMu.Unlock()

		// 如果连接已经被替换，说明有新的连接，旧连接应该退出
		if currentConn != conn || currentCtx != ctx {
			marketLog.Debugf("WebSocket 连接已被替换，退出旧的 Read goroutine")
			return
		}

		// 设置读取超时：用 deadline 让 ReadMessage 至多阻塞 readTimeout，
		// 这样无需每轮起 goroutine，避免长期运行下 goroutine churn。
		if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			marketLog.Errorf("设置读取超时失败: %v", err)
			return
		}

		// 使用 recover 包装 ReadMessage 调用，防止 panic
		// 注意：gorilla/websocket 在连接失败后重复读取会直接 panic，而不是返回错误
		// 这是库的内部行为，我们无法改变，只能通过 recover 捕获
		var message []byte
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					// 捕获 panic，特别是 "repeated read on failed websocket connection"
					// 这是 gorilla/websocket 库在连接失败后重复读取时的行为
					marketLog.Errorf("WebSocket ReadMessage 时发生 panic: %v，连接可能已失败", r)
					err = fmt.Errorf("panic: %v", r)
				}
			}()
			// 再次检查 context（在阻塞调用之前）
			select {
			case <-ctx.Done():
				err = fmt.Errorf("context canceled")
				return
			default:
			}
			_, message, err = conn.ReadMessage()
		}()

		if err != nil {
			// 检查是否是 panic 错误（连接失败后重复读取）
			errStr := err.Error()
			isPanicError := strings.Contains(errStr, "panic:") ||
				strings.Contains(errStr, "repeated read on failed websocket connection")

			// 如果是 panic 错误，立即关闭连接并触发重连
			if isPanicError {
				marketLog.Warnf("WebSocket 读取时发生 panic 错误: %v，关闭连接并触发重连", err)
				_ = conn.Close()
				m.Reconnect()
				return
			}

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
		marketSlug := "nil"
		if m.market != nil {
			marketSlug = m.market.Slug
		}
		handlerCount := m.handlers.Count()
		marketLog.Debugf("📥 [消息接收] 收到 WebSocket 消息: len=%d market=%s handlers=%d",
			len(message), marketSlug, handlerCount)
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

// subscribe 订阅市场资产（支持动态订阅和批量订阅）
// assetIDs: 要订阅的资产 ID 列表
// operation: "subscribe" 或空字符串（默认为 "subscribe"）
// forceResubscribe: 如果为 true，即使资产已标记为已订阅，也强制重新发送订阅消息（用于重连后恢复订阅）
func (m *MarketStream) subscribe(assetIDs []string, operation string, forceResubscribe ...bool) error {
	if len(assetIDs) == 0 {
		return fmt.Errorf("资产 ID 列表为空")
	}

	if operation == "" {
		operation = "subscribe"
	}

	force := false
	if len(forceResubscribe) > 0 && forceResubscribe[0] {
		force = true
	}

	// 过滤出未订阅的资产（避免重复订阅）
	// 如果 forceResubscribe 为 true，则强制重新订阅所有资产
	m.subscribedAssetsMu.Lock()
	newAssetIDs := make([]string, 0, len(assetIDs))
	for _, assetID := range assetIDs {
		if force || !m.subscribedAssets[assetID] {
			newAssetIDs = append(newAssetIDs, assetID)
			m.subscribedAssets[assetID] = true
		}
	}
	m.subscribedAssetsMu.Unlock()

	if len(newAssetIDs) == 0 {
		if force {
			marketLog.Debugf("强制重新订阅但所有资产已在订阅列表中: %v", assetIDs)
		} else {
			marketLog.Debugf("所有资产已订阅，跳过: %v", assetIDs)
		}
		return nil
	}

	subscribeMsg := map[string]interface{}{
		"assets_ids": newAssetIDs,
		"type":       "market",
	}
	if operation != "" {
		subscribeMsg["operation"] = operation
	}

	// 添加诊断日志：记录订阅详情
	forceStr := ""
	if force {
		forceStr = " (强制重新订阅)"
	}
	marketSlug := ""
	if m.market != nil {
		marketSlug = m.market.Slug
	}
	marketLog.Infof("📡 [订阅发送] 订阅市场资产%s (operation=%s): market=%s assets=%d force=%v",
		forceStr, operation, marketSlug, len(newAssetIDs), force)

	m.connMu.Lock()
	conn := m.conn
	m.connMu.Unlock()

	if conn == nil {
		// 连接未建立，回滚订阅状态
		m.subscribedAssetsMu.Lock()
		for _, assetID := range newAssetIDs {
			delete(m.subscribedAssets, assetID)
		}
		m.subscribedAssetsMu.Unlock()
		marketLog.Errorf("❌ [订阅发送] 连接未建立，无法发送订阅消息: market=%s assets=%d", marketSlug, len(newAssetIDs))
		return fmt.Errorf("连接未建立")
	}

	// 记录订阅消息内容（用于调试）
	if msgBytes, err := json.Marshal(subscribeMsg); err == nil {
		marketLog.Debugf("📤 [订阅发送] 订阅消息内容: %s", string(msgBytes))
	}

	if err := conn.WriteJSON(subscribeMsg); err != nil {
		// 发送失败，回滚订阅状态
		m.subscribedAssetsMu.Lock()
		for _, assetID := range newAssetIDs {
			delete(m.subscribedAssets, assetID)
		}
		m.subscribedAssetsMu.Unlock()
		marketLog.Errorf("❌ [订阅发送] 发送订阅消息失败: market=%s assets=%d error=%v", marketSlug, len(newAssetIDs), err)
		return fmt.Errorf("发送订阅消息失败: %w", err)
	}
	marketLog.Infof("✅ [订阅发送] 订阅消息已发送到服务器: market=%s assets=%d%s", marketSlug, len(newAssetIDs), forceStr)
	return nil
}

// subscribeMarket 订阅市场（兼容旧接口，内部调用新的 subscribe 方法）
func (m *MarketStream) subscribeMarket(market *domain.Market) error {
	if err := validateMarketForStream(market); err != nil {
		return err
	}
	m.market = market
	m.resetLogicalSubscriptionsForMarket(market)
	if market.YesAssetID == "" {
		return fmt.Errorf("market YesAssetID not set: market=%s", market.Slug)
	}
	return m.sendMarketSubscription([]string{market.YesAssetID}, "subscribe")
}

// unsubscribe 退订市场资产（支持动态退订和批量退订）
// assetIDs: 要退订的资产 ID 列表
func (m *MarketStream) unsubscribe(assetIDs []string) error {
	if len(assetIDs) == 0 {
		return nil // 空列表直接返回
	}

	// 过滤出已订阅的资产
	m.subscribedAssetsMu.Lock()
	subscribedAssetIDs := make([]string, 0, len(assetIDs))
	for _, assetID := range assetIDs {
		if m.subscribedAssets[assetID] {
			subscribedAssetIDs = append(subscribedAssetIDs, assetID)
			delete(m.subscribedAssets, assetID)
		}
	}
	m.subscribedAssetsMu.Unlock()

	if len(subscribedAssetIDs) == 0 {
		marketLog.Debugf("所有资产未订阅，跳过退订: %v", assetIDs)
		return nil
	}

	unsubscribeMsg := map[string]interface{}{
		"assets_ids": subscribedAssetIDs,
		"operation":  "unsubscribe",
		"type":       "market",
	}

	marketLog.Infof("🔕 退订市场资产: %d 个资产", len(subscribedAssetIDs))

	m.connMu.Lock()
	conn := m.conn
	m.connMu.Unlock()

	if conn == nil {
		// 连接未建立，但已从订阅列表中移除，这是合理的（连接断开时清理状态）
		marketLog.Debugf("连接未建立，但已从订阅列表中移除资产")
		return nil
	}

	if err := conn.WriteJSON(unsubscribeMsg); err != nil {
		// 发送失败，恢复订阅状态（因为实际上没有退订成功）
		m.subscribedAssetsMu.Lock()
		for _, assetID := range subscribedAssetIDs {
			m.subscribedAssets[assetID] = true
		}
		m.subscribedAssetsMu.Unlock()
		return fmt.Errorf("发送退订消息失败: %w", err)
	}
	marketLog.Infof("✅ 退订消息已发送: %d 个资产", len(subscribedAssetIDs))
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

	eventType := detectEventTypeCode(message)
	marketSlug := "nil"
	if m.market != nil {
		marketSlug = m.market.Slug
	}
	handlerCount := m.handlers.Count()

	switch eventType {
	case evtPriceChange:
		//marketLog.Infof("📨 [消息处理] 收到 price_change 消息: market=%s handlers=%d", marketSlug, handlerCount)
		m.handlePriceChange(ctx, message)
	case evtSubscribed:
		marketLog.Infof("✅ [订阅确认] MarketStream 收到订阅成功消息: market=%s handlers=%d", marketSlug, handlerCount)
		// 记录订阅确认的时间
		subscribeConfirmTime := time.Now()

		// 【修复】验证订阅状态
		m.subscribedAssetsMu.RLock()
		subscribedCount := len(m.subscribedAssets)
		subscribedAssetIDs := make([]string, 0, len(m.subscribedAssets))
		for assetID := range m.subscribedAssets {
			subscribedAssetIDs = append(subscribedAssetIDs, assetID)
		}
		m.subscribedAssetsMu.RUnlock()
		marketLog.Infof("📊 [订阅确认] 订阅成功: market=%s 已订阅资产数量=%d handlers=%d assets=%v",
			marketSlug, subscribedCount, handlerCount, func() []string {
				// 只显示前2个资产ID的前12个字符，避免日志过长
				if len(subscribedAssetIDs) <= 2 {
					result := make([]string, len(subscribedAssetIDs))
					for i, id := range subscribedAssetIDs {
						if len(id) > 12 {
							result[i] = id[:12] + "..."
						} else {
							result[i] = id
						}
					}
					return result
				}
				return []string{fmt.Sprintf("%d个资产", len(subscribedAssetIDs))}
			}())

		// 启动监控：如果订阅确认后 30 秒内没有收到任何价格数据，记录警告
		// 注意：这里不自动重新订阅，因为可能是市场本身没有数据更新
		go func() {
			time.Sleep(30 * time.Second)
			m.lastMsgMu.RLock()
			lastMsg := m.lastMessageAt
			m.lastMsgMu.RUnlock()

			// 检查是否在订阅确认后收到了新消息
			if lastMsg.Before(subscribeConfirmTime) || time.Since(lastMsg) > 30*time.Second {
				marketLog.Warnf("⚠️ [订阅监控] 订阅确认后 30 秒内未收到任何价格数据: market=%s lastMsg=%v confirmTime=%v handlers=%d",
					marketSlug, lastMsg, subscribeConfirmTime, handlerCount)
			} else {
				marketLog.Debugf("✅ [订阅监控] 订阅确认后已收到价格数据: market=%s lastMsg=%v",
					marketSlug, lastMsg)
			}
		}()
	case evtPong:
		m.healthCheckMu.Lock()
		m.lastPong = time.Now()
		m.healthCheckMu.Unlock()
		marketLog.Debugf("收到 PONG 响应")
	case evtBook:
		marketLog.Debugf("📨 [消息处理] 收到 book 消息: market=%s handlers=%d", marketSlug, handlerCount)
		// 兼容：某些情况下服务器只推 book（快照/增量），未推 price_change。
		// 为了不让策略"完全看不到实时 up/down"，这里从 book 中提取 best_ask/best_bid 并发出 PriceChangedEvent。
		m.handleBookAsPrice(ctx, message)
	case evtTickSizeChange:
		marketLog.Debugf("收到 tick size 变化消息")
	case evtLastTradePrice:
		marketLog.Debugf("💰 收到最后交易价格消息（价格变化应通过 price_change 事件发送）")
	default:
		// 未知类型：回退到 json.Unmarshal 获取 event_type 用于可观测性（非热路径）
		var msgType struct {
			EventType string `json:"event_type"`
		}
		if err := json.Unmarshal(message, &msgType); err != nil {
			msgPreview := message
			if len(msgPreview) > 200 {
				msgPreview = msgPreview[:200]
			}
			marketLog.Debugf("解析消息类型失败(可能是非JSON): %v, msg=%q", err, string(msgPreview))
			return
		}
		msgPreview := message
		if len(msgPreview) > 200 {
			msgPreview = msgPreview[:200]
		}
		marketLog.Debugf("📨 [消息处理] 收到未知消息类型: %s (消息内容: %s) market=%s",
			msgType.EventType, string(msgPreview), func() string {
				if m.market != nil {
					return m.market.Slug
				}
				return "nil"
			}())
	}
}

type orderLevel struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

type eventTypeCode uint8

const (
	evtUnknown eventTypeCode = iota
	evtPriceChange
	evtSubscribed
	evtPong
	evtBook
	evtTickSizeChange
	evtLastTradePrice
)

// detectEventTypeCode 尽量用低开销方式从 JSON 中提取 event_type，避免每条消息都 json.Unmarshal 一次。
// 这是热路径优化：只服务于我们已知的几个 event_type；未知类型会回退到 json.Unmarshal 获取字符串。
func detectEventTypeCode(message []byte) eventTypeCode {
	// 查找 "event_type"
	i := bytes.Index(message, []byte(`"event_type"`))
	if i < 0 {
		return evtUnknown
	}
	// 查找 ':'（允许中间存在空格）
	j := bytes.IndexByte(message[i:], ':')
	if j < 0 {
		return evtUnknown
	}
	j = i + j + 1
	for j < len(message) {
		c := message[j]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			j++
			continue
		}
		break
	}
	if j >= len(message) || message[j] != '"' {
		return evtUnknown
	}
	j++
	k := j
	for k < len(message) && message[k] != '"' {
		k++
	}
	if k <= j || k >= len(message) {
		return evtUnknown
	}
	et := message[j:k]

	switch {
	case bytes.Equal(et, []byte("price_change")):
		return evtPriceChange
	case bytes.Equal(et, []byte("subscribed")):
		return evtSubscribed
	case bytes.Equal(et, []byte("pong")):
		return evtPong
	case bytes.Equal(et, []byte("book")):
		return evtBook
	case bytes.Equal(et, []byte("tick_size_change")):
		return evtTickSizeChange
	case bytes.Equal(et, []byte("last_trade_price")):
		return evtLastTradePrice
	default:
		return evtUnknown
	}
}

var (
	keyMarket       = []byte(`"market"`)
	keyPriceChanges = []byte(`"price_changes"`)
	keyAssetID      = []byte(`"asset_id"`)
	keyBestBid      = []byte(`"best_bid"`)
	keyBestAsk      = []byte(`"best_ask"`)
)

func findJSONStringValue(msg []byte, key []byte) ([]byte, bool) {
	// 找到 key
	i := bytes.Index(msg, key)
	if i < 0 {
		return nil, false
	}
	// 找到 ':'
	j := bytes.IndexByte(msg[i+len(key):], ':')
	if j < 0 {
		return nil, false
	}
	j = i + len(key) + j + 1
	// skip spaces
	for j < len(msg) {
		c := msg[j]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			j++
			continue
		}
		break
	}
	if j >= len(msg) || msg[j] != '"' {
		return nil, false
	}
	j++
	start := j
	escaped := false
	for j < len(msg) {
		c := msg[j]
		if escaped {
			escaped = false
			j++
			continue
		}
		if c == '\\' {
			escaped = true
			j++
			continue
		}
		if c == '"' {
			return msg[start:j], true
		}
		j++
	}
	return nil, false
}

func findJSONArrayStart(msg []byte, key []byte) (int, bool) {
	i := bytes.Index(msg, key)
	if i < 0 {
		return 0, false
	}
	j := bytes.IndexByte(msg[i+len(key):], ':')
	if j < 0 {
		return 0, false
	}
	j = i + len(key) + j + 1
	for j < len(msg) {
		c := msg[j]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			j++
			continue
		}
		break
	}
	if j >= len(msg) || msg[j] != '[' {
		return 0, false
	}
	return j, true
}

func scanJSONObjectEnd(msg []byte, start int) (int, bool) {
	if start < 0 || start >= len(msg) || msg[start] != '{' {
		return 0, false
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(msg); i++ {
		c := msg[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

// shouldProcessMarketMessage 决定是否处理某条 market-channel 消息。
// 【关键修复】优先检查 market conditionID，确保只处理当前市场的消息，避免旧市场的消息被误处理。
func (m *MarketStream) shouldProcessMarketMessage(msgMarket string, msgAssetID string) bool {
	// 【关键修复】优先检查 market conditionID，如果 market 不匹配，直接丢弃（避免旧市场消息被误处理）
	msgMarket = strings.TrimSpace(msgMarket)
	if msgMarket != "" {
		expected := ""
		currentSlug := ""
		if m.market != nil {
			expected = strings.TrimSpace(m.market.ConditionID)
			currentSlug = m.market.Slug
		}
		// 如果当前周期 market id 已就绪，必须匹配才处理
		if expected != "" {
			if !strings.EqualFold(expected, msgMarket) {
				// market 不匹配，直接丢弃（即使 asset_id 在订阅列表中）
				// 添加诊断日志，记录被过滤的消息（限频，避免刷屏）
				assetIDPreview := msgAssetID
				if len(assetIDPreview) > 12 {
					assetIDPreview = assetIDPreview[:12] + "..."
				}
				marketLog.Debugf("🚫 [消息过滤] 丢弃旧市场消息: msg.market=%s msg.assetID=%s expected=%s current=%s",
					msgMarket, assetIDPreview, expected, currentSlug)
				return false
			}
		}
	}

	// market 匹配（或为空），再检查 asset_id 是否在订阅列表中
	if msgAssetID != "" {
		m.subscribedAssetsMu.RLock()
		isSubscribed := m.subscribedAssets[msgAssetID]
		m.subscribedAssetsMu.RUnlock()
		if isSubscribed {
			return true
		}
	}

	// 如果 market 为空且 asset_id 不在订阅列表中，回退到兼容模式
	// 部分消息可能不携带 market 字段；此时如果 asset_id 也不在订阅列表中，默认不放行
	if msgMarket == "" {
		// 如果没有 market 字段，且 asset_id 也不在订阅列表中，默认不放行（避免误处理）
		return false
	}

	// market 匹配但 asset_id 不在订阅列表中，可能是订阅状态还没更新，允许处理（兼容性）
	return true
}

// handleBookAsPrice 从 book 消息提取价格并触发 PriceChangedEvent（用于兼容“没有 price_change 但有 book”的情况）
func (m *MarketStream) handleBookAsPrice(ctx context.Context, message []byte) {
	if m.market == nil {
		return
	}

	type bookMessage struct {
		EventType string       `json:"event_type"`
		AssetID   string       `json:"asset_id"`
		Market    string       `json:"market"`
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
	// 关键过滤：非当前周期 market 的消息直接丢弃（避免通过 book->price 误入策略）
	if !m.shouldProcessMarketMessage(bm.Market, bm.AssetID) {
		expected := ""
		slug := ""
		if m.market != nil {
			expected = m.market.ConditionID
			slug = m.market.Slug
		}
		marketLog.Debugf("🚫 [market过滤] 丢弃 book: msg.market=%s msg.assetID=%s expected=%s slug=%s", bm.Market, bm.AssetID, expected, slug)
		return
	}
	if bm.AssetID == "" {
		return
	}

	// 更新 AtomicBestBook（bid/ask + size），供执行/策略无锁读取
	var tokenType domain.TokenType
	if bm.AssetID == m.market.YesAssetID {
		tokenType = domain.TokenTypeUp
	} else if bm.AssetID == m.market.NoAssetID {
		tokenType = domain.TokenTypeDown
	} else {
		return
	}

	// 解析 bid/ask（优先 best_*，再回退 level[0]）
	var bidPips, askPips uint16
	var bidCents, askCents uint16 // 兼容口径：用于 spread gate（单位 0.01）
	var bidSizeScaled, askSizeScaled uint32
	if bm.BestBid != "" {
		if p, err := parsePriceString(bm.BestBid); err == nil && p.Pips > 0 {
			bidPips = uint16(p.Pips)
			bidCents = uint16(p.ToCents())
		}
	} else if len(bm.Bids) > 0 && bm.Bids[0].Price != "" {
		if p, err := parsePriceString(bm.Bids[0].Price); err == nil && p.Pips > 0 {
			bidPips = uint16(p.Pips)
			bidCents = uint16(p.ToCents())
		}
	}
	if bm.BestAsk != "" {
		if p, err := parsePriceString(bm.BestAsk); err == nil && p.Pips > 0 {
			askPips = uint16(p.Pips)
			askCents = uint16(p.ToCents())
		}
	} else if len(bm.Asks) > 0 && bm.Asks[0].Price != "" {
		if p, err := parsePriceString(bm.Asks[0].Price); err == nil && p.Pips > 0 {
			askPips = uint16(p.Pips)
			askCents = uint16(p.ToCents())
		}
	}

	// size：优先用 bids[0]/asks[0]
	if len(bm.Bids) > 0 && bm.Bids[0].Size != "" {
		if v, err := strconv.ParseFloat(bm.Bids[0].Size, 64); err == nil && v > 0 {
			bidSizeScaled = uint32(v * 10000.0)
		}
	}
	if len(bm.Asks) > 0 && bm.Asks[0].Size != "" {
		if v, err := strconv.ParseFloat(bm.Asks[0].Size, 64); err == nil && v > 0 {
			askSizeScaled = uint32(v * 10000.0)
		}
	}

	// 原子快照始终更新（供执行层读取），但事件触发要走质量 gate
	if m.bestBook != nil {
		m.bestBook.UpdateToken(tokenType, bidPips, askPips, bidSizeScaled, askSizeScaled)
	}

	// 架构层数据质量 gate：必须是双边盘口且价差合理，才发 PriceChangedEvent
	if bidCents == 0 || askCents == 0 {
		marketLog.Debugf("⚠️ [book->price] 单边盘口，忽略价格事件: token=%s bid=%dc ask=%dc market=%s",
			tokenType, bidCents, askCents, m.market.Slug)
		return
	}
	spread := int(askCents) - int(bidCents)
	if spread < 0 {
		spread = -spread
	}
	if spread > marketDataMaxSpreadCents {
		//marketLog.Warnf("⚠️ [book->price] 盘口价差过大，忽略价格事件: token=%s bid=%dc ask=%dc spread=%dc market=%s",
		//	tokenType, bidCents, askCents, spread, m.market.Slug)
		return
	}
	mid := int(bidCents) + int(askCents)
	mid = (mid + 1) / 2
	newPrice := domain.Price{Pips: mid * 100} // 1 cent = 100 pips
	source := "book.mid"

	// 检查是否已关闭（避免处理关闭后的延迟消息）
	select {
	case <-m.closeC:
		marketLog.Debugf("⚠️ [book->price] MarketStream 已关闭，忽略价格事件: Token=%s, 价格=%dc", tokenType, newPrice.ToCents())
		return
	default:
	}

	// 【关键修复】在发送事件前，检查 handlers 是否为空（防止在关闭过程中 handlers 被清空后仍然发送事件）
	if m.handlers.Count() == 0 {
		marketLog.Debugf("⚠️ [book->price] handlers 已清空，忽略价格事件: Token=%s, 价格=%dc", tokenType, newPrice.ToCents())
		return
	}

	event := &events.PriceChangedEvent{
		Market:    m.market,
		TokenType: tokenType,
		OldPrice:  nil,
		NewPrice:  newPrice,
		Timestamp: time.Now(),
	}
	marketLog.Debugf("📤 [book->price] 触发价格变化回调: %s @ %.4f (source=%s, 市场=%s)", tokenType, newPrice.ToDecimal(), source, m.market.Slug)
	m.handlers.Emit(ctx, event)
}

// handlePriceChange 处理价格变化（直接回调，不使用事件总线）
func (m *MarketStream) handlePriceChange(ctx context.Context, message []byte) {
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

	// 诊断日志：检查 market 状态
	if m.market == nil || m.market.Slug == "" {
		marketLog.Warnf("⚠️ [价格处理] Market 未设置，忽略价格变化消息: market=%v slug=%s",
			m.market != nil, func() string {
				if m.market != nil {
					return m.market.Slug
				}
				return ""
			}())
		return
	}

	// 诊断日志：检查 handlers 状态
	handlerCount := m.handlers.Count()
	if handlerCount == 0 {
		marketLog.Warnf("⚠️ [价格处理] Handlers 为空，无法处理价格变化消息: market=%s", m.market.Slug)
		return
	}
	marketLog.Debugf("📥 [价格处理] 收到价格变化消息: market=%s handlers=%d", m.market.Slug, handlerCount)

	// 极限热路径：手写解析 market + price_changes（失败则回退到 json.Unmarshal 的慢路径）
	if m.handlePriceChangeFast(ctx, message) {
		return
	}

	m.handlePriceChangeSlow(ctx, message)
}

func (m *MarketStream) handlePriceChangeFast(ctx context.Context, message []byte) bool {
	marketBytes, ok := findJSONStringValue(message, keyMarket)
	if !ok {
		return false
	}

	// 先检查 market conditionID（快速过滤）
	if m.market != nil && !bytes.EqualFold(marketBytes, []byte(m.market.ConditionID)) {
		// 但也要检查 asset_id（可能订阅了多个市场）
		// 如果 asset_id 在订阅列表中，仍然处理
		// 这里先快速过滤，后续在解析 asset_id 时再做精确过滤
		// 为了性能，先做 market 过滤，如果匹配则继续处理
		// 如果不匹配，需要检查 asset_id（但需要解析 JSON，性能较差）
		// 为了简化，这里先做 market 过滤，后续在解析时再做 asset_id 过滤
		marketLog.Debugf("🚫 [价格处理] Market 不匹配，跳过: msg.market=%s expected=%s slug=%s",
			string(marketBytes), m.market.ConditionID, m.market.Slug)
		return true // 已知是别的 market，直接丢弃（不算失败）
	}

	// handlers 为空直接丢弃（避免无意义计算）
	handlerCount := m.handlers.Count()
	if handlerCount == 0 {
		marketLog.Warnf("⚠️ [价格处理] Handlers 为空，跳过价格处理: market=%s", m.market.Slug)
		return true
	}

	arrStart, ok := findJSONArrayStart(message, keyPriceChanges)
	if !ok {
		return false
	}

	currentMarketSlug := m.market.Slug
	var upPrice, downPrice domain.Price
	upOK := false
	downOK := false

	yesID := m.market.YesAssetID
	noID := m.market.NoAssetID

	// iterate array objects
	i := arrStart + 1
	for i < len(message) {
		// skip spaces/commas
		for i < len(message) {
			c := message[i]
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',' {
				i++
				continue
			}
			break
		}
		if i >= len(message) || message[i] == ']' {
			break
		}
		if message[i] != '{' {
			return false
		}
		end, ok := scanJSONObjectEnd(message, i)
		if !ok {
			return false
		}
		obj := message[i:end]
		i = end

		assetIDb, ok := findJSONStringValue(obj, keyAssetID)
		if !ok || len(assetIDb) == 0 {
			continue
		}
		assetID := string(assetIDb)

		// 检查 asset_id 是否在订阅列表中（支持多市场场景）
		m.subscribedAssetsMu.RLock()
		isSubscribed := m.subscribedAssets[assetID]
		m.subscribedAssetsMu.RUnlock()

		// 如果不在订阅列表中，跳过（即使 market 匹配也不处理）
		if !isSubscribed {
			continue
		}

		isUp := bytes.Equal(assetIDb, []byte(yesID))
		isDown := bytes.Equal(assetIDb, []byte(noID))
		if !isUp && !isDown {
			// asset_id 在订阅列表中，但不是当前市场的 YES/NO，可能是其他市场的资产
			// 这种情况下，我们仍然处理（支持多市场场景）
			// 但需要确保 market 匹配
			if m.market == nil || !bytes.EqualFold(marketBytes, []byte(m.market.ConditionID)) {
				continue
			}
		}

		// 解析 bid/ask（pips/cents），允许单边更新 bestBook
		var bidPips, askPips uint16
		bidCents := 0
		askCents := 0

		if bb, ok := findJSONStringValue(obj, keyBestBid); ok && len(bb) > 0 {
			if p, err := parsePriceBytes(bb); err == nil && p.Pips > 0 {
				bidPips = uint16(p.Pips)
				bidCents = pipsToCents(p.Pips)
			}
		}
		if ba, ok := findJSONStringValue(obj, keyBestAsk); ok && len(ba) > 0 {
			if p, err := parsePriceBytes(ba); err == nil && p.Pips > 0 {
				askPips = uint16(p.Pips)
				askCents = pipsToCents(p.Pips)
			}
		}

		// 更新 AtomicBestBook（允许单边更新）
		if m.bestBook != nil && (bidCents != 0 || askCents != 0) {
			if isUp {
				m.bestBook.UpdateToken(domain.TokenTypeUp, bidPips, askPips, 0, 0)
			} else {
				m.bestBook.UpdateToken(domain.TokenTypeDown, bidPips, askPips, 0, 0)
			}
		}

		// 事件触发使用 mid（双边 + 价差 gate）
		if bidCents == 0 || askCents == 0 {
			continue
		}
		spread := askCents - bidCents
		if spread < 0 {
			spread = -spread
		}
		if spread > marketDataMaxSpreadCents {
			assetID := string(assetIDb) // only on warn path
			if m.shouldLogWideSpreadWarn(assetID) {
				aid := assetID
				if len(aid) > 12 {
					aid = aid[:12] + "..."
				}
				marketLog.Warnf("⚠️ [price_change->price] 盘口价差过大，忽略价格事件: assetID=%s bid=%dc ask=%dc spread=%dc market=%s",
					aid, bidCents, askCents, spread, currentMarketSlug)
			}
			continue
		}

		mid := bidCents + askCents
		mid = (mid + 1) / 2
		newPrice := domain.Price{Pips: mid * 100} // 1 cent = 100 pips

		if isUp {
			upPrice = newPrice
			upOK = true
		} else {
			downPrice = newPrice
			downOK = true
		}
	}

	if upOK {
		m.emitPriceChanged(ctx, domain.TokenTypeUp, upPrice, currentMarketSlug)
	}
	if downOK {
		m.emitPriceChanged(ctx, domain.TokenTypeDown, downPrice, currentMarketSlug)
	}
	return true
}

func (m *MarketStream) handlePriceChangeSlow(ctx context.Context, message []byte) {
	type priceChange struct {
		AssetID string `json:"asset_id"`
		BestBid string `json:"best_bid"`
		BestAsk string `json:"best_ask"`
	}
	type priceChangeMessage struct {
		Market       string        `json:"market"`
		PriceChanges []priceChange `json:"price_changes"`
	}

	var pm priceChangeMessage
	if err := json.Unmarshal(message, &pm); err != nil {
		marketLog.Debugf("解析 price_change 消息失败: %v", err)
		return
	}

	// 诊断日志：记录收到的价格消息
	marketLog.Debugf("📥 [价格处理] 解析 price_change 消息: msg.market=%s priceChanges=%d expected=%s slug=%s",
		pm.Market, len(pm.PriceChanges), func() string {
			if m.market != nil {
				return m.market.ConditionID
			}
			return "nil"
		}(), func() string {
			if m.market != nil {
				return m.market.Slug
			}
			return "nil"
		}())

	// 关键过滤：只允许当前周期 market conditionId 的消息进入策略
	// 注意：price_change 消息可能包含多个 asset_id，需要逐个检查
	hasValidAsset := false
	for _, ch := range pm.PriceChanges {
		if m.shouldProcessMarketMessage(pm.Market, ch.AssetID) {
			hasValidAsset = true
			break
		}
	}
	if !hasValidAsset {
		marketLog.Debugf("🚫 [market过滤] 丢弃 price_change: msg.market=%s expected=%s slug=%s (无有效 asset)",
			pm.Market, m.market.ConditionID, m.market.Slug)
		return
	}

	// handlers 为空直接丢弃（避免无意义计算）
	handlerCount := m.handlers.Count()
	if handlerCount == 0 {
		marketLog.Warnf("⚠️ [价格处理] Handlers 为空，跳过价格处理: market=%s", m.market.Slug)
		return
	}

	currentMarketSlug := m.market.Slug

	// price_change 只关心当前 YES/NO 两个资产：用局部变量替代 map（零分配）
	var upPrice, downPrice domain.Price
	upOK := false
	downOK := false

	for _, ch := range pm.PriceChanges {
		assetID := ch.AssetID
		if assetID == "" {
			continue
		}

		isUp := assetID == m.market.YesAssetID
		isDown := assetID == m.market.NoAssetID
		if !isUp && !isDown {
			continue
		}

		// 解析 bid/ask（pips/cents），允许单边更新 bestBook
		var bidPips, askPips uint16
		bidCents := 0
		askCents := 0

		if ch.BestBid != "" {
			if p, err := parsePriceString(ch.BestBid); err == nil && p.Pips > 0 {
				bidPips = uint16(p.Pips)
				bidCents = pipsToCents(p.Pips)
			}
		}
		if ch.BestAsk != "" {
			if p, err := parsePriceString(ch.BestAsk); err == nil && p.Pips > 0 {
				askPips = uint16(p.Pips)
				askCents = pipsToCents(p.Pips)
			}
		}

		// 更新 AtomicBestBook（允许单边更新）
		if m.bestBook != nil && (bidCents != 0 || askCents != 0) {
			if isUp {
				m.bestBook.UpdateToken(domain.TokenTypeUp, bidPips, askPips, 0, 0)
			} else {
				m.bestBook.UpdateToken(domain.TokenTypeDown, bidPips, askPips, 0, 0)
			}
		}

		// 事件触发使用 mid（双边 + 价差 gate）
		if bidCents == 0 || askCents == 0 {
			continue
		}
		spread := askCents - bidCents
		if spread < 0 {
			spread = -spread
		}
		if spread > marketDataMaxSpreadCents {
			if m.shouldLogWideSpreadWarn(assetID) {
				aid := assetID
				if len(aid) > 12 {
					aid = aid[:12] + "..."
				}
				marketLog.Warnf("⚠️ [price_change->price] 盘口价差过大，忽略价格事件: assetID=%s bid=%dc ask=%dc spread=%dc market=%s",
					aid, bidCents, askCents, spread, currentMarketSlug)
			}
			continue
		}

		mid := bidCents + askCents
		mid = (mid + 1) / 2
		newPrice := domain.Price{Pips: mid * 100} // 1 cent = 100 pips

		if isUp {
			upPrice = newPrice
			upOK = true
		} else {
			downPrice = newPrice
			downOK = true
		}
	}

	// 触发回调（最多 2 次）
	if upOK {
		m.emitPriceChanged(ctx, domain.TokenTypeUp, upPrice, currentMarketSlug)
	}
	if downOK {
		m.emitPriceChanged(ctx, domain.TokenTypeDown, downPrice, currentMarketSlug)
	}
}

func pipsToCents(pips int) int {
	// 100 pips = 1 cent；四舍五入到最近的 cent（等价于原 ToCents 的 round）
	if pips >= 0 {
		return (pips + 50) / 100
	}
	return (pips - 50) / 100
}

func (m *MarketStream) emitPriceChanged(ctx context.Context, tokenType domain.TokenType, price domain.Price, marketSlug string) {
	// 再次检查是否已关闭（双重保险）
	select {
	case <-m.closeC:
		marketLog.Debugf("⚠️ [价格触发] MarketStream 已关闭，忽略价格事件: token=%s price=%.4f market=%s",
			tokenType, price.ToDecimal(), marketSlug)
		return
	default:
	}

	// 在发送事件前，检查 handlers 是否为空（关闭过程中会被清空）
	handlerCount := m.handlers.Count()
	if handlerCount == 0 {
		marketLog.Warnf("⚠️ [价格触发] Handlers 为空，无法触发价格事件: token=%s price=%.4f market=%s",
			tokenType, price.ToDecimal(), marketSlug)
		return
	}

	event := &events.PriceChangedEvent{
		Market:    m.market,
		TokenType: tokenType,
		OldPrice:  nil,
		NewPrice:  price,
		Timestamp: time.Now(),
	}
	marketLog.Debugf("📤 [价格触发] 触发价格变化事件: token=%s price=%.4f market=%s handlers=%d",
		tokenType, price.ToDecimal(), marketSlug, handlerCount)
	m.handlers.Emit(ctx, event)
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

	// 关闭前退订当前 market（wire 只退订 UP；不依赖 map 顺序，避免退订错 token）
	// 注意：即使退订失败，也会在 close 时断开连接；这里主要用于减少服务器侧推送与带宽
	if m.market != nil && m.market.YesAssetID != "" {
		_ = m.sendMarketSubscription([]string{m.market.YesAssetID}, "unsubscribe")
	}
	// 清空订阅列表（防止 Close 后仍被重连逻辑“恢复”）
	m.subscribedAssetsMu.Lock()
	m.subscribedAssets = make(map[string]bool)
	m.subscribedAssetsMu.Unlock()

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
