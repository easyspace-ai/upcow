package rangeboth

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/marketspec"
)

// tickMsg 定时更新消息
type tickMsg time.Time

// dashboardModel Bubbletea模型
type dashboardModel struct {
	// 数据源
	tradingService *services.TradingService
	marketSpec     *marketspec.MarketSpec
	strategy       *Strategy

	// 状态数据
	currentMarketSlug string
	volatility         VolatilitySnapshot
	filledOrders       []*domain.Order
	pendingOrders      []*domain.Order
	profit             profitData

	// UI状态
	width  int
	height int
}

// profitData 收益数据
type profitData struct {
	UpShares      float64
	DownShares    float64
	UpCost        float64
	DownCost      float64
	TotalCost     float64
	ProfitIfUpWin float64
	ProfitIfDownWin float64
	MinProfit     float64
}

// NewDashboardModel 创建新的Dashboard模型
func NewDashboardModel(tradingService *services.TradingService, marketSpec *marketspec.MarketSpec, strategy *Strategy) dashboardModel {
	return dashboardModel{
		tradingService: tradingService,
		marketSpec:     marketSpec,
		strategy:       strategy,
	}
}

// Init 初始化，返回初始命令
func (m dashboardModel) Init() tea.Cmd {
	// 立即更新一次数据
	m.refreshData()
	// 启动定时器，每秒更新一次
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Update 处理消息并更新模型
func (m dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// 终端尺寸变化
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tickMsg:
		// 定时更新数据
		m.refreshData()
		// 继续定时器
		return m, tea.Tick(time.Second, func(t time.Time) tea.Msg {
			return tickMsg(t)
		})

	case tea.KeyMsg:
		// 按键处理（可选：添加交互功能）
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			// 手动刷新
			m.refreshData()
			return m, nil
		}
	}

	return m, nil
}

// View 渲染UI
func (m dashboardModel) View() string {
	if m.width == 0 {
		return "初始化中..."
	}

	var sections []string

	// 周期信息
	sections = append(sections, m.renderCycleInfo())

	// 波动幅度
	sections = append(sections, m.renderVolatility())

	// 已成交订单
	sections = append(sections, m.renderFilledOrders())

	// 未成交挂单
	sections = append(sections, m.renderPendingOrders())

	// 收益计算
	sections = append(sections, m.renderProfit())

	// 更新时间
	updateTime := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render(fmt.Sprintf("更新时间: %s | 按 'q' 退出 | 按 'r' 刷新",
			time.Now().Format("2006-01-02 15:04:05")))

	sections = append(sections, updateTime)

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

// refreshData 刷新所有数据
func (m *dashboardModel) refreshData() {
	// 更新市场信息
	m.currentMarketSlug = m.tradingService.GetCurrentMarket()

	// 更新波动幅度
	if m.strategy != nil {
		m.volatility = m.strategy.GetVolatilitySnapshot()
	}

	// 更新订单数据
	m.refreshOrders()

	// 更新收益数据
	m.refreshProfit()
}

// refreshOrders 刷新订单数据
func (m *dashboardModel) refreshOrders() {
	m.filledOrders = make([]*domain.Order, 0)
	m.pendingOrders = make([]*domain.Order, 0)

	allOrders := m.tradingService.GetActiveOrders()

	// 分类订单
	for _, order := range allOrders {
		if order == nil {
			continue
		}
		if m.currentMarketSlug != "" && order.MarketSlug != m.currentMarketSlug {
			continue
		}

		if order.Status == domain.OrderStatusPartial {
			m.filledOrders = append(m.filledOrders, order)
		} else if order.Status == domain.OrderStatusPending ||
			order.Status == domain.OrderStatusOpen {
			m.pendingOrders = append(m.pendingOrders, order)
		}
	}

	// 从持仓中提取已成交订单
	positions := m.tradingService.GetOpenPositionsForMarket(m.currentMarketSlug)
	for _, pos := range positions {
		if pos == nil {
			continue
		}
		if pos.EntryOrder != nil && pos.EntryOrder.IsFilled() {
			exists := false
			for _, o := range m.filledOrders {
				if o.OrderID == pos.EntryOrder.OrderID {
					exists = true
					break
				}
			}
			if !exists {
				m.filledOrders = append(m.filledOrders, pos.EntryOrder)
			}
		}
		if pos.HedgeOrder != nil && pos.HedgeOrder.IsFilled() {
			exists := false
			for _, o := range m.filledOrders {
				if o.OrderID == pos.HedgeOrder.OrderID {
					exists = true
					break
				}
			}
			if !exists {
				m.filledOrders = append(m.filledOrders, pos.HedgeOrder)
			}
		}
	}

	// 排序
	sort.Slice(m.filledOrders, func(i, j int) bool {
		if m.filledOrders[i].FilledAt == nil {
			return false
		}
		if m.filledOrders[j].FilledAt == nil {
			return true
		}
		return m.filledOrders[i].FilledAt.After(*m.filledOrders[j].FilledAt)
	})

	sort.Slice(m.pendingOrders, func(i, j int) bool {
		return m.pendingOrders[i].CreatedAt.After(m.pendingOrders[j].CreatedAt)
	})
}

// refreshProfit 刷新收益数据
func (m *dashboardModel) refreshProfit() {
	m.profit = profitData{}

	if m.currentMarketSlug == "" {
		return
	}

	positions := m.tradingService.GetOpenPositionsForMarket(m.currentMarketSlug)

	for _, pos := range positions {
		if pos == nil || !pos.IsOpen() {
			continue
		}

		if pos.TokenType == domain.TokenTypeUp {
			m.profit.UpShares += pos.TotalFilledSize
			if pos.CostBasis > 0 {
				m.profit.UpCost += pos.CostBasis
			} else if pos.EntryPrice.Pips > 0 && pos.TotalFilledSize > 0 {
				m.profit.UpCost += pos.EntryPrice.ToDecimal() * pos.TotalFilledSize
			}
		} else if pos.TokenType == domain.TokenTypeDown {
			m.profit.DownShares += pos.TotalFilledSize
			if pos.CostBasis > 0 {
				m.profit.DownCost += pos.CostBasis
			} else if pos.EntryPrice.Pips > 0 && pos.TotalFilledSize > 0 {
				m.profit.DownCost += pos.EntryPrice.ToDecimal() * pos.TotalFilledSize
			}
		}
	}

	if m.profit.UpShares == 0 && m.profit.DownShares == 0 {
		allOrders := m.tradingService.GetActiveOrders()
		for _, order := range allOrders {
			if order == nil || order.MarketSlug != m.currentMarketSlug {
				continue
			}
			if order.Status != domain.OrderStatusFilled {
				continue
			}

			if order.TokenType == domain.TokenTypeUp {
				m.profit.UpShares += order.FilledSize
				if order.FilledPrice != nil {
					m.profit.UpCost += order.FilledPrice.ToDecimal() * order.FilledSize
				} else if order.Price.Pips > 0 {
					m.profit.UpCost += order.Price.ToDecimal() * order.FilledSize
				}
			} else if order.TokenType == domain.TokenTypeDown {
				m.profit.DownShares += order.FilledSize
				if order.FilledPrice != nil {
					m.profit.DownCost += order.FilledPrice.ToDecimal() * order.FilledSize
				} else if order.Price.Pips > 0 {
					m.profit.DownCost += order.Price.ToDecimal() * order.FilledSize
				}
			}
		}
	}

	m.profit.TotalCost = m.profit.UpCost + m.profit.DownCost
	m.profit.ProfitIfUpWin = m.profit.UpShares*1.0 - m.profit.TotalCost
	m.profit.ProfitIfDownWin = m.profit.DownShares*1.0 - m.profit.TotalCost
	m.profit.MinProfit = m.profit.ProfitIfUpWin
	if m.profit.ProfitIfDownWin < m.profit.ProfitIfUpWin {
		m.profit.MinProfit = m.profit.ProfitIfDownWin
	}
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
	// 信息样式（蓝色）
	infoStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
)

// renderCycleInfo 渲染周期信息
func (m dashboardModel) renderCycleInfo() string {
	title := titleStyle.Render("📊 实时交易监控面板")
	
	var content strings.Builder
	
	if m.currentMarketSlug == "" {
		content.WriteString("当前周期: 无\n")
		content.WriteString("剩余时间: --")
	} else {
		content.WriteString(fmt.Sprintf("当前周期: %s\n", m.currentMarketSlug))
		
		var remainingTime string
		if m.marketSpec != nil {
			timestamp, ok := m.marketSpec.TimestampFromSlug(m.currentMarketSlug, time.Now())
			if ok && timestamp > 0 {
				cycleDuration := m.marketSpec.Duration()
				cycleEndTime := time.Unix(timestamp, 0).Add(cycleDuration)
				remaining := cycleEndTime.Sub(time.Now())
				
				if remaining <= 0 {
					remainingTime = "周期已结束"
				} else {
					minutes := int(remaining.Minutes())
					seconds := int(remaining.Seconds()) % 60
					remainingTime = fmt.Sprintf("%02d:%02d", minutes, seconds)
				}
			} else {
				remainingTime = "计算中..."
			}
		} else {
			remainingTime = "计算中..."
		}
		
		content.WriteString(fmt.Sprintf("剩余时间: %s", remainingTime))
	}
	
	return borderStyle.Render(lipgloss.JoinVertical(lipgloss.Left, title, content.String()))
}

// renderVolatility 渲染波动幅度
func (m dashboardModel) renderVolatility() string {
	title := titleStyle.Render("📊 波动幅度监控")
	
	var content strings.Builder
	
	if m.strategy == nil {
		content.WriteString("策略未初始化，无法获取波动数据")
		return borderStyle.Render(lipgloss.JoinVertical(lipgloss.Left, title, content.String()))
	}
	
	snapshot := m.volatility
	
	content.WriteString(fmt.Sprintf("观察窗口: %d秒 | 最大允许波动: %d分\n", 
		snapshot.LookbackSeconds, snapshot.MaxRangeCents))
	content.WriteString(strings.Repeat("─", 50) + "\n")
	
	// UP方向
	if snapshot.SampleCountUp > 0 {
		upStatus := "❌ 不稳定"
		upStatusStyle := errorStyle
		if snapshot.UpStable {
			upStatus = "✅ 稳定"
			upStatusStyle = successStyle
		}
		content.WriteString(fmt.Sprintf("UP方向:   样本数=%d | 价格范围: %d-%d分 | 波动幅度: %d分 | %s\n",
			snapshot.SampleCountUp,
			snapshot.UpMinCents,
			snapshot.UpMaxCents,
			snapshot.UpRangeCents,
			upStatusStyle.Render(upStatus)))
	} else {
		content.WriteString("UP方向:   暂无数据\n")
	}
	
	// DOWN方向
	if snapshot.SampleCountDown > 0 {
		downStatus := "❌ 不稳定"
		downStatusStyle := errorStyle
		if snapshot.DownStable {
			downStatus = "✅ 稳定"
			downStatusStyle = successStyle
		}
		content.WriteString(fmt.Sprintf("DOWN方向: 样本数=%d | 价格范围: %d-%d分 | 波动幅度: %d分 | %s\n",
			snapshot.SampleCountDown,
			snapshot.DownMinCents,
			snapshot.DownMaxCents,
			snapshot.DownRangeCents,
			downStatusStyle.Render(downStatus)))
	} else {
		content.WriteString("DOWN方向: 暂无数据\n")
	}
	
	// 整体状态
	content.WriteString(strings.Repeat("─", 50) + "\n")
	overallStatus := "❌ 不满足条件"
	overallStyle := errorStyle
	if snapshot.UpStable && snapshot.DownStable {
		overallStatus = "✅ 满足条件，可以下单"
		overallStyle = successStyle
	} else if snapshot.UpStable || snapshot.DownStable {
		overallStatus = "⚠️  仅单边满足条件"
		overallStyle = warningStyle
	}
	content.WriteString(fmt.Sprintf("整体状态: %s", overallStyle.Render(overallStatus)))
	
	return borderStyle.Render(lipgloss.JoinVertical(lipgloss.Left, title, content.String()))
}

// renderFilledOrders 渲染已成交订单
func (m dashboardModel) renderFilledOrders() string {
	title := titleStyle.Render("✅ 已成交订单")
	
	var content strings.Builder
	
	if len(m.filledOrders) == 0 {
		content.WriteString("暂无已成交订单")
	} else {
		content.WriteString("订单ID          │ 方向 │ 价格(分) │ 数量    │ 成交时间\n")
		content.WriteString(strings.Repeat("─", 50) + "\n")
		
		maxDisplay := len(m.filledOrders)
		if maxDisplay > 10 {
			maxDisplay = 10
		}
		
		for i := 0; i < maxDisplay; i++ {
			order := m.filledOrders[i]
			orderID := order.OrderID
			if len(orderID) > 15 {
				orderID = orderID[:12] + "..."
			}
			
			tokenType := "UP"
			if order.TokenType == domain.TokenTypeDown {
				tokenType = "DOWN"
			}
			
			price := "0"
			if order.FilledPrice != nil {
				price = fmt.Sprintf("%d", order.FilledPrice.ToCents())
			} else if order.Price.Pips > 0 {
				price = fmt.Sprintf("%d", order.Price.ToCents())
			}
			
			size := fmt.Sprintf("%.4f", order.FilledSize)
			
			filledTime := "未知"
			if order.FilledAt != nil {
				filledTime = order.FilledAt.Format("15:04:05")
			}
			
			content.WriteString(fmt.Sprintf("%-15s │ %-4s │ %-8s │ %-7s │ %s\n",
				orderID, tokenType, price, size, filledTime))
		}
		
		if len(m.filledOrders) > 10 {
			content.WriteString(fmt.Sprintf("... 还有 %d 条订单未显示", len(m.filledOrders)-10))
		}
	}
	
	return borderStyle.Render(lipgloss.JoinVertical(lipgloss.Left, title, content.String()))
}

// renderPendingOrders 渲染未成交挂单
func (m dashboardModel) renderPendingOrders() string {
	title := titleStyle.Render("⏳ 未成交挂单")
	
	var content strings.Builder
	
	if len(m.pendingOrders) == 0 {
		content.WriteString("暂无未成交挂单")
	} else {
		content.WriteString("订单ID          │ 方向 │ 价格(分) │ 数量    │ 状态    │ 创建时间\n")
		content.WriteString(strings.Repeat("─", 50) + "\n")
		
		for _, order := range m.pendingOrders {
			orderID := order.OrderID
			if len(orderID) > 15 {
				orderID = orderID[:12] + "..."
			}
			
			tokenType := "UP"
			if order.TokenType == domain.TokenTypeDown {
				tokenType = "DOWN"
			}
			
			price := "0"
			if order.Price.Pips > 0 {
				price = fmt.Sprintf("%d", order.Price.ToCents())
			}
			
			size := fmt.Sprintf("%.4f", order.Size)
			status := string(order.Status)
			createdTime := order.CreatedAt.Format("15:04:05")
			
			content.WriteString(fmt.Sprintf("%-15s │ %-4s │ %-8s │ %-7s │ %-8s │ %s\n",
				orderID, tokenType, price, size, status, createdTime))
		}
	}
	
	return borderStyle.Render(lipgloss.JoinVertical(lipgloss.Left, title, content.String()))
}

// renderProfit 渲染收益计算
func (m dashboardModel) renderProfit() string {
	title := titleStyle.Render("💰 收益计算")
	
	var content strings.Builder
	
	if m.currentMarketSlug == "" {
		content.WriteString("当前周期: 无，无法计算收益")
		return borderStyle.Render(lipgloss.JoinVertical(lipgloss.Left, title, content.String()))
	}
	
	content.WriteString(fmt.Sprintf("UP持仓:   %-10.4f 成本: $%-10.4f\n", 
		m.profit.UpShares, m.profit.UpCost))
	content.WriteString(fmt.Sprintf("DOWN持仓: %-10.4f 成本: $%-10.4f\n", 
		m.profit.DownShares, m.profit.DownCost))
	content.WriteString(fmt.Sprintf("总成本:   $%-10.4f\n", m.profit.TotalCost))
	content.WriteString(strings.Repeat("─", 50) + "\n")
	
	// UP获胜收益
	upWinColor := successStyle
	if m.profit.ProfitIfUpWin < 0 {
		upWinColor = errorStyle
	}
	content.WriteString(fmt.Sprintf("如果UP获胜:   %s\n", 
		upWinColor.Render(fmt.Sprintf("$%.4f", m.profit.ProfitIfUpWin))))
	
	// DOWN获胜收益
	downWinColor := successStyle
	if m.profit.ProfitIfDownWin < 0 {
		downWinColor = errorStyle
	}
	content.WriteString(fmt.Sprintf("如果DOWN获胜: %s\n", 
		downWinColor.Render(fmt.Sprintf("$%.4f", m.profit.ProfitIfDownWin))))
	
	// 最小收益
	minProfitColor := successStyle
	if m.profit.MinProfit < 0 {
		minProfitColor = errorStyle
	}
	content.WriteString(fmt.Sprintf("最小收益:     %s (无论哪方获胜)", 
		minProfitColor.Render(fmt.Sprintf("$%.4f", m.profit.MinProfit))))
	
	return borderStyle.Render(lipgloss.JoinVertical(lipgloss.Left, title, content.String()))
}

// RunDashboard 运行Dashboard（在goroutine中）
// 注意：这个函数会阻塞，应该在独立的goroutine中调用
func RunDashboard(tradingService *services.TradingService, marketSpec *marketspec.MarketSpec, strategy *Strategy) error {
	model := NewDashboardModel(tradingService, marketSpec, strategy)
	// 使用AltScreen模式，提供更好的全屏体验
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return err
	}
	return nil
}
