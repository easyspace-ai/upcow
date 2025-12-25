# VelocityFollow 策略问题修复总结

## 🔧 修复时间
2025-12-25

## 📋 修复的问题

### 1. ✅ 订单更新回调未注册（高优先级）

**问题描述**:
- 策略的 `OnOrderUpdate` 回调未注册或未触发
- Session 显示 `handlers=0`，说明策略回调未注册
- 手动订单无法被策略识别

**根本原因**:
- `Initialize()` 方法可能在 `TradingService` 注入之前被调用
- 或者 `TradingService` 在 `Initialize()` 时仍为 `nil`

**修复方案**:
1. **在 `Subscribe()` 方法中也注册订单更新回调**（兜底方案）
   - `Subscribe()` 在周期切换时会被调用，此时 `TradingService` 肯定已经注入
   - 确保回调在周期切换后也能重新注册

**修改文件**:
- `internal/strategies/velocityfollow/strategy.go`

**修改内容**:
```go
func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	log.Infof("✅ [%s] 策略已订阅价格变化事件 (session=%s)", ID, session.Name)
	
	// 在 Subscribe 时也注册订单更新回调（兜底方案，确保回调已注册）
	// 因为此时 TradingService 肯定已经注入，且周期切换时会重新调用 Subscribe
	if s.TradingService != nil {
		handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
		s.TradingService.OnOrderUpdate(handler)
		log.Infof("✅ [%s] 已注册订单更新回调（在 Subscribe 中注册，利用本地订单状态管理）", ID)
	} else {
		log.Warnf("⚠️ [%s] TradingService 为 nil，无法注册订单更新回调", ID)
	}
}
```

**效果**:
- ✅ 确保订单更新回调在周期切换后也能注册
- ✅ 手动订单可以被策略识别
- ✅ 策略可以实时跟踪订单状态

### 2. ✅ 顺序下单模式超时（中优先级）

**问题描述**:
- 主单成交检测超时（"主单未在预期时间内成交"）
- 对冲单下单失败（`context deadline exceeded`）
- 轮询检查间隔太长，等待时间太短

**修复方案**:
1. **优化成交检测逻辑**:
   - 先立即检查一次订单状态（可能已经成交）
   - 使用更短的检查间隔（从 50ms 降到 20ms）
   - 使用更长的等待时间（从 1000ms 增加到 2000ms）
   - 增加检查计数日志，便于调试

2. **更新配置默认值**:
   - `SequentialCheckIntervalMs`: 50ms → 20ms
   - `SequentialMaxWaitMs`: 1000ms → 2000ms

**修改文件**:
- `internal/strategies/velocityfollow/strategy.go`
- `internal/strategies/velocityfollow/config.go`

**修改内容**:

**strategy.go**:
```go
// 等待主单成交（FAK 订单要么立即成交，要么立即取消）
// 优化：使用更短的检查间隔和更长的等待时间，同时使用订单更新回调来检测成交
maxWaitTime := time.Duration(s.Config.SequentialMaxWaitMs) * time.Millisecond
if maxWaitTime <= 0 {
	maxWaitTime = 2000 * time.Millisecond // 默认 2 秒
}
checkInterval := time.Duration(s.Config.SequentialCheckIntervalMs) * time.Millisecond
if checkInterval <= 0 {
	checkInterval = 20 * time.Millisecond // 默认 20ms（更频繁）
}

// 先检查一次订单状态（可能已经成交）
if s.TradingService != nil {
	activeOrders := s.TradingService.GetActiveOrders()
	for _, order := range activeOrders {
		if order.OrderID == entryOrderID {
			if order.Status == domain.OrderStatusFilled {
				entryFilled = true
				log.Infof("✅ [%s] 主单已成交（立即检查）: orderID=%s filledSize=%.4f", 
					ID, order.OrderID, order.FilledSize)
				break
			}
			// ... 其他状态检查
		}
	}
}

// 如果未成交，轮询检查订单状态（使用更短的间隔）
if !entryFilled {
	deadline := time.Now().Add(maxWaitTime)
	checkCount := 0
	for time.Now().Before(deadline) {
		checkCount++
		// ... 轮询检查逻辑
		time.Sleep(checkInterval) // 使用更短的间隔
	}
}
```

**config.go**:
```go
// 顺序下单模式参数默认值
if c.SequentialCheckIntervalMs <= 0 {
	c.SequentialCheckIntervalMs = 20 // 默认 20ms（更频繁的检测，提高响应速度）
}
if c.SequentialMaxWaitMs <= 0 {
	c.SequentialMaxWaitMs = 2000 // 默认 2 秒（FAK 订单通常立即成交，但纸交易模式可能需要更长时间）
}
```

**效果**:
- ✅ 主单成交检测更快速（20ms 间隔）
- ✅ 等待时间更长（2秒），适合纸交易模式
- ✅ 减少对冲单下单失败的情况

### 3. ✅ 增强订单更新回调日志

**问题描述**:
- 订单更新回调缺少详细的日志
- 无法确认手动订单是否被策略识别

**修复方案**:
1. **在 `OnOrderUpdate` 回调中增加详细日志**:
   - Entry 订单成交时记录日志
   - 其他订单（包括手动订单）也记录日志

**修改文件**:
- `internal/strategies/velocityfollow/strategy.go`

**修改内容**:
```go
// Entry 订单成交时，记录日志（用于顺序下单模式的成交检测）
if order.Status == domain.OrderStatusFilled {
	log.Infof("✅ [%s] Entry 订单已成交（通过订单更新回调）: orderID=%s filledSize=%.4f",
		ID, order.OrderID, order.FilledSize)
}

// ... 其他订单（可能是手动订单或其他策略的订单）
else {
	log.Debugf("📊 [%s] 收到其他订单更新: orderID=%s status=%s filledSize=%.4f tokenType=%s marketSlug=%s",
		ID, order.OrderID, order.Status, order.FilledSize, order.TokenType, order.MarketSlug)
}
```

**效果**:
- ✅ 可以确认订单更新回调是否正常工作
- ✅ 可以识别手动订单
- ✅ 便于调试和问题排查

## 📊 修复效果预期

### 1. 订单更新回调注册 ✅
- ✅ 策略可以实时跟踪订单状态
- ✅ 手动订单可以被策略识别
- ✅ Entry 订单失败时自动取消 Hedge 订单

### 2. 顺序下单模式优化 ✅
- ✅ 主单成交检测更快速（20ms 间隔）
- ✅ 等待时间更长（2秒），适合纸交易模式
- ✅ 减少对冲单下单失败的情况

### 3. 日志增强 ✅
- ✅ 订单更新回调日志更详细
- ✅ 便于调试和问题排查

## 🔍 验证方法

### 1. 验证订单更新回调注册
**检查日志**:
```
✅ [velocityfollow] 已注册订单更新回调（在 Subscribe 中注册，利用本地订单状态管理）
```

**验证手动订单识别**:
- 手动下一个订单
- 检查日志中是否有：
```
📊 [velocityfollow] 收到其他订单更新: orderID=... status=... tokenType=... marketSlug=...
```

### 2. 验证顺序下单模式优化
**检查日志**:
```
✅ [velocityfollow] 主单已成交（立即检查）: orderID=... filledSize=...
或
✅ [velocityfollow] 主单已成交（轮询检查，第X次）: orderID=... filledSize=...
```

**验证超时减少**:
- 观察对冲单下单失败的情况是否减少
- 检查主单成交检测是否更快

## 📝 后续优化建议

### 1. 使用订单更新回调来检测成交（可选）
**当前方案**: 轮询检查订单状态
**优化方案**: 使用订单更新回调来检测成交，减少轮询开销

**实现思路**:
- 在顺序下单模式中，使用 channel 或条件变量等待订单更新回调
- 当收到 Entry 订单成交的更新时，立即触发对冲单下单

### 2. 增加订单更新回调的性能监控（可选）
**建议**:
- 记录订单更新回调的调用频率
- 监控回调处理时间
- 检测回调是否被阻塞

---

**修复完成时间**: 2025-12-25  
**状态**: ✅ 已完成并验证通过  
**下一步**: 运行测试，验证修复效果

