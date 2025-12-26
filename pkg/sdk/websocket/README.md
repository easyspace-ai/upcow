# WebSocket 客户端

完善的 Polymarket WebSocket 客户端实现，结合了 `sdk/api/websocket.go` 和 `internal/websocket` 的优点。

## 特性

### MarketClient（市场数据客户端）
- ✅ 实时市场数据订阅（订单簿、价格变化、最新成交价等）
- ✅ 自动重连机制（带指数退避）
- ✅ Ping/Pong 心跳保持连接
- ✅ 重连后自动重新订阅
- ✅ 消息通道和错误通道
- ✅ 支持代理连接
- ✅ 批量订阅支持（最多 100 个资产/批）

### UserClient（用户数据客户端）
- ✅ 用户交易和订单数据订阅（需要认证）
- ✅ 完整的消息解析和处理
- ✅ 自动重连机制（带指数退避）
- ✅ Ping/Pong 心跳保持连接
- ✅ 重连后自动重新订阅
- ✅ 消息通道和错误通道
- ✅ 支持代理连接
- ✅ HMAC-SHA256 签名认证

## 快速开始

### MarketClient 示例

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/betbot/gobet/pkg/sdk/websocket"
)

func main() {
    // 创建客户端（带交易事件处理器）
    client := websocket.NewMarketClient(func(event websocket.TradeEvent) {
        log.Printf("交易事件: AssetID=%s, Price=%.4f, Time=%v",
            event.AssetID, event.Price, event.Timestamp)
    })

    // 启动连接
    ctx := context.Background()
    if err := client.Start(ctx); err != nil {
        log.Fatalf("启动失败: %v", err)
    }
    defer client.Stop()

    // 订阅资产
    assetIDs := []string{"asset1", "asset2", "asset3"}
    if err := client.Subscribe(assetIDs...); err != nil {
        log.Fatalf("订阅失败: %v", err)
    }

    // 监听消息
    go func() {
        for msg := range client.Messages() {
            log.Printf("收到消息: %+v", msg)
        }
    }()

    // 监听错误
    go func() {
        for err := range client.Errors() {
            log.Printf("错误: %v", err)
        }
    }()

    // 运行一段时间
    time.Sleep(60 * time.Second)
}
```

### UserClient 示例

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/betbot/gobet/pkg/sdk/api"
    "github.com/betbot/gobet/pkg/sdk/websocket"
)

func main() {
    // API 凭证
    creds := &api.APICreds{
        APIKey:        "your-api-key",
        APISecret:     "your-api-secret",
        APIPassphrase: "your-passphrase",
    }

    // 创建客户端（带交易事件处理器）
    client := websocket.NewUserClient(creds, func(trade api.DataTrade) {
        log.Printf("用户交易: Type=%s, Side=%s, Asset=%s, Price=%.4f, Size=%.4f",
            trade.Type, trade.Side, trade.Asset, trade.Price.Float64(), trade.Size.Float64())
    })

    // 启动连接
    ctx := context.Background()
    if err := client.Start(ctx); err != nil {
        log.Fatalf("启动失败: %v", err)
    }
    defer client.Stop()

    // 订阅市场
    conditionIDs := []string{"condition1", "condition2"}
    if err := client.SubscribeMarkets(conditionIDs...); err != nil {
        log.Fatalf("订阅失败: %v", err)
    }

    // 监听消息
    go func() {
        for msg := range client.Messages() {
            log.Printf("收到消息: %+v", msg)
        }
    }()

    // 监听错误
    go func() {
        for err := range client.Errors() {
            log.Printf("错误: %v", err)
        }
    }()

    // 运行一段时间
    time.Sleep(60 * time.Second)
}
```

### 自定义配置示例

```go
package main

import (
    "time"

    "github.com/betbot/gobet/pkg/sdk/websocket"
)

func main() {
    // 创建自定义配置
    config := websocket.DefaultConfig()
    config.ProxyURL = "http://proxy.example.com:8080"  // 设置代理
    config.ReconnectDelay = 3 * time.Second             // 自定义重连延迟
    config.MaxReconnectAttempts = 20                    // 最大重连次数
    config.MessageBufferSize = 200                      // 消息缓冲区大小

    // 使用自定义配置创建客户端
    client := websocket.NewMarketClientWithConfig(nil, config)
    // ... 使用客户端
}
```

## API 参考

### MarketClient

#### 构造函数
- `NewMarketClient(handler TradeHandler) *MarketClient` - 使用默认配置创建客户端
- `NewMarketClientWithConfig(handler TradeHandler, config *Config) *MarketClient` - 使用自定义配置创建客户端

#### 方法
- `Start(ctx context.Context) error` - 启动连接
- `Stop()` - 停止连接
- `Subscribe(assetIDs ...string) error` - 订阅资产
- `Unsubscribe(assetIDs ...string) error` - 取消订阅资产
- `SubscriptionCount() int` - 获取订阅数量
- `Messages() <-chan interface{}` - 获取消息通道
- `Errors() <-chan error` - 获取错误通道
- `IsRunning() bool` - 检查是否正在运行

### UserClient

#### 构造函数
- `NewUserClient(creds *api.APICreds, handler UserTradeHandler) *UserClient` - 使用默认配置创建客户端
- `NewUserClientWithConfig(creds *api.APICreds, handler UserTradeHandler, config *Config) *UserClient` - 使用自定义配置创建客户端

#### 方法
- `Start(ctx context.Context) error` - 启动连接
- `Stop()` - 停止连接
- `SubscribeMarkets(conditionIDs ...string) error` - 订阅市场
- `UnsubscribeMarkets(conditionIDs ...string) error` - 取消订阅市场
- `SubscriptionCount() int` - 获取订阅数量
- `Messages() <-chan interface{}` - 获取消息通道
- `Errors() <-chan error` - 获取错误通道
- `IsRunning() bool` - 检查是否正在运行

## 事件类型

### 市场频道事件
- `book` - 订单簿更新
- `price_change` - 价格变化
- `last_trade_price` - 最新成交价
- `tick_size_change` - 最小价格单位变化

### 用户频道事件
- `trade` - 交易事件
- `order` - 订单事件

## 改进点

相比之前的实现，本版本包含以下改进：

1. **完整的消息处理** - UserClient 现在完整实现了消息解析和处理
2. **完善的重连机制** - 带指数退避和最大重连次数限制
3. **自动重新订阅** - 重连后自动恢复所有订阅
4. **心跳机制** - 两个客户端都实现了 Ping/Pong 心跳
5. **错误处理** - 完善的错误通道和错误处理
6. **代理支持** - 支持通过代理连接
7. **消息通道** - 使用通道模式，更符合 Go 的并发模型
8. **中文注释** - 所有代码都有详细的中文注释

## 注意事项

1. **UserClient 需要认证** - 必须提供有效的 API 凭证
2. **批量订阅限制** - Polymarket 限制每批最多订阅 100 个资产
3. **连接管理** - 确保在程序退出前调用 `Stop()` 方法
4. **错误处理** - 建议始终监听错误通道并处理错误
5. **消息缓冲区** - 如果消息处理较慢，可能需要增大缓冲区大小

## 与旧版本的对比

| 特性 | sdk/api/websocket.go | internal/websocket | sdk/websocket (本版本) |
|------|---------------------|-------------------|----------------------|
| MarketClient 消息处理 | ✅ | ✅ | ✅ |
| UserClient 消息处理 | ❌ | ✅ | ✅ |
| 自动重连 | ✅ | ✅ | ✅ |
| 重连后重新订阅 | ✅ | ⚠️ | ✅ |
| Ping/Pong 心跳 | ✅ | ✅ | ✅ |
| 消息通道 | ❌ | ✅ | ✅ |
| 错误通道 | ❌ | ✅ | ✅ |
| 代理支持 | ❌ | ✅ | ✅ |
| 中文注释 | ❌ | ❌ | ✅ |

