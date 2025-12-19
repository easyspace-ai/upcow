# Polymarket CLOB Client Go SDK

Polymarket CLOB (Central Limit Order Book) 的 Go 语言客户端 SDK。

## 功能特性

- 🔐 **完整的认证支持**: EIP712 和 HMAC 签名
- 📊 **订单管理**: 创建、提交、取消订单
- 📈 **市场数据**: 获取市场信息、订单簿、价格等
- 🔌 **WebSocket 支持**: 实时市场数据和用户订单更新
- ⚡ **高性能**: 基于 Go 的高性能 HTTP 客户端

## 安装

```bash
go get github.com/betbot/gobet/clob
```

## 使用示例

```go
package main

import (
    "context"
    "github.com/betbot/gobet/clob/client"
    "github.com/betbot/gobet/clob/types"
)

func main() {
    // 初始化客户端
    clobClient := client.NewClient(
        "https://clob.polymarket.com",
        types.ChainPolygon,
        privateKey,
        apiKeyCreds,
    )
    
    // 创建订单
    order, err := clobClient.CreateOrder(context.Background(), &types.CreateOrderRequest{
        TokenID: "token-id",
        Side:    types.SideBuy,
        Price:   0.5,
        Size:    1.0,
    })
    
    // 提交订单
    resp, err := clobClient.PostOrder(context.Background(), order, types.OrderTypeGTC)
}
```

## 许可证

ISC

