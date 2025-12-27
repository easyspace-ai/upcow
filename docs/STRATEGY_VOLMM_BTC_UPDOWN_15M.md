# BTC 15m Up/Down 盘中波动做市策略（volmm）

> 目标：在 **盘中（开盘后）** 利用 `split` 得到的双边库存（UP+DOWN）做 **Delta 近中性做市**，赚盘口波动/概率波动的钱，同时把“只成交一边留下方向敞口”的风险压到可控范围。

---

## 1. 场景与约束

- **产品**：Polymarket `btc-updown-15m-*`（每 15 分钟一期），YES=UP、NO=DOWN。
- **结算基准**：以 Chainlink BTC/USD 为准（你们已接入 `crypto_prices_chainlink` websocket）。
- **精度/最小单**：
  - tick size = `0.001`
  - min order size（shares）= `5`
- **费用**：你们当前假设“无手续费”，但仍需考虑 **滑点/逆向选择/尾部跳变**。
- **资金/规模**：单期规模约 `$40`，更适合“小步多次、严格风控”的微做市。
- **交易窗口（可配置）**：
  - **不做盘前**：策略只在收到“本期 market 的行情事件”后开始工作（盘前是否交易由窗口参数控制，默认不做）
  - **常规交易窗口**：开盘后 `tradeStopAtSeconds` 之前允许做市/交易（默认 12 分钟）
  - **风控窗口**：从 `tradeStopAtSeconds` 到周期结束（默认最后 3 分钟）尽量不交易，以**撤单+降敞口**为主，避免尾部跳变风险

---

## 2. 核心思想：距离 + 时间 定公平价，速度/动能做短期修正

### 2.1 定义变量

- \(S_t\)：当前 Chainlink BTC/USD 价格（RTDS）
- \(S_0\)：本期开始时的 Chainlink 价格（作为本期“起点价/strike”）
- \(\tau\)：距离本期结算剩余时间（秒）
- `UP_bid/UP_ask, DOWN_bid/DOWN_ask`：该期 CLOB 的一档盘口（来自 Market WS）

### 2.2 基础公平价（概率锚）

我们用一个“足够实用、便于实盘调参”的映射，把“离起点多远 + 剩余时间多少”映射为 Up 概率：

1) 距离（用美元差即可，或用对数差也可）：
\[
\Delta = S_t - S_0
\]

2) 时间缩放（尾端更极端）：
\[
x = \frac{\Delta}{\sqrt{\max(\tau, 1)}}
\]

3) 概率映射（可调参）：
\[
p_{base} = \Phi(k \cdot x + c)
\]

- \(\Phi\) 为标准正态 CDF
- \(k\) 控制“价格偏离对概率的敏感度”
- \(c\) 为偏置项（可用于校正系统性偏差）

得到：
- **FairUp = \(p\)**
- **FairDown = \(1-p\)**

> 这一步体现你强调的：**离起点越远、剩余时间越少，概率越极端**。

### 2.3 秒线/动能修正（用你们已有的“速度与动能”）

从 Chainlink 秒级流（或 1s K 线）计算短期特征：

- 速度（10 秒）：
\[
v = \ln(S_t/S_{t-10s})
\]
- 动能（加速度，快慢速度差）：
\[
a = v_{10s} - v_{30s}
\]

修正后的分数：
\[
z = (k \cdot x + c) + k_v \cdot v_n + k_a \cdot a_n
\]
其中 \(v_n, a_n\) 是归一化后的速度/动能（用最近 60–180 秒的标准差归一化，避免尺度漂移）。

最终概率：
\[
p = \Phi(z)
\]

**直觉**：当价格短期加速向上时，UP 概率应在基础锚上再上调；向下同理。

---

## 3. 交易框架：四边报价 + 库存倾斜（Delta 控制）

### 3.1 目标与风险指标

你 split 后天然拥有双边库存，策略目标是：

- **主要收益来源**：在围绕公平价上下的来回波动中，反复“低买高卖/吃波动”
- **主要风控目标**：保持净敞口接近 0，避免方向赌局

定义净敞口：
\[
\Delta_{inv} = Q_{up} - Q_{down}
\]
其中 \(Q_{up}, Q_{down}\) 是该期的持仓 shares（从 TradingService positions 获取）。

设置阈值：
- `deltaMaxShares`：允许的净敞口上限（建议先用 10 shares 或总持仓的 20%）

### 3.2 报价（四边）

在 Up/Down 两侧分别挂双边：

- Up：
  - Buy：`p - s - skew`
  - Sell：`p + s - skew`
- Down：
  - Buy：`(1-p) - s + skew`
  - Sell：`(1-p) + s + skew`

其中：
- `s` 为半点差（动态）
- `skew` 为库存倾斜项（让你自动回到 \(\Delta_{inv}\approx 0\)）

### 3.3 动态点差（无手续费也必须做）

无手续费 ≠ 无风险。逆向选择会在你贴得太近时“把你吸干”。

建议：
\[
s = \max(s_{min},\; \alpha \cdot |v_n|,\; \beta/\sqrt{\max(\tau,1)})
\]

含义：
- **波动越快** → 点差越宽，减少被扫单后继续被行情拖拽
- **越临近结算** → 点差越宽，尾部跳变更大

### 3.4 库存倾斜（把风险从“方向”变成“可控库存”）

用净敞口驱动偏置：
\[
skew = k_{\Delta}\cdot \mathrm{clip}\!\left(\frac{\Delta_{inv}}{\text{deltaMaxShares}},-1,1\right)\cdot s
\]

效果：
- 当 Up 持仓过多（\(\Delta_{inv}>0\)）：
  - Up 更容易卖出、Down 更容易买回
- 当 Down 持仓过多（\(\Delta_{inv}<0\)）反之

---

## 4. 订单管理：少量常驻单 + 阈值刷新，避免无意义撤改

### 4.1 常驻订单数量

盘中以“少量常驻、持续更新”为主：
- 最多维护 4 张 GTC：
  - UP：1 张买单 + 1 张卖单
  - DOWN：1 张买单 + 1 张卖单

### 4.2 刷新条件（推荐用阈值，不要每个 tick 都改）

当满足任一条件才撤单重挂：
- 公平价变化超过 `replaceThresholdTicks * tick`
- 库存倾斜导致目标价变化超过阈值
- 订单已成交/取消/失败（通过订单更新回调）
- BBO 变化导致“买单穿价/卖单穿价”即将变成 taker（不想当 taker 时）

---

## 5. 尾盘风控（由配置控制：最后 N 分钟尽量不交易）

尾盘风险来自“最后几十秒反转/跳变”。为了降低复杂度，本策略采用**可配置时间窗**：

- **开盘后 `tradeStopAtSeconds` – 周期结束**：进入 `risk-only`（尽量不交易）
  - 立即撤掉所有可能“扩大风险”的挂单（通常是四边做市单）
  - 不再新增净敞口（禁止让 \(|\Delta_{inv}|\) 变大）
  - 如果 \(|\Delta_{inv}|\) 仍然较大：允许用小单/分步将 \(\Delta_{inv}\rightarrow 0\)（必要时用 FAK），目标是在**第 `tradeStopAtSeconds` 秒左右把风险解决掉**

> 实现上建议把 `risk-only` 理解为“**只允许降低风险的交易**”，这样即便“最后 3 分钟尽量不交易”，也不会因为被动成交/残留敞口而被尾部波动击穿。

---

## 6. 可选增强：完整套利（short complete-set）/ 盘口失衡收割

你们 split 后手里有完整 set（UP+DOWN），盘中若出现：

- **有效卖出价**：`SellUP + SellDOWN > 1 + edge`

则可以同时卖出 UP 与 DOWN（两腿），锁定“卖出溢价”。这属于“盘口结构性机会”，但仍要处理：
- 两腿不同步成交带来的短时敞口
- 需要用 `sequential` 或 `parallel` 模式 + 补腿/撤单规则控制

（实现上可复用你们已有的 `marketmath.GetEffectivePrices/CheckArbitrage` 与 `ExecuteMultiLeg`。）

---

## 7. 参数建议（按你当前环境的“可先跑起来”版本）

> tick=0.001、min shares=5、单期$40，建议先保守一点，保证稳定。

- **订单规模**
  - `quoteSizeShares`: 5（单档最小）
  - `deltaMaxShares`: 10
- **点差**
  - `s_min`: 0.002 ~ 0.004
  - `alpha`: 0.002 ~ 0.006
  - `beta`: 0.01 ~ 0.03
- **公平价映射**
  - `k`: 从 0.05 ~ 0.15 起步（需要用历史数据拟合/实盘微调）
  - `c`: 0（先不偏置）
  - `k_v`: 0.3 ~ 0.8
  - `k_a`: 0.1 ~ 0.4
- **订单刷新**
  - `quoteIntervalMs`: 200 ~ 500
  - `replaceThresholdTicks`: 2 ~ 5
- **尾盘/窗口**
  - `tradeStopAtSeconds`: 停止常规交易的时间点（秒，默认 12*60）
  - `riskOnlyEnabled`: 是否启用风控窗口逻辑（默认 true）
  - `riskOnlyCancelAllQuotes`: 进入风控窗口时是否撤掉常规挂单（默认 true）
  - `riskOnlyAllowFlatten`: 风控窗口是否允许“只为降风险”的交易（默认 true）
  - `riskOnlyMaxDeltaShares`: 风控窗口允许保留的最大净敞口（默认 0 或 5，按你们偏好）

---

## 8. 实盘落地检查清单

- **S0 对齐**：本期开始时的 Chainlink 价必须记录一次，且与结算使用同一 feed（`btc/usd`）。
- **tick 对齐**：所有报价必须按 `0.001` 舍入，否则会报错或被系统修正。
- **最小 shares**：限价单 size >= 5（你们全局默认也是 5）。
- **旧周期事件过滤**：只处理当前 market slug（你们框架层已做很多保护，但策略层仍建议做一次 guard）。
- **尾盘撤单**：进入 reduce-only/flatten 时，先撤掉可能扩大风险的挂单。

---

## 9. 策略输出（用于复盘）

建议每 1 秒记录一条状态日志（或写入 csv）：
- `cycleSlug, tRemain, S0, St, p, fairUp, fairDown`
- `UP bid/ask, DOWN bid/ask`
- `target quotes (4条), active orders (4条)`
- `Qup, Qdown, deltaInv`

这样你们可以快速回看“为什么被扫/为什么没成交/尾盘是否按规则降风险”。

---

## 10. 配置示例

- 示例策略配置文件：`yml/volmm.yaml`
- 启动方式（示例）：
  - 使用默认 base 配置 + 策略文件：`go run ./cmd/bot -strategy volmm`
  - 或显式指定策略文件：`go run ./cmd/bot -strategies yml/volmm.yaml`

