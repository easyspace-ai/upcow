## Grid 实盘标准检查清单（优先级：高）

### 目标
- **能持续跑**：不中断、不死锁、断线能自愈
- **能控制风险**：任何异常都能快速降级/停止下单
- **能观测**：出问题能定位（订单、仓位、对冲状态机、WS/对账）
- **能回滚**：配置/版本回退成本低

### A. 配置与启动（必做）
- **marketSlug 管理**：所有下单必须携带 `MarketSlug`（已在 TradingService 层强制校验）
- **最小下单金额**：确认 `min_order_size` / `auto_adjust_size` / `max_size_adjust_ratio` 的组合策略符合预期
- **滑点上限**：实盘必须设置 `max_buy_slippage_cents`（或明确为 0 并承担风险）
- **dryRun**：在上实盘前先跑 1-2 个周期 dryRun，确认策略状态机稳定
- **代理/网络**：WS 与 REST 都要能连通；代理配置明确且可复现

### B. 运行时安全阀（必做）
- **断路器/去重**：TradingService 已带 circuit breaker + in-flight 去重；实盘需确认阈值合理
- **限并发**：策略内部必须有 in-flight limiter（grid 已使用；新策略必须遵循模板）
- **卡单诊断**：任何 “isPlacingOrder / submitting” 卡住必须能自动恢复（grid 已做 placing_order_guard + HedgePlan 超时）

### C. 对账与状态一致性（必做）
- **WS + REST 双通道**：WS 负责快更新，REST 周期性兜底对账（TradingService sync 已存在）
- **跨周期串单防护**：策略只处理当前周期 market 的订单/事件（MarketSlugGuard）
- **快照恢复**：重启后先从 snapshot 热启动，再用 open orders 对账纠偏（已存在）

### D. 可观测性（强烈建议）
- **关键日志**：每次 HedgePlan 状态转移、入场/对冲下单、取消、补单、失败原因必须可追溯
- **指标**：PlaceOrder 延迟、错误数、对账次数、对冲成功率、强对冲次数

### E. 上线步骤（推荐）
- **阶段 1**：dryRun 跑 ≥ 3 个周期，确认无死锁/无重复下单/对账稳定
- **阶段 2**：小资金实盘：降低 `order_size`，提高风控（更严滑点/更短断路器阈值）
- **阶段 3**：逐步放量：每次只改一个参数，观察 2-3 个周期

