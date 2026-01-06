# VelocityFollow 策略完整文档

## 目录
1. [策略概述](#策略概述)
2. [核心逻辑](#核心逻辑)
3. [执行流程](#执行流程)
4. [配置参数](#配置参数)
5. [订单执行机制](#订单执行机制)
6. [风险控制](#风险控制)
7. [出场逻辑](#出场逻辑)
8. [队列机制](#队列机制)
9. [保护机制](#保护机制)

---

## 策略概述

### 策略原理
VelocityFollow（速度跟随策略）是一个基于价格变化速度的套利策略：

1. **监控速度**：实时监控 UP/DOWN 价格变化速度
2. **触发条件**：当某一侧速度超过阈值时触发交易
3. **套利逻辑**：
   - **Entry**：买入速度更快的一侧（FAK 订单，立即成交）
   - **Hedge**：买入对侧（GTC 限价单，等待成交）
   - **目标**：总成本 < 100c，无论哪方胜出都有利润

### 示例场景
- UP 迅速拉升到 70c，触发：
  - 吃单买 UP @ 70c（Entry）
  - 同时挂 DOWN 买单 @ (100-70-1)=29c（Hedge）
  - 总成本 = 70 + 29 = 99c，锁定 1c 利润

---

## 核心逻辑

### 1. 速度计算

#### 样本收集
- 每次价格更新时，记录 `(timestamp, priceCents)` 样本
- 维护两个样本队列：`samples[UP]` 和 `samples[DOWN]`
- 自动清理过期样本（超过 `WindowSeconds`）

#### 速度计算
```go
// 计算窗口内的价格变化
move = 最新价格 - 窗口内最早价格
velocity = move / 时间窗口（秒）

// 触发条件
move >= MinMoveCents  &&  velocity >= MinVelocityCentsPerSec
```

#### 速度指标
- `windowSeconds`: 速度计算窗口（默认 10 秒）
- `minMoveCents`: 窗口内最小上行位移（默认 3 分）
- `minVelocityCentsPerSec`: 最小速度（默认 0.3 分/秒）

### 2. 方向选择

#### 基本逻辑
- 同时计算 UP 和 DOWN 的速度
- 选择速度更快的一侧作为 `winner`
- 如果两侧都满足条件，选择速度更快的一侧

#### 价格优先选择（可选）
- `preferHigherPrice`: 当两侧速度相同时，优先选择价格更高的一侧
- 因为订单簿是镜像的，价格更高的胜率更大

### 3. 周期管理

#### 周期检测
- 通过 `OnCycle()` 回调检测周期切换
- 更新 `cycleStartMs` 和重置周期状态

#### 周期限制
- `warmupMs`: 启动/换周期后的预热窗口（默认 800ms）
- `maxTradesPerCycle`: 每周期最多交易次数（默认 6，0=不设限）
- `cycleEndProtectionMinutes`: 周期结束前保护时间（默认 3 分钟）

---

## 执行流程

### 主流程：OnPriceChanged

```
价格更新事件 (OnPriceChanged)
    ↓
1. 市场过滤：只处理目标市场
    ↓
2. 自动合并检查（可选）
    ↓
3. 出场逻辑检查（优先于开仓）
    ↓
4. 周期检测和状态更新
    ↓
5. 预热窗口检查
    ↓
6. 冷却时间检查
    ↓
7. 速度计算和方向选择
    ↓
8. Binance Bias 过滤（可选）
    ↓
9. 并发控制检查（pendingHedges）
    ↓
10. 决策引擎评估
    ↓
11. 订单执行（放入队列）
```

### 详细步骤

#### 步骤 1-2: 市场过滤和自动合并
- 只处理配置的目标市场（`marketSlugPrefix`）
- 如果启用自动合并，检查是否满足合并条件

#### 步骤 3: 出场逻辑（优先）
- 如果存在持仓，优先处理出场逻辑
- 检查止盈/止损/超时条件
- 如果触发出场，跳过开仓逻辑

#### 步骤 4-6: 周期和冷却检查
- **预热窗口**：周期开始后 `warmupMs` 内不交易
- **冷却时间**：上次触发后 `cooldownMs` 内不交易
- **交易次数限制**：每周期最多 `maxTradesPerCycle` 次

#### 步骤 7: 速度计算
- 更新价格样本
- 计算 UP/DOWN 的速度
- 选择速度更快的一侧作为 `winner`

#### 步骤 8: Binance Bias（可选）
- 如果启用 `useBinanceOpen1mBias`：
  - 等待开盘 1m K 线
  - 根据 K 线阴阳判断 bias
  - `hard` 模式：只允许 bias 方向
  - `soft` 模式：逆势门槛更高

#### 步骤 9: 并发控制
- 检查 `pendingHedges`：是否有未完成的对冲单
- 检查实际持仓：是否有未对冲的持仓
- 如果存在未对冲风险，跳过本次下单

#### 步骤 10: 决策引擎评估
- **市场质量评估**：流动性、价差、订单簿年龄
- **价格稳定性检查**：价格变化率是否过大
- **订单簿价格获取**：获取最新的 bid/ask
- **价格区间检查**：Entry 价格是否在允许范围内
- **总成本检查**：Entry + Hedge 是否 <= 100c

#### 步骤 11: 订单执行
- 创建订单执行请求
- 放入队列（异步处理）
- 立即返回（不阻塞主流程）

---

## 配置参数

### 交易参数

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `orderSize` | float64 | 1.2 | Entry 订单 shares 数量（固定数量） |
| `hedgeOrderSize` | float64 | 0 | Hedge 订单 shares 数量（0=跟随 orderSize） |

### 速度判定参数

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `windowSeconds` | int | 10 | 速度计算窗口（秒） |
| `minMoveCents` | int | 3 | 窗口内最小上行位移（分） |
| `minVelocityCentsPerSec` | float64 | 0.3 | 最小速度（分/秒） |
| `cooldownMs` | int | 1500 | 触发冷却（毫秒） |
| `warmupMs` | int | 800 | 启动/换周期后的预热窗口（毫秒） |
| `maxTradesPerCycle` | int | 6 | 每周期最多交易次数（0=不设限） |

### 下单安全参数

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `hedgeOffsetCents` | int | 1 | 对侧挂单价 = (100 - entryAskCents - offset) |
| `minEntryPriceCents` | int | 63 | 吃单价下限（分），0=不设下限 |
| `maxEntryPriceCents` | int | 89 | 吃单价上限（分），避免 99/100 假盘口 |
| `maxSpreadCents` | int | 2 | 盘口价差上限（分），0=不设限 |

### 订单执行模式

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `orderExecutionMode` | string | "sequential" | "sequential" \| "parallel" |
| `sequentialCheckIntervalMs` | int | 20 | 检查订单状态的间隔（毫秒） |
| `sequentialMaxWaitMs` | int | 2000 | 最大等待时间（毫秒） |

### 对冲单重下机制

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `hedgeReorderTimeoutSeconds` | int | 15 | 对冲单重下超时时间（秒） |
| `hedgeTimeoutFakSeconds` | int | 0 | 对冲单超时后以 FAK 吃单的时间（秒），0=禁用 |
| `allowNegativeProfitOnHedgeReorder` | bool | true | 是否允许负收益 |
| `maxNegativeProfitCents` | int | 5 | 最大允许负收益（分） |

### 风险管理系统

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `riskManagementEnabled` | bool | true | 是否启用风险管理系统 |
| `riskManagementCheckIntervalMs` | int | 5000 | 风险检查间隔（毫秒） |
| `aggressiveHedgeTimeoutSeconds` | int | 60 | Entry成交后，Hedge未成交超过此时间则激进对冲 |
| `maxAcceptableLossCents` | int | 5 | 激进对冲时允许的最大亏损（分） |

### 出场参数

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `takeProfitCents` | int | 0 | 止盈阈值（分），0=禁用 |
| `stopLossCents` | int | 0 | 止损阈值（分），0=禁用 |
| `maxHoldSeconds` | int | 0 | 最大持仓时间（秒），0=禁用 |
| `exitCooldownMs` | int | 1500 | 出场冷却（毫秒） |
| `exitBothSidesIfHedged` | bool | false | 若同周期同时持有 UP/DOWN，则同时卖出平仓 |

### 分批止盈

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `partialTakeProfits` | array | [] | 分批止盈列表，空数组表示禁用 |

示例：
```yaml
partialTakeProfits:
  - profitCents: 3
    fraction: 0.5
  - profitCents: 6
    fraction: 0.5
```

### 追踪止盈

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `enableTrailingTakeProfit` | bool | false | 是否启用追踪止盈 |
| `trailStartCents` | int | 4 | 达到该利润后开始追踪（分） |
| `trailDistanceCents` | int | 2 | 回撤触发距离（分） |

### 市场指标相关配置

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `priceStabilityCheckEnabled` | bool | true | 是否启用价格稳定性检查 |
| `maxPriceChangePercent` | float64 | 2.0 | 最大价格变化百分比 |
| `priceChangeWindowSeconds` | int | 5 | 价格变化检查窗口（秒） |
| `minLiquidityScore` | float64 | 2.0 | 最小流动性评分（0-10） |
| `minDepthAt1Percent` | float64 | 10.0 | 1% 深度最小要求（shares） |
| `minTotalLiquidity` | float64 | 20.0 | 最小总流动性（shares） |
| `maxSpreadVolatilityPercent` | float64 | 50.0 | 最大价差波动百分比 |

---

## 订单执行机制

### 执行模式

#### Sequential（顺序模式）
```
1. 下 Entry 订单（FAK，立即成交或取消）
   ↓
2. 等待 Entry 订单成交（轮询检查订单状态）
   ↓
3. Entry 成交后，下 Hedge 订单（GTC 限价单）
   ↓
4. 监控 Hedge 订单成交状态
```

**优势**：
- 风险低：确保 Entry 成交后再下 Hedge
- 适合 FAK 订单：FAK 订单通常立即成交

**参数**：
- `sequentialCheckIntervalMs`: 检查订单状态的间隔（默认 20ms）
- `sequentialMaxWaitMs`: 最大等待时间（默认 2000ms）

#### Parallel（并发模式）
```
1. 同时提交 Entry 和 Hedge 订单
   ↓
2. 监控两个订单的成交状态
   ↓
3. 如果 Entry 成交但 Hedge 未成交，启动监控
```

**优势**：
- 速度快：同时提交，减少延迟
- 适合快速市场：减少价格变化风险

**风险**：
- 如果 Entry 成交但 Hedge 失败，存在风险敞口

### 订单数量计算

#### 基础计算
```go
// OrderSize 和 HedgeOrderSize 是 shares 数量（固定数量）
entryShares = orderSize
hedgeShares = hedgeOrderSize (如果为0，则等于 entryShares)
```

#### 最小订单金额检查
```go
// 如果订单金额 < 最小订单金额，需要调整 shares
entryAmount = entryShares * entryPrice
if entryAmount < minOrderSize {
    entryShares = minOrderSize / entryPrice
}
```

#### 确保 Entry 和 Hedge shares 相等
```go
// 取两者最小值，确保完全对冲
minShares = min(entryShares, hedgeShares)
entryShares = minShares
hedgeShares = minShares
```

#### 最小 shares 检查
```go
// 确保 shares >= minShareSize
if entryShares < minShareSize {
    entryShares = minShareSize
}
if hedgeShares < minShareSize {
    hedgeShares = minShareSize
}
```

#### 精度调整
```go
// 确保 maker amount = size × price 是 2 位小数
entryShares = adjustSizeForMakerAmountPrecision(entryShares, entryPrice)
hedgeShares = adjustSizeForMakerAmountPrecision(hedgeShares, hedgePrice)
```

### 价格计算

#### Entry 价格
- 使用订单簿的 **卖一价**（asks[0]）或 **卖二价**（asks[1]）
- 优先使用卖二价（如果存在且合理），提供价格缓冲
- 价格调整后，需要重新进行精度调整

#### Hedge 价格
```go
hedgeLimitCents = 100 - entryAskCents - hedgeOffsetCents
```

- 防止"挂单穿价"：如果 `hedgeLimitCents >= hedgeAskCentsDirect`，则调整为 `hedgeAskCentsDirect - 1`
- 总成本检查：`entryAskCents + hedgeLimitCents <= 100`

### 订单提交

#### Entry 订单
- **类型**：FAK（Fill or Kill）
- **价格**：订单簿卖一价或卖二价
- **数量**：计算后的 `entryShares`

#### Hedge 订单
- **类型**：GTC（Good Till Cancel）
- **价格**：`100 - entryAskCents - hedgeOffsetCents`
- **数量**：计算后的 `hedgeShares`

---

## 风险控制

### 1. 并发控制

#### pendingHedges 跟踪
- 当 Entry 订单提交后，立即记录到 `pendingHedges[entryOrderID] = ""`
- 当 Hedge 订单提交后，更新为 `pendingHedges[entryOrderID] = hedgeOrderID`
- 当 Hedge 订单成交后，从 `pendingHedges` 中删除

#### 检查机制
- **ExecuteEntry 检查**：下单前检查是否有未完成的对冲单
- **executeSequential/executeParallel 检查**：执行前再次检查
- **持仓检查**：检查实际持仓是否完全对冲

#### 规则
- 如果存在未完成的对冲单，**不允许**开启新的交易
- 如果存在未对冲的持仓，**不允许**开启新的交易

### 2. 对冲单重下机制

#### 阶段 1：重新下 GTC 限价单
- **触发条件**：Entry 成交后 `hedgeReorderTimeoutSeconds` 内 Hedge 未成交
- **操作**：撤销旧单，重新计算价格，下新的 GTC 限价单
- **允许负收益**：如果 `allowNegativeProfitOnHedgeReorder=true`，允许小亏（不超过 `maxNegativeProfitCents`）

#### 阶段 2：强制 FAK 吃单
- **触发条件**：Entry 成交后 `hedgeTimeoutFakSeconds` 内 Hedge 未成交
- **操作**：撤单并以卖一价 FAK 吃单（强制立即成交）
- **目的**：防止亏损过多，不能长时间有风险敞口

### 3. 风险管理系统

#### 监控机制
- **检查间隔**：每 `riskManagementCheckIntervalMs` 检查一次
- **监控对象**：所有 Entry 已成交但 Hedge 未成交的订单

#### 激进对冲
- **触发条件**：Entry 成交后 `aggressiveHedgeTimeoutSeconds` 内 Hedge 未成交
- **操作**：计算当前最大亏损，如果亏损 <= `maxAcceptableLossCents`，则激进对冲
- **原则**：可以小亏，不能大亏

### 4. 市场质量过滤

#### 市场质量评估
- **流动性评分**：基于订单簿深度和流动性
- **价差检查**：检查 bid-ask 价差
- **订单簿年龄**：检查订单簿数据的新鲜度

#### 拒绝条件
- 市场质量 < `minLiquidityScore`
- 价差 > `maxSpreadCents`
- 订单簿年龄 > `marketQualityMaxBookAgeMs`

### 5. 价格稳定性检查

#### 检查机制
- **窗口**：最近 `priceChangeWindowSeconds` 内的价格变化
- **阈值**：价格变化率 <= `maxPriceChangePercent`

#### 拒绝条件
- 如果价格变化率 > `maxPriceChangePercent`，拒绝下单

---

## 出场逻辑

### 出场优先级

```
1. 硬止损 / 超时：优先全平
   ↓
2. 追踪止盈：达到 TrailStart 后开始追踪；跌破 stop 触发全平
   ↓
3. 硬止盈：达到 takeProfitCents 直接全平
   ↓
4. 分批止盈：达到 level 后卖出 fraction（每个 level 只触发一次）
```

### 硬止损
- **条件**：`当前 bid - 入场均价 <= -stopLossCents`
- **操作**：全平（卖出所有持仓）

### 超时退出
- **条件**：持仓时间 >= `maxHoldSeconds`
- **操作**：全平（卖出所有持仓）

### 追踪止盈
- **启动条件**：利润 >= `trailStartCents`
- **追踪机制**：
  - 记录最高 bid 价格
  - 计算 stop 价格 = 最高 bid - `trailDistanceCents`
  - 如果当前 bid <= stop，触发全平

### 硬止盈
- **条件**：`当前 bid - 入场均价 >= takeProfitCents`
- **操作**：全平（卖出所有持仓）

### 分批止盈
- **条件**：利润 >= `partialTakeProfits[i].profitCents`
- **操作**：卖出 `当前剩余持仓 × fraction`（每个 level 只触发一次）

### 出场冷却
- **目的**：避免短时间重复下 SELL
- **参数**：`exitCooldownMs`（默认 1500ms）

### 双边持仓处理
- **选项**：`exitBothSidesIfHedged`
- **行为**：如果同时持有 UP 和 DOWN，可以同时卖出平仓

---

## 队列机制

### 工作原理

#### 架构
```
价格更新事件 (OnPriceChanged)
    ↓
检查条件通过
    ↓
创建订单执行请求 (orderExecutionRequest)
    ↓
放入队列 (orderQueue channel, 缓冲 100)
    ↓
立即返回（不等待结果）
    ↓
[独立 goroutine] 队列 Worker
    ↓
从队列取出请求
    ↓
串行执行订单（executeSequential/executeParallel）
    ↓
发送结果到 req.result channel（但接收方已不等待）
```

#### 队列结构
- **类型**：`chan *orderExecutionRequest`
- **缓冲**：100 个请求
- **特点**：非阻塞写入（如果队列满则丢弃请求）

#### Worker Goroutine
- **启动时机**：`Initialize()` 时启动，只启动一次（使用 `sync.Once`）
- **运行方式**：独立的 goroutine，持续运行直到程序退出
- **处理方式**：串行处理，同一时间只处理一个请求

### 优点
1. **防止并发下单**：确保同一时间只有一个订单在执行
2. **不阻塞主流程**：价格更新事件不会被阻塞，UI 可以正常刷新
3. **串行化保证**：第一个订单执行完成后，才会执行第二个订单
4. **避免竞态条件**：消除了检查 `hasPendingHedge` 和实际下单之间的时间窗口

### 副作用和潜在问题

#### 1. 价格过时（最严重）
- **问题**：请求在队列中等待时，价格可能已经变化
- **影响**：Entry/Hedge 价格可能不匹配，导致总成本 > 100c
- **当前代码**：`executeSequential` 直接使用请求中的 `entryPrice` 和 `hedgePrice`

#### 2. 队列满时请求被丢弃
- **问题**：如果队列已满（100 个请求），新请求会被丢弃
- **影响**：可能错过交易机会

#### 3. 无错误反馈机制
- **问题**：请求放入队列后立即返回，不等待结果
- **影响**：无法知道订单是否成功

#### 4. Worker 阻塞导致队列积压
- **问题**：如果 `executeSequential` 执行很慢，队列会积压
- **影响**：延迟增加，价格可能已经变化

#### 5. Worker Panic 导致队列停止
- **问题**：如果 worker goroutine panic，队列会停止处理
- **影响**：队列中的请求永远不会被处理

### 改进建议

#### 1. 价格刷新机制
在 worker 处理请求前，重新获取最新价格：
```go
// 在 worker 中，处理请求前
latestPrice, err := s.TradingService.GetBestPrice(execCtx, entryAsset)
if err == nil {
    // 使用最新价格，而不是请求中的价格
    req.entryPrice = latestPrice
}
```

#### 2. 添加错误反馈机制
虽然不阻塞，但可以异步通知：
```go
// 使用回调或事件通知
if req.onComplete != nil {
    req.onComplete(err)
}
```

#### 3. 添加队列监控
- 监控队列长度
- 如果队列积压，记录警告
- 如果队列满，可以考虑增加缓冲或拒绝新请求

#### 4. 添加 Panic 恢复
```go
defer func() {
    if r := recover(); r != nil {
        log.Errorf("队列 worker panic: %v", r)
        // 重启 worker 或记录错误
    }
}()
```

---

## 保护机制

### 1. 市场过滤
- 只处理配置的目标市场（`marketSlugPrefix`）
- 防止误交易其他市场

### 2. 价格区间保护
- `minEntryPriceCents`: Entry 价格下限（避免低价时 size 被放大）
- `maxEntryPriceCents`: Entry 价格上限（避免 99/100 假盘口）

### 3. 价差保护
- `maxSpreadCents`: 盘口价差上限（避免极差盘口误触发）

### 4. 周期保护
- `warmupMs`: 启动/换周期后的预热窗口
- `cycleEndProtectionMinutes`: 周期结束前保护时间

### 5. 冷却机制
- `cooldownMs`: 触发冷却（避免频繁交易）
- `exitCooldownMs`: 出场冷却（避免频繁重复下卖单）

### 6. 交易次数限制
- `maxTradesPerCycle`: 每周期最多交易次数

### 7. 订单簿验证
- 下单前验证订单簿价格
- 如果价格偏差 > 5c，跳过下单

### 8. 流动性检查
- 检查订单簿深度是否足够
- 检查总流动性是否足够

### 9. 精度调整
- 确保 `size × price` 是 2 位小数（满足交易所要求）

### 10. 总成本检查
- 确保 `entryAskCents + hedgeLimitCents <= 100`
- 如果总成本 > 100c，拒绝下单

---

## 总结

### 策略特点
1. **速度驱动**：基于价格变化速度触发交易
2. **套利逻辑**：通过 Entry + Hedge 锁定利润
3. **风险控制**：多重保护机制，确保不会出现大额亏损
4. **自动化**：全自动执行，无需人工干预

### 关键设计
1. **队列机制**：串行化订单执行，避免并发风险
2. **并发控制**：`pendingHedges` 跟踪，确保对冲完成
3. **风险管理系统**：实时监控风险敞口，激进对冲
4. **决策引擎**：综合评估市场质量、价格稳定性等

### 注意事项
1. **价格过时问题**：队列中的请求可能使用过时的价格
2. **队列积压**：如果执行慢，队列可能积压
3. **错误反馈**：当前无错误反馈机制
4. **Worker Panic**：如果 worker panic，队列会停止

### 改进方向
1. 在 worker 处理请求前重新获取最新价格
2. 添加错误反馈机制
3. 添加队列监控和 Panic 恢复
4. 优化执行速度，减少队列积压
