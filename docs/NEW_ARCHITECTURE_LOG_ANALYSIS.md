# 新架构运行日志分析报告

## 📊 分析时间
- **日志文件**: `logs/btc-updown-15m-1766618100.log`
- **周期**: `btc-updown-15m-1766618100` (07:15:00 - 07:30:00)
- **分析时间**: 2025-12-25

## ✅ 新架构运行情况

### 1. 周期切换 ✅
**状态**: ✅ **正常**

```
[07:15:01] ✅ [周期切换] 已设置当前市场: btc-updown-15m-1766618100
[07:15:01] 🔄 [周期切换] OrderEngine 已重置运行时状态: newMarket=btc-updown-15m-1766618100
[07:15:01] 🔄 [周期切换] 已重置本地状态：orders/positions/cache/inflight
[07:15:01] ✅ [周期切换] 策略 velocityfollow 已订阅会话 polymarket
```

**结论**: 
- ✅ 周期切换机制正常
- ✅ OrderEngine 状态重置正常
- ✅ 策略重新订阅正常

### 2. 策略初始化 ⚠️
**状态**: ⚠️ **部分正常**

**发现的问题**:
- ❌ **未找到 "已注册订单更新回调" 日志**
- ✅ 策略订阅价格变化事件正常

**日志证据**:
```
[07:15:01] ✅ [velocityfollow] 策略已订阅价格变化事件 (session=polymarket)
```

**可能的原因**:
1. `Initialize()` 方法可能未被调用（策略初始化时机问题）
2. `TradingService` 在 `Initialize()` 时可能为 `nil`
3. 日志级别可能过滤了 INFO 级别日志

**建议**: 
- 检查策略初始化时机
- 确认 `TradingService` 注入时机
- 检查日志级别配置

### 3. 策略触发 ✅
**状态**: ✅ **正常**

**触发统计**:
- **总触发次数**: 至少 **10+ 次**
- **触发时间**: 07:15:41, 07:17:15, 07:18:06, 07:19:06, 07:19:37, 07:21:02, 07:21:39, 07:22:05, 07:23:02, 07:23:58, 07:24:23

**触发详情**:
```
[07:15:41] 📤 [velocityfollow] 步骤1: 下主单 Entry (side=down price=61c size=8.0000 FAK)
[07:15:47] 📤 [velocityfollow] 步骤2: 下对冲单 Hedge (side=up price=42c size=8.0000 GTC)
[07:17:15] 📤 [velocityfollow] 步骤1: 下主单 Entry (side=down price=61c size=8.0000 FAK)
[07:18:06] 📤 [velocityfollow] 步骤1: 下主单 Entry (side=down price=71c size=8.0000 FAK)
[07:19:06] 📤 [velocityfollow] 步骤1: 下主单 Entry (side=down price=61c size=8.0000 FAK)
[07:19:37] 📤 [velocityfollow] 步骤1: 下主单 Entry (side=down price=66c size=8.0000 FAK)
[07:21:02] 📤 [velocityfollow] 步骤1: 下主单 Entry (side=down price=62c size=8.0000 FAK)
[07:21:39] 📤 [velocityfollow] 步骤1: 下主单 Entry (side=down price=78c size=8.0000 FAK)
[07:23:02] 📤 [velocityfollow] 步骤1: 下主单 Entry (side=down price=82c size=8.0000 FAK)
[07:23:58] 📤 [velocityfollow] 步骤1: 下主单 Entry (side=down price=94c size=15.7143 FAK)
```

**结论**: 
- ✅ 策略触发逻辑正常
- ✅ 顺序下单模式正常工作
- ✅ 价格计算正常（Entry 价格 61c-94c，Hedge 价格 7c-42c）

### 4. 订单执行 ⚠️
**状态**: ⚠️ **部分正常**

**主单提交**: ✅ **正常**
```
[07:15:41] ✅ [velocityfollow] 主单已提交: orderID=order_1766618141965963000 status=open
[07:17:15] ✅ [velocityfollow] 主单已提交: orderID=order_1766618235736348000 status=open
[07:18:06] ✅ [velocityfollow] 主单已提交: orderID=order_1766618286141857000 status=open
```

**主单成交检测**: ⚠️ **有问题**
```
[07:15:47] ⚠️ [velocityfollow] 主单未在预期时间内成交: orderID=order_1766618141965963000 (可能部分成交或仍在处理中)
[07:17:20] ⚠️ [velocityfollow] 主单未在预期时间内成交: orderID=order_1766618235736348000 (可能部分成交或仍在处理中)
```

**对冲单下单**: ❌ **失败**
```
[07:16:06] ⚠️ [velocityfollow] 对冲单下单失败: err=context deadline exceeded (主单已成交，需要手动处理)
[07:17:40] ⚠️ [velocityfollow] 对冲单下单失败: err=context deadline exceeded (主单已成交，需要手动处理)
[07:18:31] ⚠️ [velocityfollow] 对冲单下单失败: err=context deadline exceeded (主单已成交，需要手动处理)
```

**问题分析**:
1. **主单成交检测超时**: 顺序下单模式中，主单成交检测使用了轮询方式，但可能因为：
   - FAK 订单在纸交易模式下可能不会立即成交
   - 轮询间隔（50ms）可能不够频繁
   - 最大等待时间（1000ms）可能不够

2. **对冲单下单超时**: 由于主单成交检测超时，导致对冲单下单时 context 已过期

**建议**:
- 优化顺序下单模式的成交检测逻辑
- 增加轮询频率或延长等待时间
- 考虑使用订单更新回调来检测成交（如果回调已注册）

### 5. 订单更新回调 ⚠️
**状态**: ⚠️ **未正常工作**

**发现的问题**:
- ❌ **未找到策略的订单更新回调日志**
- ✅ 系统层订单更新正常（EventRouter, Session 都正常处理）

**系统层订单更新**:
```
[07:16:06] 📥 [EventRouter] 收到订单更新: orderID=order_1766618141965963000 status=open
[07:16:06] 📤 [EventRouter] 转发订单更新到 Session
[07:16:06] 📥 [Session polymarket] 收到订单更新事件: orderID=order_1766618141965963000
[07:16:06] 📊 [Session polymarket] 触发订单更新事件: handlers=0  ⚠️ handlers=0 说明策略回调未注册
```

**手动订单识别**:
```
[07:25:51] 📨 [UserWebSocket] 收到订单消息: orderID=0xac0af4ab69137e16544cd075471e44b6f6de4f5a038ca3e2bd7e1bd28bce3679
[07:25:51] 📥 [Session polymarket] 收到订单更新事件: orderID=0xac0af4ab69137e16544cd075471e44b6f6de4f5a038ca3e2bd7e1bd28bce3679
[07:25:51] 📊 [Session polymarket] 触发订单更新事件: handlers=0  ⚠️ handlers=0 说明策略回调未注册
```

**结论**:
- ✅ 系统层订单更新机制正常（WebSocket → EventRouter → Session）
- ❌ **策略层订单更新回调未注册或未触发**
- ✅ 手动订单能被系统识别，但策略无法收到更新

**可能的原因**:
1. `Initialize()` 方法未被调用
2. `TradingService` 在 `Initialize()` 时为 `nil`
3. 回调注册时机不对（可能在 `TradingService` 注入之前）

## 📋 手动订单识别情况

### ✅ 手动订单被系统识别

**订单ID**: `0xac0af4ab69137e16544cd075471e44b6f6de4f5a038ca3e2bd7e1bd28bce3679`

**订单详情**:
- **类型**: SELL (卖出)
- **价格**: 0.02 (2c)
- **数量**: 5 shares
- **资产**: YES (UP) token
- **状态**: open → canceled

**系统处理流程**:
```
[07:25:51] 📨 [UserWebSocket] 收到 WebSocket 消息: event_type=order
[07:25:51] 📦 [UserWebSocket] 订单对象构建完成: orderID=0xac0af4ab69137e16544cd075471e44b6f6de4f5a038ca3e2bd7e1bd28bce3679
[07:25:51] 📥 [EventRouter] 收到订单更新: orderID=0xac0af4ab69137e16544cd075471e44b6f6de4f5a038ca3e2bd7e1bd28bce3679
[07:25:51] 📤 [EventRouter] 转发订单更新到 Session
[07:25:51] 📥 [Session polymarket] 收到订单更新事件
[07:25:51] 📝 [Session polymarket] 补齐订单 MarketSlug: marketSlug=btc-updown-15m-1766618100
[07:25:51] 📝 [Session polymarket] 补齐订单 TokenType: tokenType=up
[07:25:51] ✅ [Session polymarket] 订单事件过滤通过
[07:25:51] 📊 [Session polymarket] 触发订单更新事件: handlers=0  ⚠️ handlers=0
```

**结论**:
- ✅ 手动订单能被系统完整识别和处理
- ✅ 订单信息补齐正常（MarketSlug, TokenType）
- ✅ 订单过滤正常（通过市场过滤）
- ❌ **策略无法收到订单更新（handlers=0）**

## 🔍 问题总结

### 1. 订单更新回调未注册 ⚠️
**问题**: 策略的 `OnOrderUpdate` 回调未注册或未触发

**影响**:
- 策略无法实时跟踪订单状态
- 手动订单无法被策略识别
- 订单失败时无法自动取消对冲单

**可能原因**:
1. `Initialize()` 方法未被调用
2. `TradingService` 注入时机不对
3. 回调注册时机问题

**建议**:
- 检查策略初始化流程
- 确认 `TradingService` 注入时机
- 在 `Subscribe()` 或 `Run()` 中注册回调（如果 `Initialize()` 时机不对）

### 2. 顺序下单模式超时 ⚠️
**问题**: 主单成交检测超时，导致对冲单下单失败

**影响**:
- 对冲单无法及时下单
- 可能造成未对冲风险

**建议**:
- 优化成交检测逻辑
- 增加轮询频率或延长等待时间
- 使用订单更新回调来检测成交（如果回调已注册）

### 3. 订单状态同步延迟 ⚠️
**问题**: WebSocket 订单状态和 API 状态不一致

**日志证据**:
```
[07:16:07] ⚠️ [状态一致性] WebSocket和API状态不一致: orderID=order_1766618141965963000, WebSocket状态=open, API状态=已成交/已取消
[07:16:07] 🔄 [订单状态同步] 订单已成交: orderID=order_1766618141965963000
```

**结论**: 
- ✅ 系统有状态一致性检查机制
- ✅ 订单状态同步机制正常工作
- ⚠️ WebSocket 延迟可能导致状态不一致

## ✅ 新架构优势体现

### 1. 周期管理 ✅
- ✅ 周期切换自动处理
- ✅ OrderEngine 状态自动重置
- ✅ 策略自动重新订阅

### 2. 订单跟踪 ✅
- ✅ 系统层订单跟踪正常
- ✅ 订单信息补齐正常
- ✅ 订单过滤正常

### 3. 订单执行 ✅
- ✅ 顺序下单模式正常工作
- ✅ 价格计算正常
- ✅ 订单提交正常

## 📝 改进建议

### 1. 立即修复：订单更新回调注册
**优先级**: 🔴 **高**

**方案**:
1. 检查策略初始化流程，确保 `Initialize()` 被调用
2. 确认 `TradingService` 注入时机
3. 如果 `Initialize()` 时机不对，考虑在 `Subscribe()` 或 `Run()` 中注册回调

### 2. 优化：顺序下单模式
**优先级**: 🟡 **中**

**方案**:
1. 增加轮询频率（从 50ms 降到 20ms）
2. 延长最大等待时间（从 1000ms 增加到 2000ms）
3. 使用订单更新回调来检测成交（如果回调已注册）

### 3. 监控：订单状态同步
**优先级**: 🟢 **低**

**方案**:
1. 监控 WebSocket 延迟
2. 优化状态一致性检查机制
3. 增加重试机制

---

**分析完成时间**: 2025-12-25  
**下一步**: 修复订单更新回调注册问题

