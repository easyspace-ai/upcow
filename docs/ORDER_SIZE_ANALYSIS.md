# 订单数量分析报告

## 📋 问题发现

从日志分析发现，**Entry 和 Hedge 订单的数量不相等**：

### 实际日志数据（orderSize=4 时）

**策略触发**: DOWN @ 82¢, Hedge=15¢

**订单成交**:
- Entry order (82¢): `filledSize=4.0000` shares
- Hedge order (15¢): `filledSize=7.3300` shares

**问题**: 数量不相等！Entry=4, Hedge=7.33

## 🔍 代码逻辑分析

### 当前代码逻辑

```go
// size：确保满足最小金额/最小 shares（GTC）
entryShares := ensureMinOrderSize(orderSize, entryAskDec, minOrderSize)
hedgeShares := ensureMinOrderSize(hedgeSize, hedgeDec, minOrderSize)
if hedgeShares < minShareSize {
    hedgeShares = minShareSize
}
```

### ensureMinOrderSize 函数

```go
func ensureMinOrderSize(desiredShares float64, price float64, minUSDC float64) float64 {
    if desiredShares <= 0 || price <= 0 {
        return desiredShares
    }
    if minUSDC <= 0 {
        minUSDC = 1.0
    }
    minShares := minUSDC / price
    if minShares > desiredShares {
        return minShares
    }
    return desiredShares
}
```

### 问题根源

**当 orderSize=4 时**:
- Entry (82¢): `ensureMinOrderSize(4, 0.82, 1.1)` = `max(4, 1.1/0.82)` = `max(4, 1.34)` = **4 shares**
- Hedge (15¢): `ensureMinOrderSize(4, 0.15, 1.1)` = `max(4, 1.1/0.15)` = `max(4, 7.33)` = **7.33 shares**

**结果**: Entry=4, Hedge=7.33 ❌ 不相等

## ✅ 解决方案

### 方案 1: 增加 orderSize（已实施）

**当 orderSize=8 时**:
- Entry (82¢): `ensureMinOrderSize(8, 0.82, 1.1)` = `max(8, 1.34)` = **8 shares**
- Hedge (15¢): `ensureMinOrderSize(8, 0.15, 1.1)` = `max(8, 7.33)` = **8 shares**

**结果**: Entry=8, Hedge=8 ✅ 相等

### 方案 2: 统一数量计算逻辑

**问题**: Entry 没有 `minShareSize` 检查，但 Hedge 有。这可能导致不一致。

**建议**: 对 Entry 也应用 `minShareSize` 检查，或者移除 Hedge 的 `minShareSize` 检查，统一使用 `ensureMinOrderSize`。

```go
// 统一逻辑：两边都使用 ensureMinOrderSize，确保数量相等
entryShares := ensureMinOrderSize(orderSize, entryAskDec, minOrderSize)
hedgeShares := ensureMinOrderSize(hedgeSize, hedgeDec, minOrderSize)

// 可选：如果两边价格差异很大，强制使用相同的数量
// 这样可以确保成本更平衡
if entryShares != hedgeShares {
    // 使用较大的数量，确保两边相等
    maxShares := math.Max(entryShares, hedgeShares)
    entryShares = maxShares
    hedgeShares = maxShares
}
```

### 方案 3: 基于成本计算数量

**目标**: 确保 Entry 和 Hedge 的成本相近

```go
// 计算目标成本（基于较低价格）
targetCost := math.Min(orderSize * entryAskDec, orderSize * hedgeDec)
if targetCost < minOrderSize {
    targetCost = minOrderSize
}

// 基于目标成本计算数量
entryShares := targetCost / entryAskDec
hedgeShares := targetCost / hedgeDec

// 确保满足最小金额
entryShares = math.Max(entryShares, minOrderSize / entryAskDec)
hedgeShares = math.Max(hedgeShares, minOrderSize / hedgeDec)
```

## 📊 不同 orderSize 的效果对比

| orderSize | Entry (82¢) | Hedge (15¢) | 是否相等 | Entry 成本 | Hedge 成本 |
|-----------|-------------|-------------|---------|-----------|-----------|
| **4** | 4 shares | 7.33 shares | ❌ 不相等 | $3.28 | $1.10 |
| **8** | 8 shares | 8 shares | ✅ 相等 | $6.56 | $1.20 |
| **10** | 10 shares | 10 shares | ✅ 相等 | $8.20 | $1.50 |
| **15** | 15 shares | 15 shares | ✅ 相等 | $12.30 | $2.25 |

## 🎯 推荐方案

**推荐使用方案 1 + 方案 2 组合**:

1. **增加 orderSize 到 8**（已实施）✅
   - 确保在低价时也能满足 minOrderSize
   - 减少自动调整带来的不确定性

2. **统一数量计算逻辑**（建议实施）
   - 移除 Hedge 的 `minShareSize` 特殊处理
   - 或者对 Entry 也应用 `minShareSize` 检查
   - 确保两边数量始终相等

3. **添加日志输出**（建议）
   - 在下单前输出 `entryShares` 和 `hedgeShares`
   - 方便调试和验证数量是否相等

## 📝 代码修改建议

```go
// 修改前
entryShares := ensureMinOrderSize(orderSize, entryAskDec, minOrderSize)
hedgeShares := ensureMinOrderSize(hedgeSize, hedgeDec, minOrderSize)
if hedgeShares < minShareSize {
    hedgeShares = minShareSize
}

// 修改后（方案 1：统一逻辑）
entryShares := ensureMinOrderSize(orderSize, entryAskDec, minOrderSize)
hedgeShares := ensureMinOrderSize(hedgeSize, hedgeDec, minOrderSize)
// 移除 Hedge 的特殊处理，或对 Entry 也应用
if entryShares < minShareSize {
    entryShares = minShareSize
}
if hedgeShares < minShareSize {
    hedgeShares = minShareSize
}

// 修改后（方案 2：强制相等）
entryShares := ensureMinOrderSize(orderSize, entryAskDec, minOrderSize)
hedgeShares := ensureMinOrderSize(hedgeSize, hedgeDec, minOrderSize)
// 确保两边数量相等
maxShares := math.Max(entryShares, hedgeShares)
entryShares = maxShares
hedgeShares = maxShares
log.Infof("📊 [%s] 订单数量: Entry=%d shares, Hedge=%d shares (已统一)", ID, int(entryShares), int(hedgeShares))
```

---

**报告生成时间**: 2025-12-25  
**问题**: Entry 和 Hedge 订单数量不相等  
**解决方案**: 增加 orderSize 到 8，统一数量计算逻辑

