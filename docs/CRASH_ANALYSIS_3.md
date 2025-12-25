# 程序再次挂掉问题分析（第三次）

## 📅 分析时间
2025-12-25 09:16

## 🔍 问题现象

### 时间线
1. **09:16:00**: 主单下单成功（纸交易模式），订单状态是 `open`
2. **09:16:10**: 主单未在预期时间内成交（等待了10秒），但订单状态仍然是 `open`，不是 `filled`
3. **09:16:10**: 对冲单下单失败（`context deadline exceeded`）
4. **09:16:20**: 再次尝试下主单，但失败（`context deadline exceeded`）
5. **09:16:20 之后**: 日志停止，程序挂掉

### 关键日志

```
09:16:00 📤 [velocityfollow] 步骤1: 下主单 Entry (side=down price=60c size=8.0000 FAK)
09:16:00 📝 [纸交易] 模拟下单: orderID=order_1766625360227051000, status=open
09:16:00 ✅ [velocityfollow] 主单已提交: orderID=order_1766625360227051000 status=open
09:16:10 ⚠️ [velocityfollow] 主单未在预期时间内成交: orderID=order_1766625360227051000
09:16:10 📤 [velocityfollow] 步骤2: 下对冲单 Hedge (side=up price=41c size=8.0000 GTC)
09:16:10 ⚠️ [velocityfollow] 对冲单下单失败: err=context deadline exceeded
09:16:20 ⚠️ [velocityfollow] 主单下单失败: err=context deadline exceeded
```

## 🎯 根本原因

### 问题1: 纸交易模式下 FAK 订单不会自动"成交"

**问题**：
- 在纸交易模式下，`PlaceOrderAsync` 返回的订单状态是 `open`，而不是 `filled`
- 策略在 `executeSequential` 中等待主单成交，但订单状态永远不会变成 `filled`
- 策略等待了 10 秒（`SequentialMaxWaitMs: 2000ms`，但实际等待了 10 秒，说明有多次重试）

**位置**: `internal/services/io_executor.go:55-66`

```go
if e.dryRun {
    // 纸交易模式：模拟下单成功
    result.Order = order
    result.Order.Status = domain.OrderStatusOpen  // ❌ 问题：状态是 open，不是 filled
    // ...
    callback(result)
    return
}
```

**影响**：
- 策略在 `executeSequential` 中轮询检查订单状态，但订单状态永远是 `open`
- 策略等待超时后，继续下对冲单，但此时 context 可能已经超时
- 导致对冲单下单失败

### 问题2: 策略等待逻辑不适合纸交易模式

**问题**：
- 策略在 `executeSequential` 中等待主单成交，使用 `GetActiveOrders()` 轮询检查
- 但在纸交易模式下，FAK 订单应该立即"成交"，不需要等待
- 策略等待了 10 秒，导致 context 超时

**位置**: `internal/strategies/velocityfollow/strategy.go:824-897`

```go
// 等待主单成交（FAK 订单要么立即成交，要么立即取消）
maxWaitTime := time.Duration(s.Config.SequentialMaxWaitMs) * time.Millisecond
// ...
for time.Now().Before(deadline) {
    // 查询订单状态（使用本地订单状态管理）
    if s.TradingService != nil {
        activeOrders := s.TradingService.GetActiveOrders()
        for _, order := range activeOrders {
            if order.OrderID == entryOrderID {
                if order.Status == domain.OrderStatusFilled {  // ❌ 在纸交易模式下，状态永远不会是 filled
                    entryFilled = true
                    break
                }
            }
        }
    }
    time.Sleep(checkInterval)
}
```

**影响**：
- 策略等待超时，但订单状态仍然是 `open`
- 策略继续下对冲单，但 context 可能已经超时
- 导致对冲单下单失败

### 问题3: Context 超时导致程序挂掉

**问题**：
- 策略在 `executeSequential` 中使用了 `context.WithTimeout(ctx, 10*time.Second)`
- 如果 `GetTopOfBook` 或 `PlaceOrder` 超时，context 会被取消
- 但策略可能还在等待订单成交，导致后续操作失败

**位置**: `internal/strategies/velocityfollow/strategy.go:787`

```go
orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
defer cancel()
```

**影响**：
- 如果 `GetTopOfBook` 超时，context 会被取消
- 策略继续执行，但 context 已经超时
- 导致后续下单操作失败

## 🛠️ 修复方案

### 修复1: 纸交易模式下 FAK 订单立即"成交"（高优先级）

**修改文件**: `internal/services/io_executor.go`

**修复内容**:
1. 在纸交易模式下，如果订单类型是 FAK，立即将状态设置为 `filled`
2. 设置 `FilledSize = Size`，表示完全成交

**代码示例**:
```go
if e.dryRun {
    // 纸交易模式：模拟下单成功
    result.Order = order
    result.Order.Status = domain.OrderStatusOpen
    
    // ✅ 修复：FAK 订单在纸交易模式下立即"成交"
    if order.OrderType == types.OrderTypeFAK {
        result.Order.Status = domain.OrderStatusFilled
        result.Order.FilledSize = order.Size  // 完全成交
    }
    
    if result.Order.OrderID == "" {
        result.Order.OrderID = fmt.Sprintf("dry_run_%d", time.Now().UnixNano())
    }
    ioExecutorLog.Infof("📝 [纸交易] 模拟下单: orderID=%s, assetID=%s, side=%s, price=%.4f, size=%.4f, status=%s",
        result.Order.OrderID, order.AssetID, order.Side, order.Price.ToDecimal(), order.Size, result.Order.Status)
    callback(result)
    return
}
```

### 修复2: 优化策略等待逻辑（中优先级）

**修改文件**: `internal/strategies/velocityfollow/strategy.go`

**修复内容**:
1. 在纸交易模式下，如果订单类型是 FAK，立即认为已成交，不等待
2. 或者，在纸交易模式下，缩短等待时间（例如 100ms）

**代码示例**:
```go
// 等待主单成交（FAK 订单要么立即成交，要么立即取消）
maxWaitTime := time.Duration(s.Config.SequentialMaxWaitMs) * time.Millisecond

// ✅ 修复：在纸交易模式下，FAK 订单应该立即成交
if s.TradingService != nil && s.TradingService.IsDryRun() {
    // 纸交易模式：FAK 订单立即成交
    if entryOrderResult.OrderType == types.OrderTypeFAK {
        entryFilled = true
        log.Infof("✅ [%s] 主单已成交（纸交易模式，FAK 订单立即成交）: orderID=%s", 
            ID, entryOrderID)
    } else {
        // GTC 订单在纸交易模式下也需要等待，但可以缩短等待时间
        maxWaitTime = 100 * time.Millisecond
    }
}
```

### 修复3: 增加 GetTopOfBook 超时容忍度（低优先级）

**修改文件**: `internal/strategies/velocityfollow/strategy.go`

**修复内容**:
1. 在纸交易模式下，如果 `GetTopOfBook` 失败，使用默认价格或跳过
2. 或者，增加 `GetTopOfBook` 的超时时间

## 📊 修复效果预期

### 1. 纸交易模式下 FAK 订单立即成交 ✅
- ✅ FAK 订单在纸交易模式下立即设置为 `filled` 状态
- ✅ 策略不需要等待，可以立即下对冲单
- ✅ 减少超时错误

### 2. 策略等待逻辑优化 ✅
- ✅ 在纸交易模式下，FAK 订单立即认为已成交
- ✅ 减少不必要的等待时间
- ✅ 提高策略响应速度

### 3. 减少超时错误 ✅
- ✅ 减少 `context deadline exceeded` 错误
- ✅ 提高系统稳定性

## 🔍 验证方法

### 1. 检查纸交易模式下 FAK 订单状态
**检查日志**:
```
📝 [纸交易] 模拟下单: orderID=..., status=filled  # ✅ 应该是 filled
```

### 2. 检查策略等待时间
**检查日志**:
```
✅ [velocityfollow] 主单已成交（纸交易模式，FAK 订单立即成交）: orderID=...
```

### 3. 检查超时错误
**检查日志**:
- 不应该看到 `context deadline exceeded` 错误
- 不应该看到 `主单未在预期时间内成交` 警告

---

**状态**: 🔴 需要修复
**优先级**: 🔴 高（导致程序挂掉）
**下一步**: 实施修复方案1和2

