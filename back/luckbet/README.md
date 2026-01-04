# LuckBet 策略

LuckBet 是一个基于价格速度的高频交易策略，专为 Polymarket 预测市场设计。该策略通过监控 UP/DOWN 代币的价格变化速度，在检测到足够的动量时执行配对交易，旨在通过快速执行互补头寸来获得一致的小额利润。

## 核心理念

"速度跟随"：当某一方向的价格移动速度超过阈值时，立即买入该方向（Entry 订单），同时在对侧挂限价单（Hedge 订单），形成市场中性的配对交易。

## 主要特性

### 🚀 速度引擎
- 实时价格速度计算
- 可配置的时间窗口和阈值
- 线性回归或时间加权平均算法
- 自动样本管理和内存优化

### 📊 智能执行
- 支持顺序和并行执行模式
- 自动订单状态跟踪
- 指数退避重试机制
- 订单超时和重下逻辑

### 🛡️ 风险控制
- 每周期交易次数限制
- 库存不平衡监控
- 市场质量过滤
- 周期结束保护机制

### 💰 头寸管理
- 多层次止盈策略
- 时间止损保护
- 自动退出逻辑
- 实时盈亏计算

### 🖥️ 终端界面
- 实时监控面板
- 价格和速度指标显示
- 头寸和盈亏统计
- 可配置更新频率

### 🔗 外部数据集成
- Binance 开盘偏向过滤
- 底层资产移动确认
- 优雅降级机制

## 项目结构

```
back/luckbet/
├── types.go           # 核心数据类型定义
├── config.go          # 配置管理器
├── strategy.go        # 主策略实现
├── velocity_engine.go # 速度计算引擎（待实现）
├── order_executor.go  # 订单执行器（待实现）
├── risk_controller.go # 风险控制器（待实现）
├── position_manager.go# 头寸管理器（待实现）
├── terminal_ui.go     # 终端界面（待实现）
├── *_test.go          # 单元测试和属性测试
└── README.md          # 本文档
```

## 配置说明

策略配置文件位于 `yml/luckbet.yaml`，主要配置项包括：

### 基础交易参数
- `orderSize`: Entry订单大小
- `hedgeOrderSize`: Hedge订单大小

### 速度参数
- `windowSeconds`: 速度计算窗口大小
- `minMoveCents`: 最小价格变化阈值
- `minVelocityCentsPerSec`: 最小速度阈值

### 执行模式
- `orderExecutionMode`: 订单执行模式（sequential/parallel）
- `sequentialCheckIntervalMs`: 顺序模式检查间隔
- `sequentialMaxWaitMs`: 顺序模式最大等待时间

### 风险控制
- `maxTradesPerCycle`: 每周期最大交易次数
- `inventoryThreshold`: 库存不平衡阈值
- `cycleEndProtectionMinutes`: 周期结束保护时间

### 退出策略
- `takeProfitCents`: 止盈价格
- `stopLossCents`: 止损价格
- `maxHoldSeconds`: 最大持有时间
- `partialTakeProfits`: 分批止盈配置

## 环境变量覆盖

支持通过环境变量覆盖配置文件设置，环境变量格式为 `LUCKBET_FIELD_NAME`：

```bash
export LUCKBET_ORDER_SIZE=25.0
export LUCKBET_WINDOW_SECONDS=45
export LUCKBET_ENABLE_TERMINAL_UI=true
export LUCKBET_ORDER_EXECUTION_MODE=parallel
```

## 使用方法

### 1. 配置策略

编辑 `yml/luckbet.yaml` 文件，根据需要调整参数：

```yaml
orderSize: 10.0
windowSeconds: 30
minVelocityCentsPerSec: 0.5
orderExecutionMode: "sequential"
enableTerminalUI: true
```

### 2. 启动策略

在主配置文件中添加 LuckBet 策略：

```yaml
exchangeStrategies:
  - luckbet:
      orderSize: 10.0
      windowSeconds: 30
      # ... 其他配置
```

### 3. 监控运行

如果启用了终端UI，将显示实时监控面板：

```
┌─────────────────────────────────────────────────────────────┐
│                    LuckBet 策略监控                          │
├─────────────────────────────────────────────────────────────┤
│ 周期: btc-updown-15m-1234567890    剩余: 08:45              │
│ UP:   $0.6500 (↑ +2.5¢/s)         DOWN: $0.3500 (↓ -1.2¢/s)│
│ 头寸: UP 15.0 | DOWN 12.0          平衡: +3.0               │
│ 盈亏: UP胜 +$8.50 | DOWN胜 +$6.20  总计: +$14.70           │
└─────────────────────────────────────────────────────────────┘
```

## 测试

运行单元测试：

```bash
go test -v ./back/luckbet
```

运行属性测试（将在后续任务中实现）：

```bash
go test -v ./back/luckbet -run TestProperty
```

## 性能指标

策略会自动收集以下性能指标：

- **交易统计**: 总交易数、成功率、失败率
- **盈亏统计**: 总盈亏、平均盈利、平均亏损、盈亏比
- **风险指标**: 最大回撤、夏普比率、最大库存不平衡
- **执行指标**: 平均执行时间、订单成交率、滑点统计

## 故障排除

### 常见问题

1. **配置验证失败**
   - 检查配置文件语法是否正确
   - 确认所有必需参数都已设置
   - 验证参数值在有效范围内

2. **订单执行失败**
   - 检查网络连接状态
   - 确认账户余额充足
   - 验证市场是否处于交易状态

3. **速度计算异常**
   - 检查价格数据是否正常接收
   - 确认时间窗口配置合理
   - 验证样本数量是否充足

4. **UI显示问题**
   - 确认终端支持ANSI颜色
   - 检查终端窗口大小
   - 验证UI更新间隔设置

### 日志分析

策略使用结构化日志，关键日志标识：

- `🚀` 策略初始化
- `📈` 价格变化事件
- `⚡` 速度触发
- `💰` 交易执行
- `🛡️` 风险控制
- `⚠️` 警告信息
- `❌` 错误信息

## 开发状态

当前实现状态：

- ✅ 核心数据类型定义
- ✅ 配置管理器
- ✅ 基础策略框架
- ✅ 单元测试框架
- ⏳ 速度引擎（任务2）
- ⏳ 订单执行器（任务3）
- ⏳ 风险控制器（任务4）
- ⏳ 头寸管理器（任务5）
- ⏳ 终端界面（任务6）
- ⏳ 属性测试（各任务中）

## 贡献指南

1. 遵循现有代码风格和架构模式
2. 为新功能添加相应的单元测试
3. 更新相关文档和配置示例
4. 确保所有测试通过后提交代码

## 许可证

本项目遵循与主项目相同的许可证。