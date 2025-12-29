# Polymarket BTC 15分钟 Up/Down 市场中性套利策略开发指南

## 一、策略核心理解

### 1.1 你观察到的机器人行为

根据你的观察，真正的机器人在运行时有以下特点：

1. **只关心盘口价格**：不关心实时BTC价格，只关注UP/DOWN的ask/bid
2. **只买不卖**：在整个15分钟内持续买入，持有到结算
3. **持仓管理**：通过持续买入来平衡UP/DOWN持仓
4. **市场中性**：无论UP还是DOWN胜出，都能盈利

### 1.2 盈利原理

**核心公式**：`UP_ask + DOWN_ask < 1.0` 时存在套利机会

**数学证明**：
```
假设买入：
- UP数量 = Q_up，价格 = P_up
- DOWN数量 = Q_down，价格 = P_down
- 总成本 = Q_up * P_up + Q_down * P_down

结算时：
- 如果UP胜出：收益 = Q_up * 1.0 - 总成本
- 如果DOWN胜出：收益 = Q_down * 1.0 - 总成本

如果 Q_up = Q_down = Q，且 P_up + P_down < 1.0：
- 总成本 = Q * (P_up + P_down) < Q * 1.0
- 无论哪方胜出，收益 = Q * 1.0 - Q * (P_up + P_down) > 0 ✅
```

### 1.3 从CSV数据分析

查看 `bot_v0_8_cycle_1765382400.csv`：

- **典型价格组合**：
  - UP=0.48, DOWN=0.50 → 总成本=0.98（2%利润空间）
  - UP=0.45, DOWN=0.44 → 总成本=0.89（11%利润空间）
  - UP=0.20, DOWN=0.20 → 总成本=0.40（60%利润空间）

- **持仓模式**：
  - 在价格低时大量买入（如UP=0.20-0.30时买入大量UP）
  - 在价格高时买入反向（如DOWN=0.70-0.80时买入DOWN）
  - 保持UP和DOWN持仓相对平衡

## 二、策略实现方案

### 2.1 Python版本（参考实现）

已创建 `market_neutral_strategy.py`，包含：
- 核心策略逻辑
- 持仓管理
- 订单大小计算
- 状态监控

### 2.2 Go版本（集成到项目）

参考项目中的 `pairedtrading` 策略，可以创建新策略：

**文件结构**：
```
internal/strategies/marketneutral/
├── strategy.go      # 策略主逻辑
├── config.go        # 配置结构
└── README.md        # 策略说明
```

**核心逻辑**：
```go
func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
    // 1. 获取UP和DOWN的ask价格
    upAsk, _ := orderutil.QuoteBuyPrice(ctx, s.TradingService, m.YesAssetID, 0)
    downAsk, _ := orderutil.QuoteBuyPrice(ctx, s.TradingService, m.NoAssetID, 0)
    
    // 2. 计算总成本
    totalCost := upAsk.ToCents() + downAsk.ToCents()
    
    // 3. 检查套利机会
    if totalCost > (100 - s.Config.MinProfitCents) {
        return nil  // 没有套利机会
    }
    
    // 4. 获取当前持仓
    positions := s.TradingService.GetOpenPositionsForMarket(m.Slug)
    upShares, downShares := calculatePositions(positions)
    
    // 5. 计算持仓不平衡度
    imbalance := calculateImbalance(upShares, downShares)
    
    // 6. 决定买入方向
    if imbalance > s.Config.MaxImbalanceRatio {
        // 持仓不平衡，优先买入较少的一方
        if upShares < downShares {
            return s.buyUp(ctx, m, upAsk)
        } else {
            return s.buyDown(ctx, m, downAsk)
        }
    } else {
        // 持仓平衡，根据价格优势买入
        if upAsk.ToDecimal() < downAsk.ToDecimal() {
            return s.buyUp(ctx, m, upAsk)
        } else {
            return s.buyDown(ctx, m, downAsk)
        }
    }
}
```

## 三、关键参数配置

### 3.1 推荐配置

```yaml
marketneutral:
  # 套利阈值
  min_profit_cents: 2          # 最小利润2美分（2%）
  max_total_cost_cents: 98    # 最大总成本98美分
  
  # 持仓管理
  max_imbalance_ratio: 0.2     # 最大持仓不平衡比例20%
  target_position_ratio: 0.5    # 目标UP/DOWN持仓比例50:50
  
  # 订单大小
  base_order_size: 10          # 基础订单大小
  max_order_size: 50          # 最大单笔订单
  min_order_size_usd: 1.1     # 最小订单金额（满足交易所要求）
  
  # 风险控制
  max_total_position: 1000     # 最大总持仓
  max_single_side_position: 600 # 单边最大持仓
  
  # 交易频率
  cooldown_ms: 500            # 订单间隔500ms
  max_rounds_per_period: 1000 # 每个周期最大交易次数
```

### 3.2 参数调优建议

1. **min_profit_cents**：
   - 保守：5美分（5%利润空间）
   - 中等：2美分（2%利润空间）
   - 激进：1美分（1%利润空间）

2. **max_imbalance_ratio**：
   - 严格：0.1（10%不平衡）
   - 中等：0.2（20%不平衡）
   - 宽松：0.3（30%不平衡）

3. **base_order_size**：
   - 根据资金量调整
   - 建议：总资金的1-5%

## 四、实施步骤

### 4.1 第一阶段：基础实现

1. **创建策略文件**
   ```bash
   mkdir -p internal/strategies/marketneutral
   ```

2. **实现核心逻辑**
   - 参考 `pairedtrading` 策略
   - 实现价格监控
   - 实现持仓计算
   - 实现买入决策

3. **添加配置**
   - 创建 `config.go`
   - 添加参数验证

### 4.2 第二阶段：测试验证

1. **单元测试**
   - 测试套利机会识别
   - 测试持仓计算
   - 测试买入决策

2. **回测验证**
   - 使用历史数据回测
   - 验证策略有效性
   - 优化参数设置

### 4.3 第三阶段：实盘测试

1. **小资金测试**
   - 使用最小资金测试
   - 监控策略表现
   - 调整参数

2. **逐步扩大**
   - 根据表现逐步增加资金
   - 持续优化策略

## 五、监控指标

### 5.1 关键指标

1. **持仓指标**
   - UP持仓数量
   - DOWN持仓数量
   - 净持仓（UP - DOWN）
   - 持仓不平衡比例

2. **成本指标**
   - UP总成本
   - DOWN总成本
   - 总成本
   - 平均成本

3. **利润指标**
   - 如果UP胜出的利润
   - 如果DOWN胜出的利润
   - 最小利润（无论哪方胜出）

4. **交易指标**
   - 总交易次数
   - UP买入次数
   - DOWN买入次数
   - 平均订单大小

### 5.2 风险指标

1. **持仓风险**
   - 单边持仓占比
   - 总持仓规模
   - 持仓成本

2. **价格风险**
   - 当前UP/DOWN价格
   - 价格波动
   - 套利机会频率

## 六、常见问题

### 6.1 为什么只买不卖？

因为策略的目标是：
- 在价格低时买入，持有到结算
- 通过持仓平衡来降低风险
- 无论哪方胜出都能盈利

如果提前卖出，可能会：
- 错过更大的利润空间
- 增加交易成本
- 破坏持仓平衡

### 6.2 如何应对价格波动？

1. **持续监控**：实时监控价格变化
2. **动态调整**：根据价格变化调整买入策略
3. **持仓平衡**：通过持仓平衡降低单边风险

### 6.3 如何控制风险？

1. **持仓限制**：设置最大持仓
2. **成本控制**：设置最大总成本
3. **不平衡控制**：设置最大不平衡比例
4. **资金管理**：预留足够的资金缓冲

## 七、参考资源

1. **策略分析文档**：`策略分析_市场中性套利.md`
2. **Python实现**：`market_neutral_strategy.py`
3. **CSV数据**：`bot_v0_8_cycle_*.csv`
4. **项目策略参考**：`internal/strategies/pairedtrading/`

## 八、下一步行动

1. ✅ 理解策略原理
2. ✅ 分析历史数据
3. ✅ 创建Python参考实现
4. ⏳ 创建Go版本策略
5. ⏳ 添加单元测试
6. ⏳ 回测验证
7. ⏳ 实盘测试
