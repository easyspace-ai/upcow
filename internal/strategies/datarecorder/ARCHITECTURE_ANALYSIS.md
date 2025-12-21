# 数据记录策略架构分析 - 周期数据混乱问题

## 问题描述

CSV 文件中出现 0.99 的异常价格，怀疑是同时记录了多个周期的数据导致混乱。

## 系统架构分析

### 1. 数据流架构

```
MarketStream (WebSocket)
    ↓ (价格事件)
Session.OnPriceChanged
    ↓ (回调)
DataRecorderStrategy.OnPriceChanged (快路径)
    ↓ (信号)
Event Loop (单线程处理)
    ↓ (合并 UP/DOWN)
onPriceChangedInternal (实际处理)
    ↓ (记录)
DataRecorder.Record → CSV 文件
```

### 2. 周期切换机制

#### MarketScheduler 层面
- **职责**: 每 1 秒检查当前时间，当周期结束时切换市场
- **行为**: 
  - 关闭旧周期的 MarketStream
  - 创建新周期的 MarketStream
  - 更新 Session 的 Market
  - 触发策略重新订阅

#### DataRecorderStrategy 层面
- **检测方式**: 
  1. 基于 Market.Slug 变化（事件驱动）
  2. 基于时间戳检测（定时检查，每秒一次）
- **切换流程**:
  1. 保存旧周期数据
  2. 更新 `currentMarket`
  3. 重置目标价状态
  4. 开始新周期（打开新 CSV 文件）
  5. 获取新周期目标价

### 3. 发现的问题

#### 问题 1: 周期切换时没有清理价格 ⚠️ **关键问题**

**位置**: `strategy.go:305-307`

```go
// 重置目标价状态（新周期需要重新获取目标价）
s.btcTargetPrice = 0
s.btcTargetPriceSet = false
// ❌ 缺少：s.upPrice = 0, s.downPrice = 0
```

**影响**: 
- 新周期开始时，`upPrice` 和 `downPrice` 可能还保留着旧周期的值
- 如果旧周期结束时价格是 0.99，新周期开始时可能还保留这个值

#### 问题 2: 价格更新时没有验证周期 ⚠️ **关键问题**

**位置**: `strategy.go:360-365`

```go
// 更新价格
if event.TokenType == domain.TokenTypeUp {
    s.upPrice = event.NewPrice.ToDecimal()  // ❌ 没有检查 event.Market.Slug
} else if event.TokenType == domain.TokenTypeDown {
    s.downPrice = event.NewPrice.ToDecimal()  // ❌ 没有检查 event.Market.Slug
}
```

**影响**:
- 如果收到旧周期的延迟事件，会覆盖当前周期的价格
- 可能导致新周期记录旧周期的价格

#### 问题 3: 事件循环中的价格合并问题

**位置**: `event_loop.go:33-44`

```go
case <-s.priceSignalC:
    s.priceMu.Lock()
    up := s.latestPrices[domain.TokenTypeUp]
    down := s.latestPrices[domain.TokenTypeDown]
    s.latestPrices = make(map[domain.TokenType]*events.PriceChangedEvent)
    s.priceMu.Unlock()
    if up != nil {
        _ = s.onPriceChangedInternal(loopCtx, up)  // 可能来自周期 A
    }
    if down != nil {
        _ = s.onPriceChangedInternal(loopCtx, down)  // 可能来自周期 B
    }
```

**影响**:
- 如果 UP 事件来自周期 A，DOWN 事件来自周期 B
- 它们会被分别处理，但都更新到同一个 `s.upPrice` 和 `s.downPrice`
- 可能导致混合记录

#### 问题 4: 周期切换的竞态条件

**场景**:
1. 周期 A 结束，开始切换到周期 B
2. 周期 A 的延迟事件到达，更新了 `upPrice` 或 `downPrice`
3. 周期 B 的事件到达，但此时价格已经混合

**时间线示例**:
```
T1: 周期 A 结束，开始切换
T2: 周期 A 的延迟 DOWN 事件到达 → s.downPrice = 0.99 (旧周期)
T3: 周期 B 开始，但 downPrice 还是 0.99
T4: 周期 B 的 UP 事件到达 → s.upPrice = 0.47 (新周期)
T5: 记录数据 → (0.47, 0.99) ❌ 混合了！
```

## 0.99 价格的可能原因

1. **旧周期接近结算**: 0.99 可能是旧周期接近结算时的真实价格
2. **周期切换时未清理**: 新周期开始时，旧周期的 0.99 价格没有被清理
3. **延迟事件污染**: 旧周期的延迟事件在新周期中更新了价格
4. **事件混合**: UP 和 DOWN 事件来自不同周期，被混合记录

## 解决方案

### 方案 1: 周期切换时清理价格 ✅ **必须修复**

在周期切换时，重置所有价格状态：
```go
s.btcTargetPrice = 0
s.btcTargetPriceSet = false
s.upPrice = 0        // ✅ 新增
s.downPrice = 0      // ✅ 新增
```

### 方案 2: 价格更新时验证周期 ✅ **必须修复**

在更新价格前，检查事件是否属于当前周期：
```go
// 验证事件是否属于当前周期
if s.currentMarket == nil || s.currentMarket.Slug != event.Market.Slug {
    logger.Debugf("数据记录策略: 忽略非当前周期的价格事件: 当前=%s, 事件=%s",
        getSlugOrEmpty(s.currentMarket), event.Market.Slug)
    return nil
}
```

### 方案 3: 记录数据时验证周期 ✅ **建议添加**

在记录数据前，再次验证所有价格都属于当前周期：
```go
// 验证价格是否属于当前周期（通过检查价格是否合理）
// 如果价格是 0.99，可能是旧周期的价格
if upPrice >= 0.99 || downPrice >= 0.99 {
    logger.Warnf("数据记录策略: 检测到异常价格，可能来自旧周期: UP=%.4f, DOWN=%.4f",
        upPrice, downPrice)
    // 可以选择跳过记录，或者重置价格
}
```

### 方案 4: 改进事件循环的价格合并 ✅ **建议优化**

在事件循环中，只处理属于同一周期的事件：
```go
case <-s.priceSignalC:
    s.priceMu.Lock()
    up := s.latestPrices[domain.TokenTypeUp]
    down := s.latestPrices[domain.TokenTypeDown]
    s.latestPrices = make(map[domain.TokenType]*events.PriceChangedEvent)
    s.priceMu.Unlock()
    
    // ✅ 验证 UP 和 DOWN 是否属于同一周期
    if up != nil && down != nil && up.Market.Slug != down.Market.Slug {
        logger.Warnf("数据记录策略: UP 和 DOWN 事件来自不同周期，跳过处理")
        continue
    }
    
    if up != nil {
        _ = s.onPriceChangedInternal(loopCtx, up)
    }
    if down != nil {
        _ = s.onPriceChangedInternal(loopCtx, down)
    }
```

## 修复优先级

1. **P0 (必须立即修复)**:
   - 周期切换时清理价格
   - 价格更新时验证周期

2. **P1 (建议修复)**:
   - 记录数据时验证周期
   - 改进事件循环的价格合并

3. **P2 (可选优化)**:
   - 添加更详细的诊断日志
   - 添加价格异常检测

