# 成对交易策略设计文档 v2.0
## Polymarket 15分钟 BTC Up/Down 市场

---

## 1. 核心理念重构

### 1.1 抛弃"网格"的束缚

**问题诊断：**
- 网格策略的本质是"在固定价位等待价格到达"
- 这导致策略过于被动，无法适应快速变化的市场
- 网格层级的设定需要大量经验和调试

**新思路：**
不使用固定网格层级，而是基于：
- **市场动态平衡点**
- **风险敞口实时计算**
- **成对锁定机会识别**

### 1.2 策略核心：成对锁定 (Paired Lock-In)

**定义：**
在一个15分钟周期内，通过多轮"成对交易"，逐步建立并锁定利润结构。

**核心公式：**
```
当前状态：
- Q_up: UP币持仓量
- C_up: UP币总成本
- Q_down: DOWN币持仓量  
- C_down: DOWN币总成本

利润计算：
- P_up_win = Q_up × 1.0 - (C_up + C_down)     # UP胜利时的利润
- P_down_win = Q_down × 1.0 - (C_up + C_down) # DOWN胜利时的利润

理想状态：P_up_win > 0 AND P_down_win > 0 (完全锁定)
```

**与网格策略的本质区别：**
- 网格：在固定价格点被动等待触发
- 成对锁定：主动识别并抓住"成对锁定机会"

---

## 2. 策略三阶段设计

### 阶段1：快速建仓（Phase 1: Rapid Build）
**时间：** 周期开始 0-5 分钟（可配置）
**目标：** 快速建立双边基础仓位
**策略：**

```
1. 机会识别：
   - 监控 UP/DOWN 价格变化
   - 识别"低成本建仓窗口"（price < build_threshold，默认0.60）
   
2. 下单逻辑：
   IF price_up < build_threshold AND (Q_up < base_target OR Q_up/Q_down < min_ratio):
      买入 UP (size = build_lot_size)
   
   IF price_down < build_threshold AND (Q_down < base_target OR Q_down/Q_up < min_ratio):
      买入 DOWN (size = build_lot_size)
   
3. 平衡控制：
   - 保持 UP/DOWN 持仓比例在 40%-60% 之间
   - 避免单边过重
```

### 阶段2：成对锁定（Phase 2: Paired Lock-In）
**时间：** 周期中段 5-10 分钟（可配置）
**目标：** 识别并执行成对锁定交易
**策略：**

```
核心：寻找"锁定机会窗口" (Lock-In Window)

1. 机会识别：
   检查当前风险敞口：
   - risk_up = max(0, -P_up_win)     # UP方向的亏损
   - risk_down = max(0, -P_down_win) # DOWN方向的亏损
   
2. 成对锁定触发条件（满足任一）：
   
   条件A：反向风险对冲
   IF risk_up > lock_threshold AND price_up < lock_price_max:
      买入 UP (补齐利润缺口)
      
   IF risk_down > lock_threshold AND price_down < lock_price_max:
      买入 DOWN (补齐利润缺口)
   
   条件B：利用极端价格锁定
   IF price_up > extreme_high (如0.80) AND P_down_win < target_profit:
      买入 DOWN (利用低价锁定反向利润)
      
   IF price_down > extreme_high (如0.80) AND P_up_win < target_profit:
      买入 UP (利用低价锁定反向利润)
   
   条件C：双边利润均衡
   IF P_up_win > 0 AND P_down_win < 0:
      计算需要补充的 DOWN 数量，使 P_down_win >= 0
      
   IF P_down_win > 0 AND P_up_win < 0:
      计算需要补充的 UP 数量，使 P_up_win >= 0
```

### 阶段3：利润放大（Phase 3: Profit Amplification）
**时间：** 周期后段 10-15 分钟（可配置）
**目标：** 在已锁定基础上放大利润
**前提：** 必须已经完成锁定（P_up_win > 0 AND P_down_win > 0）

```
1. 检查锁定状态：
   IF NOT (P_up_win > 0 AND P_down_win > 0):
      继续执行阶段2的锁定逻辑
      RETURN  # 不执行放大
   
2. 识别主方向：
   IF price_up > direction_threshold (如0.70):
      main_direction = UP
   ELIF price_down > direction_threshold:
      main_direction = DOWN
   ELSE:
      main_direction = NEUTRAL  # 不放大
   
3. 放大逻辑：
   IF main_direction == UP AND P_up_win < amplify_target:
      IF price_up < amplify_price_max (如0.85):
         买入 UP (增加UP胜利时的利润)
         
      IF price_down < insurance_price_max (如0.20):
         买入少量 DOWN (保险，避免反向亏损)
   
   对称处理 main_direction == DOWN
```

---

## 3. 关键参数设计

### 3.1 阶段控制参数

```yaml
phase_control:
  build_duration: 300s        # 阶段1持续时间（秒）
  lock_duration: 600s         # 阶段2结束时间（秒）
  cycle_duration: 900s        # 总周期时长（15分钟）
  
  # 提前阶段切换（基于价格）
  early_lock_price: 0.85      # 价格超过此值，提前进入阶段2
  early_amplify_price: 0.90   # 价格超过此值，提前进入阶段3（如果已锁定）
```

### 3.2 建仓参数

```yaml
build_config:
  base_target: 30.0           # 基础建仓目标（shares）
  build_lot_size: 3.0         # 单次建仓数量
  build_threshold: 0.60       # 建仓价格上限
  min_ratio: 0.40             # 最小持仓比例
  max_ratio: 0.60             # 最大持仓比例
```

### 3.3 锁定参数

```yaml
lock_config:
  lock_threshold: 5.0         # 触发锁定的风险阈值（USDC）
  lock_price_max: 0.70        # 锁定阶段最高买入价格
  extreme_high: 0.80          # 极端价格阈值
  target_profit: 2.0          # 目标利润（每个方向，USDC）
  insurance_size: 1.5         # 反向保险数量
```

### 3.4 放大参数

```yaml
amplify_config:
  amplify_target: 5.0         # 放大目标利润（USDC）
  amplify_price_max: 0.85     # 放大阶段最高买入价格
  insurance_price_max: 0.20   # 反向保险最高价格
  direction_threshold: 0.70   # 主方向判定阈值
```

---

## 4. 实现架构

### 4.1 核心状态机

```
PairedTradingState:
  - current_phase: Phase (Build/Lock/Amplify)
  - position_state: ArbitragePositionState (复用现有结构)
  - last_action_time: timestamp
  - lock_achieved: bool (是否已完成锁定)
```

### 4.2 核心方法

```go
// 主控制循环
func (s *PairedTradingStrategy) OnPriceChanged(event) error {
    // 1. 更新状态
    s.updateState(event)
    
    // 2. 判断当前阶段
    phase := s.detectPhase(event)
    
    // 3. 检查锁定状态
    lockAchieved := s.checkLockAchieved()
    
    // 4. 执行对应阶段策略
    switch phase {
    case PhaseBuild:
        return s.executeBuildPhase(event)
    case PhaseLock:
        return s.executeLockPhase(event)
    case PhaseAmplify:
        if lockAchieved {
            return s.executeAmplifyPhase(event)
        } else {
            return s.executeLockPhase(event)  // 未锁定，继续锁定
        }
    }
}

// 建仓阶段
func (s *PairedTradingStrategy) executeBuildPhase(event) error {
    // 快速建立双边仓位，保持平衡
}

// 锁定阶段
func (s *PairedTradingStrategy) executeLockPhase(event) error {
    // 识别并执行成对锁定交易
    // 优先级：消除负利润 > 平衡利润 > 提升利润
}

// 放大阶段
func (s *PairedTradingStrategy) executeAmplifyPhase(event) error {
    // 在锁定基础上，安全地放大利润
}
```

---

## 5. 与现有策略的对比

| 特性 | 网格策略 | 套利策略 | 成对锁定策略 |
|------|---------|---------|------------|
| **核心思想** | 固定价格层级 | 时间分阶段 | 风险敞口驱动 |
| **触发机制** | 价格达到网格 | 时间+价格 | 实时风险计算 |
| **灵活性** | 低（固定层级） | 中 | 高（动态调整） |
| **锁定速度** | 慢（被动等待） | 中 | 快（主动识别） |
| **适应性** | 差 | 中 | 强 |
| **参数依赖** | 强（层级设置） | 中 | 弱（阈值可自适应） |

---

## 6. 关键优势

1. **不依赖固定网格层级**
   - 避免了"价格不到网格层级"的问题
   - 可以在任何价格下识别机会

2. **风险驱动，而非价格驱动**
   - 关注点是"风险敞口"，而不是"价格位置"
   - 更符合风险管理的本质

3. **清晰的三阶段目标**
   - 阶段1：建仓（速度优先）
   - 阶段2：锁定（安全优先）
   - 阶段3：放大（收益优先）

4. **可配置的阶段切换**
   - 基于时间切换（常规情况）
   - 基于价格提前切换（极端情况）

5. **保留了套利策略的成功经验**
   - 复用 ArbitragePositionState
   - 复用利润计算公式
   - 保留拆单、并发控制等工程实践

---

## 7. 实施建议

### 7.1 第一阶段：核心功能
- 实现三阶段状态机
- 实现基础的锁定逻辑
- 复用现有的订单执行框架

### 7.2 第二阶段：优化调整
- 基于回测数据调优参数
- 添加自适应阈值调整
- 增强异常处理

### 7.3 第三阶段：智能化
- 引入机器学习预测主方向
- 动态调整阶段切换时间
- 多周期经验累积

---

## 8. 配置示例

```yaml
strategies:
  enabled:
    - paired_trading
  
  paired_trading:
    # 阶段控制
    build_duration: 300          # 5分钟
    lock_start: 300              # 从5分钟开始锁定
    amplify_start: 600           # 从10分钟开始放大
    cycle_duration: 900          # 15分钟周期
    
    # 建仓配置
    base_target: 30.0
    build_lot_size: 3.0
    build_threshold: 0.60
    min_ratio: 0.40
    max_ratio: 0.60
    
    # 锁定配置
    lock_threshold: 5.0
    lock_price_max: 0.70
    extreme_high: 0.80
    target_profit_base: 2.0
    insurance_size: 1.5
    
    # 放大配置
    amplify_target: 5.0
    amplify_price_max: 0.85
    insurance_price_max: 0.20
    direction_threshold: 0.70
    
    # 通用配置
    min_order_size: 1.1
    max_buy_slippage_cents: 3
```

---

## 9. 核心指标监控

运行时需要实时监控的关键指标：

```
实时状态：
├─ 当前阶段: Build/Lock/Amplify
├─ 锁定状态: ✓ 已锁定 / ✗ 未锁定
├─ 持仓状态:
│  ├─ UP: Q=30.0, C=18.50, AvgPrice=0.62
│  └─ DOWN: Q=28.0, C=17.20, AvgPrice=0.61
├─ 利润状态:
│  ├─ P_up_win: +3.50 USDC  ✓
│  └─ P_down_win: +2.20 USDC ✓
└─ 风险敞口:
   ├─ risk_up: 0.00 (已锁定)
   └─ risk_down: 0.00 (已锁定)
```

---

## 10. 总结

这个新策略的本质是：

**从"网格思维"转向"风险管理思维"**

- 不是等价格来触发你
- 而是主动识别并抓住锁定机会
- 在安全（锁定）的基础上追求收益（放大）

这才是真正的"成对交易、对冲锁定"策略。
