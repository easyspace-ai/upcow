# 🚀 成对交易策略 - 快速启动指南

## 1️⃣ 修改配置文件

编辑 `config.yaml` 文件（如果没有，复制 `config copy.yaml`）：

```yaml
strategies:
  enabled:
    - paired_trading  # 启用成对交易策略
```

## 2️⃣ 启动机器人

```bash
go run cmd/bot/main.go
```

## 3️⃣ 观察输出

你将看到类似以下的输出：

```
✅ 成对交易策略已启动
成对交易策略: 事件循环已启动
成对交易策略: 初始化新市场 btc-updown-15m-1735689600, 周期开始时间=1735689600

成对交易策略: 市场=btc-updown-15m-1735689600, 
  阶段=Build, 锁定=✗ 未锁定, 已过时间=60s
  UP价格=0.5500, DOWN价格=0.4500
  QUp=0.0, QDown=0.0
  P_up=0.00, P_down=0.00

成对交易策略: [建仓阶段] 买入UP - 当前持仓=0.0 < 目标=30.0, 价格=0.5500 < 阈值=0.60

成对交易策略: 订单已创建 [UP/build_up]: ID=xxx, 数量=3.00, 价格=0.5500

成对交易策略: UP订单成交, 数量=3.00, 价格=0.5500, 成本=1.65, QUp=3.0, CUp=1.65
成对交易策略: 即时利润 - UP胜=-1.65 USDC, DOWN胜=-1.65 USDC

...

✅ 成对交易策略: 锁定完成！UP利润=+2.30 USDC, DOWN利润=+0.15 USDC

成对交易策略: 阶段切换 Lock → Amplify

成对交易策略: [放大阶段] 放大UP利润（当前=2.30 → 目标=5.00）
```

## 4️⃣ 关键指标

### 实时监控

- **阶段**: Build / Lock / Amplify
- **锁定状态**: ✗ 未锁定 / ✓ 已锁定
- **持仓**: QUp / QDown
- **利润**: P_up / P_down

### 成功标志

```
✅ 锁定完成！UP利润=+X.XX USDC, DOWN利润=+X.XX USDC
```

当你看到这个消息，说明策略已经成功锁定利润！

## 5️⃣ 参数调整（可选）

如果需要调整参数，编辑 `config.yaml`:

```yaml
paired_trading:
  # 资金量小？降低目标
  base_target: 20.0          # 默认 30.0
  target_profit_base: 1.5    # 默认 2.0
  amplify_target: 3.0        # 默认 5.0
  
  # 更激进？提高目标
  base_target: 50.0          # 默认 30.0
  target_profit_base: 5.0    # 默认 2.0
  amplify_target: 10.0       # 默认 5.0
  
  # 更保守？降低价格阈值
  build_threshold: 0.50      # 默认 0.60
  lock_price_max: 0.60       # 默认 0.70
  amplify_price_max: 0.75    # 默认 0.85
```

## 6️⃣ 常见问题

### Q: 为什么一直在建仓阶段？
A: 价格可能高于 `build_threshold` (默认0.60)，策略不会在高价建仓。

### Q: 为什么没有锁定？
A: 还需要时间积累持仓。观察 P_up 和 P_down，只有两个都为正才算锁定。

### Q: 为什么不进入放大阶段？
A: 必须先完成锁定。如果未锁定，即使到了放大阶段时间，也会继续执行锁定逻辑。

### Q: 如何调整资金量？
A: 修改 `base_target` 和利润目标参数。参考 README.md 中的参数调优建议。

## 7️⃣ 详细文档

- **设计文档**: `docs/paired_trading_design.md`
- **使用文档**: `internal/strategies/pairedtrading/README.md`
- **完成总结**: `docs/PAIRED_TRADING_SUMMARY.md`

---

**祝交易顺利！** 🎉
