## 标准策略模板（新策略一律按此结构）

### 目录结构（推荐）
```
internal/strategies/<strategy_name>/
  - config.go           # 配置结构体 + Validate（json/yaml tag 使用 camelCase）
  - strategy.go         # Strategy struct + 生命周期 + 核心状态机入口
  - event_loop.go       # 单 goroutine loop（使用 common.StartLoopOnce）
  - order_loop_handlers.go / handlers.go  # 事件处理拆分（保持 strategy.go 不膨胀）
```

### 必须遵守的工程规范（实盘标准）
- **单线程 loop**：所有状态机推进在单 goroutine 中完成（避免锁与竞态）
- **不阻塞 loop**：网络 IO 一律通过 `bbgo.CommandExecutor` 投递；无 executor 时允许兼容，但要避免高频触发
- **下单统一入口**：报价/滑点/最小金额调整要复用 `internal/strategies/common` 的通用组件（例如 `QuoteAndAdjustBuy`）
- **限并发**：必须使用 `common.InFlightLimiter`
- **非阻塞信号**：必须使用 `common.TrySignal` / `common.TrySend`
- **跨周期隔离**：周期切换由框架统一管理；策略如需重置状态，实现 `bbgo.CycleAwareStrategy.OnCycle`（禁止在策略内自行对比 market slug）
- **配置加载**：按 bbgo(main) 风格，配置由 loader 直接反序列化到策略 struct（不需要 adapter）

### 可直接复制的代码骨架
- 见：`internal/strategies/template/`（该包**不注册**，仅用于复制/参考，确保可编译）

