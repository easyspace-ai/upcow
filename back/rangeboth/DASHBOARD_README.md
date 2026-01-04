# Dashboard 使用说明

## 概述

Dashboard 提供了两种实现方式：

1. **ANSI版本** (`dashboard.go`) - 使用 ANSI 转义序列实现
2. **Bubbletea版本** (`dashboard_bubbletea.go`) - 使用 bubbletea 框架实现（推荐）

## 当前配置

**默认使用 Bubbletea 版本**，提供更好的用户体验：
- ✅ 自动处理终端尺寸变化
- ✅ 流畅的渲染（无闪烁）
- ✅ 支持键盘交互（按 'q' 退出，按 'r' 刷新）
- ✅ 美观的样式（使用 lipgloss）

## 功能特性

### 1. 周期信息
- 显示当前周期名称
- 实时倒计时显示剩余时间

### 2. 波动幅度监控
- 观察窗口配置（lookbackSeconds）
- 最大允许波动（maxRangeCents）
- UP/DOWN 方向的实时波动数据
- 稳定状态指示（✅/❌）
- 整体下单条件判断

### 3. 已成交订单
- 显示当前周期所有已成交订单
- 包括订单ID、方向、价格、数量、成交时间
- 最多显示10条，按时间倒序排列

### 4. 未成交挂单
- 显示当前周期所有未成交挂单
- 包括订单ID、方向、价格、数量、状态、创建时间

### 5. 收益计算
- UP/DOWN 持仓数量和成本
- 总成本统计
- 如果UP获胜的收益
- 如果DOWN获胜的收益
- 最小收益（无论哪方获胜）
- 使用颜色区分盈利（绿色）和亏损（红色）

## 键盘快捷键

- `q` 或 `Ctrl+C`: 退出 Dashboard
- `r`: 手动刷新数据

## 切换版本

如果需要使用 ANSI 版本，修改 `strategy.go` 中的 `Initialize()` 方法：

```go
// 取消注释以下代码以使用ANSI版本
if s.TradingService != nil {
    s.dashboard = NewDashboard(s.TradingService, &sp)
    s.dashboard.SetStrategy(s)
    log.Infof("✅ [%s] Dashboard已初始化（ANSI版本）", ID)
}
```

并在 `Run()` 方法中注释掉 bubbletea 版本的启动代码。

## 技术细节

### Bubbletea 版本架构

```
Strategy.Run()
  └─> RunDashboard() (goroutine)
      └─> tea.NewProgram()
          └─> dashboardModel
              ├─> Init() - 启动定时器
              ├─> Update() - 处理消息，更新数据
              └─> View() - 渲染UI
```

### 数据更新机制

- 使用 `tea.Tick()` 创建每秒更新的定时器
- 在 `Update()` 方法中处理 `tickMsg`，刷新所有数据
- 自动响应终端尺寸变化（`tea.WindowSizeMsg`）

### 样式系统

使用 `lipgloss` 定义样式：
- `borderStyle`: 边框样式（圆角，紫色）
- `titleStyle`: 标题样式（粗体，紫色）
- `successStyle`: 成功状态（绿色）
- `warningStyle`: 警告状态（黄色）
- `errorStyle`: 错误状态（红色）
- `infoStyle`: 信息样式（蓝色）

## 性能优化

- Bubbletea 使用帧率渲染，自动优化性能
- 数据更新在 `Update()` 中进行，避免阻塞渲染
- 使用缓冲区收集输出，减少系统调用

## 故障排除

### Dashboard 不显示

1. 检查终端是否支持 TUI（大多数现代终端都支持）
2. 检查是否有错误日志
3. 尝试使用 ANSI 版本作为备选

### 数据显示不正确

1. 确保 Strategy 已正确初始化
2. 检查 TradingService 是否正常工作
3. 查看日志确认数据源是否正常

### 退出问题

- 按 `q` 或 `Ctrl+C` 退出
- 如果无法退出，检查是否有其他进程占用终端

## 未来改进

- [ ] 添加鼠标交互支持
- [ ] 支持多视图切换（Tab键）
- [ ] 添加订单搜索/过滤功能
- [ ] 支持自定义样式主题
- [ ] 添加数据导出功能
