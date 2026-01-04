# LuckBet 策略设计文档

## 概述

LuckBet 是一个基于价格速度的高频交易策略，专为 Polymarket 预测市场设计。该策略通过监控 UP/DOWN 代币的价格变化速度，在检测到足够的动量时执行配对交易，旨在通过快速执行互补头寸来获得一致的小额利润。

核心理念是"速度跟随"：当某一方向的价格移动速度超过阈值时，立即买入该方向（Entry 订单），同时在对侧挂限价单（Hedge 订单），形成市场中性的配对交易。

## 架构

### 整体架构图

```
┌─────────────────────────────────────────────────────────────┐
│                    LuckBet 策略架构                          │
├─────────────────────────────────────────────────────────────┤
│  ┌─────────────────┐    ┌─────────────────┐                 │
│  │   Terminal UI   │    │  Configuration  │                 │
│  │   (Bubbletea)   │    │    Manager      │                 │
│  └─────────────────┘    └─────────────────┘                 │
├─────────────────────────────────────────────────────────────┤
│                    Strategy Core                             │
│  ┌─────────────────┐    ┌─────────────────┐                 │
│  │ Velocity Engine │    │ Risk Controller │                 │
│  │                 │    │                 │                 │
│  └─────────────────┘    └─────────────────┘                 │
│                                                             │
│  ┌─────────────────┐    ┌─────────────────┐                 │
│  │ Order Executor  │    │Position Manager │                 │
│  │                 │    │                 │                 │
│  └─────────────────┘    └─────────────────┘                 │
├─────────────────────────────────────────────────────────────┤
│                   External Services                          │
│  ┌─────────────────┐    ┌─────────────────┐                 │
│  │ Trading Service │    │ Market Data     │                 │
│  │                 │    │ (Binance)       │                 │
│  └─────────────────┘    └─────────────────┘                 │
└─────────────────────────────────────────────────────────────┘
```

### 模块职责

1. **Strategy Core**: 主策略逻辑，协调各个组件
2. **Velocity Engine**: 价格速度计算和信号生成
3. **Order Executor**: 订单执行和管理
4. **Risk Controller**: 风险控制和市场质量过滤
5. **Position Manager**: 头寸管理和退出策略
6. **Terminal UI**: 实时监控界面
7. **Configuration Manager**: 配置管理和验证

## 组件和接口

### 1. Velocity Engine

负责价格速度计算和交易信号生成。

```go
type VelocityEngine struct {
    samples     map[domain.TokenType][]PriceSample
    windowSize  time.Duration
    thresholds  VelocityThresholds
    mu          sync.RWMutex
}

type PriceSample struct {
    Timestamp   time.Time
    PriceCents  int
}

type VelocityMetrics struct {
    TokenType   domain.TokenType
    Delta       int     // 价格变化（分）
    Duration    float64 // 时间窗口（秒）
    Velocity    float64 // 速度（分/秒）
    IsValid     bool
}

// 接口方法
func (ve *VelocityEngine) AddPriceSample(tokenType domain.TokenType, price domain.Price, timestamp time.Time)
func (ve *VelocityEngine) CalculateVelocity(tokenType domain.TokenType) VelocityMetrics
func (ve *VelocityEngine) CheckTriggerConditions() (domain.TokenType, VelocityMetrics, bool)
func (ve *VelocityEngine) PruneSamples(cutoff time.Time)
```

### 2. Order Executor

处理订单执行逻辑，支持顺序和并行模式。

```go
type OrderExecutor struct {
    tradingService *services.TradingService
    mode          ExecutionMode
    config        ExecutionConfig
}

type ExecutionMode string
const (
    SequentialMode ExecutionMode = "sequential"
    ParallelMode   ExecutionMode = "parallel"
)

type TradeRequest struct {
    Market      *domain.Market
    Winner      domain.TokenType
    EntryPrice  domain.Price
    HedgePrice  domain.Price
    EntryShares float64
    HedgeShares float64
}

type TradeResult struct {
    EntryOrderID string
    HedgeOrderID string
    Success      bool
    Error        error
}

// 接口方法
func (oe *OrderExecutor) ExecuteTrade(ctx context.Context, request TradeRequest) TradeResult
func (oe *OrderExecutor) MonitorOrders(ctx context.Context, entryOrderID, hedgeOrderID string)
func (oe *OrderExecutor) CancelOrder(ctx context.Context, orderID string) error
```

### 3. Risk Controller

实施风险控制和市场质量过滤。

```go
type RiskController struct {
    config           RiskConfig
    tradeCount       int
    lastTradeTime    time.Time
    inventoryTracker *InventoryTracker
}

type RiskConfig struct {
    MaxTradesPerCycle        int
    InventoryThreshold       float64
    MaxSpreadCents          int
    CycleEndProtectionMins  int
    MarketQualityMinScore   int
}

type RiskCheckResult struct {
    Allowed bool
    Reason  string
}

// 接口方法
func (rc *RiskController) CheckTradeAllowed(market *domain.Market, tokenType domain.TokenType) RiskCheckResult
func (rc *RiskController) CheckMarketQuality(market *domain.Market) RiskCheckResult
func (rc *RiskController) UpdateTradeCount()
func (rc *RiskController) ResetCycle()
```

### 4. Position Manager

管理头寸和执行退出策略。

```go
type PositionManager struct {
    tradingService *services.TradingService
    exitConfig     ExitConfig
    positions      map[string]*PositionInfo
}

type ExitConfig struct {
    TakeProfitCents       int
    StopLossCents         int
    MaxHoldSeconds        int
    ExitBothSidesIfHedged bool
    PartialTakeProfits    []PartialTakeProfit
}

type PositionInfo struct {
    Market        *domain.Market
    TokenType     domain.TokenType
    EntryPrice    domain.Price
    Size          float64
    EntryTime     time.Time
    CostBasis     float64
}

// 接口方法
func (pm *PositionManager) CheckExitConditions(ctx context.Context) []ExitAction
func (pm *PositionManager) ExecuteExit(ctx context.Context, action ExitAction) error
func (pm *PositionManager) UpdatePositions()
func (pm *PositionManager) CalculatePnL() PnLSummary
```

### 5. Terminal UI

基于 Bubbletea 的实时监控界面。

```go
type TerminalUI struct {
    enabled        bool
    model          dashboardModel
    program        *tea.Program
    updateChannel  chan UIUpdate
}

type dashboardModel struct {
    strategy       *Strategy
    tradingService *services.TradingService
    marketSpec     *marketspec.MarketSpec
    
    // 显示数据
    currentCycle   string
    remainingTime  time.Duration
    upPrice        float64
    downPrice      float64
    upVelocity     float64
    downVelocity   float64
    positions      PositionSummary
    pnl           PnLSummary
    
    // UI 状态
    width         int
    height        int
}

type UIUpdate struct {
    Type string
    Data interface{}
}

// 接口方法
func (ui *TerminalUI) Start(ctx context.Context) error
func (ui *TerminalUI) Stop() error
func (ui *TerminalUI) UpdateData(update UIUpdate)
func (ui *TerminalUI) IsEnabled() bool
```

## 数据模型

### 1. 策略配置

```go
type Config struct {
    // 交易参数
    OrderSize           float64 `yaml:"orderSize"`
    HedgeOrderSize      float64 `yaml:"hedgeOrderSize"`
    
    // 速度参数
    WindowSeconds              int     `yaml:"windowSeconds"`
    MinMoveCents              int     `yaml:"minMoveCents"`
    MinVelocityCentsPerSec    float64 `yaml:"minVelocityCentsPerSec"`
    CooldownMs                int     `yaml:"cooldownMs"`
    WarmupMs                  int     `yaml:"warmupMs"`
    MaxTradesPerCycle         int     `yaml:"maxTradesPerCycle"`
    
    // 安全参数
    HedgeOffsetCents      int `yaml:"hedgeOffsetCents"`
    MinEntryPriceCents    int `yaml:"minEntryPriceCents"`
    MaxEntryPriceCents    int `yaml:"maxEntryPriceCents"`
    MaxSpreadCents        int `yaml:"maxSpreadCents"`
    
    // 执行模式
    OrderExecutionMode           string `yaml:"orderExecutionMode"`
    SequentialCheckIntervalMs    int    `yaml:"sequentialCheckIntervalMs"`
    SequentialMaxWaitMs          int    `yaml:"sequentialMaxWaitMs"`
    
    // 风险控制
    CycleEndProtectionMinutes    int     `yaml:"cycleEndProtectionMinutes"`
    HedgeReorderTimeoutSeconds   int     `yaml:"hedgeReorderTimeoutSeconds"`
    InventoryThreshold           float64 `yaml:"inventoryThreshold"`
    
    // 市场质量
    EnableMarketQualityGate      bool `yaml:"enableMarketQualityGate"`
    MarketQualityMinScore        int  `yaml:"marketQualityMinScore"`
    MarketQualityMaxSpreadCents  int  `yaml:"marketQualityMaxSpreadCents"`
    MarketQualityMaxBookAgeMs    int  `yaml:"marketQualityMaxBookAgeMs"`
    
    // 退出策略
    TakeProfitCents       int                   `yaml:"takeProfitCents"`
    StopLossCents         int                   `yaml:"stopLossCents"`
    MaxHoldSeconds        int                   `yaml:"maxHoldSeconds"`
    ExitCooldownMs        int                   `yaml:"exitCooldownMs"`
    ExitBothSidesIfHedged bool                  `yaml:"exitBothSidesIfHedged"`
    PartialTakeProfits    []PartialTakeProfit   `yaml:"partialTakeProfits"`
    
    // 外部数据
    UseBinanceOpen1mBias         bool    `yaml:"useBinanceOpen1mBias"`
    BiasMode                     string  `yaml:"biasMode"`
    UseBinanceMoveConfirm        bool    `yaml:"useBinanceMoveConfirm"`
    
    // UI 配置
    EnableTerminalUI             bool `yaml:"enableTerminalUI"`
    UIUpdateIntervalMs           int  `yaml:"uiUpdateIntervalMs"`
}
```

### 2. 交易状态

```go
type TradingState struct {
    // 周期状态
    CurrentCycle        string
    CycleStartTime      time.Time
    FirstSeenAt         time.Time
    
    // 交易统计
    TradesThisCycle     int
    PendingTrades       map[string]string // entryOrderID -> hedgeOrderID
    LastTriggerAt       time.Time
    LastTriggerSide     domain.TokenType
    
    // 速度数据
    VelocitySamples     map[domain.TokenType][]PriceSample
    
    // 风险状态
    InventoryBalance    float64
    UnhedgedEntries     map[string]*domain.Order
    
    // 外部数据状态
    BiasReady          bool
    BiasToken          domain.TokenType
    BiasReason         string
}
```

### 3. 性能指标

```go
type PerformanceMetrics struct {
    // 交易统计
    TotalTrades         int
    SuccessfulTrades    int
    FailedTrades        int
    SuccessRate         float64
    
    // 盈亏统计
    TotalPnL           float64
    WinningTrades      int
    LosingTrades       int
    AverageWin         float64
    AverageLoss        float64
    ProfitFactor       float64
    
    // 风险指标
    MaxDrawdown        float64
    SharpeRatio        float64
    MaxInventoryImbalance float64
    
    // 执行指标
    AverageExecutionTime time.Duration
    OrderFillRate       float64
    SlippageStats       SlippageMetrics
}
```

## 正确性属性

*属性是一个特征或行为，应该在系统的所有有效执行中保持为真——本质上是关于系统应该做什么的正式陈述。属性作为人类可读规范和机器可验证正确性保证之间的桥梁。*

基于预分析，以下是从验收标准转换而来的可测试正确性属性：

### 属性 1: 速度计算一致性
*对于任何*价格数据输入和时间窗口配置，速度计算应该在配置的时间窗口内产生一致和准确的结果
**验证需求: 1.1, 1.3**

### 属性 2: 阈值触发准确性
*对于任何*速度计算结果，当且仅当速度超过配置阈值时，策略应该正确识别更快移动的代币方向作为入场候选
**验证需求: 1.2**

### 属性 3: 样本管理效率
*对于任何*时间窗口更新，旧的价格样本应该被正确修剪以保持内存效率，同时保留计算所需的数据
**验证需求: 1.4**

### 属性 4: 订单执行完整性
*对于任何*有效的交易信号，策略应该按照配置的执行模式正确下单，确保 Entry 和 Hedge 订单的配对完整性
**验证需求: 2.1, 2.2, 5.1, 5.2**

### 属性 5: 价格计算正确性
*对于任何*入场价格和配置的对冲偏移，对冲价格计算应该确保最小利润边际并防止结构性亏损
**验证需求: 2.3**

### 属性 6: 错误处理鲁棒性
*对于任何*订单失败情况，策略应该优雅地处理错误并防止未对冲头寸的产生
**验证需求: 2.5, 5.3**

### 属性 7: UI 数据同步性
*对于任何*策略状态变化，终端界面应该准确反映当前的市场信息、头寸状态和盈亏计算
**验证需求: 3.2, 3.3, 3.4**

### 属性 8: 风险控制有效性
*对于任何*风险条件触发（交易限制、库存不平衡、市场质量差），策略应该正确应用风险控制措施
**验证需求: 4.1, 4.2, 4.3, 8.1, 8.2**

### 属性 9: 时间保护机制
*对于任何*时间相关的保护条件（周期结束保护、最大持有时间），策略应该在正确的时间点触发保护措施
**验证需求: 4.4, 6.3**

### 属性 10: 退出策略执行
*对于任何*退出条件（止盈、止损、时间止损），策略应该使用正确的订单类型和时机执行头寸退出
**验证需求: 6.1, 6.2, 6.4, 6.5**

### 属性 11: 日志记录完整性
*对于任何*重要的策略事件（交易执行、错误发生、状态变化），应该记录包含足够调试信息的日志条目
**验证需求: 7.1, 7.3, 7.4**

### 属性 12: 外部数据集成
*对于任何*外部数据源状态（可用或不可用），策略应该正确处理数据集成并在必要时优雅降级
**验证需求: 9.1, 9.3, 9.4**

## 错误处理

### 1. 订单执行错误

- **连接错误**: 实施指数退避重试机制
- **订单拒绝**: 区分系统级拒绝和策略级错误
- **部分成交**: 处理 FAK 订单的部分成交情况
- **超时错误**: 实施订单超时和重下机制

### 2. 数据源错误

- **价格数据缺失**: 使用最后已知价格并记录警告
- **外部数据源故障**: 降级到仅使用 Polymarket 数据
- **数据延迟**: 实施数据新鲜度检查

### 3. 系统错误

- **内存不足**: 实施样本数量限制和自动清理
- **网络中断**: 实施重连机制和状态恢复
- **配置错误**: 启动时验证配置并提供清晰的错误信息

## 测试策略

### 双重测试方法

策略将采用单元测试和基于属性的测试相结合的方法：

**单元测试覆盖**:
- 特定的业务逻辑示例
- 边界条件和错误情况
- 组件集成点
- UI 启动和配置禁用场景

**基于属性的测试覆盖**:
- 使用 Go 的 `testing/quick` 包进行属性测试
- 每个属性测试运行最少 100 次迭代
- 每个属性测试都标记对应的设计文档属性编号
- 测试标签格式: `**Feature: luckbet-strategy, Property {number}: {property_text}**`

**测试库选择**: 使用 Go 标准库的 `testing/quick` 包进行基于属性的测试，结合自定义生成器来创建有效的测试数据。

**测试配置**: 每个基于属性的测试配置为运行 100 次迭代，以确保充分的随机输入覆盖。

### 测试数据生成

为了支持基于属性的测试，将实现以下数据生成器：

- **价格数据生成器**: 生成有效的价格序列和时间戳
- **市场状态生成器**: 生成各种市场条件和订单簿状态
- **配置生成器**: 生成有效的策略配置组合
- **订单状态生成器**: 生成各种订单执行场景

## 性能考虑

### 1. 内存管理

- 价格样本数量限制（最大 512 个样本）
- 定期清理过期数据
- 使用对象池减少 GC 压力

### 2. 并发安全

- 使用读写锁保护共享状态
- 最小化锁持有时间
- 异步处理非关键路径操作

### 3. UI 性能

- 可配置的 UI 更新频率
- 数据更新与渲染分离
- 支持完全禁用 UI 以节省资源

### 4. 网络优化

- 连接池复用
- 批量订单操作
- 智能重试机制

## 部署和监控

### 1. 配置管理

- 支持热重载配置
- 配置验证和默认值
- 环境特定的配置覆盖

### 2. 日志和监控

- 结构化日志输出
- 性能指标收集
- 健康检查端点

### 3. 故障恢复

- 优雅关闭机制
- 状态持久化和恢复
- 自动重启策略