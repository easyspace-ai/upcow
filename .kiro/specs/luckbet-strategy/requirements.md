# Requirements Document

## Introduction

LuckBet is a high-frequency trading strategy designed for Polymarket prediction markets that captures price velocity changes to execute paired hedge trades. The strategy aims to generate consistent small profits by rapidly executing complementary UP/DOWN positions, leveraging price momentum to capture arbitrage opportunities within market cycles.

## Glossary

- **LuckBet_Strategy**: The main trading strategy implementation that monitors price velocity and executes paired trades
- **Price_Velocity**: The rate of price change measured in cents per second within a specified time window
- **Entry_Order**: The primary FAK (Fill-And-Kill) order that purchases the fast-moving token direction
- **Hedge_Order**: The complementary GTC (Good-Till-Cancel) limit order that purchases the opposite token direction
- **Market_Cycle**: The time-bounded trading period for a specific prediction market (typically 15 minutes or 1 hour)
- **Paired_Trade**: A complete trading unit consisting of one Entry_Order and one Hedge_Order
- **Terminal_Interface**: The real-time dashboard displaying trading metrics, positions, and cycle information
- **Velocity_Threshold**: The minimum price movement speed required to trigger trading
- **Hedge_Offset**: The price buffer applied to Hedge_Orders to ensure profitability
- **Inventory_Balance**: The difference between UP and DOWN token holdings

## Requirements

### 需求 1

**用户故事：** 作为交易员，我希望实时监控价格速度，以便识别有利可图的配对交易的动量机会。

#### Acceptance Criteria

1. WHEN receiving UP or DOWN token price data, THE LuckBet_Strategy SHALL calculate velocity within a configurable time window
2. WHEN velocity exceeds configured thresholds, THE LuckBet_Strategy SHALL identify the faster-moving token direction as entry candidate
3. WHEN multiple price samples exist within the time window, THE LuckBet_Strategy SHALL use linear regression or time-weighted averaging to calculate accurate velocity
4. WHEN the velocity calculation window updates, THE LuckBet_Strategy SHALL prune old samples to maintain performance
5. WHEN calculating velocity data, THE LuckBet_Strategy SHALL log velocity metrics for monitoring and debugging

### 需求 2

**用户故事：** 作为交易员，我希望自动执行配对交易，以便从价格动量中获利，同时保持市场中性风险。

#### Acceptance Criteria

1. WHEN a token direction velocity threshold is exceeded, THE LuckBet_Strategy SHALL place an Entry_Order using FAK order type
2. WHEN an Entry_Order is successfully placed, THE LuckBet_Strategy SHALL immediately place a corresponding Hedge_Order using GTC order type
3. WHEN calculating hedge price, THE LuckBet_Strategy SHALL apply configured Hedge_Offset to ensure minimum profit margin
4. WHEN both orders are placed, THE LuckBet_Strategy SHALL track the Paired_Trade status until completion
5. WHEN either order fails, THE LuckBet_Strategy SHALL handle the failure gracefully and prevent unhedged positions

### 需求 3

**用户故事：** 作为交易员，我希望有专业的终端界面，以便实时监控策略性能和市场状况。

#### Acceptance Criteria

1. WHEN the strategy starts, THE Terminal_Interface SHALL display current Market_Cycle name and remaining time countdown
2. WHEN price data updates, THE Terminal_Interface SHALL display current UP and DOWN token prices with velocity indicators
3. WHEN positions exist, THE Terminal_Interface SHALL display current Inventory_Balance showing UP/DOWN token quantities
4. WHEN calculating potential outcomes, THE Terminal_Interface SHALL display profit/loss projections for UP and DOWN winning scenarios
5. WHEN the interface is disabled through configuration, THE Terminal_Interface SHALL NOT consume system resources or display output

### 需求 4

**用户故事：** 作为交易员，我希望有可配置的风险控制，以便限制敞口并防止在波动期间过度损失。

#### Acceptance Criteria

1. WHEN the maximum trades per cycle limit is reached, THE LuckBet_Strategy SHALL reject new trading signals until the next cycle
2. WHEN inventory imbalance exceeds configured thresholds, THE LuckBet_Strategy SHALL reduce trading frequency in the overweight direction
3. WHEN market spread exceeds the allowed maximum spread, THE LuckBet_Strategy SHALL skip trade execution to avoid poor fills
4. WHEN cycle end protection is enabled, THE LuckBet_Strategy SHALL stop opening new positions within configured time before cycle end
5. WHEN stop-loss conditions are met, THE LuckBet_Strategy SHALL exit positions using market orders

### 需求 5

**用户故事：** 作为交易员，我希望有智能的订单执行模式，以便根据市场条件优化速度或安全性。

#### Acceptance Criteria

1. WHEN sequential execution mode is selected, THE LuckBet_Strategy SHALL wait for Entry_Order confirmation before placing Hedge_Order
2. WHEN parallel execution mode is selected, THE LuckBet_Strategy SHALL place Entry_Order and Hedge_Order simultaneously
3. WHEN Entry_Order fails in sequential mode, THE LuckBet_Strategy SHALL cancel any pending Hedge_Orders to prevent unhedged exposure
4. WHEN hedge order timeout is reached, THE LuckBet_Strategy SHALL attempt to re-hedge the position at market price
5. WHEN order execution repeatedly fails, THE LuckBet_Strategy SHALL implement exponential backoff to prevent system overload

### 需求 6

**用户故事：** 作为交易员，我希望有头寸管理和退出策略，以便自动实现利润并限制损失。

#### Acceptance Criteria

1. WHEN any position reaches take-profit threshold, THE LuckBet_Strategy SHALL execute exit orders using FAK order type
2. WHEN stop-loss threshold is breached, THE LuckBet_Strategy SHALL immediately exit positions to limit further losses
3. WHEN maximum holding time is exceeded, THE LuckBet_Strategy SHALL force exit all positions regardless of profit or loss
4. WHEN both UP and DOWN positions exist simultaneously, THE LuckBet_Strategy SHALL exit both sides if configured to do so
5. WHEN partial take-profit levels are configured, THE LuckBet_Strategy SHALL execute staged exits at specified profit levels

### 需求 7

**用户故事：** 作为系统管理员，我希望有全面的日志记录和监控，以便分析策略性能并排除故障。

#### Acceptance Criteria

1. WHEN executing trades, THE LuckBet_Strategy SHALL log detailed order information including price, quantity, and timestamp
2. WHEN performing velocity calculations, THE LuckBet_Strategy SHALL log velocity metrics with appropriate rate limiting to prevent log spam
3. WHEN errors occur, THE LuckBet_Strategy SHALL log error details with sufficient debugging context
4. WHEN cycle transitions occur, THE LuckBet_Strategy SHALL log cycle change events and reset internal state appropriately
5. WHEN calculating performance metrics, THE LuckBet_Strategy SHALL log profit/loss statistics and trade success rates

### 需求 8

**用户故事：** 作为交易员，我希望有市场质量过滤，以便避免在流动性差的条件下交易，这可能导致不利的成交。

#### Acceptance Criteria

1. WHEN order book quality falls below minimum score threshold, THE LuckBet_Strategy SHALL skip trade execution
2. WHEN bid-ask spread exceeds allowed maximum spread, THE LuckBet_Strategy SHALL wait for better market conditions
3. WHEN order book data exceeds configured age limit, THE LuckBet_Strategy SHALL refresh data before trading
4. WHEN market data sources are unavailable, THE LuckBet_Strategy SHALL gracefully degrade and avoid trading
5. WHEN market quality improves after degradation, THE LuckBet_Strategy SHALL resume normal trading operations

### 需求 9

**用户故事：** 作为交易员，我希望与外部市场数据集成，以便使用额外信号来改善交易决策。

#### 验收标准

1. 当币安期货数据可用时，LuckBet_策略应使用开盘蜡烛偏差来过滤交易方向
2. 当启用底层资产移动确认时，LuckBet_策略应在交易前验证方向一致性
3. 当外部数据源不可用时，LuckBet_策略应继续仅使用 Polymarket 数据运行
4. 当偏差信号与速度信号冲突时，LuckBet_策略应应用配置的偏差模式（硬或软过滤）
5. 当外部数据表明高波动性时，LuckBet_策略应相应调整风险参数

### 需求 10

**用户故事：** 作为开发者，我希望有干净且可维护的代码库，以便轻松扩展功能并修复问题。

#### 验收标准

1. 当实现策略时，LuckBet_策略应将关注点分离到不同的模块中，用于速度计算、订单执行和界面渲染
2. 当添加新功能时，LuckBet_策略应保持与现有配置文件的向后兼容性
3. 当处理错误时，LuckBet_策略应在整个代码库中使用一致的错误处理模式
4. 当编写测试时，LuckBet_策略应包括单个组件的单元测试和核心逻辑的基于属性的测试
5. 当记录代码时，LuckBet_策略应包括解释复杂算法和业务逻辑的全面注释