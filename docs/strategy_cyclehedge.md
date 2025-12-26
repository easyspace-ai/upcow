## cyclehedge（BTC 15m 周期对冲锁利）

### 适用场景
- **Polymarket `btc-updown-15m-*`** 这类“短周期结算”的二元市场。
- 目标不是预测方向，而是用 **complete-set（YES+NO）** 在单周期内锁定确定收益，并随余额滚动放大。

### 核心机制（一句话）
在同一周期里挂两腿 **GTC 买单**，让它们的成交价满足：
\[
P_{YES} + P_{NO} \le 1 - \text{profit}
\]
当两腿都成交后，持有到结算即可获得 $1/份 的到期价值，从而锁定 profit。

### 为什么这比“追涨”更贴近你的目标
- **收益来源明确**：来自“成本 < 1”的结构性确定收益，而不是价格走势。
- **单周期闭环**：15m 结算 -> 余额刷新 -> 下一周期放大 notional。
- **风控可控**：最大风险是“只成交一腿”，策略用超时/临近结算的补齐或回平来处理。

### 关键参数建议（起步到 3000U）
- **fixedNotionalUSDC / minNotionalUSDC / maxNotionalUSDC / balanceAllocationPct**
  - `fixedNotionalUSDC>0`：每周期固定投入（更稳定、更好回测/审计）。
  - 否则使用 `balanceAllocationPct` 按余额滚动放大，并受 `maxNotionalUSDC` 上限约束。
- **maxSingleSideShares**
  - 每周期最大单向持仓（shares）。用于限制“只成交一腿/偏斜”导致的风险累积。
- **profitMinCents / profitMaxCents**
  - 起步建议 `1~3c`，资金大后仍建议把上限控制在 `<=5c`（越大越难成交，可能错过周期）。
- **enableDynamicProfit / distancePenaltyBps**
  - 开启后策略会在 1–5c 内动态选 profit：profit 越大越好，但挂单离盘口越远越难成交。
  - `distancePenaltyBps` 越大越倾向“更贴盘口、更容易成交”的小 profit。
- **minNotionalUSDC / maxNotionalUSDC / balanceAllocationPct**
  - `maxNotionalUSDC: 3000`：你的资金目标上限。
  - `balanceAllocationPct: 0.8`：让策略自动滚动放大，同时留出缓冲。
- **unhedgedTimeoutSeconds / allowTakerComplete / allowFlatten**
  - 建议保持 `allowTakerComplete=true`：宁可少赚，也要避免裸奔穿到结算。
  - `allowFlatten=true`：当盘口不允许补齐（会亏）时，回平止损风险。
- **entryCutoffSeconds**
  - 建议 `20~40s`：临近结算撤单，避免最后几十秒被动成交造成单腿风险。

### 使用方式
- 参考 `yml/cyclehedge.yaml` 把策略挂到 `exchangeStrategies`。
- 建议先 `dry_run: true` 跑 1-2 天观察日志：是否能稳定完成两腿、是否频繁触发补齐/回平。

### 周期报表（保存到文件）
策略会在**周期切换**（OnCycle）时输出并写盘上一周期的报表（JSON / JSONL），默认目录：
- `data/reports/cyclehedge/`

配置项：
- `enableReport`: 是否启用（默认 true）
- `reportDir`: 输出目录（默认 `data/reports/cyclehedge`）
- `reportWritePerCycle`: 每周期一个 JSON（默认 true）
- `reportWriteJSONL`: 追加写入 `report.jsonl`（默认 true）

