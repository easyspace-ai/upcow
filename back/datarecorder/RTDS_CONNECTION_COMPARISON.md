# RTDS 连接配置对比分析

## 策略 vs 监控工具配置对比

### 相同点

1. **RTDS 客户端配置完全相同**：
   - `ReadTimeout: 60 * time.Second`
   - `PingInterval: 5 * time.Second`
   - `WriteTimeout: 10 * time.Second`
   - `Reconnect: true`
   - `ReconnectDelay: 5 * time.Second`
   - `MaxReconnect: 10`

2. **连接逻辑相同**：
   - 都使用相同的 `rtds.NewClientWithConfig()` 创建客户端
   - 都调用 `Connect()` 方法连接
   - 都使用相同的订阅方法

### 差异点

1. **日志级别**（已修复）：
   - **之前**：策略使用 `logger.Infof`，监控工具使用 `logger.Debugf`
   - **现在**：策略已改为 `logger.Debugf`，与监控工具一致
   - **影响**：减少策略中的 RTDS 内部日志输出，避免日志过多

2. **日志适配器实现**：
   - 策略：`logger.Debugf("[RTDS] "+format, v...)`
   - 监控工具：`logger.Debugf("[RTDS] "+format, v...)`
   - **现在一致**

## 已应用的改进

### 1. 读取超时优化（已修复）

**问题**：之前读取超时设置为 1 秒，导致频繁的超时检查

**修复**：已将读取超时增加到 30 秒
- 位置：`clob/rtds/client.go:387`
- 影响：策略和监控工具都受益

**代码变更**：
```go
// 之前：1 秒超时
c.conn.SetReadDeadline(time.Now().Add(1 * time.Second))

// 现在：30 秒超时
readTimeout := 30 * time.Second
if c.readTimeout > 0 && c.readTimeout < readTimeout {
    readTimeout = c.readTimeout
}
c.conn.SetReadDeadline(time.Now().Add(readTimeout))
```

### 2. 日志级别优化（已修复）

**问题**：策略中 RTDS 日志使用 `Infof`，导致日志过多

**修复**：已改为 `Debugf`，与监控工具一致
- 位置：`internal/strategies/datarecorder/strategy.go:34`
- 影响：减少策略中的日志输出

### 3. 重连机制改进（已修复）

**问题**：panic 恢复后未触发重连

**修复**：
- panic 恢复后异步触发重连
- 添加 `isReconnecting` 标志防止并发重连
- 改进重连逻辑，避免死锁

## 连接稳定性改进

### 预期效果

1. **减少频繁超时**：30 秒超时 vs 1 秒超时，减少 30 倍的超时检查频率
2. **提高连接稳定性**：减少超时导致的连接状态误判
3. **自动恢复**：连接断开后自动重连并重新订阅

### 监控建议

在策略中，可以通过以下方式监控 RTDS 连接状态：

```go
// 检查连接状态
if s.rtdsClient != nil {
    connected := s.rtdsClient.IsConnected()
    snapshot := s.rtdsClient.DebugSnapshot()
    logger.Debugf("RTDS 连接状态: connected=%v, snapshot=%s", connected, snapshot)
}
```

## 总结

策略和监控工具现在使用：
- ✅ 相同的 RTDS 客户端配置
- ✅ 相同的连接逻辑（已修复读取超时问题）
- ✅ 相同的日志级别（Debugf）
- ✅ 相同的重连机制

策略应该已经受益于所有连接稳定性改进。

