// Package websocket 提供 Polymarket WebSocket 客户端实现
// 结合了 sdk/api/websocket.go 和 internal/websocket 的优点，提供完善的实时数据订阅功能
package websocket

import (
	"encoding/json"
	"time"

	"github.com/betbot/gobet/pkg/sdk/api"
)

const (
	// WebSocket 端点
	wsMarketURL = "wss://ws-subscriptions-clob.polymarket.com/ws/market"
	wsUserURL   = "wss://ws-subscriptions-clob.polymarket.com/ws/user"

	// 重连设置
	defaultReconnectDelay    = 2 * time.Second
	defaultMaxReconnectDelay = 30 * time.Second
	defaultPingInterval      = 10 * time.Second  // 根据官方文档，每 10 秒发送一次 PING
	defaultPongTimeout       = 30 * time.Second  // Pong 超时时间（30秒）
	defaultReadTimeout       = 300 * time.Second // 读取超时（5分钟），避免代理导致的超时

	// 订阅批处理大小（Polymarket 限制每批最多 100 个资产）
	maxBatchSize = 100

	// 消息通道缓冲区大小
	defaultMessageBufferSize = 1000 // 增加到 1000，避免消息通道溢出
	defaultErrorBufferSize   = 100

	// 连接重试设置
	defaultMaxRetries = 3
)

// EventType 表示 WebSocket 事件类型
type EventType string

const (
	// 市场频道事件类型
	EventBook           EventType = "book"             // 订单簿更新
	EventPriceChange    EventType = "price_change"     // 价格变化
	EventLastTradePrice EventType = "last_trade_price" // 最新成交价
	EventTickSizeChange EventType = "tick_size_change" // 最小价格单位变化

	// 用户频道事件类型
	EventTrade EventType = "trade" // 交易事件
	EventOrder EventType = "order" // 订单事件
)

// MarketMessage 表示市场频道的 WebSocket 消息
type MarketMessage struct {
	EventType EventType       `json:"event_type"`        // 事件类型
	AssetID   string          `json:"asset_id"`          // 资产 ID
	Market    string          `json:"market"`            // 市场 ID
	Timestamp int64           `json:"timestamp"`         // 时间戳
	Price     string          `json:"price,omitempty"`   // 价格（可选）
	Hash      string          `json:"hash,omitempty"`    // 哈希（可选）
	Changes   json.RawMessage `json:"changes,omitempty"` // 变化详情（可选）
}

// BookChange 表示订单簿变化
type BookChange struct {
	Price string `json:"price"` // 价格
	Size  string `json:"size"`  // 数量
	Side  string `json:"side"`  // 方向："buy" 或 "sell"
}

// TradeEvent 表示检测到的交易事件（来自 last_trade_price 事件）
type TradeEvent struct {
	AssetID   string    // 资产 ID
	Price     float64   // 价格
	Timestamp time.Time // 时间戳
}

// TradeHandler 是交易事件的处理函数
type TradeHandler func(event TradeEvent)

// UserTradeHandler 是用户交易事件的处理函数
type UserTradeHandler func(trade api.DataTrade)

// Config 是 WebSocket 客户端配置
type Config struct {
	// 代理设置
	ProxyURL string // 代理 URL（可选）

	// 重连设置
	ReconnectEnabled     bool          // 是否启用自动重连
	ReconnectDelay       time.Duration // 重连延迟
	MaxReconnectDelay    time.Duration // 最大重连延迟
	MaxReconnectAttempts int           // 最大重连尝试次数

	// 心跳设置
	PingInterval time.Duration // Ping 间隔
	PongTimeout  time.Duration // Pong 超时时间
	ReadTimeout  time.Duration // 读取超时时间

	// 缓冲区设置
	MessageBufferSize int // 消息通道缓冲区大小
	ErrorBufferSize   int // 错误通道缓冲区大小

	// 连接设置
	ReadBufferSize   int           // 读缓冲区大小
	WriteBufferSize  int           // 写缓冲区大小
	HandshakeTimeout time.Duration // 握手超时时间
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		ReconnectEnabled:     true,
		ReconnectDelay:       defaultReconnectDelay,
		MaxReconnectDelay:    defaultMaxReconnectDelay,
		MaxReconnectAttempts: 10,
		PingInterval:         defaultPingInterval,
		PongTimeout:          defaultPongTimeout,
		ReadTimeout:          defaultReadTimeout,
		MessageBufferSize:    defaultMessageBufferSize,
		ErrorBufferSize:      defaultErrorBufferSize,
		ReadBufferSize:       4096,
		WriteBufferSize:      4096,
		HandshakeTimeout:     15 * time.Second,
	}
}
