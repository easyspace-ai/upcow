# 订单状态同步修复 - 谁先确认以谁为准

## 📋 修复目标

**核心原则**：谁先确认订单的最终状态（filled/canceled），以谁为准。订单的最终状态不应该被中间状态（open/pending）覆盖。

## ✅ 修复内容

### 1. 添加 CanceledAt 字段

**位置**：`internal/domain/order.go`

**变更**：
- 添加 `CanceledAt *time.Time` 字段，用于跟踪订单取消时间

### 2. 添加最终状态判断方法

**位置**：`internal/domain/order.go`

**新增方法**：
- `IsFinalStatus()`: 检查订单是否为最终状态（filled/canceled/failed）
- `HasFinalStatusTimestamp()`: 检查订单是否有最终状态的时间戳（FilledAt 或 CanceledAt）

### 3. 修改订单状态同步逻辑

**位置**：`internal/services/trading_sync.go`

**核心逻辑**：

#### 情况 1：订单已经是最终状态
- **如果有时间戳**（WebSocket 先确认）：
  - 如果 API 显示订单仍在 open 列表中：保持最终状态（WebSocket 先到）
  - 如果 API 确认不在 open 列表中：状态一致，保持最终状态
- **如果没有时间戳**（最终状态未确认）：
  - 如果 API 显示订单仍在 open 列表中：恢复为 open 状态（以 API 为准）
  - 如果 API 确认不在 open 列表中：设置时间戳确认最终状态（API 先确认）

#### 情况 2：订单不是最终状态
- 如果 API 显示订单不在 open 列表中：
  - 更新订单状态为已成交（API 先确认）
  - 设置 FilledAt 时间戳

### 4. 更新 WebSocket 取消订单处理

**位置**：`internal/services/trading_ws_handlers.go`

**变更**：
- 在 `handleOrderCanceled` 中设置 `CanceledAt` 时间戳（WebSocket 先确认）

### 5. 更新 API 取消订单处理

**位置**：`internal/services/trading_sync.go`

**变更**：
- 在 `syncOrderStatusImpl` 中设置 `CanceledAt` 时间戳（API 先确认）

## 🔄 状态同步流程

### WebSocket 先确认（filled/canceled）
1. WebSocket 收到订单成交/取消消息
2. 设置 `FilledAt` 或 `CanceledAt` 时间戳
3. 更新订单状态为 filled/canceled
4. 订单状态同步时：
   - 如果 API 显示订单仍在 open 列表中：保持最终状态（WebSocket 先到）
   - 如果 API 确认不在 open 列表中：状态一致，保持最终状态

### API 先确认（不在 open 列表中）
1. 订单状态同步时，发现订单不在 API 的 open 列表中
2. 如果订单状态不是最终状态：
   - 更新订单状态为 filled
   - 设置 `FilledAt` 时间戳（API 先确认）
3. 如果订单状态是最终状态但无时间戳：
   - 设置 `FilledAt` 或 `CanceledAt` 时间戳（API 先确认）

### 中间状态不覆盖最终状态
- 如果订单已经是最终状态，且有时间戳，不应该被中间状态（open/pending）覆盖
- 如果 API 显示订单仍在 open 列表中，但订单已经有最终状态的时间戳，保持最终状态

## 📊 示例场景

### 场景 1：WebSocket 先确认成交
```
1. WebSocket 收到 Trade 消息 → 设置 FilledAt → 状态 = filled
2. 订单状态同步时，API 显示订单不在 open 列表中
3. 结果：保持 filled 状态（WebSocket 先到）
```

### 场景 2：API 先确认成交
```
1. 订单状态同步时，API 显示订单不在 open 列表中
2. 订单状态 = open（无时间戳）
3. 更新状态 = filled，设置 FilledAt（API 先确认）
```

### 场景 3：WebSocket 先确认，但 API 显示仍在 open
```
1. WebSocket 收到 Trade 消息 → 设置 FilledAt → 状态 = filled
2. 订单状态同步时，API 显示订单仍在 open 列表中
3. 结果：保持 filled 状态（WebSocket 先到，有时间戳）
```

### 场景 4：最终状态无时间戳，API 显示仍在 open
```
1. 订单状态 = filled（无 FilledAt）
2. 订单状态同步时，API 显示订单仍在 open 列表中
3. 结果：恢复为 open 状态（以 API 为准）
```

## ✅ 修复效果

1. **谁先确认以谁为准**：WebSocket 或 API 谁先确认最终状态，以谁为准
2. **最终状态不被覆盖**：最终状态（filled/canceled）不会被中间状态（open/pending）覆盖
3. **时间戳跟踪**：通过 `FilledAt` 和 `CanceledAt` 时间戳跟踪谁先确认
4. **状态一致性**：确保订单状态与实际情况一致

## 🔧 需要重新编译

所有修复都需要重新编译程序才能生效：

```bash
go build -o bot cmd/bot/main.go
```

