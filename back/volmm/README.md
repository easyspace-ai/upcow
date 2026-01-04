# volmm：盘中波动做市（BTC 15m Up/Down）

本策略用于 `btc-updown-15m-*`：利用 Chainlink（RTDS）给出本期起点价（strike）与实时价，计算 UP 的“公平概率”，并在盘口上做 **四边做市**（UP 买/卖、DOWN 买/卖），同时用 **库存倾斜**把净敞口控制在可承受范围内。交易窗口与“最后阶段尽量不交易”的逻辑**全部可配置**。

---

## 一、现在这套策略能测试吗？

可以。当前仓库内已完成：
- **编译/单元编译校验**：`go test ./...` 已通过
- **dry-run 集成测试**：在 `dry_run: true` 下，策略会真实连接：
  - 市场 WS（读 UP/DOWN 盘口）
  - RTDS（订阅 Chainlink `btc/usd`）
  - 下单逻辑会被执行，但不会真实成交/产生真实风险（由 TradingService 的 dry-run 行为决定）

需要注意：
- dry-run 能验证“**逻辑是否跑通、下单参数是否符合 tick/min shares、risk-only 是否按时间窗切换**”，但无法验证真实成交质量（maker 填单、被扫单、撤改竞争等）。

---

## 二、如何做 dry-run 测试（推荐）

1) 使用策略文件（已提供示例）：
- `yml/volmm.yaml`（默认 `dry_run: true`）

2) 启动（任选其一）：

```bash
go run ./cmd/bot -strategy volmm
```

或

```bash
go run ./cmd/bot -strategies yml/volmm.yaml
```

3) 你应该能观察到：
- 周期切换日志（`[volmm] 周期切换`）
- strike 设置日志（收到本周期第一条 Chainlink 报价后：`[volmm] strike 已设置`）
- 随后开始进入“常规交易窗口”报价与撤改/下单（dry-run 下通常会打印下单信息到日志文件）
- 到达 `tradeStopAtSeconds` 后进入 `risk-only`，并按配置撤单/降风险

---

## 三、模拟交易例子（帮助理解策略如何运作）

> 以下是**示意**，用于理解流程，不代表真实会出现的价格/成交序列。tick 为 `0.001`，最小下单 shares 为 `5`。

### 例子配置（节选）

- `tradeStartAtSeconds = 0`
- `tradeStopAtSeconds = 720`（第 12 分钟进入 risk-only）
- `quoteSizeShares = 5`
- `deltaMaxShares = 10`
- `riskOnlyEnabled = true`
- `riskOnlyCancelAllQuotes = true`
- `riskOnlyAllowFlatten = true`
- `riskOnlyMaxDeltaShares = 0`（进入 risk-only 目标把净敞口压回 0）

### 0:00（开盘）—— strike 设置

- 本期开始时间：`T0 = 00:00`
- RTDS 收到第一条 `chainlink btc/usd`：`S0 = 43000`
- 策略记录：`strikePrice = 43000`，后续所有概率计算都围绕这个起点。

### 2:00（盘中）—— 形成四边报价

假设此刻：
- Chainlink 实时价：`St = 43018`
- 距离结算剩余：`τ = 780s`
- 动能特征较弱（速度/加速度≈0），先忽略动能项

策略算出（示意）：
- `FairUp = p = 0.53`
- `FairDown = 0.47`
- 动态半点差 `s = 0.003`
- 当前库存：`Qup=0, Qdown=0 => deltaInv=0`，库存倾斜 `skew=0`

则目标报价（示意，均会按 tick=0.001 舍入）：
- **UP**
  - buy：`p - s = 0.527`
  - sell：`p + s = 0.533`
- **DOWN**
  - buy：`(1-p) - s = 0.467`
  - sell：`(1-p) + s = 0.473`

策略会尝试在盘口上维护 4 张 GTC：
- UP buy 5 shares @ 0.527（maker）
- UP sell 5 shares @ 0.533（maker；若无库存则会跳过或缩小到可卖数量）
- DOWN buy 5 shares @ 0.467（maker）
- DOWN sell 5 shares @ 0.473（maker）

> 同时它会做“maker 保护”：如果你的 buy 价会穿过当前 ask（变成 taker），会自动下调到 `ask - 1tick`；sell 同理上调到 `bid + 1tick`，避免不小心变成吃单。

### 3:10（盘中）—— 单边成交后，库存倾斜开始起作用

假设发生一次成交：
- UP buy 5 shares @ 0.527 成交

此时库存变成：
- `Qup=5, Qdown=0 => deltaInv = +5`

库存倾斜会把报价“往降低 deltaInv 的方向推”：
1) `deltaInv` 为正，说明 UP 偏多
2) 策略会让：
   - UP 更容易卖出（UP 的两侧报价整体更偏“卖出友好”）
   - DOWN 更容易买入（提高 DOWN buy 的积极性）

直觉结果（示意）：
- UP sell 价格会更贴近盘口，更容易被买家拿走
- DOWN buy 价格会上调一点，更容易补回 DOWN

这样做的目的不是押方向，而是把净敞口拉回 0：**让风险从“方向赌局”变成“可控的库存管理”。**

### 6:30（盘中）—— 双边回到中性，靠波动吃到利润

假设随后：
- DOWN buy 5 shares @ 0.470 成交（价格波动导致被动成交）

库存回到：
- `Qup=5, Qdown=5 => deltaInv=0`

此时你已经完成一次“波动收割”的常见形态：
- 先在 UP 低位挂到买单成交
- 再在 DOWN 的相对低位挂到买单成交
- 形成双边库存后，继续用四边报价把其中一边在更高价卖出

### 10:00（盘中）—— 卖出高的一边（示意）

假设市场概率上行到：
- `FairUp ≈ 0.60`

策略的 UP sell 可能被成交在 `0.61`（示意），这会把 UP 库存降低，随后库存倾斜会引导你补回 UP 或卖出 DOWN，以维持净敞口稳定。

### 12:00（进入 risk-only）—— 撤单 +（可选）只为降风险的 flatten

到 `tradeStopAtSeconds=720`：
1) 如果 `riskOnlyCancelAllQuotes=true`：
   - 策略会撤掉常规做市单（4 边 GTC）
2) 如果此时仍有净敞口（例如 `deltaInv=+5`）并且：
   - `riskOnlyAllowFlatten=true`
   - `riskOnlyMaxDeltaShares=0`
   则策略会尝试用小单 SELL FAK 在 bestBid 把多出来的一侧卖掉，目标把 `deltaInv -> 0`。

> 你们要的“最后 3 分钟尽量不交易”就是通过这套配置实现：**不再做市，只允许必要的降风险动作**。如果你希望最后 3 分钟完全不动（连 flatten 都不做），把 `riskOnlyAllowFlatten=false` 即可。

---

## 四、常见调参建议（短版）

- **想更积极**：降低 `sMin`、降低 `replaceThresholdTicks`、缩短 `quoteIntervalMs`
- **想更稳**：提高 `sMin`、提高 `beta`（尾端点差更宽）、提高 `deltaMaxShares`（更早开始倾斜）、提高 `replaceThresholdTicks`（减少撤改）
- **想更严格减少尾端交易**：把 `tradeStopAtSeconds` 提前；或 `riskOnlyAllowFlatten=false`

