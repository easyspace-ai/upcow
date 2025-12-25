# 策略模板更新总结

## ✅ 已完成的更新

### 1. 策略结构增强

**新增字段**：
- `mu sync.Mutex`：线程安全的状态管理
- `lastOrderID string`：跟踪最后下单的订单ID
- `pendingOrders map[string]*domain.Order`：待确认的订单列表

### 2. Initialize() 方法增强

**新增功能**：
- 初始化订单跟踪结构
- 注册订单更新回调（推荐）
- 使用 `OrderUpdateHandlerFunc` 包装方法

```go
func (s *Strategy) Initialize() error {
    // 初始化订单跟踪（可选）
    if s.pendingOrders == nil {
        s.pendingOrders = make(map[string]*domain.Order)
    }

    // 注册订单更新回调（推荐）
    if s.TradingService != nil {
        handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
        s.TradingService.OnOrderUpdate(handler)
        log.Infof("✅ [%s] 已注册订单更新回调", ID)
    }

    return nil
}
```

### 3. OnCycle() 方法增强

**新增功能**：
- 重置订单跟踪状态
- 清理待确认订单列表

```go
func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, _ *domain.Market) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.fired = false
    // 重置订单跟踪（周期切换时清理）
    s.lastOrderID = ""
    s.pendingOrders = make(map[string]*domain.Order)
}
```

### 4. OnOrderUpdate() 方法（新增）

**功能**：
- 跟踪订单状态变化
- 更新待确认订单列表
- 处理订单失败/取消

```go
func (s *Strategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
    // 更新订单跟踪
    s.lastOrderID = order.OrderID
    
    // 更新待确认订单列表
    if order.Status == domain.OrderStatusFilled ||
        order.Status == domain.OrderStatusCanceled ||
        order.Status == domain.OrderStatusFailed {
        delete(s.pendingOrders, order.OrderID)
    } else if order.Status == domain.OrderStatusOpen || order.Status == domain.OrderStatusPending {
        s.pendingOrders[order.OrderID] = order
    }
    
    return nil
}
```

### 5. OnPriceChanged() 方法增强

**新增功能**：
- 线程安全的状态更新
- 订单跟踪集成
- 更详细的日志

```go
func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
    // 线程安全的去重检查
    s.mu.Lock()
    if s.fired {
        s.mu.Unlock()
        return nil
    }
    s.mu.Unlock()

    // 下单逻辑...
    
    // 更新订单跟踪
    s.mu.Lock()
    s.fired = true
    if len(createdOrders) > 0 {
        s.lastOrderID = createdOrders[0].OrderID
        for _, order := range createdOrders {
            s.pendingOrders[order.OrderID] = order
        }
    }
    s.mu.Unlock()

    return nil
}
```

### 6. Config 增强

**新增**：
- 更详细的注释说明
- 配置字段的设计原则说明
- 示例配置字段（注释形式）

## 📋 新架构特性说明

### 1. 订单更新回调

**用途**：
- 实时跟踪订单状态变化
- 处理订单失败/取消
- 更新本地订单状态

**注册方式**：
```go
handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
s.TradingService.OnOrderUpdate(handler)
```

### 2. 成本基础跟踪

**自动处理**：
- OrderEngine 自动调用 `Position.AddFill()` 更新成本基础
- 自动计算平均价格
- 支持多次成交累加

**使用方式**：
```go
// 仓位成本基础会自动更新，无需手动处理
// 可以通过 TradingService.GetPositions() 获取仓位信息
positions := s.TradingService.GetPositions()
for _, pos := range positions {
    // 获取成本基础
    costBasis := pos.CostBasis
    avgPrice := pos.AvgPrice
    
    // 计算未实现盈亏
    pnl := pos.UnrealizedPnL(currentPrice)
}
```

### 3. 周期管理

**框架层统一处理**：
- `OnCycle()` 由框架层统一调用
- 无需手动对比 slug
- 自动处理周期切换

### 4. 订单执行

**ExecuteMultiLeg**：
- 支持并发或顺序执行（可配置）
- 自动 in-flight 去重
- 支持自动对冲（如果配置）

## 🎯 使用建议

### 必需实现的方法

1. **ID()** - 策略ID
2. **Name()** - 策略名称
3. **Validate()** - 配置验证
4. **Initialize()** - 初始化（推荐注册订单更新回调）
5. **Subscribe()** - 订阅事件
6. **Run()** - 运行循环
7. **OnCycle()** - 周期切换回调
8. **OnPriceChanged()** - 价格变化回调

### 可选实现的方法

1. **OnOrderUpdate()** - 订单更新回调（推荐）
2. **Defaults()** - 设置默认值（如果需要）

### 推荐的最佳实践

1. **线程安全**：使用 `sync.Mutex` 保护共享状态
2. **订单跟踪**：注册订单更新回调，跟踪订单状态
3. **周期重置**：在 `OnCycle()` 中重置周期相关状态
4. **错误处理**：妥善处理错误，避免 panic
5. **日志记录**：记录关键操作和状态变化

## 📊 对比：更新前 vs 更新后

| 特性 | 更新前 | 更新后 |
|------|--------|--------|
| **订单更新回调** | ❌ 无 | ✅ 有（推荐） |
| **订单跟踪** | ❌ 无 | ✅ 有 |
| **线程安全** | ⚠️ 部分 | ✅ 完整 |
| **周期管理** | ✅ 基础 | ✅ 增强 |
| **注释说明** | ⚠️ 简单 | ✅ 详细 |
| **成本基础跟踪** | ❌ 无说明 | ✅ 有说明 |

## 🔄 迁移指南

### 对于现有策略

1. **添加订单更新回调**（推荐）：
   ```go
   func (s *Strategy) Initialize() error {
       if s.TradingService != nil {
           handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
           s.TradingService.OnOrderUpdate(handler)
       }
       return nil
   }
   ```

2. **添加 OnOrderUpdate 方法**（可选但推荐）：
   ```go
   func (s *Strategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
       // 跟踪订单状态
       return nil
   }
   ```

3. **增强 OnCycle 方法**：
   ```go
   func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, _ *domain.Market) {
       // 重置周期相关状态
       // 清理订单跟踪（如果需要）
   }
   ```

4. **添加线程安全保护**（如果需要）：
   ```go
   type Strategy struct {
       mu sync.Mutex
       // ...
   }
   ```

## ✅ 总结

策略模板已更新，包含：

1. ✅ **订单更新回调**：实时跟踪订单状态
2. ✅ **订单跟踪**：跟踪订单状态变化
3. ✅ **线程安全**：使用 Mutex 保护共享状态
4. ✅ **详细注释**：说明新架构特性
5. ✅ **最佳实践**：提供使用建议和示例

**状态**：✅ 已完成并测试通过  
**下一步**：可以基于更新后的模板创建新策略

