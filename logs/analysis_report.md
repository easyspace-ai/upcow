# 日志分析报告 - 2025-12-26

## 一、问题概述

### 1.1 核心问题：周期切换后没有价格数据

**问题描述**：
- 周期切换发生在 `20:15:00`（从 `btc-updown-15m-1766750400` 切换到 `btc-updown-15m-1766751300`）
- 策略订阅完成在 `20:15:02`
- **关键发现**：周期切换后（`btc-updown-15m-1766751300.log`）没有任何价格更新日志

**对比分析**：
- 旧周期（`btc-updown-15m-1766750400.log`）有大量价格更新日志
- 新周期（`btc-updown-15m-1766751300.log`）只有32行日志，且没有任何价格相关日志

### 1.2 时间线分析

```
20:15:00 - 周期切换开始
  ├─ 退订旧市场资产: 2个资产 ✅
  ├─ 订阅新市场资产: 2个资产 ✅
  └─ 订阅消息已发送: 2个资产 ✅

20:15:00 - 策略切换
  ├─ 重置本地状态 ✅
  ├─ 重置 OrderEngine ✅
  └─ 周期报表已写入 ✅

20:15:01 - 余额查询
  └─ 余额: 4.976874 USDC ✅

20:15:02 - 策略订阅
  ├─ 注册价格变化处理器 ✅ (handlers数量=2)
  └─ 策略已订阅会话 ✅

20:15:02 之后 - ❌ 没有任何价格更新日志
```

## 二、详细问题分析

### 2.1 价格数据缺失的可能原因

#### 原因1：WebSocket 订阅成功但未收到数据
- ✅ 订阅消息已发送成功
- ❌ 但之后没有收到任何 `price_change` 或 `book` 消息
- **可能原因**：
  - WebSocket 连接在周期切换时出现问题
  - 服务器端未推送新市场的价格数据
  - 订阅的资产ID不正确

#### 原因2：价格处理器注册时机问题
- 在 `btc-updown-15m-1766750400.log` 第33行发现警告：
  ```
  ⚠️ [Session polymarket] priceChangeHandlers 为空，价格更新将被丢弃！
  事件: up @ 0.9100 handlers数量=0
  ```
- 这说明在周期切换时，存在一个时间窗口，价格数据到达但处理器还未注册
- **但新周期没有这个警告**，说明可能根本没有收到价格数据

#### 原因3：MarketStream 过滤问题
- 检查 `shouldProcessMarketMessage` 逻辑
- 可能因为 market conditionID 或 assetID 不匹配导致消息被过滤

### 2.2 开单情况分析

#### 成功开单
在 `btc-updown-15m-1766750400.log` 中发现：
- ✅ 20:09:31 - 下单成功: `0xd29b81db...` (YES token, 价格 0.9)
- ✅ 20:09:31 - 下单成功: `0xb34f89c2...` (NO token, 价格 0.07)
- ✅ 订单金额自动调整（从 0.56 USDC 调整到 1.10 USDC）

#### 取消订单失败
- ❌ 20:09:32 - 取消订单失败: `0xd29b81db...` (HTTP 400: Invalid order payload)
- ❌ 20:09:32 - 取消订单失败: `0xb34f89c2...` (HTTP 400: Invalid order payload)

**问题**：取消订单时使用了无效的 payload，可能是：
- 订单ID格式不正确
- 缺少必要的字段
- 订单状态已变更

### 2.3 其他发现

#### 策略运行状态
- 在旧周期中，策略正常运行，有大量 `maxSingleSideShares reached` 日志
- 说明策略逻辑正常，但受限于单边持仓限制

#### WebSocket 连接状态
- 旧周期：WebSocket 连接正常，有大量消息接收日志
- 新周期：**没有任何 WebSocket 消息接收日志**

## 三、建议修复方案

### 3.1 立即修复：周期切换后价格数据缺失

#### 方案1：添加诊断日志
在 `MarketStream.Read()` 和 `handleMessage()` 中添加更详细的日志：
```go
// 在 Read() 中添加
marketLog.Infof("📥 [消息接收] 收到 WebSocket 消息: len=%d market=%s", 
    len(message), m.market.Slug)

// 在 handleMessage() 中添加
marketLog.Infof("📨 [消息处理] 收到消息: event_type=%s market=%s", 
    eventType, m.market.Slug)
```

#### 方案2：检查订阅状态
在周期切换后，添加订阅状态验证：
```go
// 在 SwitchMarket() 后添加
time.Sleep(2 * time.Second) // 等待订阅确认
if m.subscribedAssetsMu.RLock(); len(m.subscribedAssets) == 0 {
    marketLog.Warnf("⚠️ 订阅状态异常：没有已订阅的资产")
}
```

#### 方案3：主动请求价格快照
如果 WebSocket 没有推送数据，可以：
- 通过 REST API 获取当前价格
- 手动触发价格更新事件
- 确保策略能立即获得价格数据

### 3.2 修复取消订单失败

检查取消订单的 payload 构建逻辑：
- 确保订单ID格式正确
- 确保包含所有必需字段
- 检查订单状态（可能订单已成交或取消）

### 3.3 优化周期切换流程

#### 改进1：确保价格处理器先注册
在周期切换时，确保价格处理器在订阅之前注册：
```go
// 1. 先注册价格处理器
session.OnPriceChanged(strategy)

// 2. 再切换市场
marketStream.SwitchMarket(ctx, oldMarket, newMarket)
```

#### 改进2：添加价格数据超时检测
如果周期切换后30秒内没有收到价格数据，记录警告并尝试重新订阅：
```go
go func() {
    time.Sleep(30 * time.Second)
    if m.lastMessageAt.IsZero() || time.Since(m.lastMessageAt) > 30*time.Second {
        marketLog.Warnf("⚠️ 周期切换后30秒内未收到价格数据，尝试重新订阅")
        m.subscribe([]string{m.market.YesAssetID, m.market.NoAssetID}, "subscribe")
    }
}()
```

## 四、日志文件统计

### 4.1 文件大小对比
- `btc-updown-15m-1766750400.log`: 429行（有大量价格更新）
- `btc-updown-15m-1766751300.log`: 32行（只有切换日志，无价格数据）
- `bot_2025-12-26_20-00.log`: 16行（启动日志）

### 4.2 关键日志位置

#### 周期切换相关
- `btc-updown-15m-1766751300.log:1-31` - 周期切换完整流程

#### 价格数据相关
- `btc-updown-15m-1766750400.log:32-33` - 价格处理器为空警告
- `btc-updown-15m-1766750400.log:97` - 策略报价日志（有价格数据）

#### 订单相关
- `btc-updown-15m-1766750400.log:78,92` - 下单成功
- `btc-updown-15m-1766750400.log:125,144` - 取消订单失败

## 五、下一步行动

1. **立即检查**：WebSocket 连接状态和订阅确认消息
2. **添加诊断**：在关键位置添加日志，追踪价格数据流
3. **修复取消订单**：检查 payload 构建逻辑
4. **优化切换流程**：确保价格处理器在订阅前注册
5. **监控验证**：在下一个周期切换时验证修复效果

## 六、代码位置参考

- MarketStream 价格处理: `internal/infrastructure/websocket/market_stream.go`
- Session 价格处理器: `pkg/bbgo/session.go`
- 周期切换逻辑: `pkg/bbgo/market_scheduler.go`
- 策略订阅: `internal/strategies/cyclehedge/strategy.go`

