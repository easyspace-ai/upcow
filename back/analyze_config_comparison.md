# 配置对比分析报告

## 配置文件对比

### config.yaml（当前使用的配置）
- **orderSize**: 11 USDC
- **minMoveCents**: 1（更宽松）
- **minVelocityCentsPerSec**: 0.15（更宽松）
- **windowSeconds**: 8秒
- **marketQualityMinScore**: 30（更宽松）
- **marketQualityMaxSpreadCents**: 6（更宽松）
- **maxTradesPerCycle**: 3
- **cooldownMs**: 1400ms
- **cycleEndProtectionMinutes**: 3

### yml/velocityfollow.yaml（备用配置）
- **orderSize**: 5 USDC
- **minMoveCents**: 3（更严格）
- **minVelocityCentsPerSec**: 0.3（更严格）
- **windowSeconds**: 10秒
- **marketQualityMinScore**: 70（更严格）
- **marketQualityMaxSpreadCents**: 5（更严格）
- **maxTradesPerCycle**: 1
- **cooldownMs**: 1500ms
- **cycleEndProtectionMinutes**: 未设置（可能使用默认值）

## 日志分析结果

### 三个周期开单情况：
1. **周期1 (1766728800)**: 触发0次，开单0次
2. **周期2 (1766729700)**: 触发65次，开单0次
3. **周期3 (1766730600)**: 触发63次，开单0次

### 主要跳过原因：
- 冷却期保护：128次（周期2和3）
- 其他原因：685次（可能是速度/价格变化未达标、市场质量门控等）

## 问题分析

### 1. 配置参数对比
从日志中的冷却时间显示为1.40s（1400ms），说明实际运行时使用的是 **config.yaml** 配置。

### 2. 为什么策略触发了但没有开单？

**可能原因：**

1. **速度/价格变化未达到阈值**
   - 虽然config.yaml中minMoveCents=1, minVelocityCentsPerSec=0.15已经很宽松
   - 但实际价格变化可能仍然不满足条件
   - 订单簿价差过大（bid=0.01, ask=0.99）导致价格变化计算不准确

2. **市场质量门控过滤**
   - config.yaml中marketQualityMinScore=30
   - 但订单簿流动性极差（价差98分），质量分数可能低于30

3. **冷却期保护**
   - 65次触发被冷却期保护跳过
   - 说明同一方向在短时间内多次触发，但都被冷却期拦截

4. **周期结束前保护**
   - 周期1有5,748次被周期结束前保护跳过
   - 说明在周期结束前3分钟内，策略被保护机制阻止

5. **订单簿流动性问题**
   - 日志显示订单簿价格：YES bid=0.0100 ask=0.9900
   - 价差高达98分，远超marketQualityMaxSpreadCents=6的限制
   - 这会导致市场质量分数极低，无法通过质量门控

## 建议

### 1. 检查实际使用的配置文件
确认程序启动时使用的是 `config.yaml` 还是 `yml/velocityfollow.yaml`

### 2. 调整市场质量门控参数
如果订单簿流动性确实很差，可以考虑：
- 进一步降低 `marketQualityMinScore`（但风险增加）
- 或者增加 `marketQualityMaxSpreadCents`（但可能增加滑点风险）

### 3. 检查订单簿数据
订单簿显示 bid=0.01, ask=0.99 可能不正常，需要确认：
- 这是否是真实的市场数据？
- WebSocket数据是否正常更新？
- 是否有数据源问题？

### 4. 增加调试日志
在策略代码中增加更详细的日志输出：
- 速度计算的具体数值
- 市场质量分数的计算过程
- 每个过滤条件的检查结果

### 5. 考虑放宽限制（测试阶段）
如果是为了测试策略逻辑，可以临时：
- 降低 `marketQualityMinScore` 到 0（禁用质量门控）
- 增加 `marketQualityMaxSpreadCents` 到 20
- 观察是否能正常触发开单

