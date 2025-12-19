# Polymarket RTDS WebSocket 示例集合

本目录包含多个实用的 WebSocket 示例，展示如何使用 Polymarket RTDS 客户端进行各种实时数据监控和分析。

## 示例列表

### 1. 基础示例

#### `basic-connection` - 基础连接示例
最简单的连接和订阅示例，适合快速上手。

**功能**:
- 建立 WebSocket 连接
- 订阅加密货币价格
- 订阅评论事件
- 显示所有接收到的消息

**运行**:
```bash
cd examples/basic-connection
go run main.go
```

---

### 2. 价格监控示例

#### `crypto-prices` - 加密货币价格监控
实时监控 Binance 和 Chainlink 的加密货币价格。

**功能**:
- 订阅多个加密货币价格流
- 实时显示价格更新
- 支持 Binance 和 Chainlink 两种数据源

**运行**:
```bash
cd examples/crypto-prices
go run main.go
```

#### `price-alert` - 价格告警系统 ⭐ 新
当价格达到预设阈值时触发告警。

**功能**:
- 配置多个价格告警（高于/低于阈值）
- 实时监控价格变化
- 告警触发时显示详细信息
- 统计告警触发情况

**运行**:
```bash
cd examples/price-alert
go run main.go
```

**配置**: 编辑 `main.go` 中的 `alerts` 数组来设置告警阈值。

---

### 3. 市场数据示例

#### `clob-market-data` - CLOB 市场数据监控
监控 CLOB 订单簿和价格变化。

**功能**:
- 订阅订单簿更新
- 监控最后成交价
- 监控价格变化
- 显示市场深度信息

**运行**:
```bash
cd examples/clob-market-data
go run main.go
```

#### `multi-market-dashboard` - 多市场实时监控面板 ⭐ 新
同时监控多个市场的实时数据。

**功能**:
- 同时监控多个市场
- 实时显示订单簿数据
- 显示最佳买卖价和价差
- 定期更新显示（每2秒）

**运行**:
```bash
cd examples/multi-market-dashboard
go run main.go
```

**配置**: 编辑 `main.go` 中的 `marketIDs` 数组来添加要监控的市场。

#### `orderbook-visualizer` - 订单簿可视化 ⭐ 新
以表格形式可视化显示订单簿深度。

**功能**:
- 实时显示订单簿（买盘和卖盘）
- 显示价格、数量和累计量
- 计算并显示价差
- 每秒更新一次

**运行**:
```bash
cd examples/orderbook-visualizer
go run main.go
```

**配置**: 编辑 `main.go` 中的 `marketID` 来指定要监控的市场。

---

### 4. 交易分析示例

#### `activity-monitor` - 交易活动监控
监控市场交易活动。

**功能**:
- 订阅交易事件
- 显示交易详情
- 监控订单匹配

**运行**:
```bash
cd examples/activity-monitor
go run main.go
```

#### `trade-analyzer` - 交易活动分析器 ⭐ 新
分析交易活动并生成统计报告。

**功能**:
- 实时统计交易数据
- 按市场分组统计
- 区分买入和卖出交易
- 显示交易频率和最后交易时间
- 定期更新统计报告（每5秒）

**运行**:
```bash
cd examples/trade-analyzer
go run main.go
```

---

### 5. 评论监控示例

#### `comments-monitor` - 评论监控
监控特定事件或市场的评论和反应。

**功能**:
- 订阅评论创建/删除事件
- 订阅反应创建/删除事件
- 显示评论详情和用户信息

**运行**:
```bash
cd examples/comments-monitor
go run main.go
```

---

### 6. 连接管理示例

#### `reconnect-demo` - 重连机制演示 ⭐ 新
演示自动重连机制和连接状态监控。

**功能**:
- 启用自动重连
- 监控连接状态
- 显示重连配置
- 检测连接异常

**运行**:
```bash
cd examples/reconnect-demo
go run main.go
```

**测试**: 可以断开网络连接来测试重连机制。

---

### 7. 性能监控示例

#### `performance-monitor` - 性能监控 ⭐ 新
监控 WebSocket 连接的性能指标。

**功能**:
- 统计消息数量和速率
- 按主题和类型分类统计
- 计算消息延迟（平均/最小/最大）
- 监控连接状态
- 生成性能报告

**运行**:
```bash
cd examples/performance-monitor
go run main.go
```

---

## 快速开始

### 1. 选择示例

根据你的需求选择合适的示例：
- **新手**: 从 `basic-connection` 开始
- **价格监控**: 使用 `crypto-prices` 或 `price-alert`
- **市场分析**: 使用 `multi-market-dashboard` 或 `orderbook-visualizer`
- **交易分析**: 使用 `trade-analyzer`
- **性能测试**: 使用 `performance-monitor`

### 2. 运行示例

```bash
cd examples/<示例名称>
go run main.go
```

### 3. 自定义配置

大多数示例都支持自定义配置，编辑 `main.go` 文件来：
- 修改订阅的市场 ID
- 调整更新频率
- 更改显示格式
- 添加过滤条件

---

## 示例特性对比

| 示例 | 实时更新 | 数据可视化 | 统计分析 | 告警功能 | 性能监控 |
|------|---------|-----------|---------|---------|---------|
| basic-connection | ✅ | ❌ | ❌ | ❌ | ❌ |
| crypto-prices | ✅ | ✅ | ❌ | ❌ | ❌ |
| price-alert | ✅ | ✅ | ✅ | ✅ | ❌ |
| clob-market-data | ✅ | ✅ | ❌ | ❌ | ❌ |
| multi-market-dashboard | ✅ | ✅ | ✅ | ❌ | ❌ |
| orderbook-visualizer | ✅ | ✅ | ❌ | ❌ | ❌ |
| activity-monitor | ✅ | ✅ | ❌ | ❌ | ❌ |
| trade-analyzer | ✅ | ✅ | ✅ | ❌ | ❌ |
| comments-monitor | ✅ | ✅ | ❌ | ❌ | ❌ |
| reconnect-demo | ✅ | ✅ | ✅ | ✅ | ❌ |
| performance-monitor | ✅ | ✅ | ✅ | ❌ | ✅ |

---

## 使用技巧

### 1. 组合使用

你可以同时运行多个示例来监控不同的数据流：
```bash
# 终端 1: 监控价格
cd examples/price-alert && go run main.go

# 终端 2: 监控交易
cd examples/trade-analyzer && go run main.go

# 终端 3: 监控性能
cd examples/performance-monitor && go run main.go
```

### 2. 自定义处理逻辑

所有示例都展示了如何注册消息处理器，你可以：
- 修改处理器逻辑
- 添加数据过滤
- 实现自定义分析
- 集成到你的应用中

### 3. 错误处理

示例中包含了基本的错误处理，你可以：
- 添加重试逻辑
- 实现更详细的错误日志
- 添加告警通知（邮件、Slack 等）

---

## 注意事项

1. **网络连接**: 所有示例都需要网络连接来访问 Polymarket RTDS 服务器

2. **市场 ID**: 某些示例需要真实的市场 ID，可以从 Gamma API 获取

3. **资源使用**: 长时间运行可能会消耗较多资源，注意监控

4. **API 限制**: 注意遵守 Polymarket 的 API 使用限制

---

## 贡献

欢迎提交新的示例或改进现有示例！

---

## 相关文档

- [RTDS 客户端 README](../README.md)
- [Polymarket API 完整文档](../../POLYMARKET_API_完整文档.md)
- [Polymarket 官方文档](https://docs.polymarket.com/)


