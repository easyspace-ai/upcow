# PriceBreak 策略 AssetID 和 TokenType 流转梳理

## 一、买入流程

### 1. 策略层（pricebreak/strategy.go）

**第232-240行：定义 directions**
```go
directions := []struct {
    tokenType domain.TokenType
    assetID   string
    name      string
    hasPosition bool
}{
    {domain.TokenTypeUp, market.YesAssetID, "UP", hasUpPosition},
    {domain.TokenTypeDown, market.NoAssetID, "DOWN", hasDownPosition},
}
```

**关键点**：
- `UP` → `TokenTypeUp` + `market.YesAssetID`
- `DOWN` → `TokenTypeDown` + `market.NoAssetID`

**第279-292行：创建买入请求**
```go
req := execution.MultiLegRequest{
    Name:       "pricebreak_buy",
    MarketSlug: market.Slug,
    Legs: []execution.LegIntent{{
        Name:      "buy_" + strings.ToLower(dir.name),
        AssetID:   dir.assetID,        // ✅ 来自 directions
        TokenType: dir.tokenType,      // ✅ 来自 directions
        Side:      types.SideBuy,
        Price:     ask,
        Size:      s.Config.OrderSize,
        OrderType: types.OrderTypeFAK,
    }},
    Hedge: execution.AutoHedgeConfig{Enabled: false},
}
```

**第294行：执行下单**
```go
createdOrders, err := s.TradingService.ExecuteMultiLeg(ctx, req)
```

**返回的订单**：
- `createdOrders[0].AssetID` = `dir.assetID`（例如：`market.YesAssetID`）
- `createdOrders[0].TokenType` = `dir.tokenType`（例如：`TokenTypeUp`）

---

## 二、订单创建流程

### 2. ExecutionEngine（execution/engine.go）

**第226-239行：从 LegIntent 创建 Order**
```go
order := &domain.Order{
    MarketSlug:   req.MarketSlug,
    AssetID:      leg.AssetID,      // ✅ 直接来自 LegIntent
    Side:         leg.Side,
    Price:        leg.Price,
    Size:         leg.Size,
    TokenType:    leg.TokenType,    // ✅ 直接来自 LegIntent
    IsEntryOrder: true,
    Status:       domain.OrderStatusPending,
    CreatedAt:    time.Now(),
    OrderType:    leg.OrderType,
}
```

**第240行：调用 PlaceOrder**
```go
o, err := e.ops.PlaceOrder(ctx, order)
```

---

### 3. IOExecutor（io_executor.go）

**第124-129行：创建 UserOrder**
```go
userOrder := &types.UserOrder{
    TokenID: order.AssetID,  // ✅ 使用订单的 AssetID
    Price:   order.Price.ToDecimal(),
    Size:    order.Size,
    Side:    order.Side,
}
```

**第159行：转换响应**
```go
createdOrder := convertOrderResponseToDomain(orderResp, order)
```

**第191-227行：convertOrderResponseToDomain**
```go
order := &domain.Order{
    OrderID:      orderResp.OrderID,
    MarketSlug:   originalOrder.MarketSlug,
    AssetID:      originalOrder.AssetID,    // ✅ 保留原始 AssetID
    Side:         originalOrder.Side,
    Price:        originalOrder.Price,
    Size:         originalOrder.Size,
    TokenType:    originalOrder.TokenType,  // ✅ 保留原始 TokenType
    // ... 其他字段
}
```

**结论**：返回的订单**完全保留**了原始的 `AssetID` 和 `TokenType`。

---

## 三、持仓创建流程（问题所在）

### 4. Trade 消息处理（websocket/user.go）

**第1124-1131行：从 WebSocket 消息设置 TokenType**
```go
// 确定 TokenType（从 outcome 字段，如果有）
if outcome, ok := msg["outcome"].(string); ok {
    if outcome == "YES" {
        trade.TokenType = domain.TokenTypeUp
    } else {
        trade.TokenType = domain.TokenTypeDown  // ⚠️ 问题：所有非 YES 都是 DOWN
    }
}
```

**问题**：如果 WebSocket 消息中没有 `outcome` 字段，`trade.TokenType` 会是空字符串。

**第565-571行：Session 层补充 TokenType**
```go
if trade.TokenType == "" && trade.AssetID != "" {
    if trade.AssetID == market.YesAssetID {
        trade.TokenType = domain.TokenTypeUp
    } else if trade.AssetID == market.NoAssetID {
        trade.TokenType = domain.TokenTypeDown
    }
}
```

**这个逻辑是正确的**：根据 `AssetID` 推断 `TokenType`。

---

### 5. 持仓创建（order_engine.go）

**第1056-1081行：updatePositionFromTrade**
```go
position := &domain.Position{
    ID:              positionID,
    MarketSlug:      order.MarketSlug,
    Market:          trade.Market,
    EntryOrder:      order,
    EntryPrice:      trade.Price,
    EntryTime:       trade.Time,
    Size:            0,
    TokenType:       trade.TokenType,  // ⚠️ 使用 trade.TokenType
    Status:          domain.PositionStatusOpen,
    // ...
}
```

**问题**：持仓的 `TokenType` 来自 `trade.TokenType`，而不是 `order.TokenType`。

---

## 四、止损流程

### 6. 止损逻辑（pricebreak/strategy.go）

**第153-159行：根据持仓 TokenType 选择价格和 AssetID**
```go
var currentBid domain.Price
var assetID string
if pos.TokenType == domain.TokenTypeUp {
    currentBid = yesBid
    assetID = market.YesAssetID
} else {
    currentBid = noBid
    assetID = market.NoAssetID
}
```

**第182行：创建止损订单**
```go
Legs: []execution.LegIntent{{
    Name:      "stop_loss_sell",
    AssetID:   assetID,              // ✅ 根据持仓 TokenType 选择
    TokenType: pos.TokenType,        // ⚠️ 使用持仓的 TokenType
    Side:      types.SideSell,
    Price:     currentBid,
    Size:      pos.Size,
    OrderType: types.OrderTypeFAK,
}},
```

**问题**：如果 `pos.TokenType` 错误，止损订单的 `TokenType` 也会错误。

---

## 五、问题分析

### 问题根源

1. **持仓的 TokenType 来自 Trade，而不是 Order**
   - 订单的 `TokenType` 是正确的（来自策略层）
   - 但持仓的 `TokenType` 来自 `trade.TokenType`
   - 如果 `trade.TokenType` 错误，持仓就会错误

2. **Trade 的 TokenType 可能来源**
   - WebSocket 消息的 `outcome` 字段（可能缺失或错误）
   - Session 层根据 `AssetID` 推断（逻辑正确，但可能时机不对）

3. **止损时依赖持仓的 TokenType**
   - 如果持仓的 `TokenType` 错误，止损逻辑就会错误

---

## 六、修复建议

### 方案1：持仓创建时优先使用 Order 的 TokenType

```go
// updatePositionFromTrade
tokenType := order.TokenType  // ✅ 优先使用订单的 TokenType
if tokenType == "" {
    tokenType = trade.TokenType  // 兜底：使用 trade 的 TokenType
}
if tokenType == "" && trade.Market != nil && trade.AssetID != "" {
    // 最后兜底：根据 AssetID 推断
    if trade.AssetID == trade.Market.YesAssetID {
        tokenType = domain.TokenTypeUp
    } else if trade.AssetID == trade.Market.NoAssetID {
        tokenType = domain.TokenTypeDown
    }
}

position := &domain.Position{
    // ...
    TokenType: tokenType,  // ✅ 使用推断后的 TokenType
}
```

### 方案2：止损时根据 AssetID 验证 TokenType

```go
// checkAndHandleStopLoss
var currentBid domain.Price
var assetID string
var tokenType domain.TokenType

if pos.TokenType == domain.TokenTypeUp {
    currentBid = yesBid
    assetID = market.YesAssetID
    tokenType = domain.TokenTypeUp
} else if pos.TokenType == domain.TokenTypeDown {
    currentBid = noBid
    assetID = market.NoAssetID
    tokenType = domain.TokenTypeDown
} else {
    // 兜底：根据 EntryOrder 的 AssetID 推断
    if pos.EntryOrder != nil {
        if pos.EntryOrder.AssetID == market.YesAssetID {
            assetID = market.YesAssetID
            tokenType = domain.TokenTypeUp
            currentBid = yesBid
        } else if pos.EntryOrder.AssetID == market.NoAssetID {
            assetID = market.NoAssetID
            tokenType = domain.TokenTypeDown
            currentBid = noBid
        }
    }
    if assetID == "" {
        continue  // 无法确定，跳过
    }
}

// 创建止损订单时使用推断后的 tokenType
Legs: []execution.LegIntent{{
    AssetID:   assetID,
    TokenType: tokenType,  // ✅ 使用推断后的 TokenType
    // ...
}},
```

---

## 七、总结

### 数据流

```
策略层
  ↓ (directions: UP → YesAssetID, DOWN → NoAssetID)
LegIntent
  ↓ (AssetID + TokenType)
Order (✅ 正确)
  ↓ (PlaceOrder)
交易所
  ↓ (WebSocket Trade 消息)
Trade (⚠️ TokenType 可能错误)
  ↓ (updatePositionFromTrade)
Position (⚠️ TokenType 来自 Trade，可能错误)
  ↓ (止损时使用)
止损订单 (⚠️ TokenType 可能错误)
```

### 关键点

1. **订单的 AssetID 和 TokenType 是正确的**（来自策略层）
2. **持仓的 TokenType 可能错误**（来自 Trade，而不是 Order）
3. **止损依赖持仓的 TokenType**，所以可能出错

### 建议

优先使用 **方案1**：在创建持仓时优先使用 `order.TokenType`，因为订单的 TokenType 是策略层明确设置的，最可靠。

