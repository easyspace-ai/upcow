# 周期切换修复说明：先注册价格处理器，再订阅市场

## 问题根源：时序竞态（Race Condition）

### 修复前的执行顺序（有问题）

```
时间线：
T0: 周期切换开始
    ↓
T1: 订阅新市场 WebSocket
    ├─ ms.SwitchMarket() 发送订阅消息到服务器
    └─ 服务器立即开始推送价格数据
    ↓
T2: 价格数据到达（但此时还没有处理器！）
    ├─ MarketStream 收到 price_change 消息
    ├─ 检查 handlers.Count() → 返回 0（还没有注册）
    └─ ⚠️ 价格数据被丢弃或缓存（可能丢失）
    ↓
T3: 触发回调，注册价格处理器
    ├─ callback() → trader.SwitchSession()
    ├─ trader.Subscribe() → strategy.Subscribe()
    └─ session.OnPriceChanged(strategy) → handlers 注册完成
    ↓
T4: 后续价格数据到达
    └─ ✅ 现在有处理器了，可以正常处理
```

**问题**：在 T1-T3 之间的价格数据可能丢失！

### 修复后的执行顺序（正确）

```
时间线：
T0: 周期切换开始
    ↓
T1: 先更新会话市场信息
    └─ currentSession.SetMarket(nextMarket)
    ↓
T2: 先触发回调，注册价格处理器
    ├─ callback() → trader.SwitchSession()
    ├─ trader.Subscribe() → strategy.Subscribe()
    └─ session.OnPriceChanged(strategy) → handlers 注册完成 ✅
    ↓
T3: 等待 100ms，确保注册完成
    └─ time.Sleep(100 * time.Millisecond)
    ↓
T4: 现在订阅新市场 WebSocket
    ├─ ms.SwitchMarket() 发送订阅消息到服务器
    └─ 服务器开始推送价格数据
    ↓
T5: 价格数据到达
    ├─ MarketStream 收到 price_change 消息
    ├─ 检查 handlers.Count() → 返回 > 0 ✅
    └─ ✅ 价格数据正常处理，传递给策略
```

**优势**：价格处理器已经就绪，不会丢失任何数据！

## 代码流程详解

### 1. 价格处理器的注册链

```go
// 策略的 Subscribe 方法
func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
    session.OnPriceChanged(s)  // ← 这里注册价格处理器
    session.OnOrderUpdate(s)
}

// Session 的 OnPriceChanged 方法
func (s *ExchangeSession) OnPriceChanged(handler stream.PriceChangeHandler) {
    s.priceChangeHandlers.Add(handler)  // ← 添加到 Session 的处理器列表
    // ...
}

// Session 内部还会注册到 MarketStream
func (s *ExchangeSession) initMarketDataStream() {
    // ...
    s.MarketDataStream.OnPriceChanged(&sessionPriceHandler{session: s})
    // ↑ 这个在创建 Session 时就注册了，但策略的处理器需要单独注册
}
```

### 2. 价格数据的流转路径

```
WebSocket 服务器
    ↓ (推送 price_change 消息)
MarketStream.Read()
    ↓
MarketStream.handleMessage()
    ↓
MarketStream.handlePriceChange()
    ↓
MarketStream.emitPriceChanged()
    ↓
MarketStream.handlers.Emit()  ← 检查 handlers.Count()
    ↓
sessionPriceHandler.OnPriceChanged()  ← Session 的处理器
    ↓
Session.EmitPriceChanged()
    ↓
Session.priceChangeHandlers.Snapshot()  ← 检查策略处理器
    ↓
Strategy.OnPriceChanged()  ← 策略的处理器（如果已注册）
```

### 3. 关键检查点

在 `MarketStream.emitPriceChanged()` 中：

```go
func (m *MarketStream) emitPriceChanged(...) {
    // 检查 handlers 是否为空
    handlerCount := m.handlers.Count()
    if handlerCount == 0 {
        marketLog.Warnf("⚠️ Handlers 为空，无法触发价格事件")
        return  // ← 如果为空，直接返回，价格数据丢失！
    }
    // ...
    m.handlers.Emit(ctx, event)  // 只有 handlers > 0 才会执行
}
```

在 `Session.EmitPriceChanged()` 中：

```go
func (s *ExchangeSession) EmitPriceChanged(...) {
    handlers := s.priceChangeHandlers.Snapshot()
    if len(handlers) == 0 {
        // 警告：价格更新将被丢弃
        sessionLog.Warnf("⚠️ priceChangeHandlers 为空，价格更新将被丢弃！")
        // 但会缓存到 latestPrices，等待后续注册后 flush
    }
    // ...
}
```

## 为什么修复有效？

### 修复前的风险

1. **数据丢失窗口**：订阅后到注册前的价格数据可能丢失
2. **竞态条件**：如果服务器推送很快，可能在注册前就收到数据
3. **依赖缓存**：虽然 Session 会缓存价格，但 MarketStream 层面可能直接丢弃

### 修复后的保障

1. **零丢失**：处理器先注册，确保所有数据都能处理
2. **顺序保证**：明确的执行顺序，避免竞态
3. **双重保障**：
   - Session 层缓存（已有）
   - MarketStream 层直接处理（新增）

## 代码修改对比

### 修复前（market_scheduler.go）

```go
// 先订阅市场
ms.SwitchMarket(s.ctx, currentMarket, nextMarket)

// 然后才触发回调注册处理器
callback(oldSession, currentSession, nextMarket)
```

### 修复后（market_scheduler.go）

```go
// 先更新市场信息
currentSession.SetMarket(nextMarket)

// 先触发回调注册处理器
callback(oldSession, currentSession, nextMarket)
time.Sleep(100 * time.Millisecond)  // 确保注册完成

// 然后才订阅市场
ms.SwitchMarket(s.ctx, currentMarket, nextMarket)
```

## 额外的安全措施

### 1. 订阅状态验证

```go
// 在 SwitchMarket 后验证订阅状态
m.subscribedAssetsMu.RLock()
subscribedCount := len(m.subscribedAssets)
m.subscribedAssetsMu.RUnlock()

if subscribedCount == 0 {
    marketLog.Warnf("⚠️ 订阅状态异常：没有已订阅的资产！")
}
```

### 2. 价格数据超时检测

```go
// 30秒后检查是否收到价格数据
go func() {
    time.Sleep(30 * time.Second)
    if time.Since(lastMsg) > 30*time.Second {
        marketLog.Warnf("⚠️ 周期切换后30秒内未收到价格数据")
        // 尝试重新订阅
        m.subscribe(newAssetIDs, "subscribe")
    }
}()
```

### 3. 详细诊断日志

```go
// 记录 handlers 数量
marketLog.Infof("📨 [消息处理] 收到 price_change 消息: market=%s handlers=%d", 
    marketSlug, handlerCount)
```

## 总结

**核心原则**：确保数据接收端（处理器）在数据发送端（订阅）之前就绪。

这就像：
- ❌ **错误**：先打开水龙头，再准备接水的桶 → 水会流失
- ✅ **正确**：先准备接水的桶，再打开水龙头 → 水全部接住

通过调整执行顺序，我们确保了价格处理器在订阅市场之前就已经注册完成，从而避免了价格数据丢失的问题。

