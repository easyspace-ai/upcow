# velocitypairlock（速度触发双向限价对冲 + 自动合并释放资金）

## 目标（对应你的需求）

- **触发条件**：当 UP 或 DOWN 的价格在指定窗口内的“变化速度（分/秒）”达到阈值
- **下单动作**：同时在 UP 与 DOWN 两边挂 **BUY 限价单（GTC）**
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
  - 事件驱动主逻辑（价格触发、异步下单、监听成交、触发 auto-merge）

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
      cooldownMs: 3000

      profitCents: 3
      orderSize: 5
      minEntryPriceCents: 5
      maxEntryPriceCents: 95
      minOrderUSDC: 1.01

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

