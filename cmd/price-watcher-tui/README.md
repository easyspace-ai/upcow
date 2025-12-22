# 价格监控 TUI 程序

使用 [Bubble Tea](https://github.com/charmbracelet/bubbletea) 构建的终端用户界面（TUI），用于实时监控 Polymarket UP/DOWN token 价格和 BTC 价格。

## 功能特性

- ✅ **实时订单薄显示**：显示 UP 和 DOWN token 的买卖订单薄
- ✅ **BTC 价格监控**：实时显示 BTC 当前价格
- ✅ **周期信息显示**：在头部显示当前周期和开始时间
- ✅ **自动周期切换**：每15分钟自动切换到新周期
- ✅ **美观的 TUI 界面**：使用 lipgloss 美化界面，UP 显示为绿色，DOWN 显示为红色

## 界面布局

```
┌─────────────────────────────────────────────────────────┐
│ 周期: btc-updown-15m-1766394900 | BTC: $12345.67      │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  ┌───────────┐        ┌───────────┐                   │
│  │    UP     │        │   DOWN    │                   │
│  │           │        │           │                   │
│  │ 卖单(Asks)│        │ 卖单(Asks)│                   │
│  │  62.50    │        │  37.50    │                   │
│  │           │        │           │                   │
│  │中间价:62.5│        │中间价:37.5│                   │
│  │           │        │           │                   │
│  │ 买单(Bids)│        │ 买单(Bids)│                   │
│  │  62.00    │        │  37.00    │                   │
│  └───────────┘        └───────────┘                   │
│                                                         │
│  按 q 退出                                              │
└─────────────────────────────────────────────────────────┘
```

## 使用方法

### 编译

```bash
go build -o price-watcher-tui ./cmd/price-watcher-tui
```

### 运行

```bash
./price-watcher-tui
```

或者直接运行：

```bash
go run ./cmd/price-watcher-tui/main.go
```

### 退出

按 `q` 或 `Ctrl+C` 退出程序。

## 配置

程序会自动读取 `config.yaml` 文件中的代理配置（如果存在）。如果没有配置文件，会使用默认代理 `127.0.0.1:15236`。

## 技术实现

- **Bubble Tea**：TUI 框架，基于 Elm 架构
- **Lip Gloss**：终端样式和布局库
- **MarketStream**：连接 Polymarket 市场数据 WebSocket
- **RTDS**：连接 RTDS WebSocket 获取 BTC 价格
- **AtomicBestBook**：无锁的订单薄数据结构

## 数据来源

- **UP/DOWN 价格**：从 Polymarket 市场数据 WebSocket (`wss://ws-subscriptions-clob.polymarket.com/ws/market`) 获取
- **BTC 价格**：从 RTDS WebSocket (`wss://ws-live-data.polymarket.com`) 获取

## 注意事项

- 程序需要网络连接以访问 WebSocket 服务
- 确保代理配置正确（如果需要）
- 程序会自动处理周期切换，无需手动干预
- 订单薄数据来自 BestBook（top-of-book），显示最佳买卖价格

## 调试

如果遇到问题，可以启用调试日志：

```bash
DEBUG=1 go run ./cmd/price-watcher-tui/main.go
```

调试日志会保存到 `debug.log` 文件中。

