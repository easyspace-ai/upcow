# 完整日志分析报告 - 2025-12-25

## 📋 分析时间范围
- 周期 1: `btc-updown-15m-1766659500` (18:51:54 - 19:00:02)
- 周期 2: `btc-updown-15m-1766660400` (19:00:02 - 19:10:36)

## 📊 交易统计

### 周期 1 (btc-updown-15m-1766659500)

**交易次数**：3 次
**下单次数**：6 次（3 次 Entry + 3 次 Hedge）
**成交次数**：15 次（Entry 订单多次回调）

**交易详情**：

| 序号 | 时间 | Entry | Hedge | Entry Size | Hedge Size | Entry 价格 | Hedge 价格 | Entry 成本 | UP Win | DOWN Win |
|------|------|-------|-------|------------|------------|------------|------------|------------|--------|----------|
| 1 | 18:52:24 | UP | DOWN | 6.5 | 6.5 | 88c | 17c | $5.72 | $0.78 | $-5.72 |
| 2 | 18:52:26 | UP | DOWN | 10.0 | 10.0 | 90c | 11c | $9.00 | $1.00 | $-9.00 |
| 3 | 18:52:53 | UP | DOWN | 6.5 | 6.5 | 84c | 17c | $5.46 | $1.04 | $-5.46 |

**总成本**：$20.18
**总收益（如果 UP win）**：$2.82
**总亏损（如果 DOWN win）**：$-20.18

### 周期 2 (btc-updown-15m-1766660400)

**交易次数**：0 次
**跳过次数**：2439 次（主要是冷却期跳过）

## 🔍 发现的问题

### ⚠️ 问题 1: Hedge 订单 filledSize=0.0000

**现象**：
```
INFO[25-12-25 18:52:30] ✅ [velocityfollow] Hedge 订单已成交（通过订单更新回调）: orderID=order_1766659944902098000 filledSize=0.0000
```

**但订单状态同步显示**：
```
INFO[25-12-25 18:52:30] 🔄 [订单状态同步] 订单已成交: orderID=order_1766659944902098000, side=BUY, price=0.1700, size=6.50
```

**问题分析**：
1. Hedge 订单在纸交易模式下，订单更新回调中的 `filledSize=0.0000`
2. 但订单状态同步时显示 `size=6.50`（这是下单时的 size，不是实际成交的 size）
3. 这导致盈亏计算时，Hedge 订单的成本为 0，盈亏计算不正确

**影响**：
- 盈亏计算只考虑了 Entry 订单的成本
- 没有考虑 Hedge 订单的成本
- 实际盈亏计算应该是：`Entry 成本 + Hedge 成本`，但当前只计算了 `Entry 成本`

**根本原因**：
- 纸交易模式下，Hedge 订单（GTC）在模拟下单时，`status=open`，`filledSize=0.0000`
- 订单状态同步时，从 API 获取的订单信息可能没有 `filledSize` 字段，或者字段值为 0
- 策略的 `OnOrderUpdate` 回调中，`order.FilledSize` 可能没有正确更新

### ⚠️ 问题 2: WebSocket 和 API 状态不一致

**现象**：
```
WARN[25-12-25 18:52:30] ⚠️ [状态一致性] WebSocket和API状态不一致: orderID=order_1766659944902098000, WebSocket状态=open, API状态=已成交/已取消
```

**问题分析**：
1. 纸交易模式下，Hedge 订单（GTC）在 WebSocket 中显示为 `open`
2. 但订单状态同步时，从 API 获取的状态为 `已成交/已取消`
3. 这导致状态不一致警告

**影响**：
- 订单状态同步机制会尝试修复不一致
- 但可能影响订单状态的准确性

**根本原因**：
- 纸交易模式下，模拟下单的逻辑可能没有正确模拟 WebSocket 消息
- 或者订单状态同步时，API 返回的状态与 WebSocket 消息不一致

### ⚠️ 问题 3: 库存偏斜机制未启用

**现象**：
- 日志中没有看到：`✅ [velocityfollow] 库存偏斜机制已启用，阈值=50.00 shares`
- 日志中没有看到库存偏斜检查日志

**问题分析**：
1. 代码中已有库存偏斜机制的初始化逻辑
2. 配置文件中已有 `inventoryThreshold: 50.0`
3. 但日志中没有看到初始化日志

**可能原因**：
1. 代码未重新编译，程序仍在使用旧版本
2. `Initialize()` 方法没有被调用（但日志显示策略已订阅，所以应该被调用了）
3. `TradingService` 为 nil（但日志显示订单更新回调已注册，所以不应该为 nil）

**验证**：
- 检查代码：`internal/strategies/velocityfollow/strategy.go:161-165`
- 检查配置：`config.yaml:99`

### ⚠️ 问题 4: 盈亏计算未考虑 Hedge 订单

**现象**：
```
💰 [velocityfollow] 主单成交后实时盈亏计算: Entry=up @ 88c(下单)/88c(实际) size=6.5000 cost=$5.72 | UP win: $0.78 | DOWN win: $-5.72
```

**问题分析**：
1. 盈亏计算只考虑了 Entry 订单的成本（$5.72）
2. 没有考虑 Hedge 订单的成本（应该是 $1.11 = 17c * 6.5）
3. 实际总成本应该是：$5.72 + $1.11 = $6.83
4. 如果 UP win：收益 = $6.5 - $6.83 = $-0.33（亏损）
5. 如果 DOWN win：收益 = $6.5 - $6.83 = $-0.33（亏损）

**当前计算**（代码位置：`strategy.go:1049-1087`）：
- Entry 成本：$5.72
- Hedge 成本：$0（因为 `order.Status != domain.OrderStatusFilled`，所以没有进入 Hedge 成本计算分支）
- UP win：$6.5 - $5.72 = $0.78（错误，应该是 $-0.33）
- DOWN win：$-5.72（错误，应该是 $-0.33）

**代码逻辑**：
```go
// 如果对冲单已成交，重新计算（使用实际成交价格）
if hedgeOrderID != "" && s.TradingService != nil {
    activeOrders := s.TradingService.GetActiveOrders()
    for _, order := range activeOrders {
        if order.OrderID == hedgeOrderID && order.Status == domain.OrderStatusFilled {
            // 只有订单已成交时，才计算 Hedge 成本
            hedgeCost := float64(hedgeActualPriceCents) / 100.0 * hedgeFilledSize
            totalCost := entryCost + hedgeCost
            // ...
        }
    }
}
```

**问题**：
- 只有当 `order.Status == domain.OrderStatusFilled` 时，才计算 Hedge 成本
- 但在纸交易模式下，Hedge 订单（GTC）的 `status=open`，所以不会进入这个分支
- 导致盈亏计算时，Hedge 订单的成本为 0

**影响**：
- 盈亏计算不准确
- 可能导致策略决策错误

**根本原因**：
- Hedge 订单的 `filledSize=0.0000`，导致盈亏计算时没有考虑 Hedge 成本
- 盈亏计算逻辑可能没有正确处理 Hedge 订单未成交的情况

### ⚠️ 问题 5: 订单更新回调 handlers=0

**现象**：
```
📊 [Session polymarket] 触发订单更新事件: orderID=order_1766659944902098000 status=filled filledSize=0.0000 handlers=0
```

**问题分析**：
1. Session 的订单更新处理器数量为 0
2. 但策略的 `OnOrderUpdate` 回调仍然被调用（通过 EventRouter）
3. 这说明订单更新是通过 EventRouter 转发的，而不是直接通过 Session

**影响**：
- 订单更新回调机制正常工作（通过 EventRouter）
- 但 Session 的 handlers=0 可能表示某些功能未启用

**根本原因**：
- 策略的 `OnOrderUpdate` 回调是通过 `TradingService.OnOrderUpdate()` 注册的
- 而不是通过 `Session.OnOrderUpdate()` 注册的
- 所以 Session 的 handlers=0 是正常的（因为策略没有直接注册到 Session）

## ✅ 正常工作的功能

1. ✅ **周期切换机制**：正常
2. ✅ **策略订阅机制**：正常
3. ✅ **订单更新回调**：正常（通过 EventRouter）
4. ✅ **冷却期机制**：正常（方向级别去重）
5. ✅ **Entry 订单成交**：正常（FAK 订单立即成交）
6. ✅ **订单状态同步**：正常（能够检测并修复状态不一致）

## 🔧 需要修复的问题

### 优先级 1（高优先级）

1. **修复 Hedge 订单 filledSize=0.0000 的问题**
   - 纸交易模式下，Hedge 订单（GTC）的 `filledSize` 应该正确设置
   - 或者订单状态同步时，应该正确更新 `filledSize`

2. **修复盈亏计算未考虑 Hedge 订单的问题**
   - 盈亏计算应该考虑 Hedge 订单的成本
   - 如果 Hedge 订单未成交，应该使用下单时的 size 和 price 计算成本

### 优先级 2（中优先级）

3. **修复 WebSocket 和 API 状态不一致的问题**
   - 纸交易模式下，应该正确模拟 WebSocket 消息
   - 或者订单状态同步时，应该正确处理状态不一致

4. **验证库存偏斜机制是否启用**
   - 重新编译程序
   - 验证初始化日志是否出现
   - 验证库存偏斜检查是否正常工作

### 优先级 3（低优先级）

5. **优化订单更新回调机制**
   - 考虑将策略的 `OnOrderUpdate` 回调直接注册到 Session
   - 或者保持当前机制（通过 EventRouter），但确保 handlers 数量正确

## 📈 交易分析

### 周期 1 交易详情

**交易 1**：
- Entry: UP @ 88c, size=6.5, cost=$5.72
- Hedge: DOWN @ 17c, size=6.5, cost=$1.11（实际应该是这个，但日志显示 0）
- 总成本：$6.83
- UP win: $6.5 - $6.83 = $-0.33（亏损）
- DOWN win: $6.5 - $6.83 = $-0.33（亏损）

**交易 2**：
- Entry: UP @ 90c, size=10.0, cost=$9.00
- Hedge: DOWN @ 11c, size=10.0, cost=$1.10（实际应该是这个，但日志显示 0）
- 总成本：$10.10
- UP win: $10.0 - $10.10 = $-0.10（亏损）
- DOWN win: $10.0 - $10.10 = $-0.10（亏损）

**交易 3**：
- Entry: UP @ 84c, size=6.5, cost=$5.46
- Hedge: DOWN @ 17c, size=6.5, cost=$1.11（实际应该是这个，但日志显示 0）
- 总成本：$6.57
- UP win: $6.5 - $6.57 = $-0.07（亏损）
- DOWN win: $6.5 - $6.57 = $-0.07（亏损）

**总结**：
- 所有 3 笔交易都是亏损的（如果考虑 Hedge 成本）
- 总成本：$23.50
- 总收益（如果所有 UP win）：$23.00
- 净亏损：$-0.50

**注意**：
- 这是纸交易模式，实际盈亏可能不同
- 盈亏计算可能不准确（因为 Hedge 订单的 filledSize=0.0000）

## 🔍 问题根源分析

### 问题 1: Hedge 订单 filledSize=0.0000

**代码位置**：`internal/services/io_executor.go:55-66`

**当前逻辑**：
```go
if e.dryRun {
    result.Order.Status = domain.OrderStatusOpen
    
    // FAK 订单立即成交
    if order.OrderType == types.OrderTypeFAK {
        result.Order.Status = domain.OrderStatusFilled
        result.Order.FilledSize = order.Size
    }
    // GTC 订单保持 open，filledSize=0（默认值）
}
```

**问题**：
- GTC 订单（Hedge）在纸交易模式下，`status=open`，`filledSize=0.0000`
- 这是**正常的行为**（因为订单还没有成交）
- 但盈亏计算时，应该考虑 Hedge 订单的成本（即使还未成交）

**解决方案**：
- **方案 1**：盈亏计算时，如果 Hedge 订单未成交，使用下单时的 `size` 和 `price` 计算成本
- **方案 2**：纸交易模式下，GTC 订单也立即模拟成交（但这不符合实际行为）

**推荐方案 1**，因为：
- GTC 订单在纸交易模式下保持 `open` 状态是合理的
- 盈亏计算应该考虑所有已下单的订单成本（包括未成交的 Hedge 订单）

### 问题 2: WebSocket 和 API 状态不一致

**代码位置**：`internal/services/trading_sync.go:384-389`

**当前逻辑**：
- 订单状态同步时，从 API 获取开放订单列表
- 如果本地订单不在开放订单列表中，认为订单已成交
- 纸交易模式下，`GetOpenOrders` API 可能返回空列表，导致所有订单都被认为已成交

**问题**：
- 纸交易模式下，`GetOpenOrders` API 的行为可能不同
- 导致订单状态同步逻辑误判

**解决方案**：
- 纸交易模式下，跳过订单状态同步（因为订单状态已经在本地管理）
- 或者，纸交易模式下，`GetOpenOrders` API 应该返回本地管理的开放订单列表

### 问题 3: 库存偏斜机制未启用

**可能原因**：
1. 代码未重新编译
2. `Initialize()` 方法没有被调用（但日志显示策略已订阅，所以应该被调用了）
3. `TradingService` 为 nil（但日志显示订单更新回调已注册，所以不应该为 nil）

**验证步骤**：
- 检查代码：`internal/strategies/velocityfollow/strategy.go:161-165`
- 检查配置：`config.yaml:99`
- 重新编译程序并观察日志

## 🎯 建议操作

### 优先级 1（立即修复）

1. **修复盈亏计算未考虑 Hedge 订单的问题**
   - 在 `strategy.go` 的盈亏计算逻辑中，如果 Hedge 订单未成交，使用下单时的 `size` 和 `price` 计算成本
   - 代码位置：`internal/strategies/velocityfollow/strategy.go:990-1072`

2. **修复纸交易模式下订单状态同步的问题**
   - 纸交易模式下，跳过订单状态同步（因为订单状态已经在本地管理）
   - 或者，纸交易模式下，`GetOpenOrders` API 应该返回本地管理的开放订单列表

### 优先级 2（验证）

3. **验证库存偏斜机制是否启用**
   - 重新编译程序
   - 观察日志，确认是否出现：`✅ [velocityfollow] 库存偏斜机制已启用，阈值=50.00 shares`
   - 如果有交易且净持仓超过阈值，确认是否出现库存偏斜保护日志

### 优先级 3（优化）

4. **优化纸交易模式的模拟逻辑**
   - 考虑在纸交易模式下，GTC 订单也立即模拟成交（但这可能不符合实际行为）
   - 或者，改进盈亏计算逻辑，正确处理未成交的 Hedge 订单

5. **优化订单状态同步机制**
   - 纸交易模式下，订单状态同步应该使用本地管理的订单状态
   - 而不是调用交易所 API

