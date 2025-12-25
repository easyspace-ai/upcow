# 订单更新回调架构解释

## 📊 订单更新的两个路径

在我们的系统中，订单更新事件有**两个不同的路径**：

### 路径 1: TradingService → OrderEngine → 策略 ✅

```
订单状态变化
    ↓
TradingService (订单状态同步/API轮询)
    ↓
OrderEngine (内部订单状态管理)
    ↓
策略的 OnOrderUpdate() 回调
```

**特点**:
- ✅ 策略通过 `TradingService.OnOrderUpdate()` 注册回调
- ✅ 这个路径主要用于**策略自己下的订单**（Entry/Hedge）
- ✅ 当前已注册：策略在 `Subscribe()` 中注册了 `TradingService.OnOrderUpdate()`

### 路径 2: UserWebSocket → EventRouter → Session → 策略 ❌

```
WebSocket 订单消息
    ↓
UserWebSocket (接收 WebSocket 消息)
    ↓
EventRouter (事件路由器)
    ↓
Session.EmitOrderUpdate() (Session 层过滤和分发)
    ↓
策略的 OnOrderUpdate() 回调 ❌ (未注册)
```

**特点**:
- ❌ 策略**没有**通过 `Session.OnOrderUpdate()` 注册回调
- ❌ 这个路径用于**手动订单**和**对冲单的 WebSocket 更新**
- ❌ 日志显示 `handlers=0`，说明 Session 的 `orderHandlers` 列表为空

## 🔍 日志证据

从日志可以看到：

```
📊 [Session polymarket] 触发订单更新事件: orderID=... handlers=0
```

`handlers=0` 表示：
- Session 的 `orderHandlers` 列表为空
- 没有策略注册到 Session 的订单更新回调
- 订单更新事件被触发，但没有处理器接收

## 📋 代码位置

### Session 的订单更新注册

**文件**: `pkg/bbgo/session.go`

```go
// OnOrderUpdate 注册订单更新处理器
func (s *ExchangeSession) OnOrderUpdate(handler OrderHandler) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.orderHandlers = append(s.orderHandlers, handler)  // 添加到列表
}

// EmitOrderUpdate 触发订单更新事件
func (s *ExchangeSession) EmitOrderUpdate(ctx context.Context, order *domain.Order) {
    // ... 过滤逻辑 ...
    
    s.mu.RLock()
    handlers := s.orderHandlers  // 获取注册的处理器列表
    s.mu.RUnlock()
    
    sessionLog.Infof("📊 [Session %s] 触发订单更新事件: ... handlers=%d", 
        s.Name, len(handlers))  // 这里显示 handlers=0
    
    // 调用所有注册的处理器
    for i, handler := range handlers {
        handler.OnOrderUpdate(ctx, order)
    }
}
```

### 策略的当前注册方式

**文件**: `internal/strategies/velocityfollow/strategy.go`

```go
func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
    session.OnPriceChanged(s)  // ✅ 注册价格变化回调
    
    // ❌ 没有注册 Session 的订单更新回调
    // session.OnOrderUpdate(s)  // 这行代码不存在
    
    // ✅ 只注册了 TradingService 的订单更新回调
    if s.TradingService != nil {
        handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
        s.TradingService.OnOrderUpdate(handler)  // 这是路径1
    }
}
```

## ⚠️ 影响

### 1. 手动订单无法被策略识别

**问题**: 手动下的订单（通过 WebSocket 接收）会经过 Session，但策略无法接收

**影响**:
- 策略无法知道手动订单的状态变化
- 策略无法对手动订单做出响应（比如取消对应的对冲单）

### 2. 对冲单的 WebSocket 更新可能丢失

**问题**: 对冲单的订单更新可能通过 WebSocket 路径，但策略无法接收

**影响**:
- 策略无法实时知道对冲单的成交状态
- Hedge 订单成交日志无法记录（Info 级别）
- 对冲单重下机制可能无法正常工作（因为无法检测到对冲单状态）

### 3. 订单状态不同步

**问题**: 策略的订单状态可能和实际订单状态不一致

**影响**:
- 策略可能认为对冲单未成交，但实际上已成交
- 导致风险敞口计算错误

## 🔧 修复方案

### 方案 1: 在 Subscribe 中注册 Session 回调（推荐）

```go
func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
    session.OnPriceChanged(s)
    
    // ✅ 注册 Session 的订单更新回调
    session.OnOrderUpdate(s)  // 添加这一行
    
    // ✅ 同时保留 TradingService 的注册（双重保障）
    if s.TradingService != nil {
        handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
        s.TradingService.OnOrderUpdate(handler)
    }
}
```

**优点**:
- ✅ 可以接收手动订单的更新
- ✅ 可以接收对冲单的 WebSocket 更新
- ✅ 双重保障：两个路径都能接收订单更新

**缺点**:
- ⚠️ 可能收到重复的订单更新（同一个订单可能通过两个路径）
- ⚠️ 需要在 `OnOrderUpdate` 中做去重处理

### 方案 2: 只使用 Session 回调（简化）

```go
func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
    session.OnPriceChanged(s)
    
    // ✅ 只注册 Session 的订单更新回调
    session.OnOrderUpdate(s)
    
    // ❌ 移除 TradingService 的注册
}
```

**优点**:
- ✅ 统一路径，避免重复
- ✅ 可以接收所有订单更新

**缺点**:
- ⚠️ 依赖 Session 路径，如果 Session 路径有问题，订单更新会丢失

## 📊 当前状态

**状态**: 之前讨论过，用户选择暂不修复

**原因**:
- 策略自己的订单（Entry/Hedge）通过 TradingService 路径可以正常接收
- 手动订单和对冲单的 WebSocket 更新虽然无法接收，但影响较小
- 可以通过 API 状态同步来补偿（但可能有延迟）

## 💡 建议

1. **短期**: 保持现状，通过 API 状态同步来补偿
2. **中期**: 如果发现对冲单状态不同步问题，考虑修复
3. **长期**: 统一订单更新路径，避免两个路径导致的问题

## 🔍 验证方法

修复后，日志应该显示：

```
📊 [Session polymarket] 触发订单更新事件: orderID=... handlers=1  // 不再是 0
➡️ [Session polymarket] 调用 handler[0]: orderID=...
✅ [Session polymarket] handler[0] 执行成功: orderID=...
```

