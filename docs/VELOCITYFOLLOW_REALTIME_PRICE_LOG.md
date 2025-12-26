# VelocityFollow 实时订单簿价格日志功能

## 功能说明

在 `velocityfollow` 策略中新增了**实时订单簿价格日志**功能，用于记录 UP/DOWN 的 bid/ask 价格。

## 日志格式

每次价格更新时（带限流），会打印如下格式的日志：

```
💰 [velocityfollow] 实时订单簿: UP bid=0.6500 ask=0.6600, DOWN bid=0.3400 ask=0.3500 (source=ws.bestbook)
```

### 字段说明

- **UP bid**: UP token 的最佳买价（最高买价）
- **UP ask**: UP token 的最佳卖价（最低卖价）
- **DOWN bid**: DOWN token 的最佳买价（最高买价）
- **DOWN ask**: DOWN token 的最佳卖价（最低卖价）
- **source**: 数据来源
  - `ws.bestbook`: WebSocket 订单簿数据（优先）
  - `rest.orderbook`: REST API 订单簿数据（回退）

## 限流机制

为了避免频繁调用 API 和日志刷屏，实现了限流机制：

- **默认限流时间**: 2 秒
- **限流逻辑**: 每 2 秒最多打印一次订单簿价格日志
- **失败处理**: 如果获取订单簿价格失败，会静默记录 Debug 级别日志，不影响策略运行

## 实现细节

### 代码位置

`internal/strategies/velocityfollow/strategy.go`

### 关键代码

```go
// 在 OnPriceChanged 函数中
// ===== 实时订单簿价格日志 =====
// 打印 UP/DOWN 的 bid/ask 价格（带限流，避免频繁调用 API）
s.mu.Lock()
shouldLogOrderBook := false
if s.lastOrderBookLogAt.IsZero() {
    shouldLogOrderBook = true
} else {
    logThrottle := time.Duration(s.orderBookLogThrottleMs) * time.Millisecond
    if logThrottle <= 0 {
        logThrottle = 2 * time.Second // 默认 2 秒
    }
    if now.Sub(s.lastOrderBookLogAt) >= logThrottle {
        shouldLogOrderBook = true
    }
}
if shouldLogOrderBook {
    s.lastOrderBookLogAt = now
}
s.mu.Unlock()

// 在锁外获取订单簿价格并打印（避免长时间持锁）
if shouldLogOrderBook && e.Market != nil {
    // 使用背景上下文，避免阻塞策略主流程
    bookCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
    defer cancel()
    
    yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(bookCtx, e.Market)
    if err != nil {
        // 静默失败，不影响策略运行
        log.Debugf("⚠️ [%s] 获取订单簿价格失败（实时日志）: %v", ID, err)
    } else {
        yesBidDec := yesBid.ToDecimal()
        yesAskDec := yesAsk.ToDecimal()
        noBidDec := noBid.ToDecimal()
        noAskDec := noAsk.ToDecimal()
        log.Infof("💰 [%s] 实时订单簿: UP bid=%.4f ask=%.4f, DOWN bid=%.4f ask=%.4f (source=%s)",
            ID, yesBidDec, yesAskDec, noBidDec, noAskDec, source)
    }
}
```

## 使用场景

1. **调试价格数据**: 实时查看订单簿价格变化，帮助诊断价格异常
2. **监控市场状态**: 观察 bid/ask 价差，判断市场流动性
3. **分析交易条件**: 了解为什么策略没有触发交易（价格是否满足条件）

## 注意事项

1. **性能影响**: 虽然有限流机制，但仍会每 2 秒调用一次 `GetTopOfBook` API，可能增加少量延迟
2. **日志级别**: 使用 `Info` 级别日志，确保在生产环境中可见
3. **超时保护**: 获取订单簿价格设置了 500ms 超时，避免阻塞策略主流程
4. **失败处理**: 如果获取失败，会静默记录 Debug 日志，不影响策略正常运行

## 日志示例

```
[36mINFO[0m[25-12-26 22:00:02] 💰 [velocityfollow] 实时订单簿: UP bid=0.6500 ask=0.6600, DOWN bid=0.3400 ask=0.3500 (source=ws.bestbook)
[36mINFO[0m[25-12-26 22:00:04] 💰 [velocityfollow] 实时订单簿: UP bid=0.6550 ask=0.6650, DOWN bid=0.3350 ask=0.3450 (source=ws.bestbook)
[36mINFO[0m[25-12-26 22:00:06] 💰 [velocityfollow] 实时订单簿: UP bid=0.6600 ask=0.6700, DOWN bid=0.3300 ask=0.3400 (source=rest.orderbook)
```

## 相关配置

目前没有配置文件选项，限流时间硬编码为 2 秒。如需调整，可以修改代码中的 `orderBookLogThrottleMs` 默认值。

---

**创建时间**: 2025-12-26  
**功能状态**: ✅ 已实现并启用

