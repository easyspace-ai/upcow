# 为什么需要 GetTopOfBook？WebSocket 不是也可以吗？

## 🤔 问题

用户问：为什么引入 `GetTopOfBook`？用 WebSocket 不是也可以实现这个效果吗？

## ✅ 答案

**`GetTopOfBook` 已经优先使用 WebSocket 数据了！**

它不是一个替代 WebSocket 的方案，而是一个**统一接口**，优先使用 WebSocket，必要时回退到 REST API。

## 🔍 关键区别

### 之前的方法：`GetBestPrice(assetID)`

```go
// ❌ 旧方法：只能获取单个 asset 的价格
entryPrice, err := GetBestPrice(ctx, entryAsset)  // 第一次调用
hedgePrice, err := GetBestPrice(ctx, hedgeAsset)  // 第二次调用

// 问题：
// 1. 需要调用两次（YES 和 NO）
// 2. 两次调用之间有时间差，价格可能不一致
// 3. 无法同时获取 YES 和 NO 的价格，无法计算有效价格
```

### 现在的方法：`GetTopOfBook(market)`

```go
// ✅ 新方法：同时获取 YES 和 NO 的价格
yesBid, yesAsk, noBid, noAsk, source, err := GetTopOfBook(ctx, market)

// 优势：
// 1. 一次调用获取所有价格
// 2. 价格一致性：同时获取，避免时间差
// 3. 可以计算有效价格（考虑镜像订单簿）
```

## 📊 GetTopOfBook 的实现逻辑

### 两层获取策略

```go
func GetTopOfBook(ctx context.Context, market *domain.Market) {
    // 1) WS 快路径：优先使用 WebSocket 数据（✅ 已经实现了！）
    if WebSocket数据可用 && 数据新鲜（<10秒） {
        return WebSocket数据  // ✅ 立即返回，不调用 REST API
    }
    
    // 2) REST 回退：只有在 WebSocket 不可用时才调用
    yesBook = REST_API.GetOrderBook(market.YesAssetID)  // 第一次调用
    noBook = REST_API.GetOrderBook(market.NoAssetID)    // 第二次调用
    
    return 解析后的价格
}
```

### WebSocket 数据来源

```go
// WebSocket 数据来自 MarketStream 的 AtomicBestBook
// internal/infrastructure/websocket/market_stream.go

type MarketStream struct {
    // 原子快照：top-of-book（供策略/执行快速读取）
    bestBook *marketstate.AtomicBestBook  // ✅ WebSocket 实时更新
}

// WebSocket 收到 book 消息时，更新 AtomicBestBook
func (m *MarketStream) handleBookAsPrice(ctx context.Context, message []byte) {
    // 更新 AtomicBestBook（bid/ask + size），供执行/策略无锁读取
    m.bestBook.UpdateToken(tokenType, bidPips, askPips, ...)
}
```

## 🎯 为什么需要 GetTopOfBook？

### 1. 需要同时获取 YES 和 NO 的价格

**问题**：计算有效价格需要同时知道 YES 和 NO 的价格

```go
// 计算有效价格（考虑镜像订单簿）
// 买 Entry: 直接买 entryAsk 或 通过卖 hedge (成本 = 1 - hedgeBid)
effectiveBuyEntry := entryAskDec
if 1-hedgeBidDec < effectiveBuyEntry {
    effectiveBuyEntry = 1 - hedgeBidDec  // 使用镜像价格（更便宜）
}

// 买 Hedge: 直接买 hedgeAsk 或 通过卖 entry (成本 = 1 - entryBid)
effectiveBuyHedge := hedgeAskDec
if 1-entryBidDec < effectiveBuyHedge {
    effectiveBuyHedge = 1 - entryBidDec  // 使用镜像价格（更便宜）
}
```

**如果分别调用两次 `GetBestPrice`**：
- 第一次调用：获取 YES 价格
- 第二次调用：获取 NO 价格（**时间差**，价格可能已经变化）
- 价格不一致，无法正确计算有效价格

**使用 `GetTopOfBook`**：
- 一次调用：同时获取 YES 和 NO 的价格
- 价格一致：同一时刻的价格，可以正确计算有效价格

### 2. 确保价格一致性

**场景**：
```
时刻 T1: YES ask = 66¢
时刻 T2: NO ask = 37¢  (价格可能已经变化)

如果分别调用：
- GetBestPrice(YES) @ T1 → 66¢
- GetBestPrice(NO) @ T2 → 37¢
- 价格不一致！无法正确计算有效价格

如果使用 GetTopOfBook：
- GetTopOfBook(market) @ T1 → YES=66¢, NO=37¢
- 价格一致！可以正确计算有效价格
```

### 3. 优先使用 WebSocket（已实现）

**`GetTopOfBook` 的实现**：
```go
// 1) WS 快路径：优先使用 WebSocket 数据
if WebSocket数据可用 && 数据新鲜（<10秒） {
    return WebSocket数据  // ✅ 立即返回，不调用 REST API
}

// 2) REST 回退：只有在 WebSocket 不可用时才调用
// ...
```

**所以**：
- ✅ **WebSocket 数据可用时**：直接使用，不调用 REST API
- ✅ **WebSocket 数据不可用时**：回退到 REST API（作为兜底）

## 📈 性能对比

### GetBestPrice（旧方法）

```
策略触发
  ↓
调用 GetBestPrice(YES)  → WebSocket 或 REST API
  ↓
调用 GetBestPrice(NO)   → WebSocket 或 REST API
  ↓
两次调用，可能有时间差
```

### GetTopOfBook（新方法）

```
策略触发
  ↓
调用 GetTopOfBook(market)
  ↓
优先使用 WebSocket（如果可用）→ ✅ 立即返回
  ↓
或回退到 REST API（如果 WebSocket 不可用）→ 调用两次 REST API
```

**优势**：
- ✅ 如果 WebSocket 可用：**一次调用，立即返回**（不调用 REST API）
- ✅ 如果 WebSocket 不可用：**一次调用，获取所有价格**（避免时间差）

## 🔄 之前的问题

### 问题1：使用互补价公式

```go
// ❌ 旧代码：使用互补价公式
hedgeCents := 100 - askCents - hedgeOffset
```

**问题**：
- 假设 `YES.price + NO.price = 1`，但这个假设不准确
- 没有考虑镜像订单簿
- 实际成交价格可能比预期高，导致亏损

### 问题2：无法计算有效价格

**为什么无法计算？**
- 需要同时知道 YES 和 NO 的价格
- 需要比较直接买入和通过镜像订单买入的成本
- `GetBestPrice` 只能获取单个 asset 的价格，无法同时获取

## ✅ 现在的解决方案

### 使用 GetTopOfBook 计算有效价格

```go
// ✅ 新代码：使用有效价格
// 1. 同时获取 YES 和 NO 的价格（确保一致性）
yesBid, yesAsk, noBid, noAsk, source, err := GetTopOfBook(ctx, market)

// 2. 计算有效价格（考虑镜像订单簿）
effectiveBuyEntry := min(entryAsk, 1 - hedgeBid)
effectiveBuyHedge := min(hedgeAsk, 1 - entryBid)

// 3. 使用有效价格下单，避免价格滑点导致的亏损
```

**优势**：
- ✅ 价格一致性：同时获取 YES 和 NO 的价格
- ✅ 有效价格计算：考虑镜像订单簿，找到最优价格
- ✅ 优先使用 WebSocket：如果可用，立即返回，不调用 REST API

## 🎯 总结

### GetTopOfBook 的作用

1. **统一接口**：同时获取 YES 和 NO 的价格
2. **确保一致性**：避免时间差导致的价格不一致
3. **优先使用 WebSocket**：如果 WebSocket 数据可用，直接使用（不调用 REST API）
4. **回退机制**：如果 WebSocket 数据不可用，回退到 REST API

### 为什么需要它？

1. **计算有效价格**：需要同时知道 YES 和 NO 的价格
2. **避免价格滑点**：使用实际市场价格，而非公式计算
3. **确保价格一致性**：一次调用获取所有价格，避免时间差

### WebSocket 的作用

- ✅ **已经优先使用 WebSocket 了**！
- ✅ 如果 WebSocket 数据可用且新鲜（<10秒），直接使用
- ✅ 只有在 WebSocket 数据不可用或不新鲜时，才回退到 REST API

### 关键区别

| 方法 | 调用次数 | 价格一致性 | 能否计算有效价格 |
|------|---------|-----------|----------------|
| `GetBestPrice(YES)` + `GetBestPrice(NO)` | 2次 | ❌ 可能有时间差 | ❌ 无法计算 |
| `GetTopOfBook(market)` | 1次 | ✅ 同时获取，一致 | ✅ 可以计算 |

---

**结论**：
- ✅ `GetTopOfBook` **已经优先使用 WebSocket 数据了**
- ✅ 它不是一个替代 WebSocket 的方案，而是一个**统一接口**
- ✅ 引入它的原因是：**需要同时获取 YES 和 NO 的价格**，用于计算有效价格
- ✅ 如果 WebSocket 数据可用，它会直接使用，不调用 REST API

