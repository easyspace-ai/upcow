# 模拟下单模式超时问题分析

## 📅 分析时间
2025-12-25

## 🔍 问题概述

在模拟下单模式（`dry_run: true`）下，仍然出现 `context deadline exceeded` 错误。这**不正常**，因为模拟模式下不应该有网络超时。

## 📋 代码流程分析

### 1. PlaceOrder 流程

```
策略调用 PlaceOrder
  ↓
TradingService.PlaceOrder (trading_orders.go:21)
  ↓
发送命令到 OrderEngine (order_engine.go:85)
  ↓
等待 OrderEngine 回复 (trading_orders.go:88-93)
  ↓
OrderEngine.handlePlaceOrder (order_engine.go:381)
  ↓
异步调用 IOExecutor.PlaceOrderAsync (order_engine.go:431)
  ↓
IOExecutor.PlaceOrderAsync (io_executor.go:47)
  ↓
dry_run 模式：立即返回模拟结果 (io_executor.go:55-66)
  ↓
回调函数更新 OrderEngine 状态 (order_engine.go:431-454)
  ↓
回复原始命令 (order_engine.go:450-453)
```

### 2. 超时发生位置

**问题1: GetTopOfBook 超时（正常）**
- **位置**: `internal/strategies/velocityfollow/strategy.go:620`
- **原因**: 即使是在 dry_run 模式下，`GetTopOfBook` 仍然需要调用 REST API 获取订单簿数据
- **超时设置**: 25 秒
- **是否正常**: ✅ **正常** - 因为需要获取实时市场价格

**问题2: PlaceOrder 超时（不正常）**
- **位置**: `internal/services/trading_orders.go:88-93`
- **原因**: 等待 `OrderEngine` 回复超时
- **超时设置**: 25 秒（从策略传入的 context）
- **是否正常**: ❌ **不正常** - 因为 dry_run 模式下应该立即返回

## 🔎 根本原因分析

### 可能的原因1: OrderEngine 阻塞

**问题**: `OrderEngine` 的命令处理循环可能被阻塞

**检查点**:
1. `OrderEngine` 的命令队列是否满了？
2. `OrderEngine` 的 goroutine 是否还在运行？
3. 是否有死锁或死循环？

### 可能的原因2: 回调函数未执行

**问题**: `PlaceOrderAsync` 的回调函数可能未执行

**检查点**:
1. `PlaceOrderAsync` 是否真的在 dry_run 模式下立即返回？
2. 回调函数是否被正确调用？
3. `cmd.Reply` channel 是否被正确发送？

### 可能的原因3: Context 提前取消

**问题**: Context 可能在等待过程中被取消

**检查点**:
1. 策略传入的 context 是否在 25 秒内被取消？
2. 是否有其他地方取消了 context？

## 🛠️ 修复方案

### 方案1: 检查 OrderEngine 状态（推荐）

**问题**: `OrderEngine` 可能被阻塞或停止

**修复**:
1. 添加 `OrderEngine` 健康检查
2. 监控命令队列长度
3. 添加超时日志，记录阻塞位置

### 方案2: 优化 dry_run 模式下的 PlaceOrder

**问题**: dry_run 模式下仍然等待异步回调

**修复**:
```go
// 在 OrderEngine.handlePlaceOrder 中
if e.dryRun {
    // dry_run 模式：立即返回，不等待异步 IO
    result := &PlaceOrderResult{
        Order: cmd.Order,
    }
    result.Order.Status = domain.OrderStatusOpen
    if result.Order.OrderID == "" {
        result.Order.OrderID = fmt.Sprintf("dry_run_%d", time.Now().UnixNano())
    }
    select {
    case cmd.Reply <- result:
    case <-cmd.Context.Done():
    }
    return
}
```

### 方案3: 增加 GetTopOfBook 的超时容忍度

**问题**: `GetTopOfBook` 在 dry_run 模式下仍然可能超时

**修复**:
1. 增加 WebSocket 数据新鲜度容忍度（从 3 秒增加到 10 秒）
2. 添加重试机制
3. 使用更短的超时时间（10 秒），快速失败

## 📊 建议的修复优先级

1. **高优先级**: 检查 OrderEngine 状态（方案1）
   - 确认 `OrderEngine` 是否正常运行
   - 检查是否有阻塞或死锁

2. **中优先级**: 优化 dry_run 模式（方案2）
   - 在 dry_run 模式下立即返回，不等待异步回调
   - 提高模拟模式的响应速度

3. **低优先级**: 优化 GetTopOfBook（方案3）
   - 虽然 GetTopOfBook 超时是正常的，但可以优化以提高成功率

## 🔍 进一步调查

### 1. 检查 OrderEngine 日志
```bash
grep -E "(OrderEngine|handlePlaceOrder|PlaceOrderAsync)" logs/btc-updown-15m-1766620800.log | tail -50
```

### 2. 检查 dry_run 模式日志
```bash
grep -E "(纸交易|dry_run|dryRun)" logs/btc-updown-15m-1766620800.log | tail -50
```

### 3. 检查超时发生时间
```bash
grep -E "(context deadline|timeout|超时)" logs/btc-updown-15m-1766620800.log | tail -30
```

## 📝 结论

在模拟下单模式下：
- ✅ **GetTopOfBook 超时是正常的** - 因为仍然需要调用 REST API
- ❌ **PlaceOrder 超时是不正常的** - 因为 dry_run 模式下应该立即返回

**建议**: 优先检查 `OrderEngine` 的状态，确认是否有阻塞或死锁问题。

---

**状态**: 🔍 问题已分析，等待进一步调查  
**下一步**: 检查 OrderEngine 日志，确认阻塞位置

