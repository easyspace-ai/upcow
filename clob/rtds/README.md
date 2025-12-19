# Polymarket RTDS Go Client

Go 语言实现的 Polymarket Real-Time Data Socket (RTDS) WebSocket 客户端。

## 概述

Polymarket RTDS 是一个基于 WebSocket 的实时数据流服务，提供各种 Polymarket 数据流的实时更新。本客户端允许您订阅多个数据流，并在平台事件发生时接收实时更新。

**WebSocket URL**: `wss://ws-live-data.polymarket.com`

## 功能特性

- ✅ 完整的 WebSocket 连接管理
- ✅ 自动重连机制
- ✅ PING/PONG 保活机制
- ✅ 支持多种订阅主题
- ✅ 类型安全的消息处理
- ✅ 支持 CLOB 和 Gamma 认证
- ✅ 动态订阅管理
- ✅ 完整的示例代码

## 安装

```bash
go get github.com/leven/polymarket/polymarket-rtds-client
```

## 快速开始

### 基础连接示例

```go
package main

import (
    "fmt"
    "log"
    polymarketrtds "github.com/leven/polymarket/polymarket-rtds-client"
)

func main() {
    // 创建客户端
    client := polymarketrtds.NewClient()

    // 注册消息处理器
    client.RegisterHandler("*", func(msg *polymarketrtds.Message) error {
        fmt.Printf("收到消息: Topic=%s, Type=%s\n", msg.Topic, msg.Type)
        return nil
    })

    // 连接
    if err := client.Connect(); err != nil {
        log.Fatal(err)
    }
    defer client.Disconnect()

    // 订阅加密货币价格
    client.SubscribeToCryptoPrices("binance", "btcusdt", "ethusdt")
}
```

## 支持的订阅主题

### 1. 加密货币价格

#### Binance 价格流

```go
client.SubscribeToCryptoPrices("binance", "btcusdt", "ethusdt", "solusdt")
```

#### Chainlink 价格流

```go
client.SubscribeToCryptoPrices("chainlink", "btc/usd", "eth/usd")
```

#### 处理价格更新

```go
handler := polymarketrtds.CreateCryptoPriceHandler(func(price *polymarketrtds.CryptoPrice) error {
    fmt.Printf("%s: $%.2f\n", price.Symbol, price.Value)
    return nil
})
client.RegisterHandler("crypto_prices", handler)
```

### 2. 评论和反应

```go
// 订阅所有事件的评论
client.SubscribeToComments(nil, "Event", "comment_created", "reaction_created")

// 订阅特定事件的评论
eventID := 100
client.SubscribeToComments(&eventID, "Event", "comment_created")
```

#### 处理评论事件

```go
commentHandler := polymarketrtds.CreateCommentHandler(func(comment *polymarketrtds.Comment) error {
    fmt.Printf("新评论: %s\n", comment.Body)
    return nil
})

reactionHandler := polymarketrtds.CreateReactionHandler(func(reaction *polymarketrtds.Reaction) error {
    fmt.Printf("新反应: %s\n", reaction.ReactionType)
    return nil
})

client.RegisterHandler("comments", func(msg *polymarketrtds.Message) error {
    switch msg.Type {
    case "comment_created", "comment_removed":
        return commentHandler(msg)
    case "reaction_created", "reaction_removed":
        return reactionHandler(msg)
    }
    return nil
})
```

### 3. 交易活动

```go
// 订阅所有交易
client.SubscribeToActivity("", "", "trades", "orders_matched")

// 订阅特定事件的交易
client.SubscribeToActivity("event-slug", "", "trades")

// 订阅特定市场的交易
client.SubscribeToActivity("", "market-slug", "trades")
```

#### 处理交易事件

```go
tradeHandler := polymarketrtds.CreateTradeHandler(func(trade *polymarketrtds.Trade) error {
    fmt.Printf("交易: %s @ %s (Size: %s)\n", trade.Side, trade.Price, trade.Size)
    return nil
})
client.RegisterHandler("activity", func(msg *polymarketrtds.Message) error {
    if msg.Type == "trades" || msg.Type == "orders_matched" {
        return tradeHandler(msg)
    }
    return nil
})
```

### 4. CLOB 市场数据

```go
// 订阅订单簿、最后成交价和价格变化
marketIDs := []string{"0x1234...", "0x5678..."}
client.SubscribeToClobMarket(marketIDs, "agg_orderbook", "last_trade_price", "price_change")
```

#### 处理订单簿更新

```go
orderbookHandler := polymarketrtds.CreateAggOrderbookHandler(func(orderbook *polymarketrtds.AggOrderbook) error {
    fmt.Printf("订单簿更新 - 市场: %s\n", orderbook.Market)
    fmt.Printf("买盘前5档:\n")
    for i, bid := range orderbook.Bids {
        if i >= 5 { break }
        fmt.Printf("  %s @ %s\n", bid.Size, bid.Price)
    }
    return nil
})
client.RegisterHandler("clob_market", func(msg *polymarketrtds.Message) error {
    if msg.Type == "agg_orderbook" {
        return orderbookHandler(msg)
    }
    return nil
})
```

### 5. CLOB 用户数据（需要认证）

```go
auth := &polymarketrtds.ClobAuth{
    Key:       "your-api-key",
    Secret:    "your-api-secret",
    Passphrase: "your-passphrase",
}

client.SubscribeToClobUser(auth, "order", "trade")
```

#### 处理用户订单和交易

```go
orderHandler := polymarketrtds.CreateOrderHandler(func(order *polymarketrtds.Order) error {
    fmt.Printf("订单更新: %s - %s\n", order.ID, order.Status)
    return nil
})
client.RegisterHandler("clob_user", orderHandler)
```

### 6. RFQ (Request for Quote)

```go
client.SubscribeToRFQ("request_created", "quote_created")
```

## 高级配置

### 自定义客户端配置

```go
config := &polymarketrtds.ClientConfig{
    URL:            polymarketrtds.RTDSWebSocketURL,
    PingInterval:   5 * time.Second,
    WriteTimeout:   10 * time.Second,
    ReadTimeout:    60 * time.Second,
    Reconnect:      true,
    ReconnectDelay: 5 * time.Second,
    MaxReconnect:   10,
}

client := polymarketrtds.NewClientWithConfig(config)
```

### 手动订阅管理

```go
subscriptions := []polymarketrtds.Subscription{
    {
        Topic: "crypto_prices",
        Type:  "update",
        Filters: "btcusdt,ethusdt",
    },
    {
        Topic: "comments",
        Type:  "comment_created",
        Filters: `{"parentEntityID":100,"parentEntityType":"Event"}`,
    },
}

client.Subscribe(subscriptions)
```

### 取消订阅

```go
client.Unsubscribe(subscriptions)
```

## 示例代码

本仓库包含多个完整的示例：

1. **基础连接** (`examples/basic-connection/`) - 简单的连接和订阅示例
2. **加密货币价格监控** (`examples/crypto-prices/`) - 订阅 Binance 和 Chainlink 价格流
3. **评论监控** (`examples/comments-monitor/`) - 监控特定事件或市场的评论
4. **交易活动监控** (`examples/activity-monitor/`) - 监控市场交易活动
5. **CLOB 市场数据** (`examples/clob-market-data/`) - 订阅订单簿和价格变化

运行示例：

```bash
cd examples/crypto-prices
go run main.go
```

## API 参考

### 客户端方法

#### `NewClient() *Client`
创建使用默认配置的新客户端。

#### `NewClientWithConfig(config *ClientConfig) *Client`
使用自定义配置创建新客户端。

#### `Connect() error`
建立到 RTDS 服务器的 WebSocket 连接。

#### `Disconnect() error`
关闭 WebSocket 连接。

#### `IsConnected() bool`
返回客户端是否已连接。

#### `RegisterHandler(topic string, handler MessageHandler)`
为特定主题注册消息处理器。使用 `"*"` 作为通配符处理所有消息。

#### `UnregisterHandler(topic string)`
移除特定主题的消息处理器。

#### `SendMessage(message interface{}) error`
向 WebSocket 服务器发送 JSON 消息。

### 订阅方法

#### `Subscribe(subscriptions []Subscription) error`
订阅一个或多个主题。

#### `Unsubscribe(subscriptions []Subscription) error`
取消订阅一个或多个主题。

#### `SubscribeToCryptoPrices(source string, symbols ...string) error`
订阅加密货币价格更新。`source` 可以是 `"binance"` 或 `"chainlink"`。

#### `SubscribeToComments(eventID *int, entityType string, commentTypes ...string) error`
订阅评论事件。

#### `SubscribeToActivity(eventSlug, marketSlug string, activityTypes ...string) error`
订阅交易活动事件。

#### `SubscribeToClobMarket(marketIDs []string, dataTypes ...string) error`
订阅 CLOB 市场数据。

#### `SubscribeToClobUser(auth *ClobAuth, dataTypes ...string) error`
订阅 CLOB 用户数据（需要认证）。

#### `SubscribeToRFQ(rfqTypes ...string) error`
订阅 RFQ 事件。

### 消息处理函数

所有处理函数都返回 `MessageHandler` 类型，可以直接注册到客户端：

- `CreateCryptoPriceHandler(callback func(*CryptoPrice) error) MessageHandler`
- `CreateCommentHandler(callback func(*Comment) error) MessageHandler`
- `CreateReactionHandler(callback func(*Reaction) error) MessageHandler`
- `CreateTradeHandler(callback func(*Trade) error) MessageHandler`
- `CreateOrderHandler(callback func(*Order) error) MessageHandler`
- `CreateAggOrderbookHandler(callback func(*AggOrderbook) error) MessageHandler`
- `CreateLastTradePriceHandler(callback func(*LastTradePrice) error) MessageHandler`
- `CreatePriceChangesHandler(callback func(*PriceChanges) error) MessageHandler`
- `CreateRFQRequestHandler(callback func(*RFQRequest) error) MessageHandler`
- `CreateRFQQuoteHandler(callback func(*RFQQuote) error) MessageHandler`

## 数据结构

### Message
```go
type Message struct {
    Topic     string          `json:"topic"`
    Type      string          `json:"type"`
    Timestamp int64           `json:"timestamp"`
    Payload   json.RawMessage `json:"payload"`
}
```

### CryptoPrice
```go
type CryptoPrice struct {
    Symbol    string  `json:"symbol"`
    Timestamp int64   `json:"timestamp"`
    Value     float64 `json:"value"`
}
```

### Comment
```go
type Comment struct {
    Body            string    `json:"body"`
    CreatedAt       time.Time `json:"createdAt"`
    ID              string    `json:"id"`
    ParentEntityID  int       `json:"parentEntityID"`
    ParentEntityType string   `json:"parentEntityType"`
    Profile         Profile   `json:"profile"`
    // ... 更多字段
}
```

更多数据结构请参考 `types.go` 文件。

## 认证

### CLOB 认证
用于交易相关的订阅（如 `clob_user`）：

```go
auth := &polymarketrtds.ClobAuth{
    Key:       "your-api-key",
    Secret:    "your-api-secret",
    Passphrase: "your-passphrase",
}
```

### Gamma 认证
用于用户特定数据：

```go
auth := &polymarketrtds.GammaAuth{
    Address: "0x...",
}
```

## 错误处理

客户端会自动处理连接错误并尝试重连（如果启用了重连）。您可以在消息处理器中返回错误，错误会被记录但不会中断连接。

```go
handler := func(msg *polymarketrtds.Message) error {
    // 处理消息
    if someCondition {
        return fmt.Errorf("处理失败")
    }
    return nil
}
```

## 最佳实践

1. **连接管理**: 始终使用 `defer client.Disconnect()` 确保连接正确关闭
2. **错误处理**: 在消息处理器中检查并处理错误
3. **资源清理**: 在程序退出前取消所有订阅并断开连接
4. **重连机制**: 对于生产环境，建议启用自动重连
5. **消息过滤**: 使用过滤器减少不必要的数据传输

## 依赖

- `github.com/gorilla/websocket` - WebSocket 实现

## 参考资源

- [RTDS 官方文档](https://docs.polymarket.com/developers/RTDS/RTDS-overview)
- [RTDS TypeScript 客户端](https://github.com/polymarket/real-time-data-client)
- [Gamma API 文档](https://docs.polymarket.com/#gamma-markets-api)
- [CLOB API 文档](https://docs.polymarket.com/#clob-api)

## 许可证

MIT License

## 贡献

欢迎提交 Issue 和 Pull Request！


