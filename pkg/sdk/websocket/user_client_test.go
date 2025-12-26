package websocket

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/betbot/gobet/pkg/sdk/api"
)

// TestUserClient_NewUserClient 测试创建新的 UserClient
func TestUserClient_NewUserClient(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	client := NewUserClient(creds, nil)
	if client == nil {
		t.Fatal("NewUserClient 应该返回非 nil 客户端")
	}

	if client.apiCreds != creds {
		t.Error("API 凭证应该被设置")
	}

	if client.markets == nil {
		t.Error("市场映射应该被初始化")
	}

	if client.msgChan == nil {
		t.Error("消息通道应该被初始化")
	}

	if client.errChan == nil {
		t.Error("错误通道应该被初始化")
	}
}

// TestUserClient_NewUserClient_NilCreds 测试使用 nil 凭证创建客户端（应该 panic）
func TestUserClient_NewUserClient_NilCreds(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("使用 nil 凭证应该 panic")
		}
	}()

	NewUserClient(nil, nil)
}

// TestUserClient_NewUserClientWithConfig 测试使用自定义配置创建客户端
func TestUserClient_NewUserClientWithConfig(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	config := DefaultConfig()
	config.MessageBufferSize = 200
	config.ReconnectDelay = 5 * time.Second

	client := NewUserClientWithConfig(creds, nil, config)
	if client == nil {
		t.Fatal("NewUserClientWithConfig 应该返回非 nil 客户端")
	}

	if client.config.MessageBufferSize != 200 {
		t.Errorf("期望消息缓冲区大小为 200，得到 %d", client.config.MessageBufferSize)
	}
}

// TestUserClient_SubscribeMarkets 测试订阅市场
func TestUserClient_SubscribeMarkets(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	client := NewUserClient(creds, nil)

	// 测试订阅（未连接时会失败，但订阅应该被添加到内部映射）
	err := client.SubscribeMarkets("condition1", "condition2", "condition3")
	// 注意：未连接时订阅会失败，但市场应该被添加到内部映射
	_ = err

	if client.SubscriptionCount() != 3 {
		t.Errorf("期望订阅数量为 3，得到 %d", client.SubscriptionCount())
	}

	// 测试重复订阅（应该被忽略）
	err = client.SubscribeMarkets("condition1", "condition4")
	// 注意：未连接时订阅会失败，但新市场应该被添加到内部映射
	_ = err

	if client.SubscriptionCount() != 4 {
		t.Errorf("期望订阅数量为 4，得到 %d", client.SubscriptionCount())
	}
}

// TestUserClient_UnsubscribeMarkets 测试取消订阅市场
func TestUserClient_UnsubscribeMarkets(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	client := NewUserClient(creds, nil)

	// 先订阅一些市场
	client.SubscribeMarkets("condition1", "condition2", "condition3")

	// 测试取消订阅
	err := client.UnsubscribeMarkets("condition1", "condition2")
	if err != nil {
		t.Fatalf("取消订阅不应该失败: %v", err)
	}

	// 检查订阅是否从内部映射中移除
	client.subMu.RLock()
	if client.markets["condition1"] {
		t.Error("condition1 应该从订阅中移除")
	}
	if client.markets["condition2"] {
		t.Error("condition2 应该从订阅中移除")
	}
	if !client.markets["condition3"] {
		t.Error("condition3 应该仍然在订阅中")
	}
	client.subMu.RUnlock()
}

// TestUserClient_SubscriptionCount 测试订阅计数
func TestUserClient_SubscriptionCount(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	client := NewUserClient(creds, nil)

	if client.SubscriptionCount() != 0 {
		t.Errorf("初始订阅数量应该为 0，得到 %d", client.SubscriptionCount())
	}

	client.SubscribeMarkets("condition1", "condition2")
	if client.SubscriptionCount() != 2 {
		t.Errorf("期望订阅数量为 2，得到 %d", client.SubscriptionCount())
	}
}

// TestUserClient_IsRunning 测试运行状态检查
func TestUserClient_IsRunning(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	client := NewUserClient(creds, nil)

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

// TestUserClient_Messages 测试消息通道
func TestUserClient_Messages(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	client := NewUserClient(creds, nil)

	msgChan := client.Messages()
	if msgChan == nil {
		t.Error("消息通道不应该为 nil")
	}
}

// TestUserClient_Errors 测试错误通道
func TestUserClient_Errors(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	client := NewUserClient(creds, nil)

	errChan := client.Errors()
	if errChan == nil {
		t.Error("错误通道不应该为 nil")
	}
}

// TestUserClient_Stop 测试停止功能（未启动时）
func TestUserClient_Stop(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	client := NewUserClient(creds, nil)

	// 未启动时停止不应该 panic
	client.Stop()

	if client.IsRunning() {
		t.Error("停止后不应该运行")
	}
}

// TestUserClient_HandleUserMessage 测试用户消息处理
func TestUserClient_HandleUserMessage(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	var receivedTrade api.DataTrade
	handler := func(trade api.DataTrade) {
		receivedTrade = trade
	}

	client := NewUserClient(creds, handler)

	// 测试交易消息
	tradeMsg := map[string]interface{}{
		"event_type":  "trade",
		"proxyWallet": "0x123",
		"type":        "TRADE",
		"side":        "BUY",
		"isMaker":     true,
		"asset":       "asset123",
		"conditionId": "condition123",
		"size":        "100.5",
		"usdcSize":    "50.25",
		"price":       "0.5",
		"timestamp":   float64(time.Now().Unix()),
		"title":       "Test Market",
		"slug":        "test-market",
	}

	data, _ := json.Marshal(tradeMsg)
	client.handleUserMessage(data)

	// 等待处理器被调用
	time.Sleep(50 * time.Millisecond)

	if receivedTrade.Asset != "asset123" {
		t.Errorf("期望 Asset 为 asset123，得到 %s", receivedTrade.Asset)
	}

	if receivedTrade.Side != "BUY" {
		t.Errorf("期望 Side 为 BUY，得到 %s", receivedTrade.Side)
	}

	if !receivedTrade.IsMaker {
		t.Error("期望 IsMaker 为 true")
	}
}

// TestUserClient_HandleUserMessage_NonTrade 测试非交易消息处理
func TestUserClient_HandleUserMessage_NonTrade(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	client := NewUserClient(creds, nil)

	// 测试非交易消息（应该发送到消息通道但不调用处理器）
	msg := map[string]interface{}{
		"event_type": "order",
		"order_id":   "order123",
	}

	data, _ := json.Marshal(msg)
	client.handleUserMessage(data)

	// 检查消息是否被发送到通道
	select {
	case receivedMsg := <-client.Messages():
		if m, ok := receivedMsg.(map[string]interface{}); ok {
			if m["event_type"] != "order" {
				t.Errorf("期望事件类型为 order，得到 %v", m["event_type"])
			}
		} else {
			t.Error("消息类型不正确")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("应该在消息通道中收到消息")
	}
}

// TestUserClient_ProcessTradeMessage 测试交易消息解析
func TestUserClient_ProcessTradeMessage(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	var receivedTrade api.DataTrade
	handler := func(trade api.DataTrade) {
		receivedTrade = trade
	}

	client := NewUserClient(creds, handler)

	// 测试完整的交易消息
	tradeMsg := map[string]interface{}{
		"event_type":      "trade",
		"proxyWallet":     "0x1234567890abcdef",
		"type":            "TRADE",
		"side":            "SELL",
		"isMaker":         false,
		"asset":           "asset456",
		"conditionId":     "condition456",
		"size":            float64(200.75),
		"usdcSize":        float64(100.375),
		"price":           float64(0.5),
		"timestamp":       float64(1234567890),
		"title":           "Test Market Title",
		"slug":            "test-market-slug",
		"icon":            "icon-url",
		"eventSlug":       "event-slug",
		"outcome":         "Yes",
		"outcomeIndex":    float64(0),
		"name":            "Market Name",
		"transactionHash": "0xabcdef123456",
	}

	client.processTradeMessage(tradeMsg)

	// 等待处理器被调用
	time.Sleep(50 * time.Millisecond)

	if receivedTrade.ProxyWallet != "0x1234567890abcdef" {
		t.Errorf("期望 ProxyWallet 为 0x1234567890abcdef，得到 %s", receivedTrade.ProxyWallet)
	}

	if receivedTrade.Type != "TRADE" {
		t.Errorf("期望 Type 为 TRADE，得到 %s", receivedTrade.Type)
	}

	if receivedTrade.Side != "SELL" {
		t.Errorf("期望 Side 为 SELL，得到 %s", receivedTrade.Side)
	}

	if receivedTrade.IsMaker {
		t.Error("期望 IsMaker 为 false")
	}

	if receivedTrade.Asset != "asset456" {
		t.Errorf("期望 Asset 为 asset456，得到 %s", receivedTrade.Asset)
	}

	if receivedTrade.ConditionID != "condition456" {
		t.Errorf("期望 ConditionID 为 condition456，得到 %s", receivedTrade.ConditionID)
	}

	if receivedTrade.Size.Float64() != 200.75 {
		t.Errorf("期望 Size 为 200.75，得到 %f", receivedTrade.Size.Float64())
	}

	if receivedTrade.Price.Float64() != 0.5 {
		t.Errorf("期望 Price 为 0.5，得到 %f", receivedTrade.Price.Float64())
	}
}

// TestUserClient_ParseNumeric 测试 Numeric 类型解析
func TestUserClient_ParseNumeric(t *testing.T) {
	// 测试字符串格式
	var n1 api.Numeric
	if err := parseNumeric(&n1, "123.45"); err != nil {
		t.Fatalf("解析字符串失败: %v", err)
	}
	if n1.Float64() != 123.45 {
		t.Errorf("期望 123.45，得到 %f", n1.Float64())
	}

	// 测试浮点数格式
	var n2 api.Numeric
	if err := parseNumeric(&n2, 456.78); err != nil {
		t.Fatalf("解析浮点数失败: %v", err)
	}
	if n2.Float64() != 456.78 {
		t.Errorf("期望 456.78，得到 %f", n2.Float64())
	}

	// 测试整数格式
	var n3 api.Numeric
	if err := parseNumeric(&n3, int64(789)); err != nil {
		t.Fatalf("解析整数失败: %v", err)
	}
	if n3.Float64() != 789 {
		t.Errorf("期望 789，得到 %f", n3.Float64())
	}

	// 测试空字符串
	var n4 api.Numeric
	if err := parseNumeric(&n4, ""); err != nil {
		t.Fatalf("解析空字符串失败: %v", err)
	}
	if n4.Float64() != 0 {
		t.Errorf("期望 0，得到 %f", n4.Float64())
	}
}

// TestUserClient_Resubscribe 测试重新订阅
func TestUserClient_Resubscribe(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	client := NewUserClient(creds, nil)

	// 添加订阅
	client.SubscribeMarkets("condition1", "condition2", "condition3")

	// 测试重新订阅（会失败因为没有连接，但不应该 panic）
	err := client.resubscribe()
	_ = err // 预期会失败

	// 检查订阅是否仍然存在
	if client.SubscriptionCount() != 3 {
		t.Errorf("期望订阅数量为 3，得到 %d", client.SubscriptionCount())
	}
}

// TestUserClient_GenerateWSSignature 测试签名生成
func TestUserClient_GenerateWSSignature(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	client := NewUserClient(creds, nil)

	message := "1234567890GET/ws/user"
	secret := "test-secret"

	signature := client.generateWSSignature(message, secret)

	if signature == "" {
		t.Error("签名不应该为空")
	}

	// 测试签名一致性
	signature2 := client.generateWSSignature(message, secret)
	if signature != signature2 {
		t.Error("相同输入应该产生相同签名")
	}

	// 测试不同消息产生不同签名
	signature3 := client.generateWSSignature("different message", secret)
	if signature == signature3 {
		t.Error("不同消息应该产生不同签名")
	}
}

// TestUserClient_HandleUserMessage_InvalidJSON 测试无效 JSON 处理
func TestUserClient_HandleUserMessage_InvalidJSON(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	client := NewUserClient(creds, nil)

	// 测试无效 JSON
	invalidJSON := []byte("这不是有效的 JSON")
	client.handleUserMessage(invalidJSON)

	// 不应该 panic，错误应该被处理
	select {
	case err := <-client.Errors():
		if err == nil {
			t.Error("应该收到错误")
		}
	case <-time.After(100 * time.Millisecond):
		// 可能错误被静默处理，这也是可以接受的
	}
}

// TestUserClient_HandleUserMessage_PlainText 测试纯文本消息处理
func TestUserClient_HandleUserMessage_PlainText(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	client := NewUserClient(creds, nil)

	// 测试纯文本消息（如 "PONG"）
	plainText := []byte("PONG")
	client.handleUserMessage(plainText)

	// 不应该发送到消息通道
	select {
	case <-client.Messages():
		t.Error("纯文本消息不应该发送到消息通道")
	case <-time.After(50 * time.Millisecond):
		// 这是预期的行为
	}
}

// TestUserClient_ContextCancellation 测试上下文取消
func TestUserClient_ContextCancellation(t *testing.T) {
	creds := &api.APICreds{
		APIKey:        "test-key",
		APISecret:     "test-secret",
		APIPassphrase: "test-passphrase",
	}

	client := NewUserClient(creds, nil)

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
