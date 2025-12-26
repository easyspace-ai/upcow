# 策略配置文件支持覆盖全局配置

## 功能说明

现在策略配置文件（如 `cyclehedge.yaml`）可以包含并覆盖以下全局配置：

1. **`market`** - 市场配置（symbol、timeframe、kind 等）
2. **`dry_run`** - 纸交易模式开关

## 使用方式

### 在策略配置文件中设置

```yaml
# 市场配置（会覆盖主配置文件中的设置）
market:
  symbol: "btc"
  timeframe: "15m"
  kind: "updown"

# 纸交易模式（会覆盖主配置文件中的设置）
dry_run: true

exchangeStrategies:
  - on: polymarket
    cyclehedge:
      # 策略配置...
```

### 配置优先级

1. **环境变量**（最高优先级）
2. **策略配置文件**（如 `cyclehedge.yaml`）
3. **主配置文件**（`yml/config.yaml`）
4. **默认值**（最低优先级）

### 多个策略文件的情况

如果加载了多个策略配置文件，**最后一个文件的配置会生效**。

例如：
- 加载 `strategy1.yaml`（设置 `dry_run: true`）
- 加载 `strategy2.yaml`（设置 `dry_run: false`）
- 最终 `dry_run` 为 `false`（`strategy2.yaml` 的配置生效）

## 实现细节

### 代码变更

1. **`pkg/config/config.go`**：
   - 扩展 `StrategyFile` 结构体，添加 `Market` 和 `DryRun` 字段
   - 新增 `LoadStrategyFile()` 函数，返回完整的策略文件信息
   - `LoadStrategyMountsFromFile()` 保持向后兼容，内部调用 `LoadStrategyFile()`

2. **`cmd/bot/main.go`**：
   - 修改配置加载逻辑，使用 `LoadStrategyFile()` 加载策略文件
   - 在合并策略配置后，提取并覆盖全局配置（`market` 和 `dry_run`）

### 配置覆盖逻辑

```go
// 从策略配置文件中提取并覆盖全局配置
for _, filePath := range strategyFilesLoaded {
    sf, err := config.LoadStrategyFile(filePath)
    if err != nil {
        continue
    }

    // 覆盖 market 配置（如果策略文件中设置了）
    if strings.TrimSpace(sf.Market.Symbol) != "" {
        cfg.Market.Symbol = sf.Market.Symbol
    }
    // ... 其他 market 字段

    // 覆盖 dry_run 配置（如果策略文件中设置了）
    if sf.DryRun != nil {
        cfg.DryRun = *sf.DryRun
    }
}
```

## 示例

### 主配置文件 (`yml/config.yaml`)

```yaml
# 全局配置
dry_run: false  # 默认实盘模式
market:
  symbol: "eth"
  timeframe: "1h"
  kind: "updown"
```

### 策略配置文件 (`yml/cyclehedge.yaml`)

```yaml
# 覆盖全局配置
dry_run: true  # 此策略使用模拟模式
market:
  symbol: "btc"  # 此策略使用 BTC 市场
  timeframe: "15m"
  kind: "updown"

exchangeStrategies:
  - on: polymarket
    cyclehedge:
      # 策略配置...
```

### 结果

- `dry_run` = `true`（来自策略配置文件）
- `market.symbol` = `"btc"`（来自策略配置文件）
- `market.timeframe` = `"15m"`（来自策略配置文件）

## 注意事项

1. **向后兼容**：如果策略配置文件中没有设置 `market` 或 `dry_run`，将使用主配置文件或默认值
2. **部分覆盖**：`market` 配置支持部分覆盖，例如只设置 `symbol`，其他字段保持主配置的值
3. **日志提示**：当策略配置文件覆盖 `dry_run` 时，会输出日志提示：
   ```
   策略配置文件覆盖 dry_run: true (来源: yml/cyclehedge.yaml)
   ```

## 相关文件

- `pkg/config/config.go` - 配置加载逻辑
- `cmd/bot/main.go` - 配置合并逻辑
- `yml/cyclehedge.yaml` - 策略配置文件示例

