# 实际成交价格解决方案

## 🔍 问题确认

根据官方文档分析，**代码已经可以获取实际成交价格**，但盈亏计算没有使用它。

### 官方文档关键信息

**WebSocket User Channel - Trade Message**:
- `price`: **实际成交价格**（trade 的成交价格）
- `size`: 成交数量
- `maker_orders[].price`: maker 订单的价格（如果是 maker 订单）
- `maker_orders[].matched_amount`: 匹配的数量

### 代码现状

1. **已实现**:
   - `internal/infrastructure/websocket/user.go` 的 `handleTradeMessage` 函数解析 Trade Message
   - 创建 `domain.Trade` 对象，其中包含 `Price`（实际成交价格）
   - 通过 `HandleTrade` 发送到 `OrderEngine`

2. **问题**:
   - 盈亏计算（`strategy.go` 第1026行）使用 `hedgeAskCents`（下单时的 ask 价格）
   - 没有使用 Trade 的实际成交价格

## 💡 解决方案

### 方案 1: 使用 Trade 的实际成交价格（推荐）

**步骤**:
1. 在 `OrderEngine` 中，当收到 Trade 消息时，更新订单的实际成交价格
2. 在 `Order` 结构中添加 `FilledPrice` 字段（可选，也可以从 Trade 中获取）
3. 修改盈亏计算逻辑，使用 Trade 的实际成交价格

**优点**:
- ✅ 准确计算实际盈亏
- ✅ 利用现有代码（Trade Message 已经在处理）
- ✅ 不需要额外的 API 调用

**实现**:
```go
// 在 OrderEngine 中处理 Trade
func (e *OrderEngine) processTrade(trade *domain.Trade) {
    // 找到对应的订单
    order := e.findOrderByID(trade.OrderID)
    if order != nil {
        // 更新订单的实际成交价格
        order.FilledPrice = trade.Price  // 需要添加 FilledPrice 字段
        order.FilledSize = trade.Size
    }
}

// 在策略中使用实际成交价格
func (s *Strategy) calculateProfitLoss(order *domain.Order, trade *domain.Trade) {
    // 使用 Trade 的实际成交价格
    actualPrice := trade.Price
    // 而不是 order.Price（下单时的价格）
}
```

### 方案 2: 从 Trade 历史中获取实际成交价格

**步骤**:
1. 当订单成交时，查询 Trade 历史（通过 API `/trades`）
2. 找到对应的 Trade，获取实际成交价格
3. 使用实际成交价格计算盈亏

**优点**:
- ✅ 可以获取历史成交价格
- ✅ 不依赖 WebSocket

**缺点**:
- ⚠️ 需要额外的 API 调用
- ⚠️ 可能有延迟

### 方案 3: 在盈亏计算时查询 Trade

**步骤**:
1. 在盈亏计算时，通过 `TradingService` 查询订单的 Trade 历史
2. 获取实际成交价格
3. 使用实际成交价格计算盈亏

**优点**:
- ✅ 实时获取实际成交价格
- ✅ 不需要修改 Order 结构

**缺点**:
- ⚠️ 需要额外的查询逻辑
- ⚠️ 可能有性能开销

## 🎯 推荐实现

### 步骤 1: 添加 FilledPrice 字段（可选）

在 `domain.Order` 中添加 `FilledPrice` 字段：
```go
type Order struct {
    // ... 现有字段
    FilledPrice *Price  // 实际成交价格（可选）
}
```

### 步骤 2: 在 OrderEngine 中更新 FilledPrice

当收到 Trade 消息时，更新订单的实际成交价格：
```go
func (e *OrderEngine) processTrade(trade *domain.Trade) {
    order := e.findOrderByID(trade.OrderID)
    if order != nil {
        order.FilledPrice = &trade.Price
        order.FilledSize = trade.Size
    }
}
```

### 步骤 3: 修改盈亏计算逻辑

在策略中使用实际成交价格：
```go
func (s *Strategy) calculateProfitLoss(entryOrder *domain.Order, hedgeOrder *domain.Order, entryTrade *domain.Trade, hedgeTrade *domain.Trade) {
    // 使用 Trade 的实际成交价格
    entryPrice := entryTrade.Price.ToCents()
    hedgePrice := hedgeTrade.Price.ToCents()
    
    // 或者使用 Order.FilledPrice（如果已设置）
    // entryPrice := entryOrder.FilledPrice.ToCents()
    // hedgePrice := hedgeOrder.FilledPrice.ToCents()
    
    entryCost := float64(entryPrice) / 100.0 * entryOrder.FilledSize
    hedgeCost := float64(hedgePrice) / 100.0 * hedgeOrder.FilledSize
    totalCost := entryCost + hedgeCost
    
    // 计算盈亏
    // ...
}
```

## 📋 实施计划

### 阶段 1: 验证 Trade Message 数据（1天）

1. 添加日志，记录 Trade Message 中的实际成交价格
2. 对比下单价格和实际成交价格
3. 确认实际成交价格是否更好（更接近 bid 价格）

### 阶段 2: 实现 FilledPrice 更新（2-3天）

1. 在 `OrderEngine` 中处理 Trade 消息时更新 `FilledPrice`
2. 修改盈亏计算逻辑，使用 `FilledPrice` 或 Trade 的实际成交价格
3. 添加测试，验证盈亏计算正确性

### 阶段 3: 验证和优化（1-2天）

1. 运行实际交易，验证盈亏计算
2. 对比使用下单价格和实际成交价格的差异
3. 优化性能（如果需要）

## 🔍 关键发现

### Trade Message 结构

```json
{
  "price": "0.57",  // 实际成交价格
  "size": "10",
  "maker_orders": [
    {
      "price": "0.57",  // maker 订单的价格
      "matched_amount": "10"
    }
  ]
}
```

### 关键点

1. **Trade Message 的 `price`**: 这是**实际成交价格**，不是下单时的价格
2. **如果是 maker 订单**: `maker_orders[].price` 是 maker 订单的价格（通常是限价单的价格）
3. **如果是 taker 订单**: Trade 的 `price` 是实际成交价格（可能比 ask 价格更好）

### 预期效果

如果对冲单是限价单（maker），实际成交价格可能：
- **等于下单价格**（如果以 ask 价格成交）
- **更好**（如果以 bid 价格成交，或市场价格下跌）

使用实际成交价格后，盈亏计算会更准确，可能会发现实际盈亏比预期更好。

