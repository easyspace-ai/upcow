# Polymarket CLOB 订单类型说明

## 订单类型概览

Polymarket CLOB 支持四种订单类型，每种类型有不同的执行策略和适用场景：

### 1. FOK (Fill-Or-Kill) - 全部成交或取消

**特点：**
- 必须**全部成交**，否则**完全取消**
- 不允许部分成交
- 要求严格的精度：maker amount（USDC）2位小数，taker amount（tokens）4位小数
- 执行速度最快（当有足够流动性时）

**适用场景：**
- 需要立即全部成交的订单
- 对价格敏感，不接受部分成交
- 高频交易场景

**示例：**
```go
// 买入 10 个 tokens，价格 0.55，必须全部成交
order, _ := client.CreateOrder(ctx, &types.UserOrder{
    TokenID: "123...",
    Side:    types.SideBuy,
    Size:    10.0,
    Price:   0.55,
}, options)

resp, _ := client.PostOrder(ctx, order, types.OrderTypeFOK, false)
```

### 2. FAK (Fill-And-Kill) - 部分成交，剩余取消

**特点：**
- **尽可能成交**，剩余部分自动取消
- 允许部分成交（与 FOK 的主要区别）
- 始终是 TAKER（不会成为 maker 留在订单簿）
- 精度要求与 FOK 相同（2位小数 USDC，4位小数 tokens）
- **最适合复制交易**

**适用场景：**
- 复制交易（copy trading）
- 需要立即成交但不要求全部成交
- 市场流动性不确定时

**示例：**
```go
// 尝试买入 10 个 tokens，能成交多少算多少
order, _ := client.CreateOrder(ctx, &types.UserOrder{
    TokenID: "123...",
    Side:    types.SideBuy,
    Size:    10.0,
    Price:   0.55,
}, options)

resp, _ := client.PostOrder(ctx, order, types.OrderTypeFAK, false)
// 如果只成交了 5 个，剩余 5 个自动取消
```

### 3. GTC (Good-Til-Cancelled) - 限价单

**特点：**
- 留在订单簿中直到成交或手动取消
- 可以成为 **maker**（提供流动性）
- 精度要求更灵活（不需要严格的 2/4 位小数）
- 适合不急于成交的订单

**适用场景：**
- 限价单交易
- 不急于成交，等待合适价格
- 网格交易策略
- 长期持仓策略

**示例：**
```go
// 限价买入，价格 0.50，留在订单簿等待成交
order, _ := client.CreateOrder(ctx, &types.UserOrder{
    TokenID: "123...",
    Side:    types.SideBuy,
    Size:    10.0,
    Price:   0.50,
}, options)

resp, _ := client.PostOrder(ctx, order, types.OrderTypeGTC, false)
// 订单会留在订单簿，直到价格达到 0.50 或手动取消
```

### 4. GTD (Good-Til-Date) - 限时订单

**特点：**
- 类似 GTC，但在指定日期后自动过期
- 需要设置 `expiration` 字段
- 适合有时间限制的交易策略

**适用场景：**
- 有时间限制的订单
- 策略需要在特定时间前执行

**示例：**
```go
// 限价买入，在 2025-12-31 23:59:59 前有效
expiration := time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC).Unix()
order, _ := client.CreateOrder(ctx, &types.UserOrder{
    TokenID:    "123...",
    Side:       types.SideBuy,
    Size:       10.0,
    Price:      0.50,
    Expiration: &expiration,
}, options)

resp, _ := client.PostOrder(ctx, order, types.OrderTypeGTD, false)
```

## 订单类型对比表

| 特性 | FOK | FAK | GTC | GTD |
|------|-----|-----|-----|-----|
| 全部成交要求 | ✅ 必须 | ❌ 允许部分 | ❌ 允许部分 | ❌ 允许部分 |
| 部分成交 | ❌ 不允许 | ✅ 允许 | ✅ 允许 | ✅ 允许 |
| 留在订单簿 | ❌ 不 | ❌ 不 | ✅ 是 | ✅ 是（到期前） |
| 精度要求 | 严格（2/4位） | 严格（2/4位） | 灵活 | 灵活 |
| 执行速度 | 最快 | 快 | 慢（等待成交） | 慢（等待成交） |
| Maker/Taker | Taker | Taker | Maker/Taker | Maker/Taker |
| 适用场景 | 立即全部成交 | 复制交易 | 限价单 | 限时订单 |

## 精度要求详解

### FOK/FAK 精度要求

- **价格（Price）**: 2位小数（tick size 0.01）
- **数量（Size）**: 4位小数
- **Maker Amount（买入时为USDC）**: 2位小数
- **Taker Amount（买入时为tokens）**: 4位小数

### GTC/GTD 精度要求

- **价格（Price）**: 根据 tick size（通常 0.01）
- **数量（Size）**: 2位小数
- **Maker/Taker Amount**: 根据 tick size 配置

## 使用建议

1. **需要立即全部成交** → 使用 FOK
2. **复制交易或部分成交可接受** → 使用 FAK
3. **不急于成交，等待合适价格** → 使用 GTC
4. **有时间限制的订单** → 使用 GTD

## 市价单实现

市价单不是独立的订单类型，而是通过以下方式实现：

1. **获取订单簿** (`GetOrderBook`)
2. **计算最优价格** (`CalculateOptimalFill`)
3. **使用 FOK 或 FAK 下单**

示例：
```go
// 市价买入 $100 USDC 的 tokens
book, _ := client.GetOrderBook(ctx, tokenID, nil)
totalSize, avgPrice, filledUSDC := CalculateOptimalFill(book, types.SideBuy, 100.0)

order, _ := client.CreateOrder(ctx, &types.UserOrder{
    TokenID: tokenID,
    Side:    types.SideBuy,
    Size:    totalSize,
    Price:   avgPrice,
}, options)

resp, _ := client.PostOrder(ctx, order, types.OrderTypeFAK, false)
```

