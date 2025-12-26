# `oncePerCycle: true` 全面分析

## 📋 概述

`oncePerCycle` 是 velocityfollow 策略中的一个配置参数，用于限制每个交易周期最多只交易一次。

**重要提示**：根据代码注释，`oncePerCycle` 已被标记为**"已废弃"**，建议使用新的 `maxTradesPerCycle` 参数。

---

## 🔍 代码实现分析

### 1. 配置定义

```go:31:33:internal/strategies/velocityfollow/config.go
OncePerCycle            bool    `yaml:"oncePerCycle" json:"oncePerCycle"`                       // 每周期最多触发一次（已废弃，使用 maxTradesPerCycle）
WarmupMs                int     `yaml:"warmupMs" json:"warmupMs"`                               // 启动/换周期后的预热窗口（毫秒）
MaxTradesPerCycle       int     `yaml:"maxTradesPerCycle" json:"maxTradesPerCycle"`             // 每周期最多交易次数（0=不设限）
```

### 2. 验证逻辑（向后兼容）

```go:130:137:internal/strategies/velocityfollow/config.go
// maxTradesPerCycle: 0 表示不设限，>0 表示限制次数
// 如果未设置且 oncePerCycle=true，则默认为 1（向后兼容）
if c.MaxTradesPerCycle < 0 {
    c.MaxTradesPerCycle = 0
}
if c.OncePerCycle && c.MaxTradesPerCycle == 0 {
    c.MaxTradesPerCycle = 1
}
```

**关键点**：
- 如果 `oncePerCycle=true` 且 `maxTradesPerCycle=0`（未设置），会自动设置 `maxTradesPerCycle=1`
- 这确保了向后兼容性

### 3. 策略执行检查

策略在执行时会进行两层检查：

#### 3.1 旧逻辑检查（兼容性）

```go:621:626:internal/strategies/velocityfollow/strategy.go
// 5. 交易限制检查
// 5.1 兼容旧逻辑：OncePerCycle
if s.OncePerCycle && s.tradedThisCycle {
    s.mu.Unlock()
    return nil
}
```

#### 3.2 新逻辑检查（推荐）

```go:627:632:internal/strategies/velocityfollow/strategy.go
// 5.2 新逻辑：MaxTradesPerCycle 控制（0=不设限）
if s.MaxTradesPerCycle > 0 && s.tradesCountThisCycle >= s.MaxTradesPerCycle {
    s.mu.Unlock()
    log.Debugf("🔄 [%s] 跳过：本周期交易次数已达上限 (%d/%d)", ID, s.tradesCountThisCycle, s.MaxTradesPerCycle)
    return nil
}
```

### 4. 状态管理

#### 4.1 周期状态变量

```go:69:73:internal/strategies/velocityfollow/strategy.go
// 周期状态管理
firstSeenAt          time.Time // 首次看到价格的时间
lastTriggerAt        time.Time // 上次触发时间（用于冷却）
tradedThisCycle      bool      // 本周期是否已交易（兼容旧逻辑）
tradesCountThisCycle int       // 本周期已交易次数（新逻辑）
```

#### 4.2 周期切换时重置

```go:273:284:internal/strategies/velocityfollow/strategy.go
func (s *Strategy) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
    s.mu.Lock()
    defer s.mu.Unlock()

    // 重置价格样本
    s.samples = make(map[domain.TokenType][]sample)

    // 重置周期状态
    s.firstSeenAt = time.Now()
    s.tradedThisCycle = false
    s.tradesCountThisCycle = 0 // 重置交易计数
```

**关键点**：
- 每次周期切换时，`tradedThisCycle` 会被重置为 `false`
- `tradesCountThisCycle` 会被重置为 `0`
- 这确保了每个新周期都可以重新交易

#### 4.3 下单成功后更新状态

**顺序下单模式**：
```go:1686:1708:internal/strategies/velocityfollow/strategy.go
var tradesCount int
// entryOrderResult 一定不为 nil（因为如果为 nil，execErr 不为 nil，函数会提前返回）
if execErr == nil {
    now := time.Now()
    // 只在更新共享状态时持锁，避免阻塞订单更新回调/行情分发（性能关键）
    s.mu.Lock()
    s.lastTriggerAt = now
    // 注意：lastTriggerSide 和 lastTriggerSideAt 已经在上面提前更新了
    // 这里只需要更新交易计数和订单跟踪状态
    s.tradedThisCycle = true
    s.tradesCountThisCycle++ // 增加交易计数
```

**并发下单模式**：
```go:1780:1789:internal/strategies/velocityfollow/strategy.go
var tradesCount int
if execErr == nil && len(createdOrders) > 0 {
    now := time.Now()
    // 只在更新共享状态时持锁（性能关键）
    s.mu.Lock()
    s.lastTriggerAt = now
    s.lastTriggerSide = winner
    s.lastTriggerSideAt = now
    s.tradedThisCycle = true
    s.tradesCountThisCycle++ // 增加交易计数
```

**关键点**：
- 只有在**下单成功**（`execErr == nil`）后，才会更新状态
- 如果下单失败，不会更新状态，可以继续尝试

---

## ✅ 回答核心问题

### Q: `oncePerCycle: true` 是每个周期只交易一次吗？

**A: 是的，但更准确的说法是：**

1. **每个周期最多交易一次**
   - 如果周期内已经成功下单一次，后续的价格变化事件会被跳过
   - 但如果下单失败，不会计入，可以继续尝试

2. **"周期"的定义**
   - 对于 BTC 15分钟周期市场，一个周期 = 15分钟
   - 周期切换时（例如从 `btc-updown-15m-1766728800` 切换到 `btc-updown-15m-1766729700`），状态会重置

3. **实际执行逻辑**
   - 旧逻辑：检查 `oncePerCycle && tradedThisCycle`
   - 新逻辑：检查 `maxTradesPerCycle > 0 && tradesCountThisCycle >= maxTradesPerCycle`
   - 如果 `oncePerCycle=true`，会自动设置 `maxTradesPerCycle=1`

---

## 📊 与其他限制机制的关系

### 1. 冷却时间 (`cooldownMs`)

```go:633:637:internal/strategies/velocityfollow/strategy.go
// 5.3 冷却时间检查
if !s.lastTriggerAt.IsZero() && now.Sub(s.lastTriggerAt) < time.Duration(s.CooldownMs)*time.Millisecond {
    s.mu.Unlock()
    return nil
}
```

- **作用范围**：同一周期内的多次触发
- **默认值**：1500ms（1.5秒）
- **与 oncePerCycle 的关系**：
  - `oncePerCycle` 是周期级别的限制（整个周期只交易一次）
  - `cooldownMs` 是触发级别的限制（两次触发之间至少间隔 N 毫秒）
  - 如果 `oncePerCycle=true`，`cooldownMs` 的作用会被弱化（因为周期内只会交易一次）

### 2. 方向级别去重

```go:753:785:internal/strategies/velocityfollow/strategy.go
// 方向级别的去重：避免同一方向在短时间内重复触发
// 这可以显著减少 duplicate in-flight 错误
if s.lastTriggerSide == winner && !s.lastTriggerSideAt.IsZero() {
    sideCooldown := time.Duration(s.CooldownMs) * time.Millisecond
    if sideCooldown <= 0 {
        sideCooldown = 2 * time.Second // 默认 2 秒
    }
    if now.Sub(s.lastTriggerSideAt) < sideCooldown {
        // ... 跳过逻辑
    }
}
```

- **作用范围**：同一方向的重复触发
- **与 oncePerCycle 的关系**：
  - `oncePerCycle` 限制整个周期只交易一次（不区分方向）
  - 方向级别去重限制同一方向在短时间内重复触发
  - 如果 `oncePerCycle=true`，方向级别去重的作用会被弱化

### 3. 周期结束前保护 (`cycleEndProtectionMinutes`)

```go:599:619:internal/strategies/velocityfollow/strategy.go
// 4.5 周期结束前保护：在周期结束前 N 分钟不开新单（降低风险）
if s.CycleEndProtectionMinutes > 0 && e.Market != nil && e.Market.Timestamp > 0 {
    // ... 检查逻辑
    if now.After(cycleEndTime.Add(-protectionTime)) {
        s.mu.Unlock()
        log.Debugf("⏸️ [%s] 跳过：周期结束前保护（距离周期结束 %.1f 分钟）",
            ID, time.Until(cycleEndTime).Minutes())
        return nil
    }
}
```

- **作用范围**：周期结束前的最后 N 分钟
- **默认值**：3 分钟（如果配置了）
- **与 oncePerCycle 的关系**：
  - `oncePerCycle` 限制周期内交易次数
  - 周期结束前保护限制周期结束前的交易时间窗口
  - 两者可以叠加：即使 `oncePerCycle=false`，周期结束前也不会交易

---

## 🎯 实际效果分析

### 场景 1: `oncePerCycle: true`（当前配置）

**时间线**：
```
14:12:00 - 周期开始 (btc-updown-15m-1766728800)
14:12:05 - 价格变化，满足条件，下单成功 ✅
          → tradedThisCycle = true
          → tradesCountThisCycle = 1
14:12:10 - 价格变化，满足条件，但跳过（已交易）⏸️
14:12:15 - 价格变化，满足条件，但跳过（已交易）⏸️
...
14:14:55 - 周期结束前保护启动 ⏸️
14:15:00 - 周期切换 (btc-updown-15m-1766729700)
          → tradedThisCycle = false（重置）
          → tradesCountThisCycle = 0（重置）
14:15:05 - 价格变化，满足条件，可以交易 ✅
```

**结果**：每个周期最多只交易一次

### 场景 2: `oncePerCycle: false`, `maxTradesPerCycle: 3`

**时间线**：
```
14:12:00 - 周期开始
14:12:05 - 价格变化，满足条件，下单成功 ✅
          → tradesCountThisCycle = 1
14:12:10 - 价格变化，满足条件，下单成功 ✅
          → tradesCountThisCycle = 2
14:12:15 - 价格变化，满足条件，下单成功 ✅
          → tradesCountThisCycle = 3
14:12:20 - 价格变化，满足条件，但跳过（已达上限）⏸️
```

**结果**：每个周期最多交易 3 次

### 场景 3: `oncePerCycle: false`, `maxTradesPerCycle: 0`

**结果**：每个周期不设限，可以无限次交易（但仍受 `cooldownMs` 限制）

---

## 💡 为什么没有实际开单？

根据日志分析，虽然价格事件触发了 153,083 次，但实际没有下单。结合 `oncePerCycle: true` 的分析，可能的原因：

### 1. 周期结束前保护（主要原因）
- **跳过次数**：5,748 次（95.8%）
- **影响**：即使 `oncePerCycle=true`，如果周期内没有在保护窗口之前触发交易，就不会交易

### 2. 速度/价格变化阈值未达到
- 虽然价格事件触发了，但可能不满足 `minVelocityCentsPerSec: 0.3` 的阈值
- 在检查 `oncePerCycle` 之前，就已经被速度检查过滤掉了

### 3. 市场质量门控
- `enableMarketQualityGate: true`, `marketQualityMinScore: 70`
- 可能在检查 `oncePerCycle` 之前，就被市场质量检查过滤掉了

### 4. 周期内已交易
- 如果周期内已经成功交易过一次，后续的价格变化会被 `oncePerCycle` 过滤
- 但日志中未发现交易记录，说明可能从未成功交易过

---

## 🔧 建议

### 1. 迁移到新参数

**当前配置**：
```yaml
oncePerCycle: true
```

**推荐配置**：
```yaml
oncePerCycle: false          # 已废弃，但保留以兼容旧配置
maxTradesPerCycle: 1         # 明确指定每个周期最多交易1次
```

**优势**：
- 更明确的语义
- 可以灵活调整（例如设置为 2 或 3）
- 符合代码的发展方向

### 2. 调试建议

如果希望了解为什么没有交易，建议：

1. **设置日志级别为 `debug`**
   ```yaml
   log_level: "debug"
   ```

2. **查看详细的跳过原因**
   - 速度未达到阈值
   - 市场质量分数不足
   - 周期结束前保护
   - 周期内已交易（`oncePerCycle` 限制）

3. **临时禁用 `oncePerCycle` 测试**
   ```yaml
   oncePerCycle: false
   maxTradesPerCycle: 0  # 不设限
   ```
   看看是否会有交易（注意风险）

### 3. 优化建议

如果希望增加交易机会：

1. **调整 `maxTradesPerCycle`**
   ```yaml
   maxTradesPerCycle: 2  # 每个周期最多交易2次
   ```

2. **缩短周期结束前保护时间**
   ```yaml
   cycleEndProtectionMinutes: 1  # 从3分钟缩短到1分钟
   ```

3. **降低速度阈值**（谨慎）
   ```yaml
   minVelocityCentsPerSec: 0.2  # 从0.3降低到0.2
   ```

---

## 📝 总结

1. **`oncePerCycle: true` 确实意味着每个周期最多只交易一次**
2. **但该参数已被标记为"已废弃"**，建议使用 `maxTradesPerCycle: 1`
3. **实际效果**：如果周期内已经成功交易一次，后续的价格变化会被跳过
4. **与其他限制机制的关系**：
   - 周期结束前保护：限制交易时间窗口
   - 冷却时间：限制触发频率
   - 方向级别去重：限制同一方向的重复触发
5. **当前没有交易的原因**：主要是周期结束前保护（95.8%的跳过），而不是 `oncePerCycle` 的限制

---

**文档生成时间**: 2025-12-26  
**代码版本**: velocityfollow strategy v1.0

