# CycleHedge 策略设计改进方案（参考 updownthreshold）

## updownthreshold 策略的设计特点

### 1. 架构设计

**updownthreshold 策略：**
- ✅ **没有 loop/step 机制**：直接在 `OnPriceChanged` 中处理所有逻辑
- ✅ **没有 signalC channel**：不需要信号通道，直接响应价格事件
- ✅ **直接使用 e.Market**：每次价格更新都直接使用 `e.Market`，不需要合并或保存
- ✅ **简单直接**：逻辑都在 `OnPriceChanged` 中，没有复杂的事件合并机制

**cyclehedge 策略：**
- ❌ **有 loop/step 机制**：需要定期执行（tick），处理复杂的交易逻辑
- ❌ **有 signalC channel**：价格事件通过 channel 传递，存在信号丢失问题
- ❌ **依赖价格事件合并**：step 函数需要合并价格事件，存在时序竞争问题

### 2. 市场信息管理

**updownthreshold 策略：**
```go
func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
    if e == nil || e.Market == nil || s.TradingService == nil {
        return nil
    }
    // 直接使用 e.Market，不需要保存或合并
    // ...
    return s.enter(ctx, e.Market, token)
}
```

**cyclehedge 策略：**
```go
func (s *Strategy) step(ctx context.Context, now time.Time) {
    // 需要从 s.latest 合并价格事件
    s.priceMu.Lock()
    evUp := s.latest[domain.TokenTypeUp]
    evDown := s.latest[domain.TokenTypeDown]
    s.latest = make(map[domain.TokenType]*events.PriceChangedEvent)  // 立即清空
    s.priceMu.Unlock()
    
    // 如果价格事件为空，无法获取 market
    if m == nil {
        return  // 直接返回，无法继续
    }
}
```

### 3. 周期状态管理

**updownthreshold 策略：**
```go
func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, newMarket *domain.Market) {
    // 保存周期开始时间
    if newMarket != nil && newMarket.Timestamp > 0 {
        s.cycleStartAt = time.Unix(newMarket.Timestamp, 0)
    }
    // 重置状态
    s.enteredThisCycle = false
    s.inPosition = false
    // ...
}
```

**cyclehedge 策略：**
```go
func (s *Strategy) resetCycle(ctx context.Context, now time.Time, m *domain.Market) {
    s.stateMu.Lock()
    s.currentMarketSlug = m.Slug  // 只保存 slug，不保存完整 market
    s.cycleStartUnix = m.Timestamp
    // ...
}
```

## 改进方案（借鉴 updownthreshold 的设计思路）

### 方案 1：在 OnCycle 和 OnPriceChanged 中保存完整的 market 对象（推荐）

**改进思路：**
- 借鉴 updownthreshold 策略，在 `OnCycle` 和 `OnPriceChanged` 中保存完整的 market 对象
- step 函数可以使用保存的 market 对象作为 fallback
- 不依赖价格事件合并机制

**实施步骤：**

#### 步骤 1：在策略结构体中添加 currentMarket 字段

```go
type Strategy struct {
    // ...
    // per-cycle state
    currentMarketSlug string
    currentMarket     *domain.Market  // 新增：保存完整的 market 对象
    cycleStartUnix    int64
    // ...
}
```

#### 步骤 2：在 OnCycle 中保存 market 对象

```go
func (s *Strategy) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
    if newMarket == nil {
        return
    }
    // 周期结束：先落盘旧周期报表
    if oldMarket != nil {
        s.finalizeAndReport(ctx, oldMarket)
    }
    // 用周期回调快速重置
    now := time.Now()
    s.resetCycle(ctx, now, newMarket)
    
    // 保存完整的 market 对象
    s.stateMu.Lock()
    if newMarket != nil {
        cp := *newMarket
        s.currentMarket = &cp
    }
    s.stateMu.Unlock()
}
```

#### 步骤 3：在 OnPriceChanged 中更新 market 对象

```go
func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
    if e == nil || e.Market == nil {
        return nil
    }
    
    // 更新保存的 market 对象（如果 market 变化了）
    s.stateMu.Lock()
    if s.currentMarket == nil || s.currentMarket.Slug != e.Market.Slug {
        cp := *e.Market
        s.currentMarket = &cp
    }
    s.stateMu.Unlock()
    
    // 原有的逻辑...
    s.priceMu.Lock()
    s.latest[e.TokenType] = e
    s.priceMu.Unlock()
    common.TrySignal(s.signalC)
    return nil
}
```

#### 步骤 4：在 step 函数中使用保存的 market 对象作为 fallback

```go
func (s *Strategy) step(ctx context.Context, now time.Time) {
    if s.TradingService == nil {
        return
    }

    // 1. 尝试从价格事件获取 market（优先）
    var m *domain.Market
    s.priceMu.Lock()
    evUp := s.latest[domain.TokenTypeUp]
    evDown := s.latest[domain.TokenTypeDown]
    s.latest = make(map[domain.TokenType]*events.PriceChangedEvent)
    s.priceMu.Unlock()

    if evUp != nil && evUp.Market != nil {
        m = evUp.Market
    } else if evDown != nil && evDown.Market != nil {
        m = evDown.Market
    }

    // 2. 如果价格事件为空，使用保存的 market 对象（借鉴 updownthreshold 的思路）
    if m == nil {
        s.stateMu.Lock()
        if s.currentMarket != nil {
            // 复制一份，避免竞态
            cp := *s.currentMarket
            m = &cp
        }
        s.stateMu.Unlock()
        
        if m == nil {
            // 完全没有市场信息，返回
            s.drainOrders()
            return
        }
    }

    // 3. 市场过滤
    if !strings.HasPrefix(strings.ToLower(m.Slug), s.marketSlugPrefix) {
        s.drainOrders()
        return
    }
    
    // 继续执行后续逻辑...
}
```

### 方案 2：增加 signalC channel 缓冲（立即实施）

**改进：**
```go
func (s *Strategy) Initialize() error {
    if s.signalC == nil {
        s.signalC = make(chan struct{}, 50)  // 从 1 增加到 50
    }
    // ...
}
```

### 方案 3：优化 loop 函数，优先处理 signalC（可选）

**改进：**
```go
func (s *Strategy) loop(loopCtx context.Context, tickC <-chan time.Time) {
    ticker := time.NewTicker(time.Duration(s.baseLoopTickMs()) * time.Millisecond)
    defer ticker.Stop()

    for {
        select {
        case <-loopCtx.Done():
            return
        case <-s.signalC:
            // 优先处理价格/订单事件
            s.step(loopCtx, time.Now())
            // 清空 ticker channel，避免积压
            select {
            case <-ticker.C:
            default:
            }
        case <-ticker.C:
            // tick 作为保底，确保定期执行
            s.step(loopCtx, time.Now())
        }
    }
}
```

## 关键设计差异对比

| 特性 | updownthreshold | cyclehedge（当前） | cyclehedge（改进后） |
|------|----------------|-------------------|---------------------|
| 事件处理 | 直接在 OnPriceChanged 中处理 | 通过 signalC + step | 通过 signalC + step（改进） |
| Market 获取 | 直接使用 e.Market | 从价格事件合并 | 从价格事件 + 保存的 market |
| 状态保存 | 保存 cycleStartAt | 保存 currentMarketSlug | 保存 currentMarket（完整对象） |
| 复杂度 | 低 | 高 | 中等 |
| 可靠性 | 高 | 低（价格事件丢失） | 高（有 fallback） |

## 实施优先级

1. **高优先级**：方案 1（保存完整的 market 对象）
   - 核心修复，解决价格事件丢失问题
   - 借鉴 updownthreshold 的成功经验
   - 预计时间：20 分钟

2. **高优先级**：方案 2（增加 signalC 缓冲）
   - 立即减少信号丢失
   - 预计时间：2 分钟

3. **中优先级**：方案 3（优化 loop 函数）
   - 进一步优化响应速度
   - 预计时间：10 分钟

## 设计原则改进

1. **借鉴成功经验**：参考 updownthreshold 策略的简单直接设计
2. **保存完整状态**：在 OnCycle 和 OnPriceChanged 中保存完整的 market 对象
3. **提供 fallback**：step 函数不依赖价格事件，使用保存的 market 对象
4. **保持兼容性**：保留现有的 loop/step 机制，只改进 market 获取方式
