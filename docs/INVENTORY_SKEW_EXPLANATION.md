# 库存偏斜机制（Inventory Skew）详解

## 📚 核心概念

### 1. 什么是库存偏斜？

**库存偏斜（Inventory Skew）**是一种做市商策略中的风险控制机制，通过动态调整报价来平衡持仓，避免单边持仓过大。

### 2. 为什么需要库存偏斜？

**问题场景**：
```
假设我们的策略一直在买入 UP 方向：
- 第1次：Entry UP + Hedge DOWN ✅
- 第2次：Entry UP + Hedge DOWN ✅
- 第3次：Entry UP + Hedge DOWN ✅
...
- 第10次：Entry UP + Hedge DOWN ✅

结果：
- UP 持仓：10 个 Entry 订单 = 10 * 6.5 = 65 shares
- DOWN 持仓：10 个 Hedge 订单 = 10 * 6.5 = 65 shares
- 净持仓：65 - 65 = 0（看起来平衡）

但实际情况：
- 如果某个 Hedge 订单未成交，净持仓 = 65 - 0 = 65（UP 方向）
- 如果多个 Hedge 订单未成交，净持仓会越来越大
- 风险敞口越来越大！
```

**库存偏斜的作用**：
- 当净持仓偏向某个方向时，降低该方向的交易频率
- 避免单边持仓过大，降低风险敞口

## 🔍 Bot v5.1 的实现逻辑

### 1. 计算净持仓

```javascript
const netInv = this.inventory.UP - this.inventory.DOWN;
```

**含义**：
- `netInv > 0`：持有更多 UP，偏向 UP 方向
- `netInv < 0`：持有更多 DOWN，偏向 DOWN 方向
- `netInv = 0`：持仓平衡

### 2. 计算偏斜值

```javascript
const skew = netInv * this.inventorySkewFactor;
// inventorySkewFactor = 0.005 / 100 = 0.00005
```

**含义**：
- 偏斜值与净持仓成正比
- 净持仓越大，偏斜值越大
- 偏斜值用于调整报价

### 3. 调整报价

```javascript
const resPriceUp = finalFairUp - skew;
const resPriceDown = finalFairDown + skew;
```

**逻辑**：
- 如果 `netInv > 0`（持有更多 UP）：
  - `skew > 0`
  - `resPriceUp = finalFairUp - skew`（降低 UP 的买入价）
  - `resPriceDown = finalFairDown + skew`（提高 DOWN 的买入价）
  - **效果**：降低 UP 方向的交易频率，提高 DOWN 方向的交易频率

- 如果 `netInv < 0`（持有更多 DOWN）：
  - `skew < 0`
  - `resPriceUp = finalFairUp - skew`（提高 UP 的买入价）
  - `resPriceDown = finalFairDown + skew`（降低 DOWN 的买入价）
  - **效果**：提高 UP 方向的交易频率，降低 DOWN 方向的交易频率

### 4. 交易决策

```javascript
// 如果允许交易且库存未超限
if (allowTradeUp && netInv < this.stopQuoteThreshold) {
    // 只有当净持仓 < 阈值时，才允许交易 UP
}

if (allowTradeDown && netInv > -this.stopQuoteThreshold) {
    // 只有当净持仓 > -阈值时，才允许交易 DOWN
}
```

**逻辑**：
- `stopQuoteThreshold = 60`：停止报价阈值
- 如果 `netInv > 60`：停止 UP 方向的交易
- 如果 `netInv < -60`：停止 DOWN 方向的交易

## 💡 对我们的策略的适用性

### 我们的策略特点

1. **Entry + Hedge 配对**：
   - Entry 订单（FAK，立即成交）
   - Hedge 订单（GTC，可能未成交）

2. **风险敞口**：
   - 如果 Hedge 订单未成交，存在风险敞口
   - 如果多个 Hedge 订单未成交，风险敞口会累积

3. **持仓跟踪**：
   - 我们需要跟踪 UP 和 DOWN 的持仓
   - 计算净持仓（UP - DOWN）

### 实现方式

**方案 1: 基于订单状态计算净持仓**

```go
// 获取所有已成交的 Entry 订单
entryOrders := getFilledEntryOrders()
upInventory := sum(entryOrders where TokenType == UP)
downInventory := sum(entryOrders where TokenType == DOWN)

// 获取所有已成交的 Hedge 订单
hedgeOrders := getFilledHedgeOrders()
upInventory -= sum(hedgeOrders where TokenType == UP)  // Hedge UP 减少 UP 持仓
downInventory -= sum(hedgeOrders where TokenType == DOWN)  // Hedge DOWN 减少 DOWN 持仓

netPosition := upInventory - downInventory
```

**方案 2: 基于 Position 计算净持仓**

```go
// 获取所有开放仓位
positions := s.TradingService.GetOpenPositions()
upInventory := 0.0
downInventory := 0.0

for _, pos := range positions {
    if pos.TokenType == domain.TokenTypeUp {
        upInventory += pos.Size
    } else {
        downInventory += pos.Size
    }
}

netPosition := upInventory - downInventory
```

**方案 3: 简化版本（基于订单计数）**

```go
// 跟踪已成交的 Entry 订单数量
entryFilledCount := map[domain.TokenType]int{
    domain.TokenTypeUp: 0,
    domain.TokenTypeDown: 0,
}

// 跟踪已成交的 Hedge 订单数量
hedgeFilledCount := map[domain.TokenType]int{
    domain.TokenTypeUp: 0,
    domain.TokenTypeDown: 0,
}

// 计算净持仓（简化：假设每个订单大小相同）
netPosition := float64(entryFilledCount[UP] - entryFilledCount[DOWN] - hedgeFilledCount[UP] + hedgeFilledCount[DOWN]) * averageOrderSize
```

## 🎯 推荐实现方案

### 方案：基于订单状态计算净持仓（推荐）

**优点**：
- ✅ 准确：基于实际成交的订单
- ✅ 实时：订单状态实时更新
- ✅ 简单：不需要额外的持仓跟踪

**实现步骤**：
1. 在 `OnPriceChanged` 中，获取所有活跃订单
2. 筛选已成交的 Entry 订单和 Hedge 订单
3. 计算净持仓
4. 如果净持仓超过阈值，降低该方向的交易频率

### 逻辑流程

```
1. 获取所有活跃订单
   ↓
2. 筛选已成交的订单
   - Entry 订单（IsEntryOrder = true, Status = Filled）
   - Hedge 订单（IsEntryOrder = false, Status = Filled）
   ↓
3. 计算净持仓
   UP 持仓 = sum(Entry UP orders) - sum(Hedge UP orders)
   DOWN 持仓 = sum(Entry DOWN orders) - sum(Hedge DOWN orders)
   净持仓 = UP 持仓 - DOWN 持仓
   ↓
4. 检查阈值
   if netPosition > threshold && winner == UP:
       跳过 UP 方向的交易
   if netPosition < -threshold && winner == DOWN:
       跳过 DOWN 方向的交易
```

## 📋 配置参数

```yaml
inventoryThreshold: 50.0  # 净持仓阈值（shares）
# 如果净持仓 > 50，停止 UP 方向的交易
# 如果净持仓 < -50，停止 DOWN 方向的交易
```

## 🔍 示例场景

### 场景 1: 正常情况

```
Entry 订单：
- Entry UP @ 60c (已成交) ✅
- Entry UP @ 62c (已成交) ✅
- Entry UP @ 64c (已成交) ✅

Hedge 订单：
- Hedge DOWN @ 40c (已成交) ✅
- Hedge DOWN @ 38c (已成交) ✅
- Hedge DOWN @ 36c (已成交) ✅

净持仓 = (3 - 0) - (0 - 3) = 0 ✅ 平衡
→ 允许继续交易
```

### 场景 2: Hedge 订单未成交

```
Entry 订单：
- Entry UP @ 60c (已成交) ✅
- Entry UP @ 62c (已成交) ✅
- Entry UP @ 64c (已成交) ✅

Hedge 订单：
- Hedge DOWN @ 40c (已成交) ✅
- Hedge DOWN @ 38c (未成交) ⚠️
- Hedge DOWN @ 36c (未成交) ⚠️

净持仓 = (3 - 0) - (0 - 1) = 2 (UP 方向)
→ 如果 threshold = 2，停止 UP 方向的交易
→ 只允许 DOWN 方向的交易（可以平仓）
```

### 场景 3: 多个 Hedge 订单未成交

```
Entry 订单：
- Entry UP @ 60c (已成交) ✅
- Entry UP @ 62c (已成交) ✅
- Entry UP @ 64c (已成交) ✅
- Entry UP @ 66c (已成交) ✅
- Entry UP @ 68c (已成交) ✅

Hedge 订单：
- Hedge DOWN @ 40c (已成交) ✅
- Hedge DOWN @ 38c (未成交) ⚠️
- Hedge DOWN @ 36c (未成交) ⚠️
- Hedge DOWN @ 34c (未成交) ⚠️
- Hedge DOWN @ 32c (未成交) ⚠️

净持仓 = (5 - 0) - (0 - 1) = 4 (UP 方向)
→ 如果 threshold = 50，仍然允许交易（但应该降低频率）
→ 如果 threshold = 4，停止 UP 方向的交易
```

## ⚠️ 注意事项

1. **持仓计算**：
   - 需要考虑订单的实际成交数量（FilledSize），而不是下单数量（Size）
   - 需要考虑订单的实际成交价格，而不是下单价格

2. **周期隔离**：
   - 只计算当前周期的持仓，不计算旧周期的持仓
   - 周期切换时，重置持仓计数

3. **阈值设置**：
   - 阈值应该根据订单大小设置
   - 如果 `sizePerTrade = 6.5`，`threshold = 50` 意味着约 7-8 个订单的净持仓

4. **动态调整**：
   - 可以考虑根据剩余时间动态调整阈值
   - 周期结束前，可以降低阈值，更严格地控制持仓

