# 库存偏斜机制实现总结

## 📋 实现概述

已成功实现**库存偏斜机制（Inventory Skew）**，并将其封装为通用工具，供所有策略使用。

## 🎯 核心功能

### 1. 通用库存计算器 (`internal/strategies/common/inventory.go`)

**设计目标**：
- ✅ 封装为通用工具，所有策略都可以使用
- ✅ 支持周期隔离（只计算当前周期的持仓）
- ✅ 基于订单状态计算净持仓（准确、实时）

**核心方法**：

```go
// 创建库存计算器
calculator := common.NewInventoryCalculator(tradingService)

// 计算净持仓
result := calculator.CalculateNetPosition(marketSlug)
// result.NetPosition = UP 持仓 - DOWN 持仓

// 检查库存偏斜（完整检查）
shouldSkip := calculator.CheckInventorySkew(marketSlug, threshold, direction)
```

**计算逻辑**：
```
UP 持仓 = sum(已成交的 Entry UP 订单) - sum(已成交的 Hedge UP 订单)
DOWN 持仓 = sum(已成交的 Entry DOWN 订单) - sum(已成交的 Hedge DOWN 订单)
净持仓 = UP 持仓 - DOWN 持仓
```

### 2. 在 velocityfollow 策略中集成

**配置参数**：
```yaml
inventoryThreshold: 50.0  # 净持仓阈值（shares），0=禁用
```

**实现位置**：
- `Initialize()`: 初始化库存计算器
- `OnPriceChanged()`: 在确定交易方向后，检查库存偏斜

**检查逻辑**：
```go
if netPosition > threshold && direction == UP:
    跳过 UP 方向的交易
if netPosition < -threshold && direction == DOWN:
    跳过 DOWN 方向的交易
```

## 📊 使用示例

### 示例 1: 正常情况

```
Entry 订单：
- Entry UP @ 60c (已成交, size=6.5) ✅
- Entry UP @ 62c (已成交, size=6.5) ✅
- Entry UP @ 64c (已成交, size=6.5) ✅

Hedge 订单：
- Hedge DOWN @ 40c (已成交, size=6.5) ✅
- Hedge DOWN @ 38c (已成交, size=6.5) ✅
- Hedge DOWN @ 36c (已成交, size=6.5) ✅

净持仓 = (6.5 + 6.5 + 6.5) - (6.5 + 6.5 + 6.5) = 0 ✅ 平衡
→ 允许继续交易
```

### 示例 2: Hedge 订单未成交

```
Entry 订单：
- Entry UP @ 60c (已成交, size=6.5) ✅
- Entry UP @ 62c (已成交, size=6.5) ✅
- Entry UP @ 64c (已成交, size=6.5) ✅

Hedge 订单：
- Hedge DOWN @ 40c (已成交, size=6.5) ✅
- Hedge DOWN @ 38c (未成交) ⚠️
- Hedge DOWN @ 36c (未成交) ⚠️

净持仓 = (6.5 + 6.5 + 6.5) - (6.5) = 13（UP方向）
→ 如果 threshold = 10，停止 UP 方向的交易
→ 只允许 DOWN 方向的交易（可以平仓）
```

### 示例 3: 多个 Hedge 订单未成交

```
Entry 订单：
- Entry UP @ 60c (已成交, size=6.5) ✅
- Entry UP @ 62c (已成交, size=6.5) ✅
- Entry UP @ 64c (已成交, size=6.5) ✅
- Entry UP @ 66c (已成交, size=6.5) ✅
- Entry UP @ 68c (已成交, size=6.5) ✅

Hedge 订单：
- Hedge DOWN @ 40c (已成交, size=6.5) ✅
- Hedge DOWN @ 38c (未成交) ⚠️
- Hedge DOWN @ 36c (未成交) ⚠️
- Hedge DOWN @ 34c (未成交) ⚠️
- Hedge DOWN @ 32c (未成交) ⚠️

净持仓 = (6.5 * 5) - (6.5) = 26（UP方向）
→ 如果 threshold = 50，仍然允许交易（但应该降低频率）
→ 如果 threshold = 20，停止 UP 方向的交易
```

## 🔧 配置说明

### 阈值设置建议

**根据订单大小设置**：
- `orderSize = 6.5`，`threshold = 50` → 约 7-8 个订单的净持仓
- `orderSize = 8.0`，`threshold = 50` → 约 6 个订单的净持仓
- `orderSize = 10.0`，`threshold = 50` → 约 5 个订单的净持仓

**根据风险承受能力设置**：
- **保守**：`threshold = 30`（约 4-5 个订单）
- **中等**：`threshold = 50`（约 7-8 个订单）
- **激进**：`threshold = 100`（约 15 个订单）

### 禁用库存偏斜

设置 `inventoryThreshold: 0` 即可禁用库存偏斜机制。

## 🎨 其他策略使用示例

### Grid 策略

```go
import "github.com/betbot/gobet/internal/strategies/common"

type Strategy struct {
    TradingService *services.TradingService
    inventoryCalculator *common.InventoryCalculator
    // ...
}

func (s *Strategy) Initialize() error {
    if s.TradingService != nil {
        s.inventoryCalculator = common.NewInventoryCalculator(s.TradingService)
    }
    return nil
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
    // ... 确定交易方向 ...
    
    // 检查库存偏斜
    if s.inventoryCalculator != nil && s.Config.InventoryThreshold > 0 {
        shouldSkip := s.inventoryCalculator.CheckInventorySkew(
            e.Market.Slug, 
            s.Config.InventoryThreshold, 
            direction,
        )
        if shouldSkip {
            return nil // 跳过交易
        }
    }
    
    // ... 执行交易 ...
}
```

### Adaptive 策略

```go
// 获取净持仓详情
result := s.inventoryCalculator.CalculateNetPosition(marketSlug)
log.Infof("📊 净持仓: UP=%.2f, DOWN=%.2f, Net=%.2f", 
    result.UpInventory, result.DownInventory, result.NetPosition)

// 根据净持仓调整交易策略
if result.NetPosition > threshold {
    // 降低 UP 方向的交易频率
}
```

## ⚠️ 注意事项

1. **周期隔离**：
   - 只计算当前周期的持仓，不计算旧周期的持仓
   - 周期切换时，净持仓会自动重置

2. **订单状态**：
   - 只计算已成交的订单（`Status = Filled`）
   - 使用订单的实际成交数量（`FilledSize`），而不是下单数量（`Size`）

3. **订单类型识别**：
   - Entry 订单：`IsEntryOrder = true`
   - Hedge 订单：`IsEntryOrder = false`

4. **性能考虑**：
   - 库存计算器会遍历所有活跃订单，但只计算已成交的订单
   - 如果订单数量很大，可以考虑缓存结果（但需要处理订单状态更新）

## 📈 未来改进方向

1. **动态阈值**：
   - 根据剩余时间动态调整阈值
   - 周期结束前，降低阈值，更严格地控制持仓

2. **持仓统计**：
   - 添加持仓统计功能（持仓成本、平均价格等）
   - 支持多周期持仓统计

3. **性能优化**：
   - 缓存计算结果，减少重复计算
   - 只在订单状态更新时重新计算

4. **其他策略集成**：
   - 在 Grid、Adaptive 等策略中集成库存偏斜机制
   - 提供统一的配置接口

## ✅ 测试建议

1. **单元测试**：
   - 测试净持仓计算逻辑
   - 测试库存偏斜检查逻辑

2. **集成测试**：
   - 测试多个订单的净持仓计算
   - 测试周期切换时的持仓重置

3. **实盘测试**：
   - 观察库存偏斜机制是否正常工作
   - 验证阈值设置是否合理

