# 订单管理和并发下单优化实现

## 📋 实现总结

已实现利用本地订单和仓位管理来解决并发下单和 WebSocket 延时问题。

## ✅ 已实现的优化

### 1. 订单状态跟踪

在策略结构体中添加了订单跟踪字段：

```go
type Strategy struct {
    // ... 现有字段 ...
    
    // 订单跟踪：利用本地订单状态管理
    lastEntryOrderID    string
    lastHedgeOrderID    string
    lastEntryOrderStatus domain.OrderStatus
    pendingOrders       map[string]*domain.Order // 待确认的订单
}
```

### 2. 订单更新回调

实现了 `OnOrderUpdate` 方法，利用本地订单状态管理：

```go
func (s *Strategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
    // 更新本地订单跟踪
    // Entry 订单失败时，自动取消 Hedge 订单
    // 更新待确认订单列表
}
```

**功能**:
- ✅ 立即更新本地订单状态（不等待 WebSocket）
- ✅ Entry 订单失败时，自动取消 Hedge 订单
- ✅ 跟踪待确认订单，避免重复下单

### 3. 下单前检查本地订单状态

在下单前，检查是否已有相同方向的订单：

```go
// 在下单前检查本地订单状态（利用 OrderEngine 的本地状态）
if s.TradingService != nil {
    activeOrders := s.TradingService.GetActiveOrders()
    for _, order := range activeOrders {
        // 只检查当前市场的订单
        if order.MarketSlug != market.Slug {
            continue
        }
        // 检查是否相同方向且状态为 open/pending
        if order.TokenType == winner && 
           (order.Status == domain.OrderStatusOpen || order.Status == domain.OrderStatusPending) {
            // 取消旧订单
            go func(orderID string) {
                _ = s.TradingService.CancelOrder(context.Background(), orderID)
            }(order.OrderID)
        }
    }
}
```

**功能**:
- ✅ 利用 `GetActiveOrders()` 查询本地订单状态
- ✅ 发现相同方向的订单时，立即取消
- ✅ 不等待 WebSocket，使用本地状态

### 4. 下单后立即查询本地状态

下单后立即查询本地订单状态，不等待 WebSocket：

```go
// 立即查询本地订单状态（不等待 WebSocket，利用 OrderEngine 的本地状态）
if s.TradingService != nil {
    activeOrders := s.TradingService.GetActiveOrders()
    now := time.Now()
    for _, order := range activeOrders {
        // 查找刚下的订单（通过市场、方向和最近时间）
        if order.MarketSlug == market.Slug && 
           order.TokenType == winner && 
           order.CreatedAt.After(now.Add(-5*time.Second)) {
            s.lastEntryOrderID = order.OrderID
            s.lastEntryOrderStatus = order.Status
        }
    }
}
```

**功能**:
- ✅ 下单后立即查询本地状态
- ✅ 不等待 WebSocket 更新
- ✅ 立即更新本地跟踪状态

### 5. 周期切换时清理订单跟踪

在 `OnCycle` 中清理订单跟踪：

```go
func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, _ *domain.Market) {
    // ... 现有逻辑 ...
    
    // 重置订单跟踪（周期切换时清理）
    s.lastEntryOrderID = ""
    s.lastHedgeOrderID = ""
    s.lastEntryOrderStatus = ""
    s.pendingOrders = make(map[string]*domain.Order)
}
```

## 🎯 优化效果

### 解决的问题

1. **防止重复下单**: 
   - ✅ 在下单前检查本地订单状态
   - ✅ 发现相同方向的订单时，立即取消

2. **减少 WebSocket 依赖**:
   - ✅ 利用 OrderEngine 的本地状态
   - ✅ 下单后立即查询本地状态，不等待 WebSocket

3. **自动取消失败订单**:
   - ✅ Entry 订单失败时，自动取消 Hedge 订单
   - ✅ 通过订单更新回调实现

4. **更好的并发控制**:
   - ✅ 通过本地状态管理，更好地控制并发下单
   - ✅ 避免因 WebSocket 延时导致的重复下单

## 📊 工作流程

### 优化后的下单流程

1. **价格事件触发** → 策略计算
2. **检查本地订单状态** → 查询 `GetActiveOrders()`
3. **取消旧订单** → 如果发现相同方向的订单
4. **下单** → `ExecuteMultiLeg`
5. **立即查询本地状态** → 不等待 WebSocket
6. **更新本地跟踪** → 记录订单 ID 和状态
7. **订单更新回调** → WebSocket 更新时进一步同步

### 订单状态同步流程

1. **下单成功** → OrderEngine 立即更新本地状态
2. **订单更新回调** → `OnOrderUpdate` 被调用
3. **更新本地跟踪** → 记录订单状态
4. **自动处理** → Entry 失败时取消 Hedge

## 🔧 关键实现细节

### 1. 使用 OrderUpdateHandlerFunc

```go
// 使用 OrderUpdateHandlerFunc 包装方法
handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
s.TradingService.OnOrderUpdate(handler)
```

### 2. 异步取消订单

```go
// 取消旧订单（不等待结果，异步执行）
go func(orderID string) {
    _ = s.TradingService.CancelOrder(context.Background(), orderID)
}(order.OrderID)
```

### 3. 时间窗口匹配

```go
// 查找刚下的订单（通过最近 5 秒的时间窗口）
if order.CreatedAt.After(now.Add(-5*time.Second)) {
    // 匹配成功
}
```

## 💡 使用建议

### 1. 监控订单状态

通过日志可以监控：
- 订单创建时间
- 本地状态更新
- 订单取消情况

### 2. 调试

如果遇到问题，可以：
- 查看 `GetActiveOrders()` 返回的订单列表
- 检查 `pendingOrders` 中的待确认订单
- 查看订单更新回调的日志

### 3. 进一步优化

如果需要更严格的控制，可以：
- 添加订单去重逻辑（基于订单属性）
- 实现订单超时自动取消
- 添加订单状态一致性检查

---

**实现时间**: 2025-12-25  
**功能**: 利用本地订单和仓位管理，解决并发下单和 WebSocket 延时问题  
**状态**: ✅ 已实现并编译通过

