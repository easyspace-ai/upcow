package winbet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/config"
	"github.com/betbot/gobet/pkg/marketspec"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sirupsen/logrus"
)

// tickMsg 定时更新消息
type tickMsg time.Time

// logMsg 日志消息
type logMsg struct {
	level   string
	message string
	time    time.Time
}

// multiWriter 多写入器：同时写入文件和hook
type multiWriter struct {
	file *os.File
	hook *logCollector
}

func (m *multiWriter) Write(p []byte) (n int, err error) {
	// 写入文件
	if m.file != nil {
		m.file.Write(p)
	}
	return len(p), nil
}

// logCollector 日志收集器（实现logrus.Hook接口）
type logCollector struct {
	logChan chan logMsg
}

// Levels 返回要捕获的日志级别
func (h *logCollector) Levels() []logrus.Level {
	return []logrus.Level{
		logrus.ErrorLevel,
		logrus.WarnLevel,
		logrus.InfoLevel,
		logrus.DebugLevel,
	}
}

// Fire 当有日志时调用
func (h *logCollector) Fire(entry *logrus.Entry) error {
	// 只捕获最重要的日志：ERROR、WARN，以及关键的操作信息
	level := ""
	msg := entry.Message
	
	switch entry.Level {
	case logrus.ErrorLevel:
		level = "ERROR"
	case logrus.WarnLevel:
		level = "WARN"
	case logrus.InfoLevel:
		// 只捕获关键的操作信息，过滤掉常规日志
		// 捕获：交易相关、订单相关、周期切换、市场数据获取失败等
		if strings.Contains(msg, "交易") ||
			strings.Contains(msg, "订单") ||
			strings.Contains(msg, "周期") ||
			strings.Contains(msg, "市场数据") ||
			strings.Contains(msg, "持仓") ||
			strings.Contains(msg, "利润") ||
			strings.Contains(msg, "启动") ||
			strings.Contains(msg, "关闭") ||
			strings.Contains(msg, "失败") ||
			strings.Contains(msg, "错误") {
			level = "INFO"
		} else {
			// 跳过常规的INFO日志
			return nil
		}
	case logrus.DebugLevel:
		// 不捕获DEBUG日志到UI，只写入文件
		return nil
	default:
		return nil
	}

	// 格式化消息（移除时间戳，因为UI会显示）
	// 限制消息长度，避免UI显示过长
	if len(msg) > 120 {
		msg = msg[:117] + "..."
	}

	// 使用recover来捕获panic（防止向已关闭的channel发送）
	defer func() {
		if r := recover(); r != nil {
			// channel已关闭，忽略错误（UI可能已经退出）
		}
	}()

	select {
	case h.logChan <- logMsg{level: level, message: msg, time: entry.Time}:
	default:
		// channel已满，丢弃最旧的消息（非阻塞）
	}
	return nil
}

// uiModel Bubbletea模型
type uiModel struct {
	// 数据源
	tradingService *services.TradingService
	marketSpec     marketspec.MarketSpec
	strategy       *Strategy
	ctx            context.Context // 用于检查是否应该退出

	// 状态数据
	currentMarketSlug string
	lastMarketSlug    string // 用于检测周期切换
	countdown         string
	initialized       bool // 标记是否已初始化（窗口尺寸已设置）

	// UP/DOWN 价格数据
	upBid        float64
	upAsk        float64
	upVelocity   float64
	downBid      float64
	downAsk      float64
	downVelocity float64

	// 持仓数据
	upShares     float64
	downShares   float64
	upAvgPrice   float64
	downAvgPrice float64

	// 利润数据
	upWinProfit   float64
	downWinProfit float64
	upCost        float64
	downCost      float64

	// UI状态
	width  int
	height int

	// 日志显示（最近3条）
	logs    []logMsg
	logChan chan logMsg // 日志消息channel
}

// NewUIModel 创建新的UI模型
func NewUIModel(tradingService *services.TradingService, marketSpec marketspec.MarketSpec, strategy *Strategy, ctx context.Context, logChan chan logMsg) uiModel {
	return uiModel{
		tradingService: tradingService,
		marketSpec:     marketSpec,
		strategy:       strategy,
		ctx:            ctx,
		logs:           make([]logMsg, 0, 3), // 最多保存3条日志
		logChan:        logChan,
	}
}

// checkCtxMsg 检查context是否已取消的消息
type checkCtxMsg struct{}

// Init 初始化，返回初始命令
func (m uiModel) Init() tea.Cmd {
	// 立即进入alt screen并等待窗口尺寸
	// 同时启动context检查定时器（每50ms检查一次，确保快速响应关闭信号）
	// 注意：tea.EnterAltScreen已经在tea.NewProgram中通过tea.WithAltScreen()设置了
	// 所以这里不需要再次调用
	return tea.Tick(50*time.Millisecond, func(t time.Time) tea.Msg {
		return checkCtxMsg{}
	})
}

// Update 处理消息并更新模型
func (m uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// 终端尺寸变化
		m.width = msg.Width
		m.height = msg.Height
		// 首次设置窗口尺寸时，标记为已初始化并立即刷新数据
		if !m.initialized && m.width > 0 {
			m.initialized = true
			// 立即刷新数据，并启动定时刷新
			m.refreshData()
			refreshInterval := time.Duration(m.strategy.Config.UIRefreshIntervalMs) * time.Millisecond
			if refreshInterval <= 0 {
				refreshInterval = time.Second // 默认1秒
			}
			return m, tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
				return tickMsg(t)
			})
		}
		return m, nil

	case checkCtxMsg:
		// 检查context是否已取消（每50ms检查一次，确保快速响应关闭信号）
		// 使用select方式检查，确保能够立即响应
		select {
		case <-m.ctx.Done():
			// context已取消，立即退出
			return m, tea.Quit
		default:
			// context未取消，继续检查（使用50ms间隔）
			return m, tea.Tick(50*time.Millisecond, func(t time.Time) tea.Msg {
				return checkCtxMsg{}
			})
		}

	case tickMsg:
		// 在每次tick时也检查context是否已取消
		select {
		case <-m.ctx.Done():
			// context已取消，立即退出
			return m, tea.Quit
		default:
		}

		// 处理日志消息（非阻塞）
		if m.logChan != nil {
			for {
				select {
				case log := <-m.logChan:
					// 添加新日志，保持最多3条
					m.logs = append(m.logs, log)
					if len(m.logs) > 3 {
						m.logs = m.logs[len(m.logs)-3:]
					}
				default:
					// 没有更多日志消息，退出循环
					goto doneLogs
				}
			}
		doneLogs:
		}
		
		// 定时更新数据
		// 检测周期切换：如果currentMarketSlug变化，立即刷新
		newMarketSlug := m.tradingService.GetCurrentMarket()
		if newMarketSlug != "" && newMarketSlug != m.lastMarketSlug && m.lastMarketSlug != "" {
			// 周期已切换，立即刷新数据
			m.refreshData()
		} else {
			// 正常定时刷新
			m.refreshData()
		}
		// 继续定时器
		refreshInterval := time.Duration(m.strategy.Config.UIRefreshIntervalMs) * time.Millisecond
		if refreshInterval <= 0 {
			refreshInterval = time.Second
		}
		return m, tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
			return tickMsg(t)
		})

	case tea.KeyMsg:
		// 按键处理
		switch msg.String() {
		case "q", "ctrl+c":
			// Ctrl+C时，返回Quit命令，让bubbletea正确退出
			return m, tea.Quit
		case "r":
			// 手动刷新
			m.refreshData()
			return m, nil
		}
	}

	// 检查 context 是否已取消（用于响应外部关闭信号）
	// 这是最后一道检查，确保任何情况下都能响应context取消
	// 使用非阻塞方式检查，避免阻塞UI更新
	if m.ctx.Err() != nil {
		return m, tea.Quit
	}

	return m, nil
}

// View 渲染UI
func (m uiModel) View() string {
	// 即使width == 0，也尝试显示基本数据（如果可用）
	// 这样可以避免一直显示"初始化中..."
	if m.width == 0 {
		// 尝试使用默认宽度（80）来渲染基本内容
		width := 80
		var sections []string
		sections = append(sections, m.renderHeaderWithWidth(width))
		sections = append(sections, m.renderPricesWithWidth(width))
		sections = append(sections, m.renderPositionsWithWidth(width))
		sections = append(sections, m.renderProfitWithWidth(width))
		sections = append(sections, m.renderLogsWithWidth(width))
		footer := lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Render(fmt.Sprintf("更新时间: %s | 按 'q' 退出 | 按 'r' 刷新 | 等待窗口初始化...",
				time.Now().Format("15:04:05")))
		sections = append(sections, footer)
		return lipgloss.JoinVertical(lipgloss.Left, sections...)
	}

	var sections []string

	// 头部：周期信息和倒计时
	sections = append(sections, m.renderHeader())

	// 价格信息
	sections = append(sections, m.renderPrices())

	// 持仓信息
	sections = append(sections, m.renderPositions())

	// 利润分析
	sections = append(sections, m.renderProfit())

	// 日志显示（底部3行）
	sections = append(sections, m.renderLogs())

	// 底部提示
	footer := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render(fmt.Sprintf("更新时间: %s | 按 'q' 退出 | 按 'r' 刷新",
			time.Now().Format("15:04:05")))

	sections = append(sections, footer)

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

// refreshData 刷新所有数据
func (m *uiModel) refreshData() {
	// 更新市场信息
	newMarketSlug := m.tradingService.GetCurrentMarket()
	
	// 检测周期切换
	cycleSwitched := false
	if newMarketSlug != "" && newMarketSlug != m.lastMarketSlug && m.lastMarketSlug != "" {
		cycleSwitched = true
	}
	
	m.currentMarketSlug = newMarketSlug
	m.lastMarketSlug = newMarketSlug
	
	// 如果市场为空，仍然尝试更新（可能显示"无"或默认值）
	// 如果周期切换或市场不为空，更新所有数据
	if cycleSwitched || m.currentMarketSlug != "" {
		m.updateCountdown()
		m.updatePrices()
		m.updatePositions()
		m.updateProfit()
	} else if m.currentMarketSlug == "" {
		// 市场为空时，至少更新倒计时（显示"--:--"）
		m.updateCountdown()
	}
}

// updateCountdown 更新倒计时
func (m *uiModel) updateCountdown() {
	if m.currentMarketSlug == "" {
		m.countdown = "--:--"
		return
	}

	timestamp, ok := m.marketSpec.TimestampFromSlug(m.currentMarketSlug, time.Now())
	if !ok || timestamp <= 0 {
		m.countdown = "--:--"
		return
	}

	cycleDuration := m.marketSpec.Duration()
	cycleEndTime := time.Unix(timestamp, 0).Add(cycleDuration)
	remaining := time.Until(cycleEndTime)

	if remaining <= 0 {
		m.countdown = "00:00"
	} else {
		minutes := int(remaining.Minutes())
		seconds := int(remaining.Seconds()) % 60
		m.countdown = fmt.Sprintf("%02d:%02d", minutes, seconds)
	}
}

// updatePrices 更新UP/DOWN价格
func (m *uiModel) updatePrices() {
	if m.currentMarketSlug == "" {
		return
	}

	// 优先使用WebSocket的BestBookSnapshot（内存读取，无网络延迟）
	snap, ok := m.tradingService.BestBookSnapshot()
	if ok {
		// 检查market是否匹配
		curMarket := m.tradingService.GetCurrentMarketInfo()
		if curMarket != nil && curMarket.Slug == m.currentMarketSlug {
			// 使用WebSocket快照数据（最快路径）
			m.upBid = float64(snap.YesBidPips) / 10000.0
			m.upAsk = float64(snap.YesAskPips) / 10000.0

			m.downBid = float64(snap.NoBidPips) / 10000.0
			m.downAsk = float64(snap.NoAskPips) / 10000.0

			// 从策略中获取速度（通过公开方法）
			if m.strategy != nil {
				m.upVelocity = m.strategy.GetVelocityForDisplay(domain.TokenTypeUp)
				m.downVelocity = m.strategy.GetVelocityForDisplay(domain.TokenTypeDown)
			}
			return // 成功获取，直接返回
		}
	}

	// WebSocket不可用时，回退到GetTopOfBook（但使用短超时，避免阻塞）
	positions := m.tradingService.GetOpenPositionsForMarket(m.currentMarketSlug)
	var market *domain.Market
	for _, p := range positions {
		if p != nil && p.Market != nil && p.Market.IsValid() {
			market = p.Market
			break
		}
	}

	// 如果从持仓中无法获取market，尝试从TradingService获取当前market
	if market == nil {
		if marketInfo := m.tradingService.GetCurrentMarketInfo(); marketInfo != nil && marketInfo.IsValid() {
			market = marketInfo
		} else {
			return
		}
	}

	// 使用短超时（500ms），避免阻塞UI更新
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	yesBid, yesAsk, noBid, noAsk, _, err := m.tradingService.GetTopOfBook(ctx, market)
	if err == nil {
		// UP价格数据
		m.upBid = yesBid.ToDecimal()
		m.upAsk = yesAsk.ToDecimal()

		// DOWN价格数据
		m.downBid = noBid.ToDecimal()
		m.downAsk = noAsk.ToDecimal()

		// 从策略中获取速度（通过公开方法）
		if m.strategy != nil {
			m.upVelocity = m.strategy.GetVelocityForDisplay(domain.TokenTypeUp)
			m.downVelocity = m.strategy.GetVelocityForDisplay(domain.TokenTypeDown)
		}
	}
	// 如果获取失败，保留上次的值（不更新）
}

// updatePositions 更新持仓数据
func (m *uiModel) updatePositions() {
	m.upShares = 0
	m.downShares = 0
	m.upAvgPrice = 0
	m.downAvgPrice = 0

	if m.currentMarketSlug == "" {
		return
	}

	positions := m.tradingService.GetOpenPositionsForMarket(m.currentMarketSlug)

	for _, pos := range positions {
		if pos == nil || !pos.IsOpen() || pos.Size <= 0 {
			continue
		}

		var avgPrice float64
		if pos.TotalFilledSize > 0 && pos.CostBasis > 0 {
			avgPrice = pos.CostBasis / pos.TotalFilledSize
		} else if pos.AvgPrice > 0 {
			avgPrice = pos.AvgPrice
		} else if pos.EntryPrice.Pips > 0 {
			avgPrice = pos.EntryPrice.ToDecimal()
		}

		switch pos.TokenType {
		case domain.TokenTypeUp:
			m.upShares += pos.Size
			if m.upAvgPrice == 0 {
				m.upAvgPrice = avgPrice
			} else {
				// 加权平均
				totalSize := m.upShares
				m.upAvgPrice = (m.upAvgPrice*(totalSize-pos.Size) + avgPrice*pos.Size) / totalSize
			}
		case domain.TokenTypeDown:
			m.downShares += pos.Size
			if m.downAvgPrice == 0 {
				m.downAvgPrice = avgPrice
			} else {
				// 加权平均
				totalSize := m.downShares
				m.downAvgPrice = (m.downAvgPrice*(totalSize-pos.Size) + avgPrice*pos.Size) / totalSize
			}
		}
	}
}

// updateProfit 更新利润数据
func (m *uiModel) updateProfit() {
	// 计算成本
	m.upCost = m.upAvgPrice * m.upShares
	m.downCost = m.downAvgPrice * m.downShares
	totalCost := m.upCost + m.downCost

	// 计算利润
	// 如果UP胜出：收益 = UP持仓 * $1 - 总成本
	// 如果DOWN胜出：收益 = DOWN持仓 * $1 - 总成本
	m.upWinProfit = m.upShares*1.0 - totalCost
	m.downWinProfit = m.downShares*1.0 - totalCost
}

// renderHeader 渲染头部
func (m uiModel) renderHeader() string {
	return m.renderHeaderWithWidth(m.width)
}

// renderHeaderWithWidth 使用指定宽度渲染头部
func (m uiModel) renderHeaderWithWidth(width int) string {
	if width <= 0 {
		width = 80
	}
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("63")).
		Background(lipgloss.Color("236")).
		Padding(0, 1).
		Width(width - 2)

	var header strings.Builder
	header.WriteString("📊 WinBet 策略监控\n")
	header.WriteString(strings.Repeat("─", width-2) + "\n")

	var infoParts []string
	if m.currentMarketSlug != "" {
		infoParts = append(infoParts, fmt.Sprintf("周期: %s", m.currentMarketSlug))
		infoParts = append(infoParts, fmt.Sprintf("剩余时间: %s", m.countdown))
	} else {
		infoParts = append(infoParts, "周期: 无")
		infoParts = append(infoParts, "剩余时间: --:--")
	}

	header.WriteString(strings.Join(infoParts, " | "))

	return headerStyle.Render(header.String())
}

// renderPrices 渲染价格信息
func (m uiModel) renderPrices() string {
	return m.renderPricesWithWidth(m.width)
}

// renderPricesWithWidth 使用指定宽度渲染价格信息
func (m uiModel) renderPricesWithWidth(width int) string {
	if width <= 0 {
		width = 80
	}
	title := titleStyle.Render("💰 实时价格")

	var content strings.Builder

	// UP价格信息
	upVelocityStr := "N/A"
	if m.upVelocity != 0 {
		if m.upVelocity > 0 {
			upVelocityStr = fmt.Sprintf("+%.3f c/s", m.upVelocity)
		} else {
			upVelocityStr = fmt.Sprintf("%.3f c/s", m.upVelocity)
		}
	}

	if m.upBid > 0 && m.upAsk > 0 {
		content.WriteString(fmt.Sprintf("UP:   bid=%.4f ask=%.4f velocity=%s\n",
			m.upBid, m.upAsk, upVelocityStr))
	} else {
		content.WriteString("UP:   bid=0.0000 ask=0.0000 velocity=N/A\n")
	}

	// DOWN价格信息
	downVelocityStr := "N/A"
	if m.downVelocity != 0 {
		if m.downVelocity > 0 {
			downVelocityStr = fmt.Sprintf("+%.3f c/s", m.downVelocity)
		} else {
			downVelocityStr = fmt.Sprintf("%.3f c/s", m.downVelocity)
		}
	}

	if m.downBid > 0 && m.downAsk > 0 {
		content.WriteString(fmt.Sprintf("DOWN: bid=%.4f ask=%.4f velocity=%s",
			m.downBid, m.downAsk, downVelocityStr))
	} else {
		content.WriteString("DOWN: bid=0.0000 ask=0.0000 velocity=N/A")
	}

	return borderStyle.Width(width - 4).Render(lipgloss.JoinVertical(lipgloss.Left, title, content.String()))
}

// renderPositions 渲染持仓信息
func (m uiModel) renderPositions() string {
	return m.renderPositionsWithWidth(m.width)
}

// renderPositionsWithWidth 使用指定宽度渲染持仓信息
func (m uiModel) renderPositionsWithWidth(width int) string {
	if width <= 0 {
		width = 80
	}
	title := titleStyle.Render("💼 持仓信息")

	var content strings.Builder
	if m.upShares > 0 {
		content.WriteString(fmt.Sprintf("UP持仓:   %.4f shares (均价: %.4f, 成本: %.4f USDC)\n",
			m.upShares, m.upAvgPrice, m.upCost))
	} else {
		content.WriteString("UP持仓:   0.0000 shares\n")
	}
	if m.downShares > 0 {
		content.WriteString(fmt.Sprintf("DOWN持仓: %.4f shares (均价: %.4f, 成本: %.4f USDC)\n",
			m.downShares, m.downAvgPrice, m.downCost))
	} else {
		content.WriteString("DOWN持仓: 0.0000 shares\n")
	}
	totalShares := m.upShares + m.downShares
	totalCost := m.upCost + m.downCost
	content.WriteString(fmt.Sprintf("总持仓:   %.4f shares (总成本: %.4f USDC)", totalShares, totalCost))

	return borderStyle.Width(width - 4).Render(lipgloss.JoinVertical(lipgloss.Left, title, content.String()))
}

// renderProfit 渲染利润分析
func (m uiModel) renderProfit() string {
	return m.renderProfitWithWidth(m.width)
}

// renderProfitWithWidth 使用指定宽度渲染利润分析
func (m uiModel) renderProfitWithWidth(width int) string {
	if width <= 0 {
		width = 80
	}
	title := titleStyle.Render("💰 利润分析")

	var content strings.Builder

	// UP胜出时的利润
	upProfitStyle := successStyle
	if m.upWinProfit < 0 {
		upProfitStyle = errorStyle
	}
	content.WriteString(fmt.Sprintf("UP胜出利润:   %s\n",
		upProfitStyle.Render(fmt.Sprintf("%.4f USDC", m.upWinProfit))))

	// DOWN胜出时的利润
	downProfitStyle := successStyle
	if m.downWinProfit < 0 {
		downProfitStyle = errorStyle
	}
	content.WriteString(fmt.Sprintf("DOWN胜出利润: %s\n",
		downProfitStyle.Render(fmt.Sprintf("%.4f USDC", m.downWinProfit))))

	// 最小利润（无论哪方胜出）
	minProfit := m.upWinProfit
	if m.downWinProfit < minProfit {
		minProfit = m.downWinProfit
	}
	minProfitStyle := successStyle
	if minProfit < 0 {
		minProfitStyle = errorStyle
	} else if minProfit == 0 {
		minProfitStyle = warningStyle
	}
	content.WriteString(fmt.Sprintf("最小利润:     %s",
		minProfitStyle.Render(fmt.Sprintf("%.4f USDC", minProfit))))

	return borderStyle.Width(width - 4).Render(lipgloss.JoinVertical(lipgloss.Left, title, content.String()))
}

// renderLogs 渲染日志信息
func (m uiModel) renderLogs() string {
	return m.renderLogsWithWidth(m.width)
}

// renderLogsWithWidth 使用指定宽度渲染日志信息
func (m uiModel) renderLogsWithWidth(width int) string {
	if width <= 0 {
		width = 80
	}
	title := titleStyle.Render("📋 实时日志")

	var content strings.Builder
	if len(m.logs) == 0 {
		content.WriteString("暂无日志")
	} else {
		// 显示最近3条日志（从新到旧，最新的在最后）
		start := 0
		if len(m.logs) > 3 {
			start = len(m.logs) - 3
		}
		for i := start; i < len(m.logs); i++ {
			log := m.logs[i]
			// 根据日志级别设置颜色
			var levelStyle lipgloss.Style
			switch log.level {
			case "ERROR":
				levelStyle = errorStyle
			case "WARN":
				levelStyle = warningStyle
			case "INFO":
				levelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
			case "DEBUG":
				levelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
			default:
				levelStyle = lipgloss.NewStyle()
			}

			// 格式化时间（只显示时分秒）
			timeStr := log.time.Format("15:04:05")
			// 限制消息长度以适应终端宽度（考虑边框和padding）
			maxMsgLen := width - 25 // 预留空间给时间戳、级别、边框等
			if maxMsgLen < 20 {
				maxMsgLen = 20 // 最小长度
			}
			msg := log.message
			if len(msg) > maxMsgLen {
				msg = msg[:maxMsgLen-3] + "..."
			}

			content.WriteString(fmt.Sprintf("[%s] %s: %s",
				timeStr,
				levelStyle.Render(log.level),
				msg))
			if i < len(m.logs)-1 {
				content.WriteString("\n")
			}
		}
	}

	return borderStyle.Width(width - 4).Render(lipgloss.JoinVertical(lipgloss.Left, title, content.String()))
}

// 样式定义
var (
	// 边框样式
	borderStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(0, 1)

	// 标题样式
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("63")).
			MarginBottom(1)

	// 成功样式（绿色）
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	// 警告样式（黄色）
	warningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	// 错误样式（红色）
	errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

// RunUI 运行UI（阻塞调用，直到UI退出或context取消）
func (s *Strategy) RunUI(ctx context.Context) error {
	if s.TradingService == nil {
		// 在重定向日志之前输出错误
		fmt.Fprintf(os.Stderr, "❌ [%s] UI启动失败: TradingService为nil\n", ID)
		return fmt.Errorf("TradingService为nil")
	}

	// 获取market spec
	gc := config.Get()
	if gc == nil {
		fmt.Fprintf(os.Stderr, "❌ [%s] UI启动失败: 全局配置为nil\n", ID)
		return fmt.Errorf("全局配置为nil")
	}
	sp, err := gc.Market.Spec()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ [%s] UI启动失败: 获取market spec失败: %v\n", ID, err)
		return fmt.Errorf("获取market spec失败: %w", err)
	}

	// 创建日志收集器（用于在UI中显示日志）
	logChan := make(chan logMsg, 100) // 缓冲100条日志
	logCollector := &logCollector{logChan: logChan}

	// 重定向所有日志输出到文件，避免干扰TUI显示
	// 保存原始的logrus输出和标准输出
	originalOutput := logrus.StandardLogger().Out
	originalLevel := logrus.GetLevel()
	originalStdout := os.Stdout
	originalStderr := os.Stderr
	
	// 重要：立即重定向日志，在UI启动之前就生效
	// 这样可以避免UI启动前的日志输出到终端

	// 创建日志文件
	logDir := "logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		logDir = os.TempDir()
	}
	logFile := filepath.Join(logDir, fmt.Sprintf("winbet_ui_%s.log", time.Now().Format("20060102_150405")))
	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err == nil {
		// 创建多写入器：同时写入文件和UI
		multiWriter := &multiWriter{
			file: file,
			hook: logCollector,
		}
		// 将logrus输出重定向到文件
		logrus.SetOutput(multiWriter)
		logrus.SetLevel(logrus.DebugLevel)
		logrus.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: "2006-01-02 15:04:05",
			DisableColors:   true, // 禁用颜色，因为写入文件
		})
		// 添加日志收集器hook（用于UI显示）
		logrus.StandardLogger().AddHook(logCollector) // logCollector 是 *logCollector 实例
		
		// 重定向标准输出和标准错误到文件（捕获fmt.Printf等）
		os.Stdout = file
		os.Stderr = file
		
		defer func() {
			// 先移除hook，防止继续向已关闭的channel发送
			// 保存logCollector的引用，用于后续比较
			logCollectorRef := logCollector
			
			// 先关闭channel，这样Fire方法中的recover会捕获panic
			close(logChan)
			
			// 移除hook：获取所有hooks，排除logCollector，然后替换
			originalHooks := logrus.StandardLogger().Hooks
			newHooks := make(logrus.LevelHooks)
			for level, hooks := range originalHooks {
				for _, hook := range hooks {
					// 通过比较指针地址来判断是否是同一个logCollector实例
					// 使用unsafe.Pointer进行指针比较
					if hook != logCollectorRef {
						newHooks[level] = append(newHooks[level], hook)
					}
				}
			}
			logrus.StandardLogger().ReplaceHooks(newHooks)
			
			// 恢复原始输出和级别
			logrus.SetOutput(originalOutput)
			logrus.SetLevel(originalLevel)
			os.Stdout = originalStdout
			os.Stderr = originalStderr
			file.Close()
		}()
		// 记录日志文件路径（写入文件，因为输出已重定向）
		logrus.Infof("✅ [%s] UI日志已重定向到文件: %s", ID, logFile)
		fmt.Fprintf(file, "✅ [%s] 正在启动UI...\n", ID)
	} else {
		// 如果无法创建日志文件，记录警告（但继续运行）
		// 注意：这里使用originalStderr，因为os.Stderr可能已经被重定向
		fmt.Fprintf(originalStderr, "⚠️ [%s] 无法创建UI日志文件: %v，日志将继续输出到终端\n", ID, err)
		logrus.Warnf("⚠️ [%s] 无法创建UI日志文件: %v，日志将继续输出到终端", ID, err)
		// 即使无法创建文件，也添加日志收集器hook
		logrus.StandardLogger().AddHook(logCollector)
		defer func() {
			// 保存logCollector的引用，用于后续比较
			logCollectorRef := logCollector
			
			// 先关闭channel，这样Fire方法中的recover会捕获panic
			close(logChan)
			
			// 移除hook：获取所有hooks，排除logCollector，然后替换
			originalHooks := logrus.StandardLogger().Hooks
			newHooks := make(logrus.LevelHooks)
			for level, hooks := range originalHooks {
				for _, hook := range hooks {
					// 通过比较指针地址来判断是否是同一个logCollector实例
					if hook != logCollectorRef {
						newHooks[level] = append(newHooks[level], hook)
					}
				}
			}
			logrus.StandardLogger().ReplaceHooks(newHooks)
		}()
	}

	// 启动UI，传递context以便响应取消信号
	model := NewUIModel(s.TradingService, sp, s, ctx, logChan)
	program := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithContext(ctx),
	)
	
	// 直接在主线程运行UI（阻塞调用）
	// 这样bubbletea能够正确捕获终端信号（Ctrl+C）
	// 注意：这会阻塞RunUI方法，但能够确保UI正确响应信号
	// 策略的Run方法在goroutine中运行，不会阻塞主程序
	if _, err := program.Run(); err != nil {
		// 错误信息会写入日志文件（如果已重定向）
		logrus.Errorf("❌ [%s] UI运行失败: %v", ID, err)
		return fmt.Errorf("UI运行失败: %w", err)
	}
	
	// 检查context是否已取消（虽然program.Run()已经退出，但检查一下）
	select {
	case <-ctx.Done():
		logrus.Infof("UI context已取消，UI已退出")
	default:
		logrus.Infof("UI正常退出")
	}

	// UI正常退出（用户按 'q' 退出或context取消）
	// 所有输出都已重定向到文件
	logrus.Infof("✅ [%s] UI已退出", ID)
	return nil
}

// GetVelocityForDisplay 获取速度用于显示（公开方法，供UI调用）
func (s *Strategy) GetVelocityForDisplay(token domain.TokenType) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	metrics := s.computeLocked(token)
	if !metrics.ok {
		return 0
	}
	return metrics.velocity
}
