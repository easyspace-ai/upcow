# Polymarket 官方文档学习总结

## RTDS (Real-Time Data Socket) 关键信息

### 连接信息
- **WebSocket URL**: `wss://ws-live-data.polymarket.com`
- **协议**: WebSocket
- **数据格式**: JSON
- **保活机制**: 每 5 秒发送 PING 消息以保持连接

### Chainlink 加密货币价格订阅

#### 订阅格式
- **Topic**: `crypto_prices_chainlink`
- **Type**: `*` (所有类型) - **重要：不是 "update"**
- **Filters**: JSON 格式字符串，例如 `{"symbol":"btc/usd"}`
- **符号格式**: 斜杠分隔（如 `btc/usd`, `eth/usd`）
- **认证**: 不需要

#### 订阅消息示例
```json
{
  "action": "subscribe",
  "subscriptions": [
    {
      "topic": "crypto_prices_chainlink",
      "type": "*",
      "filters": "{\"symbol\":\"btc/usd\"}"
    }
  ]
}
```

#### 消息格式
```json
{
  "topic": "crypto_prices_chainlink",
  "type": "update",
  "timestamp": 1753314064237,
  "payload": {
    "symbol": "btc/usd",
    "timestamp": 1753314064213,
    "value": 67234.50
  }
}
```

### Binance 加密货币价格订阅

#### 订阅格式
- **Topic**: `crypto_prices`
- **Type**: `update`
- **Filters**: 可选，逗号分隔的符号列表（如 `"solusdt,btcusdt,ethusdt"`）
- **符号格式**: 小写连接（如 `btcusdt`, `ethusdt`）
- **认证**: 不需要

### 关键发现

1. **Chainlink 和 Binance 的订阅格式不同**：
   - Chainlink: `type = "*"`, filters 是 JSON 字符串
   - Binance: `type = "update"`, filters 是逗号分隔的字符串

2. **连接管理**：
   - 支持动态订阅（无需断开连接）
   - 连接错误会触发自动重连
   - 无效订阅可能导致连接关闭

3. **消息结构**：
   - 所有消息都包含 `topic`, `type`, `timestamp`, `payload`
   - `timestamp` 是 Unix 毫秒时间戳
   - `payload` 包含事件特定数据

## API 端点

### Gamma API
- **基础 URL**: `https://gamma-api.polymarket.com`
- **速率限制**: 
  - 一般: 750 请求/10秒
  - `/events`: 100 请求/10秒
  - `/markets`: 125 请求/10秒

### 获取市场数据
- **按 Slug**: `GET /markets/slug/{slug}`
- **按 ID**: `GET /markets/{id}`
- **列表**: `GET /markets?tag_id={id}&closed=false&limit={n}&offset={n}`

### 获取事件数据
- **按 Slug**: `GET /events/slug/{slug}`
- **按 ID**: `GET /events/{id}`
- **列表**: `GET /events?order=id&ascending=false&closed=false&limit={n}`

## CLOB (Central Limit Order Book)

### WebSocket 频道
- **Market Channel**: 公共频道，提供市场更新（Level 2 价格数据）
- **User Channel**: 需要认证，提供用户特定数据

### 认证级别
- **L1**: 使用私钥签名（非托管）
- **L2**: 使用 API 凭证（key, secret, passphrase）

## 最佳实践

1. **获取市场数据**：
   - 单个市场：使用 slug 方法
   - 分类浏览：使用 tag 过滤
   - 完整发现：使用 events 端点 + 分页

2. **RTDS 订阅**：
   - 确保正确设置 type（Chainlink 用 `*`，Binance 用 `update`）
   - 定期发送 PING 保持连接
   - 处理连接错误和重连

3. **速率限制**：
   - 实现适当的重试和退避策略
   - 监控请求频率
   - 使用分页处理大量数据

## 已修复的问题

### RTDS Chainlink 订阅类型错误
**问题**: 代码中 Chainlink 订阅使用了 `type: "update"`，但官方文档要求使用 `type: "*"`

**修复**: 在 `clob/rtds/subscription.go` 中，根据 source 类型设置正确的 messageType：
- Chainlink: `type = "*"`
- Binance: `type = "update"`

## 相关文档位置

- RTDS 概述: `developers_RTDS_RTDS-overview.md`
- RTDS 加密货币价格: `developers_RTDS_RTDS-crypto-prices.md`
- RTDS 评论: `developers_RTDS_RTDS-comments.md`
- 获取市场指南: `developers_gamma-markets-api_fetch-markets-guide.md`
- API 速率限制: `quickstart_introduction_rate-limits.md`
- CLOB 认证: `developers_CLOB_authentication.md`
- Market Channel: `developers_CLOB_websocket_market-channel.md`

