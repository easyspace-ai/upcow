# velocitypairlock（速度触发双向限价对冲 + 自动合并释放资金）

## 目标（对应你的需求）

- **触发条件**：当 UP 或 DOWN 的价格在指定窗口内的“变化速度（分/秒）”达到阈值
  - 默认：只在**正速率**（价格上涨）时触发主单（你们要的：正速率时下主单）
- **下单动作**：支持两种模式
  - **并发下单**：同时下 UP 与 DOWN（主/对冲的下单方式可分别配置：限价/吃单）
  - **顺序下单**：先下“主 leg”，主 leg 成交后再下“对冲 leg”（同样支持主/对冲分别配置限价/吃单）
- **锁定利润**：两边目标成交价满足 `UP + DOWN <= 100 - profitCents`
  - 示例：`profitCents=3`，当选择 `UP=70` 则 `DOWN=27`（锁 3 个点）
- **资金复用**：两边都成交后，触发 **merge complete sets（YES+NO -> USDC）** 释放资金
- **循环开单**：merge 完成并刷新余额后，策略回到 idle，允许下一次触发

## 模块拆分（便于维护）

- `config.go`
  - 策略最小配置集（窗口/速度/利润/下单数量/自动合并）
  - `Defaults/Validate` 负责参数兜底与硬校验
- `velocity.go`
  - `VelocityTracker`：滑动窗口速度计算（分/秒）
- `pricer.go`
  - `PricePairLock`：利润锁定定价（primary + hedge = 100 - profit）
- `state.go`
  - 策略运行期状态机（idle/placing/open/filled/merging/cooldown）
- `strategy.go`
  - 事件驱动主逻辑（价格触发、并发/顺序下单、监听成交、对冲后盯盘止损、触发 auto-merge）

## 配置方式（bbgo main 风格）

在你的策略 YAML 里挂载：

```yaml
exchangeStrategies:
  - on: polymarket
    velocitypairlock:
      enabled: true
      windowSeconds: 10
      minMoveCents: 3
      minVelocityCentsPerSec: 0.3
      velocityDirectionMode: positive
      primaryPickMode: max_velocity
      cooldownMs: 3000

      profitCents: 3
      orderSize: 5
      minEntryPriceCents: 5
      maxEntryPriceCents: 95
      minOrderUSDC: 1.01

      # ===== 下单模式 =====
      # parallel | sequential
      orderExecutionMode: sequential
      # 主/对冲下单方式：
      # - limit: 限价挂单（GTC，使用锁利目标价）
      # - taker: 吃单（FAK，使用 bestAsk + takerOffsetCents）
      primaryOrderStyle: limit
      hedgeOrderStyle: limit
      takerOffsetCents: 0
      # 顺序模式 gate：只在主 leg 价格处于区间时才允许“先主后对冲”
      sequentialPrimaryMinCents: 40
      sequentialPrimaryMaxCents: 80
      sequentialPrimaryMaxWaitMs: 2000

      # ===== 成交确认（WS -> API 兜底）=====
      wsFillConfirmTimeoutSeconds: 5
      cancelIfNotFilledAfterConfirm: true

      # ===== 对冲后实时盯盘锁损 =====
      priceStopEnabled: true
      priceStopCheckIntervalMs: 200
      # 亏损区间：-5~-10（soft=-5, hard=-10）
      priceStopSoftLossCents: -5
      priceStopHardLossCents: -10
      # 最大可接受亏损（超过则拒绝自动锁损）
      maxAcceptableLossCents: 20

      cycleEndProtectionMinutes: 1
      maxTradesPerCycle: 0

      autoMerge:
        enabled: true
        minCompleteSets: 5
        intervalSeconds: 15
        reconcileAfterMerge: true
        reconcileMaxWaitSeconds: 30
        mergeTriggerDelaySeconds: 30
        metadata: "velocitypairlock:autoMerge"
```

> 说明：本策略当前实现是“**单对单（同一时刻最多 1 对）**”，更贴合“资金有限、成交后 merge 再继续”的工作方式。

