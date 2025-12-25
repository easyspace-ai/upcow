# VelocityFollow 策略价格优化总结

## 🎯 优化目标

**问题**：策略使用互补价公式计算 Hedge 价格，导致实际成交价格与预期不符，产生了额外成本（约 $0.66，6.2%）。

**目标**：使用有效价格（Effective Prices）计算，确保使用最优价格，避免亏损。

## 🔍 问题分析

### 原有实现的问题

```go
// ❌ 原有代码：使用互补价公式
hedgeCents := 100 - askCents - hedgeOffset
```

**问题**：
1. 假设了 `YES.price + NO.price = 1`，但由于 Polymarket 订单簿的镜像特性，这个假设可能不准确
2. 没有考虑镜像订单簿，可能错过更优的价格
3. 实际成交价格可能比预期高，导致成本增加

### 实际案例

从日志分析：
- **策略触发时**：UP @ 66¢, Hedge(DOWN) @ 31¢（通过互补价计算）
- **实际成交**：UP @ 66¢, DOWN @ 37¢
- **价格差异**：37¢ - 31¢ = 6¢
- **额外成本**：11 shares × 6¢ = $0.66（约 6.2%）

## ✅ 优化方案

### 1. 使用有效价格计算

**核心改进**：使用 `GetTopOfBook` 同时获取 YES 和 NO 的实际市场价格，然后计算有效价格。

```go
// ✅ 新代码：使用有效价格
// 1. 获取 YES 和 NO 的实际市场价格（同时获取，确保一致性）
yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(orderCtx, market)

// 2. 计算有效价格（考虑镜像订单簿）
// 买 Entry: 直接买 entryAsk 或 通过卖 hedge (成本 = 1 - hedgeBid)
effectiveBuyEntry := entryAskDec
if 1-hedgeBidDec < effectiveBuyEntry {
    effectiveBuyEntry = 1 - hedgeBidDec
}

// 买 Hedge: 直接买 hedgeAsk 或 通过卖 entry (成本 = 1 - entryBid)
effectiveBuyHedge := hedgeAskDec
if 1-entryBidDec < effectiveBuyHedge {
    effectiveBuyHedge = 1 - entryBidDec
}
```

### 2. 价格滑点保护

添加了总成本检查，如果总成本过高（> $1.05），拒绝下单：

```go
// ===== 价格滑点保护 =====
totalCostDec := effectiveBuyEntry + effectiveBuyHedge
totalCostCents := int(totalCostDec*100 + 0.5)

// 如果总成本过高（> $1.05），说明价格可能有问题，拒绝下单
if totalCostCents > 105 {
    log.Warnf("⚠️ [%s] 价格滑点保护触发: 总成本过高 (%dc > 105c)", ID, totalCostCents)
    return nil
}
```

### 3. 详细日志记录

添加了详细的有效价格计算日志，便于调试和监控：

```go
log.Debugf("💰 [%s] 有效价格计算: Entry=%dc (直接=%dc, 镜像=%dc), Hedge=%dc (直接=%dc, 镜像=%dc), 总成本=%dc, source=%s",
    ID, entryAskCents, int(entryAskDec*100+0.5), int((1-hedgeBidDec)*100+0.5),
    hedgeAskCents, int(hedgeAskDec*100+0.5), int((1-entryBidDec)*100+0.5),
    totalCostCents, source)
```

## 📊 优化效果

### 预期改进

1. **价格准确性**：
   - ✅ 使用实际市场价格，而非公式计算
   - ✅ 考虑镜像订单簿，找到最优价格
   - ✅ 避免价格滑点导致的成本增加

2. **成本控制**：
   - ✅ 总成本检查，拒绝不合理的价格
   - ✅ 使用有效价格，确保使用最优路径

3. **可观测性**：
   - ✅ 详细的价格计算日志
   - ✅ 记录价格来源（WebSocket 或 REST）
   - ✅ 记录直接价格和镜像价格的对比

### 预期成本节省

- **之前**：预期成本 $10.67，实际成本 $11.33，额外成本 $0.66（6.2%）
- **优化后**：使用有效价格，预期成本更接近实际成本，减少价格滑点导致的亏损

## 🔧 技术细节

### 有效价格计算公式

基于 `poly-sdk` 的实现：

```go
// 买 YES: min(YES.ask, 1 - NO.bid)
effectiveBuyYes := min(yesAsk, 1 - noBid)

// 买 NO: min(NO.ask, 1 - YES.bid)
effectiveBuyNo := min(noAsk, 1 - yesBid)

// 卖 YES: max(YES.bid, 1 - NO.ask)
effectiveSellYes := max(yesBid, 1 - noAsk)

// 卖 NO: max(NO.bid, 1 - YES.ask)
effectiveSellNo := max(noBid, 1 - yesAsk)
```

### 为什么有效价格更优？

1. **考虑镜像订单簿**：
   - Polymarket 的订单簿有镜像特性：买 YES @ P = 卖 NO @ (1-P)
   - 同一订单会出现在两个订单簿中
   - 有效价格考虑了这种镜像关系，找到最优路径

2. **避免重复计算**：
   - 简单相加 `YES.ask + NO.ask` 会重复计算镜像订单
   - 有效价格避免了这个问题

3. **找到最优路径**：
   - 比较直接买入和通过镜像订单买入的成本
   - 选择成本更低的方式

## 📝 代码变更

### 主要变更

1. **价格获取**：
   - 从 `GetBestPrice(entryAsset)` 改为 `GetTopOfBook(market)`
   - 同时获取 YES 和 NO 的价格，确保一致性

2. **价格计算**：
   - 从互补价公式改为有效价格计算
   - 考虑镜像订单簿，找到最优价格

3. **价格验证**：
   - 添加总成本检查
   - 添加价格滑点保护

4. **日志增强**：
   - 添加详细的有效价格计算日志
   - 记录价格来源和对比

## 🎯 下一步建议

1. **监控效果**：
   - 观察实际成交价格是否更接近预期
   - 检查是否还有价格滑点导致的亏损

2. **进一步优化**：
   - 考虑添加订单簿深度检查
   - 考虑添加部分成交保护（sizeSafetyFactor）
   - 考虑添加自动修复不平衡机制

3. **测试验证**：
   - 在测试环境验证价格计算的准确性
   - 对比优化前后的成本差异

---

**优化时间**: 2025-12-25  
**状态**: ✅ 已完成并编译通过  
**参考**: `@catalyst-team/poly-sdk` 的有效价格计算实现

