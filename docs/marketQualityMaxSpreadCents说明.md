# marketQualityMaxSpreadCents 参数说明

## 作用

`marketQualityMaxSpreadCents` 用于**市场质量门控（Market Quality Gate）**，控制允许的最大订单簿价差（单位：分）。

## 使用逻辑

### 1. 参数优先级

```go
// 代码位置：internal/strategies/velocityfollow/strategy.go:875-881
maxSpreadCentsGate := s.MarketQualityMaxSpreadCents
if maxSpreadCentsGate <= 0 {
    maxSpreadCentsGate = maxSpread  // 如果未设置，使用 maxSpreadCents
}
if maxSpreadCentsGate <= 0 {
    maxSpreadCentsGate = 10  // 如果都未设置，默认使用 10 分
}
```

**优先级顺序：**
1. `marketQualityMaxSpreadCents`（如果 > 0）
2. `maxSpreadCents`（如果 `marketQualityMaxSpreadCents` <= 0）
3. 默认值 10 分（如果都 <= 0）

### 2. 转换为 Pips

```go
// 代码位置：internal/strategies/velocityfollow/strategy.go:888
MaxSpreadPips: maxSpreadCentsGate * 100  // 1分 = 100 pips
```

**单位转换：**
- 1 分（cent） = 100 pips
- 例如：`marketQualityMaxSpreadCents: 6` → `MaxSpreadPips: 600`

### 3. 价差检查与扣分

```go
// 代码位置：internal/services/market_quality.go:222-244
// YES 方向的价差计算
mq.YesSpreadPips = mq.Top.YesAskPips - mq.Top.YesBidPips  // YES的ask - YES的bid
if mq.YesSpreadPips > opts.MaxSpreadPips {
    mq.Problems = append(mq.Problems, "wide_spread_yes")
    mq.Score -= 20  // 扣分 20 分
}

// NO 方向的价差计算
mq.NoSpreadPips = mq.Top.NoAskPips - mq.Top.NoBidPips  // NO的ask - NO的bid
if mq.NoSpreadPips > opts.MaxSpreadPips {
    mq.Problems = append(mq.Problems, "wide_spread_no")
    mq.Score -= 20  // 扣分 20 分
}
```

**价差计算逻辑：**
- **YES (UP) 方向价差** = YES的ask价格 - YES的bid价格
- **NO (DOWN) 方向价差** = NO的ask价格 - NO的bid价格
- 分别检查 YES 和 NO 两个方向的价差（**同方向的ask-bid**）
- 如果任一方向的价差超过限制，会：
  1. 添加问题标记：`wide_spread_yes` 或 `wide_spread_no`
  2. **扣分 20 分**（从市场质量分数中扣除）

### 4. 市场质量分数计算

市场质量分数初始值为 **100 分**，根据各种问题扣分：

| 问题类型 | 扣分 | 说明 |
|---------|------|------|
| `incomplete_top` | -50 | 订单簿不完整 |
| `crossed_yes/no` | -40 | 买卖价交叉 |
| `ws_partial` | -35 | WebSocket 数据不完整 |
| `ws_stale` | -25 | WebSocket 数据过期 |
| **`wide_spread_yes/no`** | **-20** | **价差过大** |
| `effective_price_failed` | -20 | 有效价格计算失败 |
| `mirror_gap_buy_yes/no` | -10 | 镜像偏差过大 |

**最终分数范围：** 0-100 分

### 5. 门控判断

```go
// 代码位置：internal/strategies/velocityfollow/strategy.go:899
if mq == nil || mq.Score < s.MarketQualityMinScore {
    // 跳过交易，不执行下单
    return nil
}
```

**判断条件：**
- 如果市场质量分数 < `marketQualityMinScore`，则**跳过交易**
- 如果市场质量分数 >= `marketQualityMinScore`，则**允许交易**

## 实际案例

### 当前配置（config.yaml）

```yaml
marketQualityMaxSpreadCents: 6  # 最大价差 6 分
marketQualityMinScore: 30       # 最小质量分数 30 分
```

### 场景分析

**场景1：正常价差**
- YES 价差：3 分（300 pips）
- NO 价差：4 分（400 pips）
- 结果：✅ 不扣分，质量分数保持 100 分

**场景2：价差略超**
- YES 价差：YES的ask(0.07) - YES的bid(0.00) = 7 分（700 pips）> 6 分限制
- NO 价差：NO的ask(0.05) - NO的bid(0.00) = 5 分（500 pips）
- 结果：⚠️ YES 方向扣 20 分，质量分数 = 80 分
- 判断：✅ 80 >= 30，**允许交易**

**场景3：价差严重超限（日志中的情况）**
- 日志显示：`YES bid=0.0100 ask=0.9900, NO bid=0.0100 ask=0.9900`
- YES 价差：YES的ask(0.99) - YES的bid(0.01) = 0.98 = **98 分**（9800 pips）>> 6 分限制
- NO 价差：NO的ask(0.99) - NO的bid(0.01) = 0.98 = **98 分**（9800 pips）>> 6 分限制
- 结果：⚠️ 两个方向各扣 20 分，共扣 40 分
- 如果还有其他问题（如 `ws_stale`、`incomplete_top` 等），可能再扣分
- 判断：❌ 质量分数可能 < 30，**跳过交易**

## 配置建议

### 1. 正常市场条件
```yaml
marketQualityMaxSpreadCents: 6   # 允许 6 分价差
marketQualityMinScore: 30        # 最低 30 分即可交易
```

### 2. 流动性较差的市场
```yaml
marketQualityMaxSpreadCents: 10  # 放宽到 10 分
marketQualityMinScore: 20         # 降低最低分数要求
```

### 3. 测试阶段（临时放宽）
```yaml
marketQualityMaxSpreadCents: 20  # 大幅放宽
marketQualityMinScore: 0         # 禁用质量门控（不推荐生产环境）
```

## 注意事项

1. **价差过大 = 滑点风险高**
   - 价差越大，实际成交价与预期价格偏差越大
   - 建议不要过度放宽 `marketQualityMaxSpreadCents`

2. **与其他参数的关系**
   - `maxSpreadCents`：用于下单前的价差检查（硬性限制）
   - `marketQualityMaxSpreadCents`：用于质量分数计算（软性限制）
   - 两者可以不同，但建议保持一致

3. **日志中的订单簿价差**
   - 日志显示：`bid=0.0100, ask=0.9900`（价差 98 分）
   - 这远超 `marketQualityMaxSpreadCents: 6` 的限制
   - 会导致质量分数大幅扣分，无法通过门控

## 调试建议

如果遇到订单簿价差异常（如 98 分），需要检查：
1. WebSocket 数据是否正常更新
2. 订单簿数据源是否准确
3. 市场是否真的流动性极差
4. 是否需要调整 `marketQualityMaxSpreadCents` 以适应市场条件

