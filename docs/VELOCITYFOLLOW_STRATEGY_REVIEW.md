# VelocityFollow 策略梳理与实盘配置

## 策略概述

**VelocityFollow** 是一个基于价格变化速度的追涨策略：

### 核心逻辑
1. **监控价格速度**：在时间窗口内计算价格变化速度（cents/second）
2. **触发条件**：当价格快速上涨且满足速度阈值时触发交易
3. **交易执行**：
   - **Entry Order (FAK)**: 立即吃单买入上涨的一方
   - **Hedge Order (GTC)**: 同时挂限价单买入对侧作为对冲

### 示例
- UP 价格从 50c 快速拉升到 70c（10秒内）
- 触发：
  - 吃单买入 UP @ 70c
  - 挂限价单买入 DOWN @ (100-70-3)=27c

## 关键参数说明

### 交易参数
- **orderSize**: Entry order 的 shares 数量
- **hedgeOrderSize**: Hedge order 的 shares 数量（0 = 跟随 orderSize）

### 速度判定参数
- **windowSeconds**: 速度计算窗口（秒），默认 10s
- **minMoveCents**: 窗口内最小价格变化（分），默认 3c
- **minVelocityCentsPerSec**: 最小速度阈值（分/秒），默认 0.3c/s

### 安全参数
- **hedgeOffsetCents**: 对侧挂单价格偏移（分），默认 3c
- **maxEntryPriceCents**: 吃单价上限（分），避免 99/100 假盘口，默认 95c
- **maxSpreadCents**: 盘口价差上限（分），避免极差盘口，默认 5c

### 风控参数
- **cooldownMs**: 触发冷却时间（毫秒），默认 1500ms
- **oncePerCycle**: 每周期最多触发一次，默认 true
- **warmupMs**: 周期切换后的预热窗口（毫秒），默认 800ms

## 20 美金实盘配置分析

### 成本计算
假设平均价格 50c：
- Entry order: 2 shares @ 50c = $1.0
  - 需要满足 minOrderSize (1.1 USD)，实际约 $1.1
- Hedge order: 2 shares @ 50c = $1.0
  - 需要满足 minOrderSize (1.1 USD)，实际约 $1.1
- **每次交易总成本：约 $2.2**

### 预算分配
- 20 美金可以支持约 **9 次交易**
- 考虑到：
  - 1h 周期，oncePerCycle = true，一天最多 24 次机会
  - 实际触发频率取决于市场条件（需要满足速度阈值）
  - 建议保守配置：orderSize = 2 shares

### 风险控制
- ✅ 每周期最多触发一次（oncePerCycle: true）
- ✅ 方向级别去重（sideCooldownMs: 1000ms）
- ✅ 价格上限保护（maxEntryPriceCents: 95）
- ✅ 盘口价差保护（maxSpreadCents: 5）
- ✅ 最小订单金额保护（minOrderSize: 1.1 USD）

## 实盘配置建议

### 保守配置（推荐）
- orderSize: 2 shares
- 每次交易成本：约 $2.2
- 20 美金可支持：9 次交易
- 适合：首次实盘测试

### 中等配置
- orderSize: 3 shares
- 每次交易成本：约 $3.3
- 20 美金可支持：6 次交易
- 适合：有一定经验后

### 注意事项
1. **dry_run**: 必须设置为 `false` 才能实盘交易
2. **市场选择**: 当前配置为 1h BTC 市场
3. **监控**: 建议密切监控前几次交易
4. **止损**: 策略本身没有止损，依赖对冲订单

## 策略优势
- ✅ 追涨逻辑清晰
- ✅ 自动对冲保护
- ✅ 多重安全参数
- ✅ 周期级别和方向级别去重

## 策略风险
- ⚠️ 追涨可能追在高点
- ⚠️ 对冲订单可能不成交
- ⚠️ 市场快速反转风险
- ⚠️ 流动性不足时可能滑点

## 实盘前检查清单
- [ ] dry_run 设置为 false
- [ ] orderSize 设置为 2-3 shares
- [ ] 确认钱包余额充足（> 20 USD）
- [ ] 确认市场配置正确（symbol, timeframe）
- [ ] 检查日志级别和文件路径
- [ ] 确认代理配置（如需要）
- [ ] 测试连接和订阅是否正常

