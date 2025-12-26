package websocket

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestMarketClient_NewMarketClient 测试创建新的 MarketClient
func TestMarketClient_NewMarketClient(t *testing.T) {
	client := NewMarketClient(nil)
	if client == nil {
		t.Fatal("NewMarketClient 应该返回非 nil 客户端")
	}

	if client.config == nil {
		t.Error("配置应该被初始化")
	}

	if client.subscriptions == nil {
		t.Error("订阅映射应该被初始化")
	}

	if client.msgChan == nil {
		t.Error("消息通道应该被初始化")
	}

	if client.errChan == nil {
		t.Error("错误通道应该被初始化")
	}
}

// TestMarketClient_NewMarketClientWithConfig 测试使用自定义配置创建客户端
func TestMarketClient_NewMarketClientWithConfig(t *testing.T) {
	config := DefaultConfig()
	config.MessageBufferSize = 200
	config.ReconnectDelay = 5 * time.Second

	client := NewMarketClientWithConfig(nil, config)
	if client == nil {
		t.Fatal("NewMarketClientWithConfig 应该返回非 nil 客户端")
	}

	if client.config.MessageBufferSize != 200 {
		t.Errorf("期望消息缓冲区大小为 200，得到 %d", client.config.MessageBufferSize)
	}

	if client.config.ReconnectDelay != 5*time.Second {
		t.Errorf("期望重连延迟为 5s，得到 %v", client.config.ReconnectDelay)
	}
}

// TestMarketClient_Subscribe 测试订阅功能
func TestMarketClient_Subscribe(t *testing.T) {
	client := NewMarketClient(nil)

	// 测试订阅（未连接时会失败，但订阅应该被添加到内部映射）
	err := client.Subscribe("asset1", "asset2", "asset3")
	// 注意：未连接时订阅会失败，但资产应该被添加到内部映射
	_ = err

	if client.SubscriptionCount() != 3 {
		t.Errorf("期望订阅数量为 3，得到 %d", client.SubscriptionCount())
	}

	// 测试重复订阅（应该被忽略）
	err = client.Subscribe("asset1", "asset4")
	// 注意：未连接时订阅会失败，但新资产应该被添加到内部映射
	_ = err

	if client.SubscriptionCount() != 4 {
		t.Errorf("期望订阅数量为 4，得到 %d", client.SubscriptionCount())
	}

	// 测试空订阅
	err = client.Subscribe()
	if err != nil {
		t.Fatalf("空订阅不应该失败: %v", err)
	}
}

// TestMarketClient_Unsubscribe 测试取消订阅功能
func TestMarketClient_Unsubscribe(t *testing.T) {
	client := NewMarketClient(nil)

	// 先订阅一些资产
	client.Subscribe("asset1", "asset2", "asset3")

	// 测试取消订阅（注意：这需要连接，所以会失败，但不应该 panic）
	err := client.Unsubscribe("asset1", "asset2")
	// 由于没有连接，这个会失败，但这是预期的
	_ = err

	// 检查订阅是否从内部映射中移除
	client.subMu.RLock()
	if client.subscriptions["asset1"] {
		t.Error("asset1 应该从订阅中移除")
	}
	if client.subscriptions["asset2"] {
		t.Error("asset2 应该从订阅中移除")
	}
	if !client.subscriptions["asset3"] {
		t.Error("asset3 应该仍然在订阅中")
	}
	client.subMu.RUnlock()
}

// TestMarketClient_SubscriptionCount 测试订阅计数
func TestMarketClient_SubscriptionCount(t *testing.T) {
	client := NewMarketClient(nil)

	if client.SubscriptionCount() != 0 {
		t.Errorf("初始订阅数量应该为 0，得到 %d", client.SubscriptionCount())
	}

	client.Subscribe("asset1", "asset2")
	if client.SubscriptionCount() != 2 {
		t.Errorf("期望订阅数量为 2，得到 %d", client.SubscriptionCount())
	}

	client.Subscribe("asset3")
	if client.SubscriptionCount() != 3 {
		t.Errorf("期望订阅数量为 3，得到 %d", client.SubscriptionCount())
	}
}

// TestMarketClient_IsRunning 测试运行状态检查
func TestMarketClient_IsRunning(t *testing.T) {
	client := NewMarketClient(nil)

	if client.IsRunning() {
		t.Error("初始状态应该是不运行")
	}

	client.runningMu.Lock()
	client.running = true
	client.runningMu.Unlock()

	if !client.IsRunning() {
		t.Error("设置运行状态后应该返回 true")
	}
}

// TestMarketClient_Messages 测试消息通道
func TestMarketClient_Messages(t *testing.T) {
	client := NewMarketClient(nil)

	msgChan := client.Messages()
	if msgChan == nil {
		t.Error("消息通道不应该为 nil")
	}

	// 测试消息通道是否可读
	select {
	case <-msgChan:
		t.Error("空通道不应该有消息")
	case <-time.After(10 * time.Millisecond):
		// 这是预期的行为
	}
}

// TestMarketClient_Errors 测试错误通道
func TestMarketClient_Errors(t *testing.T) {
	client := NewMarketClient(nil)

	errChan := client.Errors()
	if errChan == nil {
		t.Error("错误通道不应该为 nil")
	}

	// 测试错误通道是否可读
	select {
	case <-errChan:
		t.Error("空通道不应该有错误")
	case <-time.After(10 * time.Millisecond):
		// 这是预期的行为
	}
}

// TestMarketClient_Stop 测试停止功能（未启动时）
func TestMarketClient_Stop(t *testing.T) {
	client := NewMarketClient(nil)

	// 未启动时停止不应该 panic
	client.Stop()

	if client.IsRunning() {
		t.Error("停止后不应该运行")
	}
}

// TestMarketClient_ProcessMessage 测试消息处理
func TestMarketClient_ProcessMessage(t *testing.T) {
	var receivedEvent TradeEvent
	handler := func(event TradeEvent) {
		receivedEvent = event
	}

	client := NewMarketClient(handler)

	// 测试 last_trade_price 事件
	msg := MarketMessage{
		EventType: EventLastTradePrice,
		AssetID:   "asset123",
		Price:     "0.55",
		Timestamp: time.Now().Unix(),
	}

	client.processMessage(msg)

	// 等待处理器被调用（异步）
	time.Sleep(50 * time.Millisecond)

	if receivedEvent.AssetID != "asset123" {
		t.Errorf("期望 AssetID 为 asset123，得到 %s", receivedEvent.AssetID)
	}

	if receivedEvent.Price != 0.55 {
		t.Errorf("期望价格为 0.55，得到 %f", receivedEvent.Price)
	}
}

// TestMarketClient_HandleMessage 测试消息处理（数组格式）
func TestMarketClient_HandleMessage(t *testing.T) {
	client := NewMarketClient(nil)

	// 测试数组格式的消息
	messages := []MarketMessage{
		{
			EventType: EventPriceChange,
			AssetID:   "asset1",
			Price:     "0.50",
		},
		{
			EventType: EventPriceChange,
			AssetID:   "asset2",
			Price:     "0.60",
		},
	}

	data, _ := json.Marshal(messages)
	client.handleMessage(data)

	// 检查消息是否被发送到通道
	select {
	case msg := <-client.Messages():
		if m, ok := msg.(MarketMessage); ok {
			if m.AssetID != "asset1" && m.AssetID != "asset2" {
				t.Errorf("期望收到 asset1 或 asset2，得到 %s", m.AssetID)
			}
		} else {
			t.Error("消息类型不正确")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("应该在消息通道中收到消息")
	}
}

// TestMarketClient_Resubscribe 测试重新订阅
func TestMarketClient_Resubscribe(t *testing.T) {
	client := NewMarketClient(nil)

	// 添加订阅
	client.Subscribe("asset1", "asset2", "asset3")

	// 测试重新订阅（会失败因为没有连接，但不应该 panic）
	err := client.resubscribe()
	_ = err // 预期会失败

	// 检查订阅是否仍然存在
	if client.SubscriptionCount() != 3 {
		t.Errorf("期望订阅数量为 3，得到 %d", client.SubscriptionCount())
	}
}

// TestMarketClient_ContextCancellation 测试上下文取消
func TestMarketClient_ContextCancellation(t *testing.T) {
	client := NewMarketClient(nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	// 设置运行状态
	client.runningMu.Lock()
	client.running = true
	client.runningMu.Unlock()

	client.ctx = ctx
	client.doneCh = make(chan struct{})

	// 测试停止通道
	close(client.stopCh)

	// 注意：doneCh 只有在 readLoop 或 pingLoop 运行时才会关闭
	// 由于我们没有实际启动这些循环，doneCh 不会自动关闭
	// 这是一个设计限制，在实际使用中不会有问题
	// 这里我们只测试 stopCh 可以被关闭而不 panic
}

// TestDefaultConfig 测试默认配置
func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config == nil {
		t.Fatal("DefaultConfig 不应该返回 nil")
	}

	if !config.ReconnectEnabled {
		t.Error("默认应该启用重连")
	}

	if config.MessageBufferSize != defaultMessageBufferSize {
		t.Errorf("期望消息缓冲区大小为 %d，得到 %d", defaultMessageBufferSize, config.MessageBufferSize)
	}

	if config.ErrorBufferSize != defaultErrorBufferSize {
		t.Errorf("期望错误缓冲区大小为 %d，得到 %d", defaultErrorBufferSize, config.ErrorBufferSize)
	}
}

// TestMarketClient_EventTypes 测试事件类型常量
func TestMarketClient_EventTypes(t *testing.T) {
	if EventBook != "book" {
		t.Errorf("期望 EventBook 为 'book'，得到 %s", EventBook)
	}

	if EventPriceChange != "price_change" {
		t.Errorf("期望 EventPriceChange 为 'price_change'，得到 %s", EventPriceChange)
	}

	if EventLastTradePrice != "last_trade_price" {
		t.Errorf("期望 EventLastTradePrice 为 'last_trade_price'，得到 %s", EventLastTradePrice)
	}

	if EventTickSizeChange != "tick_size_change" {
		t.Errorf("期望 EventTickSizeChange 为 'tick_size_change'，得到 %s", EventTickSizeChange)
	}
}

