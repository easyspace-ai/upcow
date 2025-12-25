# Poly-SDK 学习总结：盘口和套利

## 📚 项目概述

`@catalyst-team/poly-sdk` 是一个用于 Polymarket 的 TypeScript SDK，提供了完整的交易、套利检测和市场数据分析功能。

## 🔑 核心概念

### 1. Polymarket 订单簿的镜像特性

**关键发现**：Polymarket 订单簿有一个容易被忽略的镜像特性

```
买 YES @ P = 卖 NO @ (1-P)
```

这意味着**同一订单会出现在两个订单簿中**。例如：
- 一个 "卖 NO @ 0.50" 的订单会同时作为 "买 YES @ 0.50" 出现在 YES 订单簿中
- 一个 "买 YES @ 0.66" 的订单会同时作为 "卖 NO @ 0.34" 出现在 NO 订单簿中

**常见错误**：
```typescript
// ❌ 错误: 简单相加会重复计算镜像订单
const askSum = YES.ask + NO.ask;  // ≈ 1.998-1.999，而非 ≈ 1.0
const bidSum = YES.bid + NO.bid;  // ≈ 0.001-0.002，而非 ≈ 1.0
```

### 2. 有效价格（Effective Prices）

**正确做法**：使用有效价格来避免重复计算镜像订单

```typescript
// 计算考虑镜像后的最优价格
const effective = getEffectivePrices(yesAsk, yesBid, noAsk, noBid);

// effective.effectiveBuyYes = min(YES.ask, 1 - NO.bid)
// effective.effectiveBuyNo = min(NO.ask, 1 - YES.bid)
// effective.effectiveSellYes = max(YES.bid, 1 - NO.ask)
// effective.effectiveSellNo = max(NO.bid, 1 - YES.ask)
```

**实现逻辑**（来自 `price-utils.ts`）：
```typescript
export function getEffectivePrices(
  yesAsk: number,
  yesBid: number,
  noAsk: number,
  noBid: number
): {
  effectiveBuyYes: number;
  effectiveBuyNo: number;
  effectiveSellYes: number;
  effectiveSellNo: number;
} {
  return {
    // 买 YES: 直接买 YES.ask 或 通过卖 NO (成本 = 1 - NO.bid)
    effectiveBuyYes: Math.min(yesAsk, 1 - noBid),

    // 买 NO: 直接买 NO.ask 或 通过卖 YES (成本 = 1 - YES.bid)
    effectiveBuyNo: Math.min(noAsk, 1 - yesBid),

    // 卖 YES: 直接卖 YES.bid 或 通过买 NO (收入 = 1 - NO.ask)
    effectiveSellYes: Math.max(yesBid, 1 - noAsk),

    // 卖 NO: 直接卖 NO.bid 或 通过买 YES (收入 = 1 - YES.ask)
    effectiveSellNo: Math.max(noBid, 1 - yesAsk),
  };
}
```

### 3. 套利检测

**Long Arbitrage（做多套利）**：
- 策略：买入 YES + NO（有效成本 < $1）→ Merge → $1 USDC
- 利润 = 1 - (effectiveBuyYes + effectiveBuyNo)

**Short Arbitrage（做空套利）**：
- 策略：卖出预先持有的 YES + NO（有效收入 > $1）
- 利润 = (effectiveSellYes + effectiveSellNo) - 1

**检测逻辑**：
```typescript
export function checkArbitrage(
  yesAsk: number,
  noAsk: number,
  yesBid: number,
  noBid: number
): { type: 'long' | 'short'; profit: number; description: string } | null {
  const effective = getEffectivePrices(yesAsk, yesBid, noAsk, noBid);

  // Long arbitrage: Buy complete set (YES + NO) cheaper than $1
  const effectiveLongCost = effective.effectiveBuyYes + effective.effectiveBuyNo;
  const longProfit = 1 - effectiveLongCost;

  if (longProfit > 0) {
    return {
      type: 'long',
      profit: longProfit,
      description: `Buy YES @ ${effective.effectiveBuyYes.toFixed(4)} + NO @ ${effective.effectiveBuyNo.toFixed(4)}, Merge for $1`,
    };
  }

  // Short arbitrage: Sell complete set (YES + NO) for more than $1
  const effectiveShortRevenue = effective.effectiveSellYes + effective.effectiveSellNo;
  const shortProfit = effectiveShortRevenue - 1;

  if (shortProfit > 0) {
    return {
      type: 'short',
      profit: shortProfit,
      description: `Split $1, Sell YES @ ${effective.effectiveSellYes.toFixed(4)} + NO @ ${effective.effectiveSellNo.toFixed(4)}`,
    };
  }

  return null;
}
```

## 💡 对我们策略的启示

### 问题分析

我们当前的 `velocityfollow` 策略存在以下问题：

1. **使用互补价公式计算 Hedge 价格**：
   ```go
   hedgeCents := 100 - askCents - hedgeOffset
   ```
   这假设了 `YES.price + NO.price = 1`，但实际上由于镜像订单簿的特性，这个假设可能不准确。

2. **没有考虑镜像订单簿**：
   - 我们直接使用 `GetBestPrice` 获取的价格可能不是最优价格
   - 应该使用有效价格（effective prices）来找到最优的买入/卖出价格

### 改进建议

1. **使用有效价格计算 Hedge 价格**：
   ```go
   // 当前代码（问题）
   hedgeCents := 100 - askCents - hedgeOffset

   // 应该改为：使用有效价格
   // 1. 获取 YES 和 NO 的实际市场价格
   yesBestBid, yesBestAsk, _ := s.TradingService.GetBestPrice(orderCtx, market.YesAssetID)
   noBestBid, noBestAsk, _ := s.TradingService.GetBestPrice(orderCtx, market.NoAssetID)

   // 2. 计算有效价格
   effectiveBuyNo := min(noBestAsk, 1 - yesBestBid)
   effectiveSellNo := max(noBestBid, 1 - yesBestAsk)

   // 3. 如果选择 UP，Hedge 是买 NO，使用 effectiveBuyNo
   hedgeCents := int(effectiveBuyNo * 100)
   ```

2. **在下单前检查价格变化**：
   - 使用有效价格可以确保我们使用的是最优价格
   - 如果价格变化超过阈值，取消下单

3. **考虑订单簿深度**：
   - 使用 `sizeSafetyFactor`（例如 0.8）来避免部分成交
   - 检查订单簿深度，确保有足够的流动性

## 🎯 ArbitrageService 的最佳实践

### 1. 部分成交保护

```typescript
// 使用 sizeSafetyFactor 避免部分成交
const safetyFactor = 0.8; // 只使用 80% 的订单簿深度
const orderbookLongSize = Math.min(yesAsks[0]?.size || 0, noAsks[0]?.size || 0) * safetyFactor;
```

### 2. 自动修复不平衡

```typescript
// 如果一侧订单失败，自动卖出多余的代币
if (buyYesResult.success !== buyNoResult.success) {
  await this.fixImbalanceIfNeeded();
}
```

### 3. 实时监控订单簿

```typescript
// 使用 WebSocket 实时监控订单簿变化
this.wsManager.on('bookUpdate', this.handleBookUpdate.bind(this));

private handleBookUpdate(update: BookUpdate): void {
  // 更新订单簿状态
  // 检查套利机会
  this.checkAndHandleOpportunity();
}
```

### 4. 再平衡机制

```typescript
// 自动维持 USDC 和代币的平衡
if (usdcRatio > maxUsdcRatio) {
  // USDC 太多，Split 创建代币
  await this.ctf.split(conditionId, amount);
} else if (usdcRatio < minUsdcRatio) {
  // USDC 太少，Merge 回收 USDC
  await this.ctf.mergeByTokenIds(conditionId, tokenIds, amount);
}
```

## 📊 关键代码片段

### 订单簿处理

```typescript
// 排序订单簿（bids 从高到低，asks 从低到高）
this.orderbook.yesBids = bids.sort((a, b) => b.price - a.price);
this.orderbook.yesAsks = asks.sort((a, b) => a.price - b.price);
this.orderbook.noBids = bids.sort((a, b) => b.price - a.price);
this.orderbook.noAsks = asks.sort((a, b) => a.price - b.price);
```

### 套利机会检测

```typescript
checkOpportunity(): ArbitrageOpportunity | null {
  const { yesBids, yesAsks, noBids, noAsks } = this.orderbook;
  
  const yesBestBid = yesBids[0]?.price || 0;
  const yesBestAsk = yesAsks[0]?.price || 1;
  const noBestBid = noBids[0]?.price || 0;
  const noBestAsk = noAsks[0]?.price || 1;

  // 计算有效价格
  const effective = getEffectivePrices(yesBestAsk, yesBestBid, noBestAsk, noBestBid);

  // 检查套利机会
  const longCost = effective.effectiveBuyYes + effective.effectiveBuyNo;
  const longProfit = 1 - longCost;
  
  if (longProfit > this.config.profitThreshold) {
    // 找到套利机会
    return { type: 'long', profitRate: longProfit, ... };
  }
  
  return null;
}
```

## 🔧 实施建议

1. **立即修复**：使用有效价格计算 Hedge 价格
2. **添加价格滑点保护**：在下单前检查价格变化
3. **优化订单执行**：使用 `sizeSafetyFactor` 避免部分成交
4. **监控订单簿**：实时监控订单簿变化，及时调整策略

---

**学习时间**: 2025-12-25  
**来源**: `@catalyst-team/poly-sdk`  
**状态**: ✅ 已学习并总结关键概念

