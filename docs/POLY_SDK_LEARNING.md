# Poly-SDK 学习总结：订单簿与套利价格计算

## 🔍 核心发现

### 1. Polymarket 订单簿的镜像特性

**关键洞察**：买 YES @ P = 卖 NO @ (1-P)

```
┌─────────────────────────────────────────────────────────────┐
│                    等价关系                                   │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│   买 YES @ $0.40  ≡  卖 NO @ $0.60                          │
│   卖 YES @ $0.40  ≡  买 NO @ $0.60                          │
│                                                             │
│   一般形式:                                                 │
│   买 YES @ P  ≡  卖 NO @ (1-P)                              │
│   卖 YES @ P  ≡  买 NO @ (1-P)                              │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

**为什么这样？**
- 做市商持有 YES + NO 对（通过 Split $1 获得）
- 卖 YES @ $0.40 = 收到 $0.40
- 买 NO @ $0.60 = 支付 $0.60，但获得完整的 YES+NO 对，可以 Merge 获得 $1
- 净收益 = $1 - $0.60 = $0.40（等价）

### 2. 有效价格（Effective Prices）计算

**问题**：直接使用 YES.ask + NO.ask 是错误的，因为同一订单被计算了两次！

**正确方法**：使用有效价格，考虑镜像订单的影响

```typescript
function getEffectivePrices(
  yesAsk: number,  // YES token 的最低卖价
  yesBid: number,  // YES token 的最高买价
  noAsk: number,   // NO token 的最低卖价
  noBid: number    // NO token 的最高买价
): {
  effectiveBuyYes: number;   // 买 YES 的最低成本
  effectiveBuyNo: number;    // 买 NO 的最低成本
  effectiveSellYes: number;   // 卖 YES 的最高收入
  effectiveSellNo: number;    // 卖 NO 的最高收入
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

### 3. 套利计算

**Long Arbitrage（买双边 + Merge）**:
```typescript
const effective = getEffectivePrices(yesAsk, yesBid, noAsk, noBid);
const longCost = effective.effectiveBuyYes + effective.effectiveBuyNo;
const longProfit = 1 - longCost;

// 当 longCost < 1 时，存在套利机会
```

**Short Arbitrage（Split + 卖双边）**:
```typescript
const effective = getEffectivePrices(yesAsk, yesBid, noAsk, noBid);
const shortRevenue = effective.effectiveSellYes + effective.effectiveSellNo;
const shortProfit = shortRevenue - 1;

// 当 shortRevenue > 1 时，存在套利机会
```

## ⚠️ 我们策略的问题

### 当前问题

我们的策略直接使用：
```go
entryCost := float64(entryAskCents) / 100.0 * entryFilledSize
hedgeCost := float64(hedgeAskCents) / 100.0 * hedgeFilledSize
totalCost := entryCost + hedgeCost
```

**问题**：
- 如果 Entry = YES @ 60c, Hedge = NO @ 41c
- 直接相加：60c + 41c = 101c > 100c
- 但这**没有考虑镜像订单**！

### 正确的计算方式

应该使用有效价格：
```go
// Entry = YES, Hedge = NO
effectiveBuyYes := min(yesAsk, 1 - noBid)  // min(60c, 1 - noBid)
effectiveBuyNo := min(noAsk, 1 - yesBid)   // min(41c, 1 - yesBid)

// 如果 noBid = 40c, yesBid = 59c
effectiveBuyYes := min(60c, 60c) = 60c
effectiveBuyNo := min(41c, 41c) = 41c
// 总成本 = 60c + 41c = 101c（仍然 > 100c）

// 但如果 noBid = 39c（更好）
effectiveBuyYes := min(60c, 61c) = 60c
effectiveBuyNo := min(41c, 41c) = 41c
// 总成本 = 60c + 41c = 101c（仍然 > 100c）
```

**关键发现**：
- 即使使用有效价格，如果订单簿价差（spread）> 0，总成本仍然可能 > $1.00
- 这是因为 spread = 交易成本
- 在正常市场中，spread > 0，所以没有套利机会

## 💡 对我们的策略的启示

### 1. 策略逻辑是正确的

我们的策略是**赚波动的钱**，不是做套利：
- Entry 是 FAK（吃单），价格是 ask 价格
- Hedge 是 GTC（挂单），价格也是 ask 价格
- 如果市场价格波动，Hedge 可能以更好价格成交

### 2. 盈亏计算需要改进

**当前**：使用下单时的 ask 价格
**应该**：使用实际成交价格（我们已经修复了）

**但更重要的是**：
- 如果 Entry + Hedge 的 ask 价格总和 > 100c，说明订单簿有价差
- 这是**正常的**，因为 spread = 交易成本
- 策略通过价格波动赚钱，而不是通过套利

### 3. 价格保护机制

我们之前移除了总成本保护（因为策略逻辑不同），这是**正确的**：
- 策略不是做套利的，不需要总成本 <= $1.00
- 策略通过价格波动赚钱，允许总成本 > $1.00
- 关键是 Hedge 是限价单，实际成交价格可能更好

## 📋 建议

### 1. 保持当前策略逻辑

- ✅ Entry: FAK（吃单，立即成交）
- ✅ Hedge: GTC（挂单，等待更好价格）
- ✅ 通过价格波动赚钱

### 2. 改进盈亏计算

- ✅ 使用实际成交价格（已修复）
- ✅ 记录价格差异日志（已添加）
- ⚠️ 考虑添加有效价格计算（可选，用于分析）

### 3. 监控和优化

- 📊 监控实际成交价格 vs 下单价格
- 📊 如果 Hedge 实际成交价格经常 = ask 价格，说明策略可能有问题
- 📊 如果 Hedge 实际成交价格经常 < ask 价格，说明策略工作正常

## 🎯 总结

1. **镜像订单**：买 YES @ P = 卖 NO @ (1-P)
2. **有效价格**：考虑镜像订单后的最优价格
3. **套利计算**：使用有效价格，而不是直接相加
4. **我们的策略**：不是套利策略，是波动策略，允许总成本 > $1.00
5. **关键修复**：使用实际成交价格计算盈亏（已完成）

