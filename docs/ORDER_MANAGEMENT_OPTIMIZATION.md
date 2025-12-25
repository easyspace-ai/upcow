# 订单管理和并发下单优化方案

## 📋 问题分析

### 当前问题

1. **WebSocket 延时**: 订单状态同步有延时，可能导致重复下单
2. **并发下单**: 多个订单同时下单，没有利用本地订单状态
3. **订单管理**: 没有在下单前检查是否已有相同方向的订单
4. **状态不一致**: 本地状态和 WebSocket 状态可能不一致

### 系统现有能力

系统已经有完善的本地订单和仓位管理：

1. **OrderEngine**: 
   - `openOrders`: 未完成订单
   - `orderStore`: 所有订单（包括已成交的）
   - `positions`: 当前仓位

2. **订单状态同步**:
   - `SyncOrderStatus`: 定期同步订单状态
   - `GetActiveOrders`: 获取活跃订单

3. **订单更新回调**:
   - `OnOrderUpdate`: 注册订单更新回调

## 💡 优化方案

### 方案 1: 在下单前查询本地订单状态（推荐）

**思路**: 在下单前，通过 OrderEngine 查询本地订单状态，检查是否已有相同方向的订单。

**实现**:

```go
// 在下单前，检查是否已有相同方向的订单
func (s *Strategy) checkExistingOrders(ctx context.Context, winner domain.TokenType) (bool, []*domain.Order) {
    // 获取活跃订单
    activeOrders := s.TradingService.GetActiveOrders()
    
    // 过滤相同方向的订单
    sameSideOrders := make([]*domain.Order, 0)
    for _, order := range activeOrders {
        // 只检查当前市场的订单
        if order.MarketSlug != s.currentMarketSlug {
            continue
        }
        // 检查是否相同方向
        if order.TokenType == winner && order.Status == domain.OrderStatusOpen {
            sameSideOrders = append(sameSideOrders, order)
        }
    }
    
    return len(sameSideOrders) > 0, sameSideOrders
}

// 在下单前调用
func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
    // ... 现有逻辑 ...
    
    // 在下单前检查
    hasExisting, existingOrders := s.checkExistingOrders(ctx, winner)
    if hasExisting {
        log.Debugf("🔄 [%s] 跳过：已有相同方向的订单: %d 个", ID, len(existingOrders))
        // 可选：取消旧订单
        for _, order := range existingOrders {
            _ = s.TradingService.CancelOrder(ctx, order.OrderID)
        }
    }
    
    // 继续下单逻辑
    // ...
}
```

### 方案 2: 利用订单更新回调管理订单状态

**思路**: 注册订单更新回调，在下单后立即更新本地状态，避免重复下单。

**实现**:

```go
// 在策略初始化时注册订单更新回调
func (s *Strategy) OnOrderUpdate(order *domain.Order) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    // 更新本地订单状态
    if order.IsEntryOrder {
        s.lastEntryOrderID = order.OrderID
        s.lastEntryOrderStatus = order.Status
        s.lastEntryOrderFilledSize = order.FilledSize
    }
    if order.HedgeOrderID != nil {
        s.lastHedgeOrderID = *order.HedgeOrderID
    }
    
    // 如果 Entry 订单失败，取消对应的 Hedge 订单
    if order.IsEntryOrder && order.Status == domain.OrderStatusFailed {
        if order.HedgeOrderID != nil {
            _ = s.TradingService.CancelOrder(context.Background(), *order.HedgeOrderID)
        }
    }
    
    return nil
}

// 在策略初始化时注册
func (s *Strategy) Initialize() {
    s.TradingService.OnOrderUpdate(s.OnOrderUpdate)
}
```

### 方案 3: 使用订单 ID 立即更新本地状态

**思路**: 下单后立即拿到订单 ID，立即更新本地状态，不等待 WebSocket。

**实现**:

```go
// 修改 ExecuteMultiLeg 的返回值，立即返回订单 ID
_, execErr := s.TradingService.ExecuteMultiLeg(orderCtx, req)
if execErr == nil {
    // 立即查询订单状态（不等待 WebSocket）
    // 通过 OrderEngine 查询本地状态
    activeOrders := s.TradingService.GetActiveOrders()
    
    // 找到刚下的订单（通过时间戳或订单属性）
    for _, order := range activeOrders {
        if order.MarketSlug == market.Slug && 
           order.TokenType == winner && 
           order.CreatedAt.After(time.Now().Add(-5*time.Second)) {
            // 立即更新本地状态
            s.lastEntryOrderID = order.OrderID
            s.lastEntryOrderStatus = order.Status
        }
    }
}
```

### 方案 4: 添加订单去重逻辑

**思路**: 在下单前，检查是否在短时间内已经下过相同方向的订单。

**实现**:

```go
// 在策略中添加订单跟踪
type Strategy struct {
    // ... 现有字段 ...
    
    // 订单跟踪
    lastEntryOrderID    string
    lastHedgeOrderID    string
    lastEntryOrderTime  time.Time
    lastHedgeOrderTime  time.Time
    pendingOrders       map[string]*domain.Order // 待确认的订单
}

// 在下单前检查
func (s *Strategy) canPlaceOrder(winner domain.TokenType) bool {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    // 检查是否在冷却期内
    if !s.lastEntryOrderTime.IsZero() {
        cooldown := time.Duration(s.CooldownMs) * time.Millisecond
        if time.Since(s.lastEntryOrderTime) < cooldown {
            return false
        }
    }
    
    // 检查是否已有待确认的订单
    for _, order := range s.pendingOrders {
        if order.TokenType == winner && 
           (order.Status == domain.OrderStatusPending || order.Status == domain.OrderStatusOpen) {
            return false
        }
    }
    
    return true
}
```

## 🎯 推荐实现方案

### 组合方案：方案 1 + 方案 2 + 方案 4

**步骤**:

1. **在下单前检查本地订单状态**:
   ```go
   // 检查是否已有相同方向的订单
   activeOrders := s.TradingService.GetActiveOrders()
   for _, order := range activeOrders {
       if order.MarketSlug == market.Slug && 
          order.TokenType == winner && 
          order.Status == domain.OrderStatusOpen {
           // 取消旧订单
           _ = s.TradingService.CancelOrder(ctx, order.OrderID)
       }
   }
   ```

2. **注册订单更新回调**:
   ```go
   // 在策略初始化时
   s.TradingService.OnOrderUpdate(s.OnOrderUpdate)
   ```

3. **添加订单去重逻辑**:
   ```go
   // 在下单前检查
   if !s.canPlaceOrder(winner) {
       return nil
   }
   ```

4. **立即更新本地状态**:
   ```go
   // 下单后立即查询本地状态
   activeOrders := s.TradingService.GetActiveOrders()
   // 更新本地跟踪状态
   ```

## 📝 代码修改建议

### 1. 在 velocityfollow 策略中添加订单状态检查

```go
// 在下单前添加检查
func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
    // ... 现有逻辑 ...
    
    // 在下单前检查本地订单状态
    activeOrders := s.TradingService.GetActiveOrders()
    for _, order := range activeOrders {
        if order.MarketSlug == market.Slug && 
           order.TokenType == winner && 
           (order.Status == domain.OrderStatusOpen || order.Status == domain.OrderStatusPending) {
            log.Debugf("🔄 [%s] 发现已有相同方向的订单，取消旧订单: orderID=%s", ID, order.OrderID)
            _ = s.TradingService.CancelOrder(ctx, order.OrderID)
        }
    }
    
    // 继续下单逻辑
    // ...
}
```

### 2. 添加订单更新回调

```go
// 在策略中添加订单更新回调
func (s *Strategy) OnOrderUpdate(order *domain.Order) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    // 只处理当前市场的订单
    if order.MarketSlug != s.currentMarketSlug {
        return nil
    }
    
    // 更新本地订单跟踪
    if order.IsEntryOrder {
        s.lastEntryOrderID = order.OrderID
        s.lastEntryOrderStatus = order.Status
    }
    
    // Entry 订单失败时，取消 Hedge 订单
    if order.IsEntryOrder && order.Status == domain.OrderStatusFailed {
        if order.HedgeOrderID != nil {
            log.Infof("🔄 [%s] Entry 订单失败，取消 Hedge 订单: hedgeOrderID=%s", ID, *order.HedgeOrderID)
            _ = s.TradingService.CancelOrder(context.Background(), *order.HedgeOrderID)
        }
    }
    
    return nil
}

// 在策略初始化时注册
func (s *Strategy) Initialize() {
    s.TradingService.OnOrderUpdate(s.OnOrderUpdate)
}
```

### 3. 添加订单去重逻辑

```go
// 在策略结构体中添加
type Strategy struct {
    // ... 现有字段 ...
    
    // 订单跟踪
    lastEntryOrderID    string
    lastHedgeOrderID    string
    pendingOrders       map[string]*domain.Order
}

// 在下单前检查
func (s *Strategy) canPlaceOrder(ctx context.Context, winner domain.TokenType, marketSlug string) bool {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    // 检查本地活跃订单
    activeOrders := s.TradingService.GetActiveOrders()
    for _, order := range activeOrders {
        if order.MarketSlug == marketSlug && 
           order.TokenType == winner && 
           (order.Status == domain.OrderStatusOpen || order.Status == domain.OrderStatusPending) {
            log.Debugf("🔄 [%s] 已有相同方向的订单: orderID=%s status=%s", ID, order.OrderID, order.Status)
            return false
        }
    }
    
    return true
}
```

## 🔧 具体实现步骤

### 步骤 1: 添加订单状态检查

在 `OnPriceChanged` 方法中，在下单前添加检查：

```go
// 在下单前检查本地订单状态
if !s.canPlaceOrder(ctx, winner, market.Slug) {
    s.mu.Unlock()
    log.Debugf("🔄 [%s] 跳过：已有相同方向的订单", ID)
    return nil
}
```

### 步骤 2: 注册订单更新回调

在策略初始化时注册回调：

```go
func (s *Strategy) Initialize() {
    s.TradingService.OnOrderUpdate(s.OnOrderUpdate)
}
```

### 步骤 3: 实现订单更新回调

```go
func (s *Strategy) OnOrderUpdate(order *domain.Order) error {
    // 更新本地状态
    // 处理订单失败情况
    // 取消对应的 Hedge 订单
}
```

## 📊 预期效果

1. **防止重复下单**: 在下单前检查本地订单状态，避免重复下单
2. **及时取消旧订单**: 发现旧订单时立即取消
3. **状态一致性**: 利用本地订单状态，减少对 WebSocket 的依赖
4. **更好的并发控制**: 通过本地状态管理，更好地控制并发下单

---

**报告生成时间**: 2025-12-25  
**问题**: WebSocket 延时导致订单状态同步问题，并发下单导致重复订单  
**方案**: 利用 OrderEngine 的本地订单状态，在下单前检查并管理订单

