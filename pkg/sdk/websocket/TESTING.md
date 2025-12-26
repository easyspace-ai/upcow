# WebSocket 测试文档

## 测试覆盖

本包包含完整的单元测试，覆盖了以下功能：

### MarketClient 测试 (`market_client_test.go`)

1. **客户端创建**
   - `TestMarketClient_NewMarketClient` - 测试默认配置创建
   - `TestMarketClient_NewMarketClientWithConfig` - 测试自定义配置创建

2. **订阅管理**
   - `TestMarketClient_Subscribe` - 测试订阅功能
   - `TestMarketClient_Unsubscribe` - 测试取消订阅
   - `TestMarketClient_SubscriptionCount` - 测试订阅计数

3. **状态管理**
   - `TestMarketClient_IsRunning` - 测试运行状态检查
   - `TestMarketClient_Stop` - 测试停止功能

4. **消息处理**
   - `TestMarketClient_Messages` - 测试消息通道
   - `TestMarketClient_Errors` - 测试错误通道
   - `TestMarketClient_ProcessMessage` - 测试消息处理
   - `TestMarketClient_HandleMessage` - 测试消息处理（数组格式）

5. **其他功能**
   - `TestMarketClient_Resubscribe` - 测试重新订阅
   - `TestMarketClient_ContextCancellation` - 测试上下文取消
   - `TestMarketClient_EventTypes` - 测试事件类型常量

### UserClient 测试 (`user_client_test.go`)

1. **客户端创建**
   - `TestUserClient_NewUserClient` - 测试默认配置创建
   - `TestUserClient_NewUserClient_NilCreds` - 测试 nil 凭证（应该 panic）
   - `TestUserClient_NewUserClientWithConfig` - 测试自定义配置创建

2. **订阅管理**
   - `TestUserClient_SubscribeMarkets` - 测试订阅市场
   - `TestUserClient_UnsubscribeMarkets` - 测试取消订阅市场
   - `TestUserClient_SubscriptionCount` - 测试订阅计数

3. **状态管理**
   - `TestUserClient_IsRunning` - 测试运行状态检查
   - `TestUserClient_Stop` - 测试停止功能

4. **消息处理**
   - `TestUserClient_Messages` - 测试消息通道
   - `TestUserClient_Errors` - 测试错误通道
   - `TestUserClient_HandleUserMessage` - 测试用户消息处理
   - `TestUserClient_HandleUserMessage_NonTrade` - 测试非交易消息处理
   - `TestUserClient_HandleUserMessage_InvalidJSON` - 测试无效 JSON 处理
   - `TestUserClient_HandleUserMessage_PlainText` - 测试纯文本消息处理
   - `TestUserClient_ProcessTradeMessage` - 测试交易消息解析

5. **工具函数**
   - `TestUserClient_ParseNumeric` - 测试 Numeric 类型解析
   - `TestUserClient_GenerateWSSignature` - 测试签名生成
   - `TestUserClient_Resubscribe` - 测试重新订阅
   - `TestUserClient_ContextCancellation` - 测试上下文取消

### 类型和配置测试 (`types_test.go`)

1. **配置测试**
   - `TestDefaultConfig_Complete` - 测试默认配置完整性
   - `TestConfig_CustomValues` - 测试自定义配置值

2. **类型测试**
   - `TestEventTypes` - 测试事件类型常量
   - `TestConstants` - 测试常量值
   - `TestMarketMessage` - 测试 MarketMessage 结构
   - `TestBookChange` - 测试 BookChange 结构
   - `TestTradeEvent` - 测试 TradeEvent 结构

## 运行测试

### 运行所有测试
```bash
go test ./sdk/websocket/... -v
```

### 运行特定测试
```bash
# 只运行 MarketClient 测试
go test ./sdk/websocket/... -v -run TestMarketClient

# 只运行 UserClient 测试
go test ./sdk/websocket/... -v -run TestUserClient

# 运行特定测试函数
go test ./sdk/websocket/... -v -run TestMarketClient_Subscribe
```

### 运行测试并查看覆盖率
```bash
go test ./sdk/websocket/... -cover
```

### 生成覆盖率报告
```bash
go test ./sdk/websocket/... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

## 测试策略

### 单元测试
- 测试不依赖外部服务（如实际的 WebSocket 服务器）
- 测试客户端状态管理和订阅管理
- 测试消息解析和处理逻辑
- 测试错误处理

### Mock 测试
- 使用模拟数据测试消息处理
- 测试各种消息格式（JSON、数组、纯文本等）
- 测试边界情况（空消息、无效 JSON 等）

### 集成测试（未来）
- 可以添加集成测试，连接到测试 WebSocket 服务器
- 使用环境变量控制是否运行集成测试
- 例如：`INTEGRATION_TEST=true go test ./sdk/websocket/...`

## 测试注意事项

1. **未连接状态**：某些测试在客户端未连接时运行，这是预期的行为
2. **异步处理**：消息处理是异步的，测试中使用 `time.Sleep` 等待处理完成
3. **上下文取消**：doneCh 只有在实际运行 readLoop/pingLoop 时才会关闭
4. **签名测试**：签名生成测试验证一致性和唯一性

## 测试覆盖率目标

- 代码覆盖率：> 80%
- 关键路径覆盖率：100%
- 错误处理覆盖率：> 90%

## 添加新测试

添加新测试时，请遵循以下规范：

1. 测试函数名以 `Test` 开头
2. 使用表驱动测试（table-driven tests）处理多个测试用例
3. 使用 `t.Run` 创建子测试
4. 测试应该独立，不依赖其他测试的执行顺序
5. 清理资源（如关闭通道、取消上下文等）

## 示例：添加新测试

```go
func TestMarketClient_NewFeature(t *testing.T) {
    t.Run("success case", func(t *testing.T) {
        client := NewMarketClient(nil)
        // 测试代码
    })

    t.Run("error case", func(t *testing.T) {
        client := NewMarketClient(nil)
        // 测试错误情况
    })
}
```

