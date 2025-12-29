# targetNotional 说明

## 📖 什么是 Notional？

**Notional（名义金额）** 是金融术语，指的是**交易的总价值**，而不是交易的数量。

在 cyclehedge 策略中，`targetNotional` 表示：**每个周期计划投入的总资金规模（USDC）**。

---

## 🎯 targetNotional 的作用

### 1. 定义每周期的资金投入目标

`targetNotional` 决定了每个周期要投入多少 USDC 来建仓。

**示例**：
- 配置：`fixedNotionalUSDC: 50`
- 含义：每个周期投入 **50 USDC** 来建仓

---

### 2. 用于计算目标 Shares

策略使用 `targetNotional` 来计算需要买入多少 shares：

```go
// 计算公式
costCents := 100 - chosenProfit  // 成本（分）
shares := targetNotional * 100.0 / float64(costCents)
```

**示例计算**：
- `targetNotional = 50 USDC`
- `chosenProfit = 4 cents`（锁定的利润）
- `costCents = 100 - 4 = 96 cents`（每 share 的成本）
- `shares = 50 * 100 / 96 = 52.08 shares`

**含义**：
- 投入 50 USDC
- 买入 52.08 shares 的 UP token
- 买入 52.08 shares 的 DOWN token
- 总成本：52.08 * 0.96 = 50 USDC
- 结算时：52.08 * 1.00 = 52.08 USDC
- **利润：2.08 USDC（4 cents per share）**

---

## 🔄 targetNotional 的计算方式

### 方式1：固定模式（FixedNotionalUSDC > 0）

**配置**：
```yaml
fixedNotionalUSDC: 50  # 固定投入 50 USDC
```

**计算逻辑**：
```go
tn = s.FixedNotionalUSDC  // 50
// 安全护栏：不超过可用余额
cap := bal * alloc  // 余额 * 分配比例
if tn > cap {
    tn = cap  // 如果超过余额，限制到余额
}
```

**示例**：
- 配置：`fixedNotionalUSDC: 50`
- 余额：6000 USDC
- 结果：`targetNotional = 50`（可以使用完整的50）

**如果余额不足**：
- 配置：`fixedNotionalUSDC: 50`
- 余额：30 USDC
- 结果：`targetNotional = 30`（被余额限制）

---

### 方式2：滚动模式（FixedNotionalUSDC == 0）

**配置**：
```yaml
fixedNotionalUSDC: 0  # 不固定，按余额滚动
minNotionalUSDC: 30
maxNotionalUSDC: 3000
balanceAllocationPct: 0.8  # 使用80%的余额
```

**计算逻辑**：
```go
tn = math.Max(s.MinNotionalUSDC, bal * s.BalanceAllocationPct)
tn = math.Min(tn, s.MaxNotionalUSDC)
tn = math.Max(tn, s.MinNotionalUSDC)
```

**示例**：
- 余额：1000 USDC
- `balanceAllocationPct: 0.8`
- 计算：`tn = max(30, 1000*0.8) = 800`
- 限制：`tn = min(800, 3000) = 800`
- 结果：`targetNotional = 800`

**含义**：
- 每个周期使用余额的80%来建仓
- 如果余额增长，投入也会增长（滚动复利）
- 如果余额减少，投入也会减少（风险控制）

---

## 📊 实际例子

### 例子1：固定50 USDC

**配置**：
```yaml
fixedNotionalUSDC: 50
```

**执行**：
1. 周期重置时：`targetNotional = 50`
2. 选择 profit：`chosenProfit = 4 cents`
3. 计算 shares：`shares = 50 * 100 / 96 = 52.08`
4. 下单：买入 52.08 shares UP + 52.08 shares DOWN
5. 成本：52.08 * 0.96 = 50 USDC
6. 结算：52.08 * 1.00 = 52.08 USDC
7. **利润：2.08 USDC（4 cents per share）**

---

### 例子2：余额限制

**配置**：
```yaml
fixedNotionalUSDC: 50
```

**情况**：
- 余额：30 USDC（不足50）
- 安全护栏：`targetNotional = min(50, 30) = 30`

**执行**：
1. 周期重置时：`targetNotional = 30`（被限制）
2. 选择 profit：`chosenProfit = 4 cents`
3. 计算 shares：`shares = 30 * 100 / 96 = 31.25`
4. 下单：买入 31.25 shares UP + 31.25 shares DOWN
5. 成本：31.25 * 0.96 = 30 USDC
6. 结算：31.25 * 1.00 = 31.25 USDC
7. **利润：1.25 USDC（4 cents per share）**

---

## 🔍 关键点总结

### 1. targetNotional 是资金规模，不是数量

- ✅ `targetNotional = 50` 表示投入 **50 USDC**
- ❌ 不是买入 50 shares

### 2. targetNotional 决定目标 Shares

- 公式：`shares = targetNotional * 100 / costCents`
- costCents = 100 - profitCents（锁定的利润）

### 3. targetNotional 可能被余额限制

- 如果余额不足，会被限制到可用余额
- 这是**安全护栏**，防止资金不足导致单边成交

### 4. 固定模式 vs 滚动模式

- **固定模式**：每周期固定投入（如50 USDC）
- **滚动模式**：按余额比例投入（如80%的余额）

---

## 💡 为什么需要 targetNotional？

### 1. 资金管理

- 控制每周期的资金投入
- 避免过度投入导致风险

### 2. 利润锁定

- 通过 `targetNotional` 和 `profitCents` 计算目标 shares
- 确保锁定预期的利润（如4 cents per share）

### 3. 风险控制

- 安全护栏：不超过可用余额
- 防止资金不足导致单边成交

---

## 📝 配置建议

### 小资金测试（推荐）

```yaml
fixedNotionalUSDC: 50      # 固定投入50 USDC
minNotionalUSDC: 30        # 最小投入30 USDC
maxNotionalUSDC: 100       # 最大投入100 USDC
```

### 滚动复利模式

```yaml
fixedNotionalUSDC: 0       # 不固定，按余额滚动
minNotionalUSDC: 30        # 最小投入30 USDC
maxNotionalUSDC: 3000      # 最大投入3000 USDC
balanceAllocationPct: 0.8  # 使用80%的余额
```

---

## 🎯 总结

**targetNotional** = 每个周期计划投入的总资金规模（USDC）

- 用于计算目标 shares
- 可能被余额限制（安全护栏）
- 可以是固定值或按余额滚动

**公式**：
```
shares = targetNotional * 100 / (100 - profitCents)
```

**示例**：
- `targetNotional = 50 USDC`
- `profitCents = 4 cents`
- `shares = 50 * 100 / 96 = 52.08 shares`
