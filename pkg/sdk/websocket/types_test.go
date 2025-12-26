package websocket

import (
	"testing"
	"time"
)

// TestDefaultConfig_Complete 测试默认配置的完整性
func TestDefaultConfig_Complete(t *testing.T) {
	config := DefaultConfig()

	if config == nil {
		t.Fatal("DefaultConfig 不应该返回 nil")
	}

	// 测试默认值
	if !config.ReconnectEnabled {
		t.Error("默认应该启用重连")
	}

	if config.ReconnectDelay != defaultReconnectDelay {
		t.Errorf("期望重连延迟为 %v，得到 %v", defaultReconnectDelay, config.ReconnectDelay)
	}

	if config.MaxReconnectDelay != defaultMaxReconnectDelay {
		t.Errorf("期望最大重连延迟为 %v，得到 %v", defaultMaxReconnectDelay, config.MaxReconnectDelay)
	}

	if config.PingInterval != defaultPingInterval {
		t.Errorf("期望 Ping 间隔为 %v，得到 %v", defaultPingInterval, config.PingInterval)
	}

	if config.PongTimeout != defaultPongTimeout {
		t.Errorf("期望 Pong 超时为 %v，得到 %v", defaultPongTimeout, config.PongTimeout)
	}

	if config.ReadTimeout != defaultReadTimeout {
		t.Errorf("期望读取超时为 %v，得到 %v", defaultReadTimeout, config.ReadTimeout)
	}

	if config.MessageBufferSize != defaultMessageBufferSize {
		t.Errorf("期望消息缓冲区大小为 %d，得到 %d", defaultMessageBufferSize, config.MessageBufferSize)
	}

	if config.ErrorBufferSize != defaultErrorBufferSize {
		t.Errorf("期望错误缓冲区大小为 %d，得到 %d", defaultErrorBufferSize, config.ErrorBufferSize)
	}
}

// TestConfig_CustomValues 测试自定义配置值
func TestConfig_CustomValues(t *testing.T) {
	config := DefaultConfig()

	// 修改配置值
	config.ProxyURL = "http://proxy.example.com:8080"
	config.ReconnectDelay = 5 * time.Second
	config.MaxReconnectAttempts = 20
	config.MessageBufferSize = 200

	if config.ProxyURL != "http://proxy.example.com:8080" {
		t.Errorf("期望代理 URL 为 http://proxy.example.com:8080，得到 %s", config.ProxyURL)
	}

	if config.ReconnectDelay != 5*time.Second {
		t.Errorf("期望重连延迟为 5s，得到 %v", config.ReconnectDelay)
	}

	if config.MaxReconnectAttempts != 20 {
		t.Errorf("期望最大重连次数为 20，得到 %d", config.MaxReconnectAttempts)
	}

	if config.MessageBufferSize != 200 {
		t.Errorf("期望消息缓冲区大小为 200，得到 %d", config.MessageBufferSize)
	}
}

// TestEventTypes 测试事件类型常量
func TestEventTypes(t *testing.T) {
	tests := []struct {
		name      string
		eventType EventType
		expected  string
	}{
		{"EventBook", EventBook, "book"},
		{"EventPriceChange", EventPriceChange, "price_change"},
		{"EventLastTradePrice", EventLastTradePrice, "last_trade_price"},
		{"EventTickSizeChange", EventTickSizeChange, "tick_size_change"},
		{"EventTrade", EventTrade, "trade"},
		{"EventOrder", EventOrder, "order"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.eventType) != tt.expected {
				t.Errorf("期望 %s 为 '%s'，得到 '%s'", tt.name, tt.expected, tt.eventType)
			}
		})
	}
}

// TestConstants 测试常量值
func TestConstants(t *testing.T) {
	if maxBatchSize != 100 {
		t.Errorf("期望 maxBatchSize 为 100，得到 %d", maxBatchSize)
	}

	if defaultMessageBufferSize != 100 {
		t.Errorf("期望 defaultMessageBufferSize 为 100，得到 %d", defaultMessageBufferSize)
	}

	if defaultErrorBufferSize != 100 {
		t.Errorf("期望 defaultErrorBufferSize 为 100，得到 %d", defaultErrorBufferSize)
	}

	if defaultMaxRetries != 3 {
		t.Errorf("期望 defaultMaxRetries 为 3，得到 %d", defaultMaxRetries)
	}
}

// TestMarketMessage 测试 MarketMessage 结构
func TestMarketMessage(t *testing.T) {
	msg := MarketMessage{
		EventType: EventPriceChange,
		AssetID:   "asset123",
		Market:    "market123",
		Timestamp: 1234567890,
		Price:     "0.55",
	}

	if msg.EventType != EventPriceChange {
		t.Errorf("期望事件类型为 EventPriceChange，得到 %v", msg.EventType)
	}

	if msg.AssetID != "asset123" {
		t.Errorf("期望资产 ID 为 asset123，得到 %s", msg.AssetID)
	}

	if msg.Price != "0.55" {
		t.Errorf("期望价格为 0.55，得到 %s", msg.Price)
	}
}

// TestBookChange 测试 BookChange 结构
func TestBookChange(t *testing.T) {
	change := BookChange{
		Price: "0.50",
		Size:  "100",
		Side:  "buy",
	}

	if change.Price != "0.50" {
		t.Errorf("期望价格为 0.50，得到 %s", change.Price)
	}

	if change.Size != "100" {
		t.Errorf("期望数量为 100，得到 %s", change.Size)
	}

	if change.Side != "buy" {
		t.Errorf("期望方向为 buy，得到 %s", change.Side)
	}
}

// TestTradeEvent 测试 TradeEvent 结构
func TestTradeEvent(t *testing.T) {
	now := time.Now()
	event := TradeEvent{
		AssetID:   "asset123",
		Price:     0.55,
		Timestamp: now,
	}

	if event.AssetID != "asset123" {
		t.Errorf("期望资产 ID 为 asset123，得到 %s", event.AssetID)
	}

	if event.Price != 0.55 {
		t.Errorf("期望价格为 0.55，得到 %f", event.Price)
	}

	if !event.Timestamp.Equal(now) {
		t.Errorf("期望时间戳为 %v，得到 %v", now, event.Timestamp)
	}
}

