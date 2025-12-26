# Split 策略实现方案

## 概述

本文档提供 Polymarket BTC-15分钟 Split 策略的具体实现方案，包括代码结构、关键算法和配置示例。

---

## 策略架构设计

### 1. 核心组件

```
SplitStrategy
├─ PreMarketHandler      # 盘前挂单处理
├─ PostMarketHandler     # 开盘后动态卖出
├─ EndGameHandler        # 尾盘锁定
├─ RiskManager          # 风险管理
└─ StateManager         # 状态管理
```

### 2. 状态机设计

```
状态流转：
[Idle] 
  └─> [PreMarket] (开盘前5分钟)
        └─> [PostMarket] (开盘后0-12分钟)
              └─> [EndGame] (开盘后12-15分钟)
                    └─> [Settled] (结算)
```

---

## 实现细节

### 1. 盘前挂单策略（PreMarketHandler）

#### 1.1 核心逻辑

```go
type PreMarketHandler struct {
    config       PreMarketConfig
    tradingService *services.TradingService
    market       *domain.Market
    positions    *SplitPositions
}

func (h *PreMarketHandler) Execute(ctx context.Context) error {
    // 1. 检查是否在盘前时间窗口
    if !h.isInPreMarketWindow() {
        return nil
    }
    
    // 2. 计算不平衡度
    imbalance := h.calculateImbalance()
    if imbalance < h.config.MinImbalanceCents {
        return nil // 不平衡度不够，不交易
    }
    
    // 3. 确定挂单方向
    side := h.determineOrderSide(imbalance)
    
    // 4. 计算挂单价格和数量
    price, size := h.calculateOrderParams(side)
    
    // 5. 执行挂单
    return h.placeOrder(ctx, side, price, size)
}

func (h *PreMarketHandler) calculateImbalance() int {
    upPrice := h.getCurrentPrice(domain.TokenTypeUp)
    downPrice := h.getCurrentPrice(domain.TokenTypeDown)
    return int(math.Abs(upPrice - downPrice) * 100)
}

func (h *PreMarketHandler) determineOrderSide(imbalance int) domain.TokenType {
    upPrice := h.getCurrentPrice(domain.TokenTypeUp)
    downPrice := h.getCurrentPrice(domain.TokenTypeDown)
    
    // 优先挂单价格更高的一方（预期会回调）
    if upPrice > downPrice {
        return domain.TokenTypeUp
    }
    return domain.TokenTypeDown
}
```

#### 1.2 时间窗口管理

```go
func (h *PreMarketHandler) isInPreMarketWindow() bool {
    now := time.Now()
    marketStart := time.Unix(h.market.Timestamp, 0)
    timeUntilStart := marketStart.Sub(now)
    
    return timeUntilStart <= time.Duration(h.config.StartSecondsBefore)*time.Second &&
           timeUntilStart >= time.Duration(h.config.EndSecondsBefore)*time.Second
}

func (h *PreMarketHandler) getAggressiveness() float64 {
    now := time.Now()
    marketStart := time.Unix(h.market.Timestamp, 0)
    timeUntilStart := marketStart.Sub(now).Seconds()
    
    // 越接近开盘，越保守
    if timeUntilStart <= 60 {
        return 0.3 // 保守模式
    }
    return 1.0 // 激进模式
}
```

---

### 2. 开盘后动态卖出策略（PostMarketHandler）

#### 2.1 价格动量计算

```go
type PostMarketHandler struct {
    config         PostMarketConfig
    tradingService *services.TradingService
    market         *domain.Market
    positions      *SplitPositions
    priceHistory   *PriceHistory
}

func (h *PostMarketHandler) Execute(ctx context.Context) error {
    // 1. 更新价格历史
    h.priceHistory.Update(h.getCurrentPrices())
    
    // 2. 检查当前阶段
    phase := h.getCurrentPhase()
    switch phase {
    case PhaseObservation:
        return nil // 观察期，不交易
    case PhaseActive:
        return h.executeActivePhase(ctx)
    case PhaseConservative:
        return h.executeConservativePhase(ctx)
    }
    return nil
}

func (h *PostMarketHandler) calculateMomentum(tokenType domain.TokenType) float64 {
    currentPrice := h.getCurrentPrice(tokenType)
    price5sAgo := h.priceHistory.GetPrice(tokenType, 5*time.Second)
    
    if price5sAgo <= 0 {
        return 0
    }
    
    return (currentPrice - price5sAgo) / price5sAgo
}

func (h *PostMarketHandler) executeActivePhase(ctx context.Context) error {
    // 1. 计算动量
    momentumUp := h.calculateMomentum(domain.TokenTypeUp)
    momentumDown := h.calculateMomentum(domain.TokenTypeDown)
    
    // 2. 检查卖出条件
    if momentumUp > h.config.MomentumThreshold && h.positions.Up > 0 {
        sellSize := h.positions.Up * h.config.SellRatio
        return h.sellToken(ctx, domain.TokenTypeUp, sellSize)
    }
    
    if momentumDown > h.config.MomentumThreshold && h.positions.Down > 0 {
        sellSize := h.positions.Down * h.config.SellRatio
        return h.sellToken(ctx, domain.TokenTypeDown, sellSize)
    }
    
    return nil
}
```

#### 2.2 价差套利逻辑

```go
func (h *PostMarketHandler) calculateSpread() float64 {
    upPrice := h.getCurrentPrice(domain.TokenTypeUp)
    downPrice := h.getCurrentPrice(domain.TokenTypeDown)
    return upPrice - downPrice
}

func (h *PostMarketHandler) executeConservativePhase(ctx context.Context) error {
    spread := h.calculateSpread()
    spreadCents := int(spread * 100)
    
    // 价差套利：卖出价格高的一方
    if spreadCents > h.config.SpreadThreshold*100 {
        if h.positions.Up > 0 {
            sellSize := h.positions.Up * h.config.SellRatio
            return h.sellToken(ctx, domain.TokenTypeUp, sellSize)
        }
    } else if spreadCents < -h.config.SpreadThreshold*100 {
        if h.positions.Down > 0 {
            sellSize := h.positions.Down * h.config.SellRatio
            return h.sellToken(ctx, domain.TokenTypeDown, sellSize)
        }
    }
    
    return nil
}
```

---

### 3. 尾盘锁定策略（EndGameHandler）

#### 3.1 渐进式锁定

```go
type EndGameHandler struct {
    config         EndGameConfig
    tradingService *services.TradingService
    market         *domain.Market
    positions      *SplitPositions
    lockState      *LockState
}

func (h *EndGameHandler) Execute(ctx context.Context) error {
    // 1. 检查是否在锁定时间窗口
    if !h.isInLockWindow() {
        return nil
    }
    
    // 2. 确定锁定方向
    direction := h.determineLockDirection()
    if direction == DirectionUnknown {
        return nil // 方向不明确，不锁定
    }
    
    // 3. 执行渐进式锁定
    return h.executeProgressiveLock(ctx, direction)
}

func (h *EndGameHandler) determineLockDirection() Direction {
    // 多重确认
    trendConfirmed := h.confirmTrend()
    spreadLarge := h.isSpreadLarge()
    positionRatio := h.calculatePositionRatio()
    
    if trendConfirmed && spreadLarge && positionRatio > h.config.MinPositionRatio {
        upPrice := h.getCurrentPrice(domain.TokenTypeUp)
        downPrice := h.getCurrentPrice(domain.TokenTypeDown)
        
        if upPrice > downPrice {
            return DirectionUp
        }
        return DirectionDown
    }
    
    return DirectionUnknown
}

func (h *EndGameHandler) executeProgressiveLock(ctx context.Context, direction Direction) error {
    timeLeft := h.getTimeUntilSettlement()
    
    // 根据剩余时间决定卖出比例
    var sellRatio float64
    if timeLeft > 120*time.Second {
        sellRatio = h.config.FirstSellRatio
    } else if timeLeft > 30*time.Second {
        sellRatio = h.config.SecondSellRatio
    } else {
        sellRatio = 1.0 - h.config.FinalReserveRatio
    }
    
    // 卖出弱势方
    weakSide := h.getWeakSide(direction)
    sellSize := h.positions.Get(weakSide) * sellRatio
    
    return h.sellToken(ctx, weakSide, sellSize)
}
```

#### 3.2 趋势确认

```go
func (h *EndGameHandler) confirmTrend() bool {
    // 检查过去3分钟的价格趋势
    now := time.Now()
    threeMinutesAgo := now.Add(-3 * time.Minute)
    
    upPriceNow := h.getCurrentPrice(domain.TokenTypeUp)
    upPrice3mAgo := h.priceHistory.GetPriceAt(domain.TokenTypeUp, threeMinutesAgo)
    
    downPriceNow := h.getCurrentPrice(domain.TokenTypeDown)
    downPrice3mAgo := h.priceHistory.GetPriceAt(domain.TokenTypeDown, threeMinutesAgo)
    
    // 趋势确认：价格持续上涨或下跌
    upTrend := (upPriceNow - upPrice3mAgo) > 0.05 // 5分涨幅
    downTrend := (downPriceNow - downPrice3mAgo) > 0.05
    
    return upTrend || downTrend
}
```

---

### 4. 风险管理（RiskManager）

```go
type RiskManager struct {
    config RiskControlConfig
}

func (rm *RiskManager) CheckSellOrder(tokenType domain.TokenType, size float64, price float64) error {
    // 1. 检查卖出比例限制
    totalPosition := rm.getTotalPosition()
    sellRatio := size / totalPosition
    if sellRatio > rm.config.MaxSellRatio {
        return fmt.Errorf("卖出比例超过限制: %.2f > %.2f", sellRatio, rm.config.MaxSellRatio)
    }
    
    // 2. 检查价格保护
    costPrice := rm.getCostPrice(tokenType)
    minPrice := costPrice - float64(rm.config.PriceProtectionCents)/100
    if price < minPrice {
        return fmt.Errorf("卖出价格低于保护价: %.4f < %.4f", price, minPrice)
    }
    
    // 3. 检查滑点
    bestBid := rm.getBestBid(tokenType)
    slippage := (bestBid - price) / bestBid
    if slippage > float64(rm.config.MaxSlippageCents)/100 {
        return fmt.Errorf("滑点过大: %.4f > %.4f", slippage, float64(rm.config.MaxSlippageCents)/100)
    }
    
    return nil
}
```

---

## 数据结构

### SplitPositions

```go
type SplitPositions struct {
    Up   float64 // UP 持仓
    Down float64 // DOWN 持仓
    
    UpCost   float64 // UP 总成本
    DownCost float64 // DOWN 总成本
    
    UpAvgPrice   float64 // UP 平均价格
    DownAvgPrice float64 // DOWN 平均价格
}

func (sp *SplitPositions) GetTotal() float64 {
    return sp.Up + sp.Down
}

func (sp *SplitPositions) Get(tokenType domain.TokenType) float64 {
    if tokenType == domain.TokenTypeUp {
        return sp.Up
    }
    return sp.Down
}

func (sp *SplitPositions) CalculateProfit(tokenType domain.TokenType, currentPrice float64) float64 {
    if tokenType == domain.TokenTypeUp {
        return sp.Up*currentPrice - (sp.UpCost + sp.DownCost)
    }
    return sp.Down*currentPrice - (sp.UpCost + sp.DownCost)
}
```

### PriceHistory

```go
type PriceHistory struct {
    data map[domain.TokenType][]PricePoint
    mu   sync.RWMutex
}

type PricePoint struct {
    Price     float64
    Timestamp time.Time
}

func (ph *PriceHistory) Update(prices map[domain.TokenType]float64) {
    ph.mu.Lock()
    defer ph.mu.Unlock()
    
    now := time.Now()
    for tokenType, price := range prices {
        ph.data[tokenType] = append(ph.data[tokenType], PricePoint{
            Price:     price,
            Timestamp: now,
        })
        
        // 只保留最近1分钟的数据
        cutoff := now.Add(-1 * time.Minute)
        filtered := []PricePoint{}
        for _, point := range ph.data[tokenType] {
            if point.Timestamp.After(cutoff) {
                filtered = append(filtered, point)
            }
        }
        ph.data[tokenType] = filtered
    }
}

func (ph *PriceHistory) GetPrice(tokenType domain.TokenType, ago time.Duration) float64 {
    ph.mu.RLock()
    defer ph.mu.RUnlock()
    
    points := ph.data[tokenType]
    if len(points) == 0 {
        return 0
    }
    
    targetTime := time.Now().Add(-ago)
    for i := len(points) - 1; i >= 0; i-- {
        if points[i].Timestamp.Before(targetTime) || points[i].Timestamp.Equal(targetTime) {
            return points[i].Price
        }
    }
    
    return points[0].Price
}
```

---

## 配置示例

### YAML 配置

```yaml
strategies:
  enabled:
    - split_strategy
  
  split_strategy:
    # 盘前挂单配置
    pre_market:
      enabled: true
      start_seconds_before: 300      # 开盘前5分钟
      end_seconds_before: 30         # 开盘前30秒
      min_imbalance_cents: 3          # 最小不平衡度（分）
      initial_order_ratio: 0.5        # 初始挂单比例
      max_price_adjustments: 3        # 最大价格调整次数
      aggressive_spread_cents: 2      # 激进模式价差
      conservative_spread_cents: 1    # 保守模式价差
      
    # 开盘后动态卖出配置
    post_market:
      enabled: true
      observation_period: 180         # 观察期（秒）
      active_period_start: 180        # 积极交易期开始（秒）
      active_period_end: 480          # 积极交易期结束（秒）
      conservative_period_start: 480  # 保守交易期开始（秒）
      conservative_period_end: 720    # 保守交易期结束（秒）
      
      # 价格动量策略
      momentum_threshold: 0.02        # 动量阈值（2%）
      momentum_sell_ratio: 0.3         # 动量触发时卖出比例
      min_hold_seconds: 10            # 最小持有时间（秒）
      
      # 价差套利策略
      spread_threshold: 0.10          # 价差阈值（10分）
      spread_sell_ratio: 0.3          # 价差触发时卖出比例
      max_spread: 0.20                # 最大价差（超过则异常）
      
    # 尾盘锁定配置
    end_game:
      enabled: true
      lock_start_seconds: 720         # 锁定开始时间（12分钟）
      trend_confirmation_minutes: 3    # 趋势确认时间（分钟）
      min_spread_cents: 15            # 最小价差（分）
      min_position_ratio: 0.6          # 最小持仓比例
      
      # 渐进式锁定
      first_sell_ratio: 0.3           # 第一次卖出比例
      second_sell_ratio: 0.3          # 第二次卖出比例
      final_reserve_ratio: 0.4         # 最终保留比例
      
      # 对冲保护
      hedge_ratio: 0.3                # 对冲保留比例
      reversal_threshold: 0.05        # 反转阈值（5%）
      
    # 风险控制
    risk_control:
      max_sell_ratio: 0.8             # 最大卖出比例
      min_reserve_ratio: 0.2          # 最小保留比例
      price_protection_cents: 1       # 价格保护（不低于成本价-1分）
      max_slippage_cents: 2           # 最大滑点（分）
      min_order_size: 1.1             # 最小订单金额（USDC）
```

---

## 实施步骤

### 第一阶段：基础框架

1. **创建策略结构**
   - 实现 `SplitStrategy` 主结构
   - 实现 `StateManager` 状态管理
   - 实现 `SplitPositions` 持仓管理

2. **实现盘前挂单**
   - 实现 `PreMarketHandler`
   - 实现不平衡度计算
   - 实现时间窗口管理

3. **实现基础风险控制**
   - 实现 `RiskManager`
   - 实现价格保护
   - 实现滑点控制

### 第二阶段：动态卖出

1. **实现价格历史**
   - 实现 `PriceHistory` 数据结构
   - 实现价格动量计算

2. **实现开盘后策略**
   - 实现 `PostMarketHandler`
   - 实现价格动量策略
   - 实现价差套利策略

### 第三阶段：尾盘锁定

1. **实现趋势确认**
   - 实现趋势判断逻辑
   - 实现多重确认机制

2. **实现尾盘策略**
   - 实现 `EndGameHandler`
   - 实现渐进式锁定
   - 实现反转保护

### 第四阶段：优化和测试

1. **参数调优**
   - 基于回测数据调整参数
   - 优化时间窗口设置

2. **异常处理**
   - 增强错误处理
   - 添加日志和监控

3. **性能优化**
   - 优化价格历史存储
   - 优化并发控制

---

## 监控和日志

### 关键指标监控

```go
type Metrics struct {
    CurrentPhase      string
    Positions         SplitPositions
    RealizedProfit    float64
    UnrealizedProfit  float64
    Imbalance         int
    Momentum          map[domain.TokenType]float64
    Spread            float64
    TimeUntilStart    time.Duration
    TimeUntilSettle   time.Duration
}

func (s *SplitStrategy) GetMetrics() Metrics {
    return Metrics{
        CurrentPhase:     s.stateManager.GetCurrentPhase(),
        Positions:        s.positions,
        RealizedProfit:   s.calculateRealizedProfit(),
        UnrealizedProfit: s.calculateUnrealizedProfit(),
        Imbalance:        s.calculateImbalance(),
        Momentum:         s.calculateMomentum(),
        Spread:           s.calculateSpread(),
        TimeUntilStart:   s.getTimeUntilStart(),
        TimeUntilSettle:  s.getTimeUntilSettle(),
    }
}
```

### 日志记录

```go
log.Infof("🎯 [split] 阶段切换: %s -> %s", oldPhase, newPhase)
log.Infof("💰 [split] 持仓状态: UP=%.2f@%.4f, DOWN=%.2f@%.4f", 
    positions.Up, positions.UpAvgPrice, positions.Down, positions.DownAvgPrice)
log.Infof("📊 [split] 利润状态: 已实现=%.2f, 未实现=%.2f", 
    realizedProfit, unrealizedProfit)
log.Infof("⚡ [split] 价格动量: UP=%.4f, DOWN=%.4f", 
    momentumUp, momentumDown)
log.Infof("📈 [split] 价差: %.2f分", spread*100)
```

---

## 总结

本实现方案提供了完整的 Split 策略实现框架，包括：

1. **三阶段策略**：盘前挂单、开盘后动态卖出、尾盘锁定
2. **风险管理**：价格保护、滑点控制、仓位限制
3. **状态管理**：清晰的状态机和阶段切换
4. **监控指标**：实时监控关键指标

通过这个框架，可以根据实际市场情况灵活调整策略参数，实现稳定的盈利。
