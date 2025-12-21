# 架构问题根本原因分析

## 问题描述

在周期切换时，旧周期的价格数据（如 UP=0.9900, DOWN=0.9900）会混入新周期的数据记录中，导致数据污染。

## 架构流程分析

### 1. 周期切换流程（MarketScheduler）

```
checkAndSwitchMarket() {
  1. 检测到周期结束
  2. 调用 currentSession.Close()  // 关闭旧 Session
  3. 创建新 Session
  4. 调用 sessionSwitchCallback(oldSession, newSession, newMarket)
}
```

### 2. Session.Close() 流程

```go
func (s *ExchangeSession) Close() error {
  1. priceChangeHandlers.Clear()  // 清空 Session 的 handlers
  2. latestPrices = make(...)      // 清空价格缓存
  3. loopCancel()                  // 取消价格事件分发 loop
  4. MarketDataStream.Close()       // 关闭 MarketStream
}
```

### 3. MarketStream.Close() 流程

```go
func (m *MarketStream) Close() error {
  1. close(m.closeC)                // 发送关闭信号
  2. m.handlers.Clear()             // 清空 MarketStream 的 handlers
  3. connCancel()                   // 取消连接 context
  4. conn.Close()                   // 关闭 WebSocket 连接
  5. 等待 goroutine 完成
}
```

## 根本问题

### 问题1：MarketStream.handlers 清空时机不对

**当前顺序**：
1. `close(m.closeC)` - 发送关闭信号
2. `m.handlers.Clear()` - 清空 handlers

**问题**：
- 在 `Close()` 调用之前，可能已经有消息在 WebSocket 读取缓冲区中
- `Read()` goroutine 可能正在处理这些消息
- 即使 `closeC` 已关闭，`handlePriceChange` 会检查并返回
- **但是**，如果消息处理在 `Close()` 调用之前开始，但在 `handlers.Clear()` 之后完成，事件仍然会被发送

**更严重的问题**：
- `handlePriceChange` 在函数开头检查 `closeC`，如果已关闭就返回
- 但是，在检查之后、发送事件之前，`handlers` 可能被清空
- 这会导致事件丢失，但不会导致数据污染

### 问题2：Session.priceChangeHandlers 清空时机不对

**当前顺序**：
1. `priceChangeHandlers.Clear()` - 清空 Session 的 handlers
2. `MarketDataStream.Close()` - 关闭 MarketStream

**问题**：
- 如果 `MarketStream` 在 `Close()` 之前还在发送事件
- 这些事件会通过 `sessionPriceHandler` 转发到 `Session`
- 但是 `Session.priceChangeHandlers` 已经被清空
- 所以事件会被丢弃，不会导致数据污染

### 问题3：真正的根本问题 - 事件处理的竞态条件

**关键发现**：

在 `MarketStream.handlePriceChange()` 中：

```go
func (m *MarketStream) handlePriceChange(ctx context.Context, msg map[string]interface{}) {
  // 检查1：检查 closeC
  select {
  case <-m.closeC:
    return
  default:
  }
  
  // ... 处理消息 ...
  
  // 检查2：再次检查 closeC
  select {
  case <-m.closeC:
    continue
  default:
  }
  
  // 发送事件
  m.handlers.Emit(ctx, event)
}
```

**问题**：
- 如果消息处理在 `Close()` 调用之前开始
- 检查1通过（`closeC` 未关闭）
- 开始处理消息（可能需要时间）
- 在此期间，`Close()` 被调用，`closeC` 被关闭，`handlers` 被清空
- 检查2通过（因为是在循环中，`continue` 只跳过当前迭代）
- **但是**，如果事件已经在 `handlers.Emit()` 的调用栈中，它仍然会被发送

**更关键的问题**：
- `handlers.Emit()` 是同步调用
- 如果 `handlers` 在 `Emit()` 调用过程中被清空，可能导致：
  1. 事件被发送到已清空的 handlers（不会导致数据污染）
  2. 或者事件被发送到新的 handlers（如果新周期已经注册了 handlers）

### 问题4：新周期 handlers 注册时机

**流程**：
1. `MarketScheduler` 创建新 Session
2. `Session.Connect()` 注册 `sessionPriceHandler` 到 `MarketStream`
3. 策略通过 `Session.OnPriceChanged()` 注册到 `Session.priceChangeHandlers`

**问题**：
- 如果旧周期的 `MarketStream` 在关闭过程中发送事件
- 这些事件会通过 `sessionPriceHandler` 转发到 `Session`
- 如果新周期的策略已经注册到 `Session.priceChangeHandlers`
- 这些旧周期的事件会被新周期的策略处理
- **但是**，策略会检查 `event.Market.Slug`，如果与 `currentMarket.Slug` 不匹配，会忽略事件

**但是**，如果时序有问题：
- 旧周期事件到达时，策略的 `currentMarket` 可能还没有更新
- 或者，策略的 `currentMarket` 已经更新，但事件中的 `Market` 对象是旧周期的
- 这可能导致事件被错误处理

## 真正的根本问题

### 问题：MarketStream 在关闭时没有立即停止处理消息

**当前实现**：
- `MarketStream.Close()` 关闭 `closeC`，然后清空 `handlers`
- `handlePriceChange` 检查 `closeC`，如果已关闭就返回
- **但是**，如果消息处理在检查之后、发送事件之前，`handlers` 可能被清空

**解决方案**：
1. **先清空 handlers，再关闭 closeC**：
   - 这样可以确保在关闭信号发送之前，不会有新的事件被发送
   - 但是，已经在处理的消息仍然可能发送事件

2. **在 handlePriceChange 中，在发送事件前再次检查 handlers**：
   - 如果 `handlers` 为空，不发送事件
   - 但这需要 `handlers` 是线程安全的

3. **使用原子操作标记关闭状态**：
   - 在 `Close()` 中设置原子标志
   - 在 `handlePriceChange` 中检查原子标志
   - 这样可以确保在关闭过程中不会有新的事件被发送

## 推荐的解决方案

### 方案1：调整 MarketStream.Close() 的顺序

```go
func (m *MarketStream) Close() error {
  // 1. 先清空 handlers（阻止新事件被发送）
  m.handlers.Clear()
  
  // 2. 再关闭 closeC（停止消息处理）
  close(m.closeC)
  
  // 3. 取消连接 context
  // 4. 关闭连接
  // 5. 等待 goroutine 完成
}
```

### 方案2：在 handlePriceChange 中增加 handlers 检查

```go
func (m *MarketStream) handlePriceChange(ctx context.Context, msg map[string]interface{}) {
  // 检查 closeC
  select {
  case <-m.closeC:
    return
  default:
  }
  
  // ... 处理消息 ...
  
  // 在发送事件前，检查 handlers 是否为空
  if m.handlers.Count() == 0 {
    marketLog.Debugf("⚠️ [价格处理] handlers 已清空，忽略价格事件")
    return
  }
  
  // 发送事件
  m.handlers.Emit(ctx, event)
}
```

### 方案3：使用原子标志（最可靠）

```go
type MarketStream struct {
  // ...
  closed int32  // 原子标志
}

func (m *MarketStream) Close() error {
  // 1. 设置关闭标志
  atomic.StoreInt32(&m.closed, 1)
  
  // 2. 清空 handlers
  m.handlers.Clear()
  
  // 3. 关闭 closeC
  close(m.closeC)
  
  // ...
}

func (m *MarketStream) handlePriceChange(ctx context.Context, msg map[string]interface{}) {
  // 检查关闭标志
  if atomic.LoadInt32(&m.closed) == 1 {
    return
  }
  
  // ... 处理消息 ...
  
  // 在发送事件前再次检查
  if atomic.LoadInt32(&m.closed) == 1 {
    return
  }
  
  // 发送事件
  m.handlers.Emit(ctx, event)
}
```

## 建议

**优先采用方案1 + 方案2**：
- 方案1 确保在关闭过程中不会有新事件被发送
- 方案2 提供双重保险，即使有时序问题也能防止事件被发送
- 这两个方案组合使用，可以最大程度地防止数据污染

