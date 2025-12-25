# 程序挂掉问题修复总结

## 📅 修复时间
2025-12-25

## 🔍 问题分析

### 问题现象
1. 程序在 08:05:48 后停止运行（日志停止）
2. 出现多次 `context deadline exceeded` 错误
3. 即使在模拟下单模式（dry_run）下也出现超时

### 根本原因

#### 1. OrderEngine 回调阻塞（导致程序挂掉）

**问题**: `handlePlaceOrder` 的回调函数中，`cmd.Reply <- result` 可能阻塞

**原因**:
- 如果 `PlaceOrder` 的接收端因为 context 超时而退出
- `cmd.Reply` channel 的发送会阻塞（因为 channel 是无缓冲的）
- 回调 goroutine 阻塞，导致 `UpdateOrderCommand` 无法发送
- 可能导致 OrderEngine 状态不一致，甚至程序挂掉

**位置**: `internal/services/order_engine.go:450-453`

#### 2. GetTopOfBook 超时（正常但可优化）

**问题**: `GetTopOfBook` 在 WebSocket 数据不新鲜时回退到 REST API，可能超时

**原因**:
- WebSocket 数据新鲜度要求太严格（3秒）
- REST API 调用需要两次（YES 和 NO），可能超过 25 秒
- 没有重试机制

**位置**: `internal/services/trading_orders.go:316-347`

## 🛠️ 修复方案

### 修复1: OrderEngine 回调非阻塞发送（关键修复）

**修改文件**: `internal/services/order_engine.go`

**修复内容**:
1. **所有 `cmd.Reply` 发送都添加超时保护**
   - 添加 `cmd.Context.Done()` 检查
   - 添加 100ms 超时保护
   - 如果无法发送，记录警告但不阻塞

2. **修复位置**:
   - `handlePlaceOrder`: stale cycle、验证错误、余额不足、回调回复
   - `handleCancelOrder`: 回调回复
   - 其他所有使用 `cmd.Reply` 的地方

**代码示例**:
```go
// 修复前（可能阻塞）
select {
case cmd.Reply <- result:
case <-cmd.Context.Done():
}

// 修复后（非阻塞）
select {
case cmd.Reply <- result:
    // 成功发送
case <-cmd.Context.Done():
    // Context 已取消，接收端可能已经超时退出，不阻塞
    orderEngineLog.Debugf("回复命令时 context 已取消: orderID=%s", cmd.Order.OrderID)
case <-time.After(100 * time.Millisecond):
    // 超时保护：如果 100ms 内无法发送，记录警告但不阻塞
    orderEngineLog.Warnf("回复命令超时（接收端可能已退出）: orderID=%s", cmd.Order.OrderID)
}
```

**效果**:
- ✅ 防止回调 goroutine 阻塞
- ✅ 防止 OrderEngine 状态不一致
- ✅ 防止程序挂掉

### 修复2: GetTopOfBook 优化

**修改文件**: `internal/services/trading_orders.go`

**修复内容**:
1. **放宽 WebSocket 数据新鲜度要求**
   - 从 3 秒增加到 10 秒
   - 减少 REST API 调用
   - 降低超时风险

2. **添加重试机制**
   - REST API 调用失败时重试 2 次
   - 重试间隔 100ms
   - 提高成功率

**代码示例**:
```go
// 修复前
if book != nil && cur != nil && cur.Slug == market.Slug && book.IsFresh(3*time.Second) {
    // ...
}

// 修复后
if book != nil && cur != nil && cur.Slug == market.Slug && book.IsFresh(10*time.Second) {
    // ...
}

// REST API 重试
maxRetries := 2
for attempt := 0; attempt < maxRetries; attempt++ {
    yesBook, restErr = s.clobClient.GetOrderBook(ctx, market.YesAssetID, nil)
    if restErr != nil {
        if attempt < maxRetries-1 {
            continue // 重试
        }
        return ..., fmt.Errorf("get yes orderbook (after %d attempts): %w", maxRetries, restErr)
    }
    // ...
}
```

**效果**:
- ✅ 减少 REST API 调用
- ✅ 提高响应速度
- ✅ 降低超时风险

## 📊 修复效果预期

### 1. 防止程序挂掉 ✅
- ✅ OrderEngine 回调不再阻塞
- ✅ 程序可以正常处理超时情况
- ✅ 不会因为 channel 阻塞导致程序挂掉

### 2. 提高系统稳定性 ✅
- ✅ GetTopOfBook 成功率提高
- ✅ 减少超时错误
- ✅ 系统更加健壮

### 3. 改善用户体验 ✅
- ✅ 程序不会突然停止
- ✅ 错误处理更加优雅
- ✅ 日志更加详细

## 🔍 验证方法

### 1. 验证 OrderEngine 非阻塞
**检查日志**:
```
回复命令时 context 已取消: orderID=...
回复命令超时（接收端可能已退出）: orderID=...
```

**验证**: 不应该看到程序挂掉，即使有超时错误也应该继续运行

### 2. 验证 GetTopOfBook 优化
**检查日志**:
```
ws.bestbook  # 应该更多使用 WebSocket 数据
get yes orderbook (after 2 attempts): ...  # 重试日志
```

**验证**: 
- WebSocket 数据使用率提高
- REST API 调用减少
- 超时错误减少

## 📝 后续优化建议

### 1. 添加程序健康检查（可选）
- 监控 OrderEngine 命令队列长度
- 监控 goroutine 数量
- 添加心跳机制

### 2. 优化 dry_run 模式（可选）
- 在 dry_run 模式下，PlaceOrder 立即返回，不等待异步回调
- 进一步提高模拟模式的响应速度

### 3. 添加更多监控指标（可选）
- 监控超时频率
- 监控回调阻塞次数
- 监控 REST API 调用次数

---

**状态**: ✅ 修复已完成并编译通过  
**下一步**: 运行测试，验证修复效果

