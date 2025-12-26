# dry_run 配置问题修复说明

## 问题描述

用户发现：在 `yml/cyclehedge.yaml` 中设置了 `dry_run: true`（模拟交易模式），但实际执行了真实交易。

## 根本原因

**`dry_run` 是全局配置，只能从主配置文件读取，不能从策略配置文件读取。**

### 配置加载机制

1. **主配置文件**（`yml/config.yaml` 或 `yml/base.yaml`）：
   - 包含全局配置：`dry_run`、`log_level`、`minOrderSize` 等
   - 通过 `config.LoadFromFileWithOptions()` 加载
   - 全局配置会生效

2. **策略配置文件**（如 `yml/cyclehedge.yaml`）：
   - 只包含 `exchangeStrategies` 部分
   - 通过 `config.LoadStrategyMountsFromFile()` 加载
   - 只加载策略配置，**不加载全局配置**

### 代码证据

```549:551:pkg/config/config.go
type StrategyFile struct {
	ExchangeStrategies []ExchangeStrategyMount `yaml:"exchangeStrategies" json:"exchangeStrategies"`
}
```

`StrategyFile` 结构体只包含 `ExchangeStrategies` 字段，不包含 `dry_run` 等全局配置。

```463:472:pkg/config/config.go
		DryRun: func() bool {
			if configFile != nil {
				return configFile.DryRun
			}
			// 从环境变量读取，默认为 false
			if envVal := getEnv("DRY_RUN", ""); envVal != "" {
				return envVal == "true" || envVal == "1"
			}
			return false // 默认关闭纸交易模式
		}(),
```

`DryRun` 配置只从主配置文件（`configFile`）读取，如果主配置文件中没有设置，默认值为 `false`（实盘模式）。

## 修复方案

### 方案1：在主配置文件中设置（推荐）

在 `yml/config.yaml` 中添加 `dry_run` 配置：

```yaml
# 纸交易模式（dry run）
dry_run: true  # true = 仅模拟交易，false = 真实交易
```

### 方案2：使用环境变量

设置环境变量：

```bash
export DRY_RUN=true
```

环境变量的优先级高于配置文件。

## 已修复内容

1. ✅ 在 `yml/config.yaml` 中添加了 `dry_run: true`
2. ✅ 在 `yml/cyclehedge.yaml` 中添加了说明注释，提醒用户 `dry_run` 应放在主配置文件中

## 验证方法

启动程序后，检查日志中是否有以下提示：

```
📝 纸交易模式已启用：不会进行真实交易，订单信息仅记录在日志中
```

如果看到这个提示，说明 `dry_run: true` 已生效。

## 注意事项

- **全局配置**（如 `dry_run`、`log_level`、`minOrderSize`）必须放在主配置文件中
- **策略配置**（如 `exchangeStrategies`）可以放在策略配置文件中
- 如果主配置文件中没有 `dry_run`，默认值为 `false`（实盘模式）
- 环境变量 `DRY_RUN` 的优先级高于配置文件

## 相关文件

- `pkg/config/config.go` - 配置加载逻辑
- `cmd/bot/main.go` - 配置加载入口
- `yml/config.yaml` - 主配置文件
- `yml/cyclehedge.yaml` - 策略配置文件示例

