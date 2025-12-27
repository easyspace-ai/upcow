# Circuit Breaker 触发原因分析

**分析时间**: 2025-12-27  
**日志文件**: `logs/btc-updown-15m-1766777400.log`, `logs/btc-updown-15m-1766778300.log`  
**熔断配置**: `MaxConsecutiveErrors: 10`

---

## 🔍 触发原因分析

### 核心问题

**Circuit Breaker 被触发的原因：连续 10 次下单失败**

### 时间线分析

#### 第一个周期（btc-updown-15m-1766777400）

**03:30:08** - ✅ 成功下单
- Entry 订单：成功（DOWN @ 56c, size=4.9821）
- Hedge 订单：成功（UP @ 41c, size=5.0000）

**03:30:13 - 03:30:18** - ✅ 成功平仓
- 部分止盈：成功（UP @ 48c, 47c）
- 止损：成功（DOWN @ 51c）

**03:30:18 之后** - ⚠️ 开始出现问题

**关键发现**：大量**卖单金额小于最小要求**的警告

```
03:30:18 - ⚠️ 卖单金额 0.00 USDC 小于最小要求 1.10 USDC
03:30:39 - ⚠️ 卖单金额 0.00 USDC 小于最小要求 1.10 USDC
03:30:42 - ⚠️ 卖单金额 0.07 USDC 小于最小要求 1.10 USDC
03:30:44 - ⚠️ 卖单金额 0.07 USDC 小于最小要求 1.10 USDC
...（共 10+ 次）
```

**问题分析**：

1. **剩余持仓太小**：经过部分止盈后，剩余持仓只有 `0.1596 shares`
2. **卖单金额不足**：`0.1596 shares × 44c = 0.07 USDC < 1.1 USDC`
3. **订单被拒绝**：这些卖单虽然通过了 `adjustOrderSize` 检查（只记录警告），但**实际发送到交易所后被拒绝**
4. **错误计数累积**：每次拒绝都会调用 `OnError()`，增加连续错误计数

### 触发熔断的流程

```
03:30:18 - 止损平仓后，剩余持仓 0.1596 shares
  ↓
03:30:39 - 尝试出场（trailing_stop），金额 0.00 USDC < 1.1 USDC
  ↓
订单发送到交易所 → 被拒绝 → OnError() → consecutiveErrors = 1
  ↓
03:30:42 - 再次尝试出场，金额 0.07 USDC < 1.1 USDC
  ↓
订单被拒绝 → OnError() → consecutiveErrors = 2
  ↓
...（持续尝试出场）
  ↓
连续 10 次失败 → consecutiveErrors = 10
  ↓
AllowTrading() 检查 → consecutiveErrors >= 10 → 触发熔断
  ↓
halted = true → 所有后续下单被拒绝
```

### 第二个周期（btc-updown-15m-1766778300）

**03:45:00** - 周期切换，但 Circuit Breaker 状态**保持**（halted = true）

**03:45:05** - 第一次尝试下单 → ❌ 失败（circuit breaker open）

**原因**：Circuit Breaker 在第一个周期结束时已经被打开，状态在周期切换时**没有重置**

---

## 📊 详细数据

### 卖单金额不足的订单

| 时间 | 原因 | Token | Bid | Size | 金额 | 状态 |
|------|------|-------|-----|------|------|------|
| 03:30:18 | stop_loss | UP | 1c | 0.1596 | 0.00 USDC | ❌ 被拒绝 |
| 03:30:39 | stop_loss | UP | 1c | 0.1596 | 0.00 USDC | ❌ 被拒绝 |
| 03:30:42 | trailing_stop | UP | 44c | 0.1596 | 0.07 USDC | ❌ 被拒绝 |
| 03:30:44 | trailing_stop | UP | 44c | 0.1596 | 0.07 USDC | ❌ 被拒绝 |
| 03:30:46 | trailing_stop | UP | 44c | 0.1596 | 0.07 USDC | ❌ 被拒绝 |
| 03:30:47 | trailing_stop | UP | 44c | 0.1596 | 0.07 USDC | ❌ 被拒绝 |
| 03:30:49 | trailing_stop | UP | 44c | 0.1596 | 0.07 USDC | ❌ 被拒绝 |
| 03:30:50 | trailing_stop | UP | 44c | 0.1596 | 0.07 USDC | ❌ 被拒绝 |
| 03:30:52 | trailing_stop | UP | 44c | 0.1596 | 0.07 USDC | ❌ 被拒绝 |
| 03:30:54 | trailing_stop | UP | 44c | 0.1596 | 0.07 USDC | ❌ 被拒绝 |

**总计**：至少 10 次卖单尝试，金额均小于 1.1 USDC，全部被交易所拒绝

---

## 🔧 根本原因

### 1. 部分止盈导致剩余持仓过小

**策略行为**：
- 分批止盈：`profitCents: 3, fraction: 0.5` → 卖出 50%
- 再次分批止盈：`profitCents: 6, fraction: 0.5` → 再卖出剩余 50% 的 50%
- **结果**：剩余持仓 `0.1596 shares`，金额太小

**示例计算**：
```
初始持仓：5 shares
第一次止盈（3c）：卖出 2.5 shares，剩余 2.5 shares
第二次止盈（6c）：卖出 1.25 shares，剩余 1.25 shares
...（可能还有其他平仓）
最终剩余：0.1596 shares
```

### 2. 卖单金额检查逻辑

**代码逻辑**（`adjustOrderSize`）：
```go
if adjustedOrder.Side == types.SideSell {
    if requiredAmount < minOrderSize {
        log.Warnf("⚠️ 卖单金额 %.2f USDC 小于最小要求 %.2f USDC...")
        // ⚠️ 只记录警告，不拒绝订单，订单继续发送
    }
    return &adjustedOrder  // 订单继续发送
}
```

**问题**：
- ✅ 代码层面：只记录警告，不阻止订单发送
- ❌ 交易所层面：**实际拒绝订单**（金额 < 1.0 USDC）
- ❌ 结果：订单失败 → `OnError()` → 错误计数增加

### 3. Circuit Breaker 状态在周期切换时未重置

**问题**：
- Circuit Breaker 状态（halted）在周期切换时**保持**
- 第一个周期结束时熔断，第二个周期开始时仍然熔断
- 需要手动调用 `Resume()` 才能恢复

---

## 💡 解决方案

### 1. 立即修复：在卖单金额不足时直接跳过

**修改 `adjustOrderSize` 函数**：

```go
if adjustedOrder.Side == types.SideSell {
    if requiredAmount < minOrderSize {
        log.Warnf("⚠️ 卖单金额 %.2f USDC 小于最小要求 %.2f USDC，跳过下单（避免被交易所拒绝）",
            requiredAmount, minOrderSize)
        // ❌ 返回错误，阻止订单发送
        return nil, fmt.Errorf("sell order amount %.2f USDC < min %.2f USDC", 
            requiredAmount, minOrderSize)
    }
    return &adjustedOrder
}
```

**或者**：在策略层面检查，如果金额太小，直接跳过出场逻辑

### 2. 优化部分止盈逻辑

**问题**：部分止盈可能导致剩余持仓过小

**解决方案**：
- 设置最小剩余持仓阈值（如 1.0 shares）
- 如果剩余持仓 < 阈值，则全部平仓，而不是部分平仓

### 3. 添加 Circuit Breaker 状态日志

**在 `AllowTrading()` 中添加日志**：

```go
func (cb *CircuitBreaker) AllowTrading() error {
    if cb.halted.Load() {
        log.Warnf("🚨 Circuit Breaker OPEN: consecutiveErrors=%d/%d, dailyPnl=%d",
            cb.consecutiveErrors.Load(),
            cb.maxConsecutiveErrors.Load(),
            cb.dailyPnlCents.Load())
        return ErrCircuitBreakerOpen
    }
    // ... 其他检查
}
```

### 4. 纸交易模式配置

**在配置文件中添加 Circuit Breaker 开关**：

```yaml
# yml/velocityfollow.yaml
circuitBreaker:
  enabled: false  # 纸交易模式下禁用
  # 或者使用更宽松的配置
  maxConsecutiveErrors: 100  # 纸交易模式下放宽阈值
```

---

## 📝 总结

### 触发原因

1. **直接原因**：连续 10 次卖单失败（金额 < 1.1 USDC，被交易所拒绝）
2. **根本原因**：部分止盈导致剩余持仓过小（0.1596 shares）
3. **设计问题**：卖单金额检查只记录警告，不阻止发送，导致订单被交易所拒绝

### 时间线

```
03:30:08 - 成功开仓
03:30:13-18 - 成功平仓（部分止盈）
03:30:18+ - 剩余持仓过小，尝试出场
03:30:39-54 - 连续 10+ 次卖单失败（金额不足）
→ 触发熔断（consecutiveErrors >= 10）
03:45:00 - 周期切换，熔断状态保持
03:45:05+ - 所有下单尝试被拒绝
```

### 建议优先级

1. 🔴 **高优先级**：修复卖单金额检查逻辑，金额不足时直接跳过
2. 🟡 **中优先级**：优化部分止盈逻辑，避免剩余持仓过小
3. 🟢 **低优先级**：添加 Circuit Breaker 状态日志和配置开关

