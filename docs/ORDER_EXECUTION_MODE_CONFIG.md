# 订单执行模式配置说明

## 📋 配置选项

在 `config.yaml` 中可以配置订单执行模式：

```yaml
velocityfollow:
  # ====== 订单执行模式 ======
  # sequential: 顺序下单（先下 Entry，成交后再下 Hedge）- 风险低，速度慢（~200-500ms）
  # parallel: 并发下单（同时提交 Entry 和 Hedge）- 速度快（~100-200ms），风险高
  orderExecutionMode: "sequential"  # "sequential" 或 "parallel"，默认 "sequential"
  
  # 顺序下单模式参数（仅在 orderExecutionMode="sequential" 时生效）
  sequentialCheckIntervalMs: 50    # 检查订单状态的间隔（毫秒），默认 50ms
  sequentialMaxWaitMs: 1000        # 最大等待时间（毫秒），默认 1000ms（FAK 订单通常立即成交）
```

## 🔄 两种模式对比

### 1. Sequential（顺序下单）- 默认

**执行流程**：
1. 先下 Entry 订单（FAK，价格 >= `minPreferredPriceCents`）
2. 等待 Entry 订单成交（轮询检查，间隔 `sequentialCheckIntervalMs`，最多等待 `sequentialMaxWaitMs`）
3. Entry 成交后，再下 Hedge 订单（GTC）

**特点**：
- ✅ **风险低**：只有 Entry 成交后才下 Hedge
- ✅ **资金效率高**：先确认 Entry 成交，再下 Hedge
- ✅ **订单管理简单**：避免部分成交的复杂情况
- ⏱️ **速度慢**：需要等待 Entry 成交（典型间隔 100-300ms）

**日志标识**：`触发(顺序)`

### 2. Parallel（并发下单）

**执行流程**：
1. 同时提交 Entry 和 Hedge 订单（使用 `ExecuteMultiLeg`）
2. 两个订单几乎同时提交（间隔 < 100ms）

**特点**：
- ✅ **速度快**：两个订单几乎同时提交（~100-200ms）
- ✅ **价格一致性高**：两个订单在同一时间点提交
- ❌ **风险高**：如果 Entry 失败，Hedge 可能已经提交
- ❌ **资金占用**：两个订单同时占用资金

**日志标识**：`触发(并发)`

## 📊 性能对比

| 指标 | Sequential | Parallel |
|------|-----------|----------|
| **总耗时** | ~200-500ms | ~100-200ms |
| **价格一致性** | 中（间隔短） | 高（同时提交） |
| **风险控制** | 高（先确认 Entry） | 低（可能部分成交） |
| **资金效率** | 高（按需占用） | 低（同时占用） |
| **订单管理** | 简单（顺序执行） | 复杂（需要处理部分成交） |

## 🎯 使用建议

### 测试阶段

**建议使用 Sequential（顺序下单）**：
- 风险控制优先
- 便于观察和调试
- FAK 订单通常立即成交，等待时间短（< 300ms）

### 生产环境

**根据实际情况选择**：
- **Sequential**：如果风险控制更重要，或市场流动性较好
- **Parallel**：如果速度更重要，或市场流动性较差（需要快速锁定价格）

## 🔧 配置示例

### 顺序下单（推荐）
```yaml
velocityfollow:
  orderExecutionMode: "sequential"
  sequentialCheckIntervalMs: 50    # 快速检测（50ms）
  sequentialMaxWaitMs: 1000        # FAK 订单通常立即成交（1秒足够）
```

### 并发下单
```yaml
velocityfollow:
  orderExecutionMode: "parallel"
```

## 📝 日志示例

### 顺序下单日志
```
📤 [velocityfollow] 步骤1: 下主单 Entry (side=up price=66c size=11.0000 FAK)
✅ [velocityfollow] 主单已提交: orderID=0x... status=pending
✅ [velocityfollow] 主单已成交: orderID=0x... filledSize=11.0000
📤 [velocityfollow] 步骤2: 下对冲单 Hedge (side=down price=37c size=11.0000 GTC)
✅ [velocityfollow] 对冲单已提交: orderID=0x... status=open (关联主单=0x...)
⚡ [velocityfollow] 触发(顺序): side=up ask=66c hedge=37c ...
```

### 并发下单日志
```
⚡ [velocityfollow] 触发(并发): side=up ask=66c hedge=37c ... orders=2
```

## 💡 注意事项

1. **FAK 订单特性**：
   - FAK 订单通常立即成交或立即取消
   - 顺序下单的等待时间通常很短（< 300ms）
   - 如果 Entry 订单失败，不会下 Hedge 订单

2. **价格滑点**：
   - 顺序下单：在等待期间，Hedge 价格可能变化（但通常变化不大）
   - 并发下单：两个订单同时提交，价格一致性更好

3. **订单关联**：
   - 顺序下单：Hedge 订单会关联 Entry 订单ID
   - 并发下单：通过 `ExecuteMultiLeg` 管理订单关联

---

**配置时间**: 2025-12-25  
**默认模式**: Sequential（顺序下单）  
**状态**: ✅ 已实现并编译通过

