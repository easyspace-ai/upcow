# 程序再次挂掉问题分析（第二次）

## 📅 分析时间
2025-12-25 08:50

## 🔍 问题现象

1. **程序在 08:50:08 后停止运行**（日志停止）
2. **出现 `context deadline exceeded` 错误**
3. **价格变化事件还在继续**（说明程序没有完全挂掉，但策略被阻塞）

## 📋 错误日志

```
⚠️ [velocityfollow] 主单下单失败: err=context deadline exceeded side=up market=btc-updown-15m-1766623500
```

之后日志停止，但价格变化事件还在继续。

## 🔎 根本原因分析

### 问题：GetTopOfBook 超时阻塞策略

**位置**: `internal/strategies/velocityfollow/strategy.go:620`

```go
orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
defer cancel()

yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(orderCtx, market)
if err != nil {
    log.Debugf("⚠️ [%s] 获取订单簿失败: %v", ID, err)
    return nil
}
```

**问题**：
1. `GetTopOfBook` 超时（25秒）后，策略返回 `nil`
2. **但是**，如果 `GetTopOfBook` 内部阻塞（例如 REST API 调用卡住），即使 context 超时，也可能导致策略 goroutine 被阻塞
3. 策略 goroutine 被阻塞后，后续的价格变化事件无法处理

### 可能的原因

1. **REST API 调用卡住**
   - `GetOrderBook` 调用可能卡住，即使 context 超时也不返回
   - 这会导致 `GetTopOfBook` 阻塞，进而阻塞策略

2. **网络问题**
   - 代理或网络连接问题，导致 REST API 调用卡住
   - Context 超时可能无法及时取消阻塞的网络调用

3. **API 限流**
   - Polymarket API 可能限流，导致请求卡住
   - 即使 context 超时，底层 HTTP 客户端可能仍在等待

## 🛠️ 修复方案

### 方案1: 使用更短的超时时间 + 快速失败（推荐）

**问题**: 25秒超时太长，如果 API 真的有问题，策略会被阻塞太久

**修复**:
```go
// 使用更短的超时时间（10秒），快速失败
orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
defer cancel()

yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(orderCtx, market)
if err != nil {
    log.Warnf("⚠️ [%s] 获取订单簿失败（快速失败）: %v", ID, err)
    return nil // 快速返回，不阻塞策略
}
```

**效果**:
- ✅ 减少阻塞时间（从 25 秒降到 10 秒）
- ✅ 快速失败，不阻塞策略
- ✅ 策略可以继续处理后续的价格变化事件

### 方案2: 在独立的 goroutine 中调用 GetTopOfBook（可选）

**问题**: 如果 `GetTopOfBook` 真的卡住，即使 context 超时也可能无法及时取消

**修复**:
```go
// 在独立的 goroutine 中调用 GetTopOfBook
resultChan := make(chan struct {
    yesBid, yesAsk, noBid, noAsk domain.Price
    source string
    err error
}, 1)

go func() {
    yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(orderCtx, market)
    resultChan <- struct {
        yesBid, yesAsk, noBid, noAsk domain.Price
        source string
        err error
    }{yesBid, yesAsk, noBid, noAsk, source, err}
}()

select {
case result := <-resultChan:
    if result.err != nil {
        log.Warnf("⚠️ [%s] 获取订单簿失败: %v", ID, result.err)
        return nil
    }
    yesBid, yesAsk, noBid, noAsk = result.yesBid, result.yesAsk, result.noBid, result.noAsk
case <-orderCtx.Done():
    log.Warnf("⚠️ [%s] 获取订单簿超时（快速失败）: %v", ID, orderCtx.Err())
    return nil
}
```

**效果**:
- ✅ 即使 `GetTopOfBook` 卡住，策略也不会被阻塞
- ✅ 超时后立即返回，不等待 API 响应
- ✅ 策略可以继续处理后续的价格变化事件

**缺点**:
- 代码更复杂
- 如果 API 真的卡住，goroutine 会泄漏（需要清理）

### 方案3: 检查 HTTP 客户端的超时设置（推荐）

**问题**: HTTP 客户端可能没有正确使用 context 超时

**检查**:
- `clobClient` 的 HTTP 客户端是否设置了超时
- HTTP 请求是否使用了传入的 context

**修复**:
- 确保 HTTP 客户端使用传入的 context
- 设置合理的 HTTP 客户端超时时间

## 📊 修复优先级

1. **高优先级**: 缩短超时时间（方案1）
   - 快速失败，不阻塞策略
   - 简单有效

2. **中优先级**: 检查 HTTP 客户端超时设置（方案3）
   - 确保 context 超时能正确取消 HTTP 请求
   - 防止底层 HTTP 调用卡住

3. **低优先级**: 独立 goroutine（方案2）
   - 如果方案1和3无法解决问题，再考虑
   - 代码复杂度较高

## 🔍 验证方法

### 1. 检查 HTTP 客户端超时设置

```bash
grep -r "Timeout\|timeout" clob/rtds/client.go
grep -r "http.Client\|http.DefaultClient" clob/
```

### 2. 检查 context 使用

```bash
grep -r "GetOrderBook\|WithContext\|ctx.Done()" clob/
```

### 3. 测试超时场景

- 模拟网络延迟
- 模拟 API 限流
- 检查策略是否被阻塞

---

**状态**: 🔍 问题分析中  
**下一步**: 实施修复方案1（缩短超时时间）

