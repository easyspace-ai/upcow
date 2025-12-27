# Circuit Breaker（熔断器）原理与机制分析

## 📚 目录

1. [概述](#概述)
2. [设计原理](#设计原理)
3. [核心机制](#核心机制)
4. [状态管理](#状态管理)
5. [触发条件](#触发条件)
6. [恢复机制](#恢复机制)
7. [代码实现分析](#代码实现分析)
8. [问题分析](#问题分析)
9. [改进建议](#改进建议)

---

## 概述

Circuit Breaker（熔断器）是一种**保护性设计模式**，用于防止系统在异常情况下继续执行可能导致更大损失的操作。在交易系统中，它用于：

- **防止连续错误**：当系统连续失败时，停止继续尝试
- **保护资金安全**：当日亏损达到阈值时，立即停止交易
- **快速失败**：避免在系统异常时继续执行无效操作

---

## 设计原理

### 1. 核心思想

Circuit Breaker 类似于电路中的保险丝：
- **正常状态**：允许电流通过（允许交易）
- **异常状态**：熔断，阻止电流（阻止交易）
- **恢复状态**：需要手动或自动重置（恢复交易）

### 2. 设计目标

1. **快速失败**：在检测到异常时立即停止，避免资源浪费
2. **保护系统**：防止错误累积导致更大损失
3. **高并发安全**：使用原子操作，保证线程安全
4. **低延迟**：快路径检查，最小化性能开销

---

## 核心机制

### 数据结构

```go
type CircuitBreaker struct {
    // 状态标志：是否已熔断（halted）
    halted atomic.Bool
    
    // 错误计数：连续错误次数
    consecutiveErrors atomic.Int64
    
    // 盈亏统计：当日累计盈亏（分）
    dailyPnlCents atomic.Int64
    
    // 日期标识：用于跨日重置（YYYYMMDD）
    dayKey atomic.Int64
    
    // 配置参数
    maxConsecutiveErrors atomic.Int64  // 最大连续错误数
    dailyLossLimitCents  atomic.Int64  // 当日最大亏损（分）
}
```

### 关键特性

1. **原子操作**：所有状态变量使用 `atomic` 包，保证并发安全
2. **无锁设计**：避免锁竞争，提高性能
3. **快路径检查**：`AllowTrading()` 方法快速返回，最小化延迟

---

## 状态管理

### 状态转换图

```
┌─────────────┐
│   CLOSED    │  ← 正常状态：允许交易
│  (正常)     │
└──────┬──────┘
       │
       │ 连续错误 >= 阈值
       │ 或 当日亏损 >= 阈值
       │
       ▼
┌─────────────┐
│    OPEN     │  ← 熔断状态：禁止交易
│  (熔断)     │
└──────┬──────┘
       │
       │ 手动调用 Resume()
       │
       ▼
┌─────────────┐
│   CLOSED    │  ← 恢复状态：重新允许交易
│  (恢复)     │
└─────────────┘
```

### 状态说明

#### 1. CLOSED（正常状态）

- **条件**：`halted == false` 且错误计数未达阈值
- **行为**：允许所有交易请求通过
- **转换**：当连续错误达到阈值时 → OPEN

#### 2. OPEN（熔断状态）

- **条件**：`halted == true`
- **行为**：立即拒绝所有交易请求，返回 `ErrCircuitBreakerOpen`
- **转换**：需要手动调用 `Resume()` → CLOSED

---

## 触发条件

### 1. 连续错误熔断

**触发条件**：
```go
if maxConsecutiveErrors > 0 && consecutiveErrors >= maxConsecutiveErrors {
    halted.Store(true)  // 熔断
    return ErrCircuitBreakerOpen
}
```

**默认配置**：
- `MaxConsecutiveErrors: 10`
- 意味着：连续 10 次下单失败后，触发熔断

**错误计数逻辑**：
```go
// 下单失败时
OnError() → consecutiveErrors.Add(1)

// 下单成功时
OnSuccess() → consecutiveErrors.Store(0)  // 重置计数
```

**特点**：
- ✅ 一次成功即可重置计数
- ❌ 一旦达到阈值，立即熔断
- ❌ **没有自动恢复机制**

### 2. 当日亏损熔断

**触发条件**：
```go
if dailyLossLimitCents > 0 {
    rollDayIfNeeded()  // 检查是否需要跨日重置
    pnl := dailyPnlCents.Load()
    if pnl <= -dailyLossLimitCents {  // 亏损达到阈值
        halted.Store(true)  // 熔断
        return ErrCircuitBreakerOpen
    }
}
```

**默认配置**：
- `DailyLossLimitCents: 0`（未启用）

**PnL 更新逻辑**：
```go
// 在确认成交/平仓时调用
AddPnLCents(delta)  // delta < 0 表示亏损
```

**跨日重置**：
```go
func rollDayIfNeeded() {
    now := time.Now()
    key := int64(now.Year()*10000 + int(now.Month())*100 + now.Day())
    if dayKey != key {
        dayKey = key
        dailyPnlCents.Store(0)  // 清零当日 PnL
    }
}
```

### 3. 手动熔断

**触发方式**：
```go
cb.Halt()  // 手动设置 halted = true
```

**使用场景**：
- 人工介入，发现异常情况
- 系统检测到严重错误
- 需要紧急停止交易

---

## 恢复机制

### 当前实现

**手动恢复**：
```go
func (cb *CircuitBreaker) Resume() {
    cb.halted.Store(false)      // 清除熔断标志
    cb.consecutiveErrors.Store(0) // 重置错误计数
}
```

**特点**：
- ✅ 简单直接
- ❌ **需要外部调用**，没有自动恢复
- ❌ **没有冷却时间机制**

### 问题：为什么一直保持打开？

**根本原因**：

1. **一旦熔断，永久保持**：
   ```go
   if cb.halted.Load() {
       return ErrCircuitBreakerOpen  // 直接返回，不检查其他条件
   }
   ```

2. **没有自动恢复逻辑**：
   - 没有冷却时间（cooldown）
   - 没有半开状态（half-open）
   - 没有自动重试机制

3. **错误计数不会自动重置**：
   - 即使系统恢复正常，错误计数仍然保持
   - 需要手动调用 `Resume()` 才能恢复

---

## 代码实现分析

### 1. 初始化

```go
// 在 TradingService 创建时初始化
circuitBreaker: risk.NewCircuitBreaker(risk.CircuitBreakerConfig{
    MaxConsecutiveErrors: 10,  // 默认 10 次
    DailyLossLimitCents:  0,   // 默认不启用
})
```

### 2. 交易前检查

```go
// 在 PlaceOrder() 中，下单前检查
if s.circuitBreaker != nil {
    if e := s.circuitBreaker.AllowTrading(); e != nil {
        metrics.PlaceOrderBlockedCircuit.Add(1)
        return nil, e  // 直接返回错误，不执行下单
    }
}
```

**执行流程**：
```
下单请求
  ↓
AllowTrading() 检查
  ↓
halted == true? → 是 → 返回错误，拒绝下单
  ↓ 否
consecutiveErrors >= 10? → 是 → 设置 halted=true，返回错误
  ↓ 否
dailyPnlCents <= -limit? → 是 → 设置 halted=true，返回错误
  ↓ 否
返回 nil，允许下单
```

### 3. 错误处理

```go
// 下单失败时
if err != nil {
    metrics.PlaceOrderErrors.Add(1)
    if s.circuitBreaker != nil {
        s.circuitBreaker.OnError()  // 错误计数 +1
    }
    return created, err
}

// 下单成功时
if s.circuitBreaker != nil {
    s.circuitBreaker.OnSuccess()  // 重置错误计数为 0
}
```

**关键点**：
- ✅ 成功一次即可重置计数
- ❌ 但一旦达到阈值并熔断，需要手动恢复

---

## 问题分析

### 问题 1：为什么日志中所有下单都失败？

**原因分析**：

1. **Circuit Breaker 在启动时或启动后某个时刻被打开**
   - 可能是在日志记录开始前就已经达到错误阈值
   - 或者系统启动时某些初始化操作失败

2. **一旦打开，所有后续请求都被拒绝**
   ```go
   if cb.halted.Load() {
       return ErrCircuitBreakerOpen  // 直接返回，不执行任何检查
   }
   ```

3. **没有状态日志**
   - 无法知道何时被打开
   - 无法知道错误计数是多少
   - 无法追踪熔断原因

### 问题 2：为什么没有自动恢复？

**设计缺陷**：

1. **缺少半开状态（Half-Open）**
   - 传统 Circuit Breaker 有三种状态：Closed、Open、Half-Open
   - Half-Open 用于测试系统是否恢复
   - 当前实现只有 Closed 和 Open

2. **缺少冷却时间**
   - 没有在熔断后等待一段时间再尝试恢复
   - 没有渐进式恢复机制

3. **缺少自动重试**
   - 没有定期检查系统是否恢复
   - 没有自动尝试恢复的逻辑

### 问题 3：纸交易模式下是否应该启用？

**当前行为**：
- 纸交易模式下，Circuit Breaker 仍然生效
- 这意味着测试时也可能被熔断阻止

**考虑**：
- 纸交易主要用于测试，不应该被熔断器阻止
- 或者使用更宽松的阈值

---

## 改进建议

### 1. 添加状态日志

**问题**：无法追踪熔断原因和状态

**解决方案**：
```go
func (cb *CircuitBreaker) AllowTrading() error {
    if cb == nil {
        return nil
    }

    if cb.halted.Load() {
        // 添加详细日志
        log.Warnf("Circuit Breaker OPEN: consecutiveErrors=%d/%d, dailyPnl=%d",
            cb.consecutiveErrors.Load(),
            cb.maxConsecutiveErrors.Load(),
            cb.dailyPnlCents.Load())
        return ErrCircuitBreakerOpen
    }

    // ... 其他检查
}
```

### 2. 添加自动恢复机制

**方案 A：冷却时间后自动恢复**

```go
type CircuitBreaker struct {
    // ... 现有字段
    lastHaltedAt atomic.Int64  // Unix timestamp
    cooldownSeconds atomic.Int64  // 冷却时间（秒）
}

func (cb *CircuitBreaker) AllowTrading() error {
    // ... 现有检查
    
    // 检查是否在冷却期内
    if cb.halted.Load() {
        lastHalted := cb.lastHaltedAt.Load()
        cooldown := cb.cooldownSeconds.Load()
        if cooldown > 0 && time.Now().Unix() - lastHalted >= cooldown {
            // 冷却时间已过，尝试恢复
            cb.halted.Store(false)
            cb.consecutiveErrors.Store(0)
            log.Info("Circuit Breaker auto-recovered after cooldown")
        } else {
            return ErrCircuitBreakerOpen
        }
    }
    
    // ... 其他检查
}
```

**方案 B：半开状态（Half-Open）**

```go
type CircuitBreakerState int

const (
    StateClosed CircuitBreakerState = iota
    StateOpen
    StateHalfOpen  // 新增：半开状态
)

func (cb *CircuitBreaker) AllowTrading() error {
    state := cb.state.Load()
    
    switch state {
    case StateOpen:
        // 检查是否应该进入半开状态
        if time.Since(cb.lastHaltedAt) >= cooldown {
            cb.state.Store(StateHalfOpen)
            cb.testAttempts.Store(0)
            log.Info("Circuit Breaker entering Half-Open state")
        } else {
            return ErrCircuitBreakerOpen
        }
        fallthrough
        
    case StateHalfOpen:
        // 允许少量请求通过，测试系统是否恢复
        attempts := cb.testAttempts.Add(1)
        if attempts > maxTestAttempts {
            // 测试失败，回到 Open 状态
            cb.state.Store(StateOpen)
            cb.lastHaltedAt.Store(time.Now().Unix())
            return ErrCircuitBreakerOpen
        }
        // 允许这次请求通过
        
    case StateClosed:
        // 正常检查
    }
    
    // ... 其他检查
}
```

### 3. 纸交易模式优化

**方案**：在纸交易模式下禁用或放宽 Circuit Breaker

```go
func NewTradingService(clobClient *client.Client, dryRun bool) *TradingService {
    // ...
    
    var cbConfig risk.CircuitBreakerConfig
    if dryRun {
        // 纸交易模式：使用更宽松的配置
        cbConfig = risk.CircuitBreakerConfig{
            MaxConsecutiveErrors: 100,  // 更大的阈值
            DailyLossLimitCents:  0,
        }
    } else {
        // 真实交易：使用严格配置
        cbConfig = risk.CircuitBreakerConfig{
            MaxConsecutiveErrors: 10,
            DailyLossLimitCents:  0,
        }
    }
    
    circuitBreaker: risk.NewCircuitBreaker(cbConfig),
}
```

### 4. 添加状态查询接口

**问题**：无法查询当前状态

**解决方案**：
```go
type CircuitBreakerStatus struct {
    IsHalted            bool
    ConsecutiveErrors    int64
    MaxConsecutiveErrors int64
    DailyPnlCents       int64
    DailyLossLimitCents int64
    LastHaltedAt        time.Time
}

func (cb *CircuitBreaker) GetStatus() CircuitBreakerStatus {
    return CircuitBreakerStatus{
        IsHalted:            cb.halted.Load(),
        ConsecutiveErrors:    cb.consecutiveErrors.Load(),
        MaxConsecutiveErrors: cb.maxConsecutiveErrors.Load(),
        DailyPnlCents:       cb.dailyPnlCents.Load(),
        DailyLossLimitCents:  cb.dailyLossLimitCents.Load(),
        LastHaltedAt:        time.Unix(cb.lastHaltedAt.Load(), 0),
    }
}
```

---

## 总结

### 当前实现特点

✅ **优点**：
- 线程安全（使用原子操作）
- 性能优秀（快路径检查）
- 简单直接（易于理解）

❌ **缺点**：
- 没有自动恢复机制
- 缺少状态日志
- 纸交易模式下仍然生效
- 没有半开状态

### 核心问题

**一旦熔断，永久保持打开状态，需要手动恢复**。这导致：
1. 系统无法自动恢复
2. 需要人工干预
3. 无法追踪熔断原因

### 建议优先级

1. 🔴 **高优先级**：添加状态日志，追踪熔断原因
2. 🟡 **中优先级**：添加自动恢复机制（冷却时间）
3. 🟢 **低优先级**：优化纸交易模式下的行为

---

## 参考

- [Circuit Breaker Pattern](https://martinfowler.com/bliki/CircuitBreaker.html)
- [Go 原子操作文档](https://pkg.go.dev/sync/atomic)
- 代码位置：`internal/risk/circuit_breaker.go`

