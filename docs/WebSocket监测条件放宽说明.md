# WebSocket 监测条件放宽说明

## 📝 修改内容

为了优先使用 WebSocket 数据，减少回退到 REST API，我们放宽了以下监测条件：

### 1. GetTopOfBook（策略主要接口）

**文件：** `internal/services/trading_orders.go:322-336`

**修改前：**
```go
book.IsFresh(10*time.Second)  // 数据新鲜度：10秒
// 要求：YES/NO 都必须有完整的 bid/ask
```

**修改后：**
```go
book.IsFresh(60*time.Second)  // 数据新鲜度：60秒（放宽6倍）
// 要求：YES/NO 都必须有完整的 bid/ask（保持不变）
```

**影响：**
- ✅ WebSocket 数据在60秒内都视为有效
- ✅ 减少因数据"过期"而回退到 REST API 的情况
- ✅ 提高 WebSocket 数据使用率

### 2. GetBestPrice（单个资产价格）

**文件：** `internal/services/trading_orders.go:231-273`

**修改前：**
```go
book.IsFresh(3*time.Second)   // 数据新鲜度：3秒
spreadCents <= 10              // 价差检查：10分
```

**修改后：**
```go
book.IsFresh(30*time.Second)  // 数据新鲜度：30秒（放宽10倍）
spreadCents <= 30             // 价差检查：30分（放宽3倍）
```

**影响：**
- ✅ WebSocket 数据在30秒内都视为有效
- ✅ 允许更大的价差（30分），减少因价差过大而回退到 REST API
- ✅ 提高 WebSocket 数据使用率

### 3. MarketQualityOptions（市场质量评估）

**文件：** `internal/services/market_quality.go:38-41`

**修改前：**
```go
o.MaxBookAge = 3 * time.Second  // 默认数据新鲜度：3秒
```

**修改后：**
```go
o.MaxBookAge = 60 * time.Second  // 默认数据新鲜度：60秒（放宽20倍）
```

**影响：**
- ✅ 市场质量评估时，WebSocket 数据在60秒内都视为有效
- ✅ 提高 WebSocket 数据使用率

## 📊 修改对比表

| 位置 | 参数 | 修改前 | 修改后 | 放宽倍数 |
|------|------|--------|--------|----------|
| GetTopOfBook | 数据新鲜度 | 10秒 | **60秒** | 6倍 |
| GetBestPrice | 数据新鲜度 | 3秒 | **30秒** | 10倍 |
| GetBestPrice | 价差检查 | 10分 | **30分** | 3倍 |
| MarketQualityOptions | 默认MaxBookAge | 3秒 | **60秒** | 20倍 |

## ✅ 预期效果

1. **提高 WebSocket 数据使用率**
   - 数据在更长时间内被视为有效
   - 减少回退到 REST API 的频率

2. **减少 REST API 调用**
   - 降低超时风险
   - 减少网络延迟
   - 提高策略响应速度

3. **保持数据质量**
   - 仍然要求数据完整（YES/NO都有bid/ask）
   - 仍然检查价差合理性（30分以内）
   - 只是放宽了时间窗口

## ⚠️ 注意事项

1. **数据新鲜度**
   - 虽然放宽到60秒，但 WebSocket 数据通常是实时更新的
   - 如果数据真的超过60秒未更新，可能说明连接有问题

2. **价差检查**
   - 虽然放宽到30分，但正常情况下价差只有1-6分
   - 如果价差真的达到30分，说明市场流动性有问题

3. **监控建议**
   - 观察日志中 `source` 字段的变化
   - 如果 `source=ws.bestbook` 的比例提高，说明修改生效
   - 如果仍然大量使用 `rest.orderbook`，需要检查 WebSocket 连接状态

## 🔍 验证方法

查看日志中的 `source` 字段：

```
# 修改前（可能大量使用 REST API）
📊 [velocityfollow] 订单簿价格: ... (source=rest.orderbook)

# 修改后（应该更多使用 WebSocket）
📊 [velocityfollow] 订单簿价格: ... (source=ws.bestbook)
```

## 📌 总结

通过放宽 WebSocket 监测条件，我们：
- ✅ 优先使用 WebSocket 数据（实时、低延迟）
- ✅ 减少 REST API 调用（降低超时风险）
- ✅ 提高策略响应速度
- ✅ 保持数据质量检查（完整性、价差合理性）

这些修改应该能够显著提高 WebSocket 数据的使用率，减少因数据"过期"而回退到 REST API 的情况。

