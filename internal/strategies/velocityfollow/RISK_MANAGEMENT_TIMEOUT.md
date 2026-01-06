# 风险管理系统 - 超时逻辑梳理

## 配置参数

根据你的配置文件 `yml/velocityfollow.yaml`：
```yaml
aggressiveHedgeTimeoutSeconds: 15  # Entry成交后，Hedge未成交超过此时间（秒）则激进对冲
maxAcceptableLossCents: 5          # 激进对冲时允许的最大亏损（分）
riskManagementCheckIntervalMs: 5000 # 风险检查间隔（毫秒），默认 5 秒
```

## 完整流程

### 1. Entry 订单成交 → 注册风险敞口

**触发时机**：Entry 订单状态变为 `Filled`

**执行逻辑**：
```go
// 在 OMS.OnOrderUpdate 中
if order.IsEntryOrder && order.IsFilled() {
    // 注册到 RiskManager
    o.riskManager.RegisterEntry(order, hedgeOrderID)
}
```

**注册内容**：
- Entry 订单信息（ID、价格、数量、成交时间）
- Hedge 订单 ID（如果已创建）
- 初始状态：`HedgeStatus = Pending`

### 2. 风险监控循环

**监控频率**：每 5 秒检查一次（`riskManagementCheckIntervalMs: 5000`）

**检查内容**：
1. 更新每个风险敞口的持续时间：`ExposureSeconds = now - EntryFilledTime`
2. 检查 Hedge 订单状态
3. 如果 Hedge 已成交，移除风险敞口
4. 如果 Hedge 未成交且超过超时时间，触发激进对冲

### 3. 倒计时计算

**计算公式**：
```go
countdownSeconds = aggressiveHedgeTimeoutSeconds - ExposureSeconds
// 你的配置：countdownSeconds = 15 - ExposureSeconds
```

**显示逻辑**：
- 如果 `countdownSeconds > 0`：显示剩余时间（如 "倒计时:10s"）
- 如果 `countdownSeconds <= 0`：显示 "倒计时:超时"

### 4. 超时触发条件

**触发条件**（同时满足）：
1. `ExposureSeconds >= aggressiveHedgeTimeoutSeconds`（你的配置：>= 15 秒）
2. Hedge 订单状态仍为 `Pending` 或 `Open`（未成交）
3. Hedge 订单仍然存在（可以通过 `GetOrder` 获取到）

**触发时机**：
- 在 `monitorLoop` 的每次检查中（每 5 秒）
- 如果满足条件，立即触发（异步执行）

### 5. 激进对冲执行流程

**执行步骤**（在 `aggressiveHedge` 函数中）：

1. **获取 Market 对象**
   ```go
   positions := rm.tradingService.GetOpenPositionsForMarket(exp.MarketSlug)
   // 从持仓中获取 market 对象
   ```
   - ⚠️ **可能失败**：如果无法获取 market 对象，激进对冲失败，状态保持 "Idle"

2. **取消旧 Hedge 订单**（如果存在）
   ```go
   if hedgeOrder.OrderID != "" {
       rm.tradingService.CancelOrder(ctx, hedgeOrder.OrderID)
       time.Sleep(500 * time.Millisecond) // 等待撤单确认
   }
   ```
   - 更新状态：`currentAction = "canceling"`

3. **获取当前订单簿价格**
   ```go
   _, yesAsk, _, noAsk, _, err := rm.tradingService.GetTopOfBook(ctx, market)
   ```
   - ⚠️ **可能失败**：如果获取价格失败，激进对冲失败

4. **计算预期亏损并检查**
   ```go
   totalCostCents = EntryPriceCents + HedgeAskCents
   expectedLossCents = totalCostCents - 100
   lossMultiplier = expectedLossCents / maxAcceptableLossCents
   ```
   
   **策略选择**：
   - **如果亏损 <= 阈值**：正常执行对冲
   - **如果亏损 > 阈值但 <= 2倍阈值**：仍然执行对冲（小亏总比大亏好，避免价格继续恶化）
   - **如果亏损 > 2倍阈值**：**拒绝执行对冲**，记录严重警告
     - 原因：如果价格已经跑得太远，对冲可能造成巨大亏损
     - 建议：等待价格回调或手动处理

5. **下 FAK 订单立即成交**
   ```go
   fakHedgeOrder := &domain.Order{
       OrderType: types.OrderTypeFAK,  // FAK = Fill And Kill，立即成交
       Price: hedgeAskPrice,           // 以 ask 价吃单
       Size: exp.EntrySize,
   }
   hedgeResult, err := rm.tradingService.PlaceOrder(ctx, fakHedgeOrder)
   ```
   - ⚠️ **可能失败**：如果下单失败，激进对冲失败

6. **更新状态**
   - 如果 FAK 订单立即成交：移除风险敞口，状态恢复 "Idle"
   - 如果 FAK 订单未立即成交：更新 Hedge 订单 ID，继续监控

## 为什么显示"超时"但状态是"Idle"？

### 可能的原因

1. **激进对冲已触发但还在处理中**
   - 激进对冲在 goroutine 中异步执行
   - 状态可能还在 "canceling" 或 "aggressive_hedging"
   - 但 UI 更新可能有延迟

2. **激进对冲失败**
   - **无法获取 Market 对象**：`❌ 无法获取market对象，无法执行激进对冲`
   - **获取订单簿价格失败**：`❌ 获取订单簿价格失败，无法执行激进对冲`
   - **下单失败**：`❌ 激进对冲下单失败`

3. **检查间隔延迟**
   - 监控循环每 5 秒检查一次
   - 如果刚好在检查间隔之间，可能还没触发

4. **Hedge 订单已不存在**
   - 如果 `GetOrder(hedgeOrderID)` 返回失败，会记录警告但不触发激进对冲
   - 这种情况应该立即触发（因为没有对冲单，风险更大）

## 调试建议

### 1. 查看日志

查找以下关键日志：
```
🚨 检测到风险敞口超时: entryOrderID=... exposure=... hedgeOrderID=... hedgeStatus=...
🚀 执行激进对冲: 以ask价FAK吃单 price=... size=... expectedLoss=...
✅ 激进对冲订单已提交: orderID=... price=... size=... expectedLoss=...
❌ 无法获取market对象，无法执行激进对冲
❌ 获取订单簿价格失败，无法执行激进对冲
❌ 激进对冲下单失败
```

### 2. 检查状态

在 Dashboard 中查看：
- **CurrentAction**：应该是 "canceling" 或 "aggressive_hedging"（如果正在处理）
- **CurrentActionDesc**：显示当前操作描述
- **TotalAggressiveHedges**：激进对冲总次数（应该增加）

### 3. 检查配置

确认配置正确：
```yaml
riskManagementEnabled: true
aggressiveHedgeTimeoutSeconds: 15
maxAcceptableLossCents: 5
riskManagementCheckIntervalMs: 5000
```

## 超过最大亏损阈值的处理策略

### 当前策略（分级处理）

当激进对冲时，如果预期亏损超过 `maxAcceptableLossCents`，采用分级策略：

1. **亏损 <= 阈值**（例如 <= 5分）
   - ✅ **正常执行对冲**
   - 这是理想情况

2. **亏损 > 阈值但 <= 2倍阈值**（例如 5-10分）
   - ⚠️ **仍然执行对冲**
   - 原因：小亏总比大亏好，避免价格继续恶化导致更大损失
   - 日志：`⚠️ 预期亏损超过最大可接受值，但仍执行对冲（避免更大风险）`

3. **亏损 > 2倍阈值**（例如 > 10分）
   - 🚨 **拒绝执行对冲**
   - 原因：如果价格已经跑得太远，对冲可能造成巨大亏损，不如等待价格回调或手动处理
   - 日志：`🚨 拒绝激进对冲：预期亏损严重超过阈值 (2.0x)，价格已跑得太远`
   - 状态：`currentActionDesc = "拒绝对冲：亏损过大 (2.0x阈值)"`

### 策略原理

**为什么采用 2 倍阈值作为分界线？**

- **<= 2倍阈值**：价格波动在可接受范围内，及时止损比等待更好
- **> 2倍阈值**：价格已经严重偏离，可能：
  - 市场出现异常波动
  - 订单簿深度不足导致价格失真
  - 等待价格回调可能更合理

### 配置建议

根据你的风险承受能力调整 `maxAcceptableLossCents`：

- **保守策略**：`maxAcceptableLossCents: 3`（拒绝阈值 = 6分）
- **当前配置**：`maxAcceptableLossCents: 5`（拒绝阈值 = 10分）
- **激进策略**：`maxAcceptableLossCents: 10`（拒绝阈值 = 20分）

### 手动处理建议

如果系统拒绝执行对冲（亏损 > 2倍阈值），建议：

1. **查看日志**：确认具体亏损金额和倍数
2. **评估市场**：判断价格是否可能回调
3. **手动决策**：
   - 如果价格可能回调：等待
   - 如果价格继续恶化：手动执行对冲
   - 如果市场异常：考虑平仓止损

## 改进建议

如果发现超时后没有处理，可以考虑：

1. **降低检查间隔**：从 5 秒改为 1-2 秒，更快响应
2. **增加日志**：在关键步骤添加更详细的日志
3. **重试机制**：如果激进对冲失败，可以重试
4. **状态显示**：在 UI 中显示失败原因
5. **调整阈值**：根据实际情况调整 `maxAcceptableLossCents` 和 2 倍阈值策略