# GetTopOfBook 详解

## 📋 什么是 GetTopOfBook？

`GetTopOfBook` 是一个获取订单簿最佳买卖价格（一档盘口）的函数。

### 功能

**作用**：获取 YES 和 NO token 的**最佳买卖价格**（订单簿的第一档）

**返回**：
- `yesBid`: YES token 的最佳买价（最高买价）
- `yesAsk`: YES token 的最佳卖价（最低卖价）
- `noBid`: NO token 的最佳买价（最高买价）
- `noAsk`: NO token 的最佳卖价（最低卖价）
- `source`: 数据来源（`ws.bestbook` 或 `rest.orderbook`）

## 🎯 为什么策略需要它？

### 1. 计算有效价格（避免亏损）

**问题背景**：
- 之前策略使用**互补价公式**计算 Hedge 价格：`hedgePrice = 100 - entryPrice - offset`
- 但这个公式**不准确**，因为 Polymarket 的订单簿有**镜像特性**

**解决方案**：
- 使用 `GetTopOfBook` 获取**实际市场价格**
- 计算**有效价格**（考虑镜像订单簿）
- 找到**最优价格路径**，避免价格滑点导致的亏损

### 2. 价格计算示例

```go
// 策略触发：买 UP @ 66¢
// 需要计算 Hedge (DOWN) 的价格

// ❌ 旧方法：使用互补价公式
hedgePrice = 100 - 66 - 3 = 31¢  // 不准确！

// ✅ 新方法：使用 GetTopOfBook 获取实际价格
yesBid, yesAsk, noBid, noAsk = GetTopOfBook(market)
// 假设：yesAsk = 66¢, noBid = 63¢, noAsk = 37¢

// 计算有效价格：
// 买 DOWN 有两种方式：
// 1. 直接买 NO @ noAsk = 37¢
// 2. 通过镜像：卖 YES @ (1 - noBid) = 1 - 0.63 = 37¢
// 有效价格 = min(37¢, 37¢) = 37¢

// 实际成交：DOWN @ 37¢（而不是 31¢）
// 避免了价格滑点导致的亏损
```

### 3. 代码中的使用

```go
// internal/strategies/velocityfollow/strategy.go:622

// 获取 YES 和 NO 的实际市场价格（同时获取，确保一致性）
yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(orderCtx, market)
if err != nil {
    log.Warnf("⚠️ [%s] 获取订单簿失败（快速失败，不阻塞策略）: %v", ID, err)
    return nil // 快速返回，不阻塞策略
}

// 计算有效价格（考虑镜像订单簿）
effectiveBuyEntry := entryAskDec
if 1-hedgeBidDec < effectiveBuyEntry {
    effectiveBuyEntry = 1 - hedgeBidDec  // 使用镜像价格（更便宜）
}

effectiveBuyHedge := hedgeAskDec
if 1-entryBidDec < effectiveBuyHedge {
    effectiveBuyHedge = 1 - entryBidDec  // 使用镜像价格（更便宜）
}
```

## 🔍 GetTopOfBook 的实现逻辑

### 两层获取策略

```go
// internal/services/trading_orders.go:316

func GetTopOfBook(ctx context.Context, market *domain.Market) {
    // 1) WS 快路径：优先使用 WebSocket 数据（快速，实时）
    if WebSocket数据可用 && 数据新鲜（<10秒） {
        return WebSocket数据  // ✅ 立即返回，不阻塞
    }
    
    // 2) REST 回退：如果 WebSocket 数据不可用，调用 REST API
    // ⚠️ 这里可能超时！
    yesBook = REST_API.GetOrderBook(market.YesAssetID)  // 第一次调用
    noBook = REST_API.GetOrderBook(market.NoAssetID)    // 第二次调用
    
    return 解析后的价格
}
```

### 为什么可能超时？

1. **WebSocket 数据不可用**
   - WebSocket 连接断开
   - 数据不新鲜（>10秒）
   - 数据不完整（缺少 YES 或 NO）

2. **REST API 调用慢**
   - 需要调用**两次** REST API（YES 和 NO）
   - 网络延迟
   - API 限流
   - 代理问题

3. **超时时间设置**
   - 之前：25 秒（太长，策略被阻塞）
   - 现在：10 秒（快速失败）

## 🚨 它在程序挂掉问题中的角色

### 问题链条

```
策略触发
  ↓
调用 GetTopOfBook (超时 25 秒)
  ↓
WebSocket 数据不可用 → 回退到 REST API
  ↓
REST API 调用卡住（网络问题/API 限流）
  ↓
策略被阻塞 25 秒
  ↓
后续价格变化事件无法处理
  ↓
程序看起来"挂掉"了（虽然进程还在运行）
```

### 为什么是主要原因？

1. **调用频率高**
   - 每次策略触发都会调用
   - 如果策略频繁触发，会频繁调用

2. **阻塞时间长**
   - 之前超时设置为 25 秒
   - 如果 REST API 卡住，策略会被阻塞 25 秒

3. **阻塞策略执行**
   - 策略在 `GetTopOfBook` 处阻塞
   - 后续的价格变化事件无法处理
   - 虽然程序没有完全挂掉，但策略功能失效

### 修复方案

1. **缩短超时时间**（已修复）
   - 从 25 秒降到 10 秒
   - 快速失败，不阻塞策略

2. **优化 WebSocket 数据使用**（已优化）
   - 放宽新鲜度要求（3秒 → 10秒）
   - 减少 REST API 调用

3. **添加重试机制**（已添加）
   - REST API 失败时重试 2 次
   - 提高成功率

## 📊 数据来源对比

### WebSocket 数据（快路径）

**优点**：
- ✅ **快速**：实时数据，无需网络请求
- ✅ **不阻塞**：直接从内存读取
- ✅ **低延迟**：毫秒级响应

**缺点**：
- ❌ 可能不新鲜（>10秒）
- ❌ 可能不完整（缺少 YES 或 NO）
- ❌ 依赖 WebSocket 连接

### REST API 数据（回退路径）

**优点**：
- ✅ **可靠**：总是能获取到数据
- ✅ **完整**：包含完整的订单簿信息

**缺点**：
- ❌ **慢**：需要网络请求（可能几百毫秒到几秒）
- ❌ **可能超时**：网络问题或 API 限流时可能超时
- ❌ **阻塞**：如果超时，会阻塞策略执行

## 💡 最佳实践

### 1. 优先使用 WebSocket 数据

```go
// ✅ 好的做法：优先使用 WebSocket 数据
if WebSocket数据可用 && 数据新鲜 {
    return WebSocket数据  // 快速返回
}
// 只有在必要时才回退到 REST API
```

### 2. 设置合理的超时时间

```go
// ✅ 好的做法：快速失败
ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
defer cancel()

// ❌ 不好的做法：超时时间太长
ctx, cancel := context.WithTimeout(ctx, 25*time.Second)  // 太长！
```

### 3. 错误处理

```go
// ✅ 好的做法：快速失败，不阻塞策略
yesBid, yesAsk, noBid, noAsk, source, err := GetTopOfBook(ctx, market)
if err != nil {
    log.Warnf("获取订单簿失败（快速失败，不阻塞策略）: %v", err)
    return nil  // 快速返回，不阻塞策略
}

// ❌ 不好的做法：阻塞等待
yesBid, yesAsk, noBid, noAsk, source, err := GetTopOfBook(ctx, market)
if err != nil {
    // 等待重试...  // 阻塞策略！
}
```

## 🎯 总结

### GetTopOfBook 的作用

1. **获取实际市场价格**：YES 和 NO 的最佳买卖价格
2. **计算有效价格**：考虑镜像订单簿，找到最优价格路径
3. **避免价格滑点**：使用实际市场价格，而非公式计算

### 它在程序挂掉问题中的角色

1. **主要原因**：如果 REST API 调用卡住，会阻塞策略执行
2. **触发条件**：WebSocket 数据不可用 → 回退到 REST API → REST API 超时
3. **影响**：策略被阻塞，后续价格变化事件无法处理

### 修复效果

1. ✅ **缩短超时时间**：从 25 秒降到 10 秒
2. ✅ **优化 WebSocket 使用**：放宽新鲜度要求，减少 REST API 调用
3. ✅ **添加重试机制**：提高 REST API 成功率
4. ✅ **快速失败**：不阻塞策略，继续处理后续事件

---

**结论**：
- ✅ `GetTopOfBook` **是**程序挂掉的主要原因之一
- ✅ 它用于获取**实际市场价格**，计算**有效价格**，避免价格滑点
- ✅ 通过缩短超时时间、优化 WebSocket 使用、添加重试机制，问题已修复

