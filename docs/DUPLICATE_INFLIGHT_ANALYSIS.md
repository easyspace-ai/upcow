# Duplicate In-Flight 错误分析

**问题**: 日志中出现大量 "duplicate in-flight" 错误

## 📊 问题现象

从日志可以看到：
```
[17:09:47] ✅ 第一次下单成功（Entry 订单）
[17:09:47] ⚡ 触发(顺序): side=down ... trades=1/3
[17:09:48] ⚠️ 主单下单失败: err=duplicate in-flight side=down (重复多次)
```

**特点**:
- 第一次下单成功后，立即出现大量 "duplicate in-flight" 错误
- 所有错误都是同一方向（side=down）
- 时间间隔很短（1秒内）

## 🔍 原因分析

### 1. **两层去重机制**

系统有两层去重机制：

#### 第一层：策略层去重（`lastTriggerSide`）
- **位置**: `internal/strategies/velocityfollow/strategy.go` (第556-566行)
- **逻辑**: 检查同一方向是否在冷却期内（`cooldownMs: 1500ms`）
- **问题**: `lastTriggerSideAt` 是在下单**成功后**更新的（第1046行）
- **结果**: 如果下单很快（FAK 立即成交），`lastTriggerSideAt` 会立即更新，但策略可能在 1.5 秒内多次触发

#### 第二层：执行层去重（`inFlightDeduper`）
- **位置**: `internal/services/trading_orders.go` (第60-66行)
- **逻辑**: 检查相同订单 key 是否在 TTL 窗口内（`TTL: 2秒`）
- **去重 key**: `MarketSlug|AssetID|Side|Price|Size|OrderType`
- **问题**: 如果订单参数完全相同，会在 2 秒内被拒绝

### 2. **时间线分析**

```
T+0ms:   价格变化事件触发策略
T+10ms:  策略检查通过，开始下单
T+50ms:  下单成功（FAK 立即成交）
T+60ms:  更新 lastTriggerSideAt = now
T+100ms: 价格变化事件再次触发策略（同一方向）
T+110ms: 策略检查：lastTriggerSideAt 距离现在只有 50ms < 1500ms
         → 应该被跳过，但可能没有生效
T+120ms: 尝试下单 → duplicate in-flight（订单还在 2 秒窗口内）
```

### 3. **为什么策略层去重没有生效？**

可能的原因：
1. **价格变化事件太频繁**: 在 1.5 秒内多次触发，但 `lastTriggerSideAt` 更新有延迟
2. **并发问题**: 多个价格变化事件并发处理，导致去重检查失效
3. **去重检查位置**: 去重检查在 `lastTriggerAt` 之后，但 `lastTriggerAt` 是在下单成功后更新的

## 💡 解决方案

### 方案 1: 提前更新 `lastTriggerSideAt`（推荐）

在下单**之前**就更新 `lastTriggerSideAt`，而不是在下单成功后：

```go
// 在确定 winner 后，立即更新 lastTriggerSideAt
s.lastTriggerSide = winner
s.lastTriggerSideAt = now  // 提前更新

// 然后再检查去重（这样后续触发会被立即跳过）
if s.lastTriggerSide == winner && !s.lastTriggerSideAt.IsZero() {
    // 检查冷却期
}
```

**优点**:
- ✅ 简单有效
- ✅ 避免重复下单尝试
- ✅ 减少日志噪音

### 方案 2: 增加策略层去重检查的日志

添加 Debug 日志，确认去重检查是否生效：

```go
if s.lastTriggerSide == winner && !s.lastTriggerSideAt.IsZero() {
    sideCooldown := time.Duration(s.CooldownMs) * time.Millisecond
    if now.Sub(s.lastTriggerSideAt) < sideCooldown {
        log.Debugf("🔄 [%s] 跳过：同一方向 %s 在冷却期内（距离上次触发 %.2fs）", 
            ID, winner, now.Sub(s.lastTriggerSideAt).Seconds())
        return nil
    }
}
```

### 方案 3: 降低 in-flight TTL

将 `inFlightDeduper` 的 TTL 从 2 秒降低到 500ms：

```go
inFlightDeduper: execution.NewInFlightDeduper(500*time.Millisecond, 64)
```

**优点**:
- ✅ 减少去重窗口
- ✅ 允许更快重试

**缺点**:
- ⚠️ 可能影响正常的下单流程

## 📋 当前状态

**是否正常？**: **部分正常**

**解释**:
- ✅ **保护机制正常工作**: "duplicate in-flight" 错误说明去重机制在工作，防止了重复下单
- ⚠️ **策略层去重可能不够**: 策略应该在更早的阶段就跳过，避免尝试下单
- ⚠️ **日志噪音**: 大量警告日志可能掩盖真正的问题

## 🔧 建议修复

**推荐**: 方案 1（提前更新 `lastTriggerSideAt`）

这样可以：
1. 在策略层就避免重复触发
2. 减少不必要的下单尝试
3. 减少日志噪音
4. 提高效率

## 📊 验证方法

修复后，日志应该显示：
```
[17:09:47] ✅ 第一次下单成功
[17:09:48] 🔄 跳过：同一方向 down 在冷却期内（距离上次触发 0.05s）
[17:09:48] 🔄 跳过：同一方向 down 在冷却期内（距离上次触发 0.10s）
```

而不是：
```
[17:09:48] ⚠️ 主单下单失败: err=duplicate in-flight
```

