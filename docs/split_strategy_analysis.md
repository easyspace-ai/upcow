# Polymarket BTC-15分钟 Split 策略分析

## 背景

通过 split 功能，10 USDC 可以同时获得 10 share UP + 10 share DOWN。在下一期市场尚未真正开始时，盘口已有挂单，但通常不均衡（如 UP 53c / DOWN 47c，或反之）。

**核心问题：** 如何在 split 后通过挂单获利并减少风险？

---

## 策略方向分析

### 方向1：盘前挂单策略（Pre-Market Order Strategy）

#### 1.1 策略描述
在开盘前通过挂单处理掉 split 获得的代币，利用盘口不均衡获利。

#### 1.2 核心问题
- **市场偏好导致单边成交**：通常开盘前只会成交一个方向（市场有明确偏好）
- **剩余方向风险**：未成交的一方只能等开盘，但开盘有风险（容易卖掉的那一份一路上涨）

#### 1.3 优化策略

##### 策略 1.3.1：动态价格调整 + 时间窗口管理
```
核心思路：
1. 开盘前 T-5分钟 到 T-1分钟：激进挂单
2. T-1分钟 到 T：保守挂单，准备撤单
3. T+0：立即撤单未成交部分，转为方向2策略

参数设计：
- PreMarketStartSeconds: 300  # 开盘前5分钟开始
- PreMarketEndSeconds: 60      # 开盘前1分钟结束激进挂单
- AggressiveSpreadCents: 2     # 激进模式：允许2分价差
- ConservativeSpreadCents: 1  # 保守模式：只允许1分价差
- CancelThresholdSeconds: 30   # 开盘前30秒撤单未成交部分
```

##### 策略 1.3.2：不平衡度监控 + 选择性挂单
```
核心思路：
1. 监控盘口不平衡度：imbalance = |UP_price - DOWN_price|
2. 只在不平衡度 > 阈值时挂单
3. 优先挂单价格偏离更大的一方（预期会回调）

参数设计：
- MinImbalanceCents: 3        # 最小不平衡度（分）
- MaxImbalanceCents: 10        # 最大不平衡度（超过则可能异常）
- PreferHigherSide: true       # 优先挂单价格更高的一方
```

##### 策略 1.3.3：分批挂单 + 逐步调整
```
核心思路：
1. 初始：50% 挂单价格较高的一方（预期回调）
2. 如果成交，继续挂剩余50%
3. 如果未成交，调整价格或转为方向2策略

参数设计：
- InitialOrderRatio: 0.5      # 初始挂单比例
- PriceAdjustmentCents: 1      # 未成交时价格调整幅度
- MaxAdjustments: 3            # 最大调整次数
```

#### 1.4 风险控制
- **时间止损**：开盘前30秒强制撤单未成交部分
- **价格保护**：设置最低卖出价格（如不低于成本价-1分）
- **仓位限制**：单次 split 后最多挂单80%，保留20%作为缓冲

---

### 方向2：开盘后动态卖出策略（Post-Market Dynamic Selling）

#### 2.1 策略描述
开盘后根据市场动态寻找机会卖出 split 获得的代币。

#### 2.2 核心挑战
- 缺乏明确的卖出时机判断标准
- 需要平衡获利和风险

#### 2.3 策略设计

##### 策略 2.3.1：价格动量策略
```
核心思路：
1. 监控价格变化速度（momentum）
2. 当价格快速上涨时，分批卖出
3. 当价格快速下跌时，等待反弹

指标计算：
- momentum_up = (current_price_up - price_5s_ago_up) / price_5s_ago_up
- momentum_down = (current_price_down - price_5s_ago_down) / price_5s_ago_down

触发条件：
- IF momentum_up > 0.02 (2%涨幅) AND position_up > 0:
    卖出 30% UP 仓位
- IF momentum_down > 0.02 AND position_down > 0:
    卖出 30% DOWN 仓位

参数设计：
- MomentumThreshold: 0.02      # 动量阈值（2%）
- SellRatio: 0.3                # 单次卖出比例
- MinHoldSeconds: 10            # 最小持有时间（避免频繁交易）
```

##### 策略 2.3.2：价差套利策略
```
核心思路：
1. 监控 UP 和 DOWN 的价格差
2. 当价差扩大时，卖出价格高的一方
3. 当价差缩小时，等待或卖出价格低的一方

指标计算：
- spread = UP_price - DOWN_price
- spread_normalized = spread / (UP_price + DOWN_price)

触发条件：
- IF spread > 0.10 (10分价差) AND UP_price > DOWN_price:
    卖出 UP（预期价差会缩小）
- IF spread < -0.10 AND DOWN_price > UP_price:
    卖出 DOWN

参数设计：
- SpreadThreshold: 0.10         # 价差阈值（10分）
- MaxSpread: 0.20              # 最大价差（超过则可能异常）
```

##### 策略 2.3.3：时间分段策略
```
核心思路：
1. 开盘后0-3分钟：观察期，不交易
2. 3-8分钟：积极交易，寻找机会
3. 8-12分钟：保守交易，逐步减仓
4. 12-15分钟：转为方向3策略（尾盘策略）

参数设计：
- ObservationPeriod: 180        # 观察期（秒）
- ActivePeriodStart: 180        # 积极交易期开始
- ActivePeriodEnd: 480          # 积极交易期结束
- ConservativePeriodStart: 480   # 保守交易期开始
- ConservativePeriodEnd: 720     # 保守交易期结束
```

##### 策略 2.3.4：波动率策略
```
核心思路：
1. 计算短期波动率（volatility）
2. 高波动率时：快速卖出，锁定利润
3. 低波动率时：等待更好的价格

指标计算：
- volatility = std_dev(prices_last_30s)
- volatility_normalized = volatility / average_price

触发条件：
- IF volatility > high_threshold:
    快速卖出（分批，每次20%）
- IF volatility < low_threshold:
    等待或挂限价单

参数设计：
- HighVolatilityThreshold: 0.05 # 高波动率阈值
- LowVolatilityThreshold: 0.01  # 低波动率阈值
- QuickSellRatio: 0.2           # 快速卖出比例
```

#### 2.4 组合策略建议
**推荐组合：价格动量 + 时间分段**
- 开盘后0-3分钟：观察，不交易
- 3-8分钟：使用价格动量策略，积极交易
- 8-12分钟：使用价差套利策略，保守交易
- 12-15分钟：转为方向3策略

---

### 方向3：尾盘锁定策略（End-Game Lock Strategy）

#### 3.1 策略描述
在尾盘大方向锁定后，卖掉价值低的一方，持有价值高的一方等待结算。

#### 3.2 核心风险
- **最后一秒反转风险**：在结算前价格可能突然反转
- **方向判断错误**：如果判断错误，可能错失利润

#### 3.3 策略设计

##### 策略 3.3.1：渐进式锁定策略
```
核心思路：
1. 不等到最后一刻才决定
2. 在最后3-5分钟开始渐进式锁定
3. 根据价格趋势和持仓比例动态调整

时间分段：
- T-5分钟到T-3分钟：开始评估方向
- T-3分钟到T-1分钟：逐步卖出弱势方（每次20-30%）
- T-1分钟到T-30秒：继续卖出弱势方（累计达到60-70%）
- T-30秒到T：保留30-40%作为保险，或全部卖出

参数设计：
- LockStartSeconds: 300         # 锁定开始时间（开盘后12分钟）
- FirstSellRatio: 0.3           # 第一次卖出比例
- SecondSellRatio: 0.3          # 第二次卖出比例
- FinalReserveRatio: 0.4         # 最终保留比例
```

##### 策略 3.3.2：价格趋势确认策略
```
核心思路：
1. 需要多重确认才锁定方向
2. 避免单一指标误判

确认条件（需同时满足）：
1. 价格趋势：连续3分钟价格持续上涨/下跌
2. 价差扩大：UP和DOWN价差 > 15分
3. 成交量：成交量集中在优势方向
4. 持仓比例：当前持仓中优势方向 > 60%

触发条件：
- IF all_conditions_met AND time_left < 180s:
    开始锁定（卖出弱势方）

参数设计：
- TrendConfirmationMinutes: 3   # 趋势确认时间（分钟）
- MinSpreadCents: 15             # 最小价差（分）
- MinPositionRatio: 0.6          # 最小持仓比例
```

##### 策略 3.3.3：对冲保护策略
```
核心思路：
1. 不完全卖出弱势方
2. 保留一定比例作为对冲
3. 降低反转风险

策略：
- 卖出弱势方的70%，保留30%
- 如果价格反转，保留的30%可以部分对冲损失
- 如果价格继续，剩余的70%可以继续获利

参数设计：
- SellRatio: 0.7                # 卖出比例
- HedgeRatio: 0.3                # 对冲保留比例
- ReversalThreshold: 0.05        # 反转阈值（如果价格反转5%，考虑重新平衡）
```

##### 策略 3.3.4：时间加权决策策略
```
核心思路：
1. 越接近结算，决策越保守
2. 根据剩余时间动态调整卖出比例

时间权重函数：
- weight(t) = 1 - (t / total_time)
- sell_ratio(t) = base_sell_ratio * weight(t)

参数设计：
- BaseSellRatio: 0.8            # 基础卖出比例
- MinSellRatio: 0.5              # 最小卖出比例（最后30秒）
- MaxSellRatio: 0.9              # 最大卖出比例（T-3分钟）
```

#### 3.4 风险控制
- **反转保护**：设置价格反转阈值，如果价格反转超过阈值，立即重新平衡
- **时间止损**：最后30秒强制决策，避免犹豫不决
- **仓位限制**：最多卖出80%，保留20%作为保险

---

## 综合策略建议

### 推荐策略组合：三阶段联动

```
阶段1（开盘前）：盘前挂单策略（方向1）
├─ 开盘前5分钟：开始监控不平衡度
├─ 开盘前3分钟：如果不平衡度 > 3分，开始挂单
├─ 开盘前1分钟：调整挂单价格（更保守）
└─ 开盘前30秒：撤单未成交部分，转为阶段2

阶段2（开盘后0-12分钟）：动态卖出策略（方向2）
├─ 0-3分钟：观察期，监控价格动量和价差
├─ 3-8分钟：价格动量策略，积极交易
├─ 8-12分钟：价差套利策略，保守交易
└─ 12分钟：评估是否进入阶段3

阶段3（开盘后12-15分钟）：尾盘锁定策略（方向3）
├─ 12-13分钟：评估方向，开始渐进式锁定
├─ 13-14分钟：根据趋势确认，继续锁定
├─ 14-14.5分钟：最后调整，保留保险仓位
└─ 14.5-15分钟：等待结算或最后决策
```

### 关键参数配置

```yaml
split_strategy:
  # 阶段1：盘前挂单
  pre_market:
    enabled: true
    start_seconds_before: 300      # 开盘前5分钟
    end_seconds_before: 30         # 开盘前30秒
    min_imbalance_cents: 3          # 最小不平衡度
    initial_order_ratio: 0.5        # 初始挂单比例
    max_price_adjustments: 3        # 最大价格调整次数
    
  # 阶段2：开盘后动态卖出
  post_market:
    enabled: true
    observation_period: 180         # 观察期（秒）
    active_period_start: 180        # 积极交易期开始
    active_period_end: 480          # 积极交易期结束
    momentum_threshold: 0.02        # 动量阈值（2%）
    spread_threshold: 0.10          # 价差阈值（10分）
    sell_ratio: 0.3                 # 单次卖出比例
    
  # 阶段3：尾盘锁定
  end_game:
    enabled: true
    lock_start_seconds: 720         # 锁定开始时间（12分钟）
    trend_confirmation_minutes: 3   # 趋势确认时间
    min_spread_cents: 15            # 最小价差
    sell_ratio: 0.7                 # 卖出比例
    hedge_ratio: 0.3                # 对冲保留比例
    reversal_threshold: 0.05        # 反转阈值（5%）
    
  # 通用风险控制
  risk_control:
    max_sell_ratio: 0.8             # 最大卖出比例
    min_reserve_ratio: 0.2          # 最小保留比例
    price_protection_cents: 1       # 价格保护（不低于成本价-1分）
    max_slippage_cents: 2           # 最大滑点
```

---

## 实施建议

### 第一阶段：基础实现
1. 实现方向1（盘前挂单）的基础功能
2. 实现方向2（开盘后动态卖出）的价格动量策略
3. 实现方向3（尾盘锁定）的渐进式锁定策略

### 第二阶段：优化调整
1. 基于回测数据调优参数
2. 添加更多指标和策略变体
3. 增强风险控制机制

### 第三阶段：智能化
1. 引入机器学习预测价格趋势
2. 动态调整策略参数
3. 多周期经验累积和自适应

---

## 风险提示

1. **市场流动性风险**：盘前和尾盘流动性可能较差，可能导致滑点较大
2. **方向判断风险**：如果方向判断错误，可能错失利润或产生亏损
3. **技术风险**：网络延迟、订单执行延迟等可能导致策略失效
4. **极端情况**：最后一秒价格反转、市场异常波动等极端情况需要特别处理

---

## 监控指标

运行时需要实时监控的关键指标：

```
实时状态：
├─ 当前阶段: PreMarket / PostMarket / EndGame
├─ 持仓状态:
│  ├─ UP: shares=10.0, avg_price=0.50, current_price=0.53
│  └─ DOWN: shares=10.0, avg_price=0.50, current_price=0.47
├─ 利润状态:
│  ├─ 已实现利润: +0.30 USDC
│  └─ 未实现利润: +0.00 USDC
├─ 风险指标:
│  ├─ 不平衡度: 6分
│  ├─ 价格动量: +0.02 (2%)
│  └─ 价差: 6分
└─ 时间状态:
   ├─ 距离开盘: 120秒
   └─ 距离结算: 780秒
```

---

## 总结

三个方向的策略各有优劣，建议采用**三阶段联动策略**：

1. **方向1（盘前挂单）**：适合利用盘口不平衡快速获利，但需要严格的时间控制和风险保护
2. **方向2（开盘后动态卖出）**：适合根据市场动态灵活调整，但需要明确的交易信号和时机判断
3. **方向3（尾盘锁定）**：适合在趋势明确时锁定利润，但需要多重确认和反转保护

通过三个阶段的有效衔接，可以在不同市场环境下最大化利润并控制风险。
