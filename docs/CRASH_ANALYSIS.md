# 系统崩溃分析报告

## 📅 分析时间
2025-12-25

## 🔍 问题概述

系统在 08:05:48 后停止运行，日志显示多个 `context deadline exceeded` 错误。

## 📋 错误日志

### 1. 对冲单下单失败（08:05:23）
```
⚠️ [velocityfollow] 对冲单下单失败: err=context deadline exceeded (主单已成交，需要手动处理)
⚠️ [velocityfollow] 下单失败: err=context deadline exceeded side=down market=btc-updown-15m-1766620800
```

### 2. 主单下单失败（08:05:48）
```
⚠️ [velocityfollow] 主单下单失败: err=context deadline exceeded side=down market=btc-updown-15m-1766620800
```

## 🔎 根本原因分析

### 问题1: `GetTopOfBook` 超时

**位置**: `internal/strategies/velocityfollow/strategy.go:620`

```go
yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(orderCtx, market)
```

**超时设置**: `orderCtx` 使用 `context.WithTimeout(ctx, 25*time.Second)`

**可能原因**:
1. **REST API 响应慢**: `GetTopOfBook` 需要调用两次 REST API（YES 和 NO 订单簿）
2. **网络问题**: 代理或网络连接不稳定
3. **API 限流**: Polymarket API 可能限流，导致响应延迟

### 问题2: `PlaceOrder` 超时

**位置**: `internal/strategies/velocityfollow/strategy.go:executeSequential`

**超时设置**: 同样使用 `orderCtx`（25秒超时）

**可能原因**:
1. **API 响应慢**: 下单 API 响应时间超过 25 秒
2. **网络延迟**: 代理或网络连接问题
3. **API 限流**: 频繁下单触发限流

## 🛠️ 修复方案

### 方案1: 增加超时时间（临时方案）

**问题**: 25秒可能不够，特别是在网络不稳定或API限流时

**修复**:
```go
// 增加超时时间到 60 秒
orderCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
```

**缺点**: 如果API真的有问题，增加超时时间只是延迟失败，不能解决根本问题

### 方案2: 优化 `GetTopOfBook` 使用 WebSocket 数据（推荐）

**问题**: `GetTopOfBook` 优先使用 WebSocket 数据，但如果 WebSocket 数据不新鲜，会回退到 REST API

**修复**:
1. **确保 WebSocket 数据新鲜**: 检查 `bestBook.IsFresh(3*time.Second)` 逻辑
2. **增加 WebSocket 数据容忍度**: 允许使用稍微过期的 WebSocket 数据（例如 5-10 秒）
3. **添加重试机制**: REST API 失败时重试一次

### 方案3: 添加重试和错误处理

**修复**:
```go
// 在 GetTopOfBook 调用时添加重试
maxRetries := 2
for i := 0; i < maxRetries; i++ {
    yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(orderCtx, market)
    if err == nil {
        break
    }
    if i < maxRetries-1 {
        log.Debugf("⚠️ [%s] 获取订单簿失败，重试 %d/%d: %v", ID, i+1, maxRetries, err)
        time.Sleep(100 * time.Millisecond)
    }
}
```

### 方案4: 使用更短的超时时间，但添加快速失败机制

**修复**:
```go
// 使用更短的超时时间（10秒），但如果失败，快速返回，不阻塞策略
orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
defer cancel()

yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(orderCtx, market)
if err != nil {
    log.Debugf("⚠️ [%s] 获取订单簿失败（快速失败）: %v", ID, err)
    return nil // 快速返回，不阻塞策略
}
```

## 📊 建议的修复优先级

1. **高优先级**: 优化 `GetTopOfBook` 使用 WebSocket 数据（方案2）
   - 减少 REST API 调用
   - 提高响应速度
   - 降低超时风险

2. **中优先级**: 添加重试机制（方案3）
   - 处理临时网络问题
   - 提高成功率

3. **低优先级**: 增加超时时间（方案1）
   - 作为临时缓解措施
   - 不能解决根本问题

## 🔍 进一步调查

### 1. 检查 WebSocket 数据新鲜度
```bash
grep -E "(bestBook|IsFresh|ws.bestbook)" logs/btc-updown-15m-1766620800.log | tail -50
```

### 2. 检查 REST API 响应时间
- 查看是否有 API 响应慢的日志
- 检查代理连接状态

### 3. 检查 API 限流
- 查看是否有 rate limit 错误
- 检查下单频率

## 📝 后续行动

1. ✅ 分析日志，确认问题
2. ⏳ 优化 `GetTopOfBook` 使用 WebSocket 数据
3. ⏳ 添加重试机制
4. ⏳ 增加错误处理和日志
5. ⏳ 监控和测试修复效果

---

**状态**: 🔍 问题已分析，等待修复  
**下一步**: 实施修复方案2（优化 WebSocket 数据使用）

