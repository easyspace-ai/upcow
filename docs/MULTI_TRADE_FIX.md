# 多轮下单问题修复

**修复时间**: 2025-12-25  
**问题**: 策略每个周期只下单一次，无法触发多轮交易

## 🔍 问题分析

### 问题现象
- 配置中设置了 `maxTradesPerCycle: 3`，理论上每个周期可以下3次单
- 但实际运行中，每个周期只下单1次
- 日志显示 `trades=1/3`，说明还有2次机会，但没有触发

### 根本原因

**Bug 1: `oncePerCycle` 配置冲突**

配置文件中同时设置了：
```yaml
oncePerCycle: true         # 每周期最多触发一次（已废弃）
maxTradesPerCycle: 3       # 每周期最多交易次数
```

代码逻辑（第394行）：
```go
// 5.1 兼容旧逻辑：OncePerCycle
if s.OncePerCycle && s.tradedThisCycle {
    s.mu.Unlock()
    return nil  // ❌ 直接返回，不会继续检查 maxTradesPerCycle
}
```

**问题**: 即使设置了 `maxTradesPerCycle: 3`，如果 `oncePerCycle: true`，只要 `tradedThisCycle` 为 true，就会直接返回，不会继续检查 `maxTradesPerCycle`。

### 其他可能阻止多轮下单的因素

1. **冷却时间** (`cooldownMs: 1500ms`)
   - 两次触发之间必须间隔至少 1.5 秒
   - 如果价格变化太快，可能被冷却时间阻止

2. **方向去重** (第528-538行)
   - 同一方向在冷却期内会被跳过
   - 如果两次触发都是同一方向（比如都是 DOWN），且距离上次触发 < 1.5秒，会被跳过
   - 如果两次触发是不同方向（一次 DOWN，一次 UP），应该可以触发

3. **速度条件**
   - 必须满足速度阈值：`minVelocityCentsPerSec: 0.3` c/s
   - 必须满足价格移动：`minMoveCents: 3` c
   - 如果后续价格变化不满足条件，不会触发

4. **价格优先选择** (`preferHigherPrice: true`, `minPreferredPriceCents: 60`)
   - 如果价格低于 60c，不会触发
   - 可能限制了触发机会

## ✅ 修复方案

### 修复 1: 禁用 `oncePerCycle`

将配置中的 `oncePerCycle` 改为 `false`：

```yaml
oncePerCycle: false         # 每周期最多触发一次（已废弃，使用 maxTradesPerCycle）
maxTradesPerCycle: 3       # 每周期最多交易次数（0=不设限）
```

### 修复 2: 优化代码逻辑（可选）

可以考虑完全移除 `oncePerCycle` 的检查，只使用 `maxTradesPerCycle`：

```go
// 5. 交易限制检查
// 5.1 MaxTradesPerCycle 控制（0=不设限）
if s.MaxTradesPerCycle > 0 && s.tradesCountThisCycle >= s.MaxTradesPerCycle {
    s.mu.Unlock()
    log.Debugf("🔄 [%s] 跳过：本周期交易次数已达上限 (%d/%d)", ID, s.tradesCountThisCycle, s.MaxTradesPerCycle)
    return nil
}
// 5.2 冷却时间检查
if !s.lastTriggerAt.IsZero() && now.Sub(s.lastTriggerAt) < time.Duration(s.CooldownMs)*time.Millisecond {
    s.mu.Unlock()
    return nil
}
```

## 📋 多轮下单的限制条件

修复后，多轮下单需要满足以下条件：

1. ✅ **交易次数限制**: `tradesCountThisCycle < maxTradesPerCycle` (3)
2. ✅ **冷却时间**: 距离上次触发 >= `cooldownMs` (1500ms)
3. ✅ **方向去重**: 如果同一方向，距离上次触发 >= `cooldownMs` (1500ms)
4. ✅ **速度条件**: 速度 >= `minVelocityCentsPerSec` (0.3 c/s)
5. ✅ **价格移动**: 移动 >= `minMoveCents` (3 c)
6. ✅ **价格优先**: 如果启用，价格 >= `minPreferredPriceCents` (60c)

## 🧪 验证方法

修复后，重新运行程序，观察日志：

1. **检查配置加载**:
   ```
   ✅ [velocityfollow] 配置加载: maxTradesPerCycle=3, oncePerCycle=false
   ```

2. **观察触发日志**:
   ```
   ⚡ [velocityfollow] 触发(顺序): side=down ... trades=1/3
   ⚡ [velocityfollow] 触发(顺序): side=up ... trades=2/3
   ⚡ [velocityfollow] 触发(顺序): side=down ... trades=3/3
   ```

3. **检查限制日志**:
   ```
   🔄 [velocityfollow] 跳过：本周期交易次数已达上限 (3/3)
   🔄 [velocityfollow] 跳过：同一方向 down 在冷却期内（距离上次触发 0.50s）
   ```

## 📝 相关文件

- `config.yaml` - 配置文件（已修复）
- `internal/strategies/velocityfollow/strategy.go` - 策略实现
- `internal/strategies/velocityfollow/config.go` - 配置验证

## ⚠️ 注意事项

1. **冷却时间**: 1.5秒的冷却时间可能仍然会限制高频触发，如果市场变化很快，可能需要调整
2. **方向去重**: 如果希望同一方向也能多次触发，需要调整方向去重逻辑
3. **价格优先**: `minPreferredPriceCents: 60` 可能限制了触发机会，如果价格经常低于60c，可能需要降低阈值

