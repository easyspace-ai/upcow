# RTDS 监控工具

独立的 RTDS (Real-Time Data Stream) 监控应用，用于实时监控 Polymarket 的数据流。

## 功能特性

- ✅ 监控加密货币价格（Binance 和 Chainlink）
- ✅ 监控评论数据
- ✅ 监控交易数据
- ✅ 监控订单簿数据
- ✅ 支持代理配置
- ✅ 自动重连
- ✅ 显示原始 JSON 消息

## 使用方法

### 基本用法

```bash
# 监控 Chainlink BTC 价格（带详细日志）
go run cmd/rtds-monitor/main.go -proxy=http://127.0.0.1:15236 -crypto-source=chainlink -crypto-symbols=btc/usd -verbose

# 监控 Binance 多个加密货币价格
go run cmd/rtds-monitor/main.go -proxy=http://127.0.0.1:15236 -crypto-source=binance -crypto-symbols=btcusdt,ethusdt,solusdt -verbose

# 监控评论数据
go run cmd/rtds-monitor/main.go -proxy=http://127.0.0.1:15236 -comments -verbose

# 显示原始 JSON 消息（用于调试）
go run cmd/rtds-monitor/main.go -proxy=http://127.0.0.1:15236 -crypto-source=chainlink -crypto-symbols=btc/usd -raw -verbose

# 使用运行脚本（推荐）
./cmd/rtds-monitor/run.sh
```

### 使用运行脚本

```bash
# 使用默认配置
./cmd/rtds-monitor/run.sh

# 自定义参数
PROXY=http://127.0.0.1:15236 CRYPTO_SOURCE=chainlink CRYPTO_SYMBOLS=btc/usd,eth/usd ./cmd/rtds-monitor/run.sh

# 保存日志到文件
LOG_FILE=logs/rtds-monitor.log ./cmd/rtds-monitor/run.sh
```

### 编译运行

```bash
# 编译
go build -o bin/rtds-monitor cmd/rtds-monitor/main.go

# 运行
./bin/rtds-monitor -crypto-source=chainlink -crypto-symbols=btc/usd
```

## 命令行参数

- `-proxy`: 代理 URL（例如: `http://127.0.0.1:15236`）
- `-crypto-source`: 加密货币价格源 (`binance` 或 `chainlink`)
- `-crypto-symbols`: 加密货币符号，逗号分隔（例如: `btc/usd,eth/usd`）
- `-comments`: 订阅评论数据
- `-trades`: 订阅交易数据（需要市场 slug）
- `-orderbook`: 订阅订单簿数据（需要市场 slug）
- `-verbose`: 显示详细日志
- `-raw`: 显示原始 JSON 消息

## 代理配置优先级

1. 命令行参数 `-proxy`
2. 全局配置文件中的代理设置
3. 环境变量 `HTTP_PROXY` 或 `HTTPS_PROXY`

## 示例输出

```
INFO[25-12-21 19:06:40] 🚀 RTDS 监控工具启动
INFO[25-12-21 19:06:40] ✅ RTDS 连接成功
INFO[25-12-21 19:06:40] ✅ 加密货币价格订阅成功
[19:06:42] 💰 CHAINLINK btc/usd: $88567.63 (时间: 19:06:41)
[19:06:43] 💰 CHAINLINK btc/usd: $88567.50 (时间: 19:06:42)
```

### 调试模式输出

使用 `-verbose` 参数可以看到详细的连接和消息处理日志：

```
DEBU[25-12-21 19:06:40] [RTDS] Connecting to RTDS via proxy: http://127.0.0.1:15236
DEBU[25-12-21 19:06:40] [RTDS] Sending RTDS message: {"action":"subscribe",...}
DEBU[25-12-21 19:06:42] [RTDS] Received RTDS message: topic=crypto_prices_chainlink, type=update
DEBU[25-12-21 19:06:42] [RTDS] Calling handler for crypto_prices_chainlink, payload_preview="..."
DEBU[25-12-21 19:06:42] [RTDS] Successfully handled crypto_prices_chainlink message
```

### 原始消息模式

使用 `-raw` 参数可以看到完整的 JSON 消息：

```json
[19:06:42] 原始消息:
{
  "topic": "crypto_prices_chainlink",
  "type": "update",
  "timestamp": 1766315201000,
  "payload": {
    "symbol": "btc/usd",
    "timestamp": 1766315201000,
    "value": 88567.627009,
    "full_accuracy_value": "88567627009000000000000"
  }
}
```

## 调试技巧

### 查看连接状态

使用 `-verbose` 参数可以看到：
- RTDS 连接过程
- 订阅消息的发送和确认
- 收到的消息类型和内容预览
- 重连过程

### 常见问题

1. **连接失败**
   - 检查代理是否正常运行
   - 检查网络连接
   - 查看详细日志：`-verbose`

2. **收不到价格更新**
   - 确认订阅成功（查看日志中的 "✅ 加密货币价格订阅成功"）
   - 检查 symbol 格式是否正确（chainlink 使用 `btc/usd`，binance 使用 `btcusdt`）
   - 使用 `-raw` 查看原始消息

3. **连接频繁断开**
   - RTDS 连接可能不稳定，工具会自动重连
   - 查看重连日志了解重连过程
   - 检查代理连接是否稳定

## 注意事项

- 交易和订单簿订阅需要指定市场 slug，当前版本暂未实现
- 使用 `-raw` 参数可以查看完整的原始 JSON 消息，便于调试
- 使用 `-verbose` 参数可以查看详细的连接状态和重连信息
- 建议使用代理连接，直接连接可能不稳定

