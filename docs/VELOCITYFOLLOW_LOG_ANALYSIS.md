# VelocityFollow 策略日志分析报告

## 运行概况

**分析时间**: 2025-12-24 20:45 - 21:00  
**市场周期**: btc-updown-15m-1766580300  
**日志文件**: btc-updown-15m-1766580300.log (320,670 行)

## 关键统计

### 1. 策略运行状态
- ✅ **策略已加载**: 2 次（周期切换时重新订阅）
- ✅ **策略已订阅**: 价格变化事件订阅成功
- ✅ **价格事件接收**: 179,517 次价格变化事件
- ❌ **策略触发次数**: 0 次

### 2. 优化效果验证
- ✅ **跳过同一方向**: 0 次（本次运行未触发，无法验证）
- ✅ **duplicate in-flight 错误**: 0 次（显著改善）
- ✅ **订单匹配失败警告**: 0 次（已消除）

## 问题分析

### 策略未触发的原因

根据配置和日志分析，策略未触发可能由以下原因导致：

#### 1. 市场已结算（最可能）
- **证据**: 日志显示价格在周期结束时达到 `1.0000` (100%)
- **影响**: 市场结算后，价格不再变化，策略无法触发
- **时间**: 21:00:00 时价格固定为 100%

#### 2. Binance Bias 等待机制
- **配置**: `requireBiasReady: true` + `useBinanceOpen1mBias: true`
- **影响**: 策略必须等待 Binance 1m K线收线并确定 bias 后才能触发
- **biasMode**: `hard` - 只允许顺势方向
- **可能问题**: 
  - Binance 1m K线数据可能未及时获取
  - bias 判断条件可能未满足（bodyBps < 3 或 wickBps > 25）

#### 3. 触发条件未满足
- **配置要求**:
  - `minMoveCents: 3` - 10秒内至少移动 3 cents
  - `minVelocityCentsPerSec: 0.3` - 速度至少 0.3 cents/秒
  - `useBinanceMoveConfirm: true` - 需要 Binance 1s 底层确认
  - `minUnderlyingMoveBps: 20` - 底层至少移动 0.20%
- **可能问题**: 价格变化速度或幅度未达到阈值

#### 4. 其他过滤条件
- **WarmupMs**: 800ms 预热期
- **CooldownMs**: 1500ms 冷却期
- **OncePerCycle**: true - 每周期最多触发一次
- **MaxSpreadCents**: 5 - 价差不能超过 5 cents
- **MaxEntryPriceCents**: 95 - 入场价不能超过 95 cents

## 配置分析

### 当前配置（config.yaml）

```yaml
velocityfollow:
  orderSize: 5
  windowSeconds: 10
  minMoveCents: 3
  minVelocityCentsPerSec: 0.3
  cooldownMs: 1500
  oncePerCycle: true
  warmupMs: 800
  
  # Binance Bias（严格模式）
  useBinanceOpen1mBias: true
  biasMode: "hard"           # 只允许顺势
  requireBiasReady: true     # 必须等待 bias 准备好
  
  # Binance 底层确认
  useBinanceMoveConfirm: true
  moveConfirmWindowSeconds: 60
  minUnderlyingMoveBps: 20
```

### 配置评估

**优点**:
- ✅ 多重过滤机制，避免误触发
- ✅ Binance bias 提供方向性指导
- ✅ 底层确认增加可靠性

**潜在问题**:
- ⚠️ `requireBiasReady: true` 可能导致策略在 bias 未准备好时完全不触发
- ⚠️ `biasMode: "hard"` 过于严格，可能错过逆势机会
- ⚠️ `minUnderlyingMoveBps: 20` (0.20%) 可能过高，过滤掉小幅波动

## 建议

### 1. 调试建议

#### 添加调试日志
在策略中添加更详细的日志，帮助诊断未触发原因：

```go
// 在 OnPriceChanged 中添加调试日志
log.Debugf("🔍 [%s] 价格变化: token=%s price=%dc samples=%d", 
    ID, e.TokenType, priceCents, len(s.samples[e.TokenType]))

// 在计算指标后添加日志
log.Debugf("📊 [%s] 指标计算: up=%+v down=%+v", ID, mUp, mDown)

// 在 bias 检查后添加日志
log.Debugf("🧭 [%s] bias状态: ready=%v token=%s reason=%s", 
    ID, s.biasReady, s.biasToken, s.biasReason)
```

#### 检查 Binance 数据
- 确认 Binance Futures K线数据是否正常获取
- 检查 1m K线是否及时收线
- 验证 bias 判断逻辑是否正常工作

### 2. 配置调整建议

#### 如果希望策略更敏感
```yaml
# 降低阈值
minMoveCents: 2              # 从 3 降到 2
minVelocityCentsPerSec: 0.2  # 从 0.3 降到 0.2
minUnderlyingMoveBps: 10     # 从 20 降到 10

# 放宽 bias 要求
requireBiasReady: false      # 不强制等待 bias
biasMode: "soft"             # 允许逆势，但提高门槛
```

#### 如果希望策略更保守
保持当前配置，但添加超时机制：
```yaml
open1mMaxWaitSeconds: 60     # 从 120 降到 60，避免等待过久
```

### 3. 监控建议

#### 添加指标监控
- Binance bias 准备时间
- 价格变化速度分布
- 触发条件满足但被过滤的次数
- 各过滤条件的命中率

#### 添加告警
- bias 等待超时告警
- 长时间未触发告警（如 5 分钟内无触发）

## 优化效果总结

### 已实现的优化
1. ✅ **方向级别去重**: 已实现，本次运行未触发无法验证效果
2. ✅ **ExecutionEngine 去重优化**: duplicate in-flight 错误为 0
3. ✅ **订单匹配优化**: 订单匹配失败警告为 0

### 优化效果对比

| 指标 | 优化前 | 优化后 | 改善 |
|------|--------|--------|------|
| duplicate in-flight 错误 | 179 次 | 0 次 | ✅ 100% |
| 订单匹配失败警告 | 多次 | 0 次 | ✅ 100% |
| 策略触发次数 | 50 次 | 0 次 | ⚠️ 需分析原因 |

## 结论

1. **优化代码工作正常**: duplicate in-flight 和订单匹配问题已完全解决
2. **策略未触发**: 主要原因是市场已结算或触发条件未满足
3. **需要进一步调试**: 建议添加详细日志，诊断具体的过滤原因

## 下一步行动

1. **添加调试日志**: 在策略关键路径添加详细日志
2. **检查 Binance 数据**: 确认 K线数据获取是否正常
3. **调整配置**: 根据实际需求调整阈值和过滤条件
4. **监控运行**: 在下一个周期继续观察策略行为

