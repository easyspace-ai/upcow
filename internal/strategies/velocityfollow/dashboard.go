package velocityfollow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/config"
	"github.com/betbot/gobet/pkg/marketspec"
	"github.com/sirupsen/logrus"
)

// tickMsg 定时更新消息
type tickMsg time.Time

// dashboardModel Bubbletea模型
type dashboardModel struct {
	// 数据源
	tradingService   *services.TradingService
	binanceKlines    *services.BinanceFuturesKlines
	marketSpec       marketspec.MarketSpec
	strategy         *Strategy

	// 状态数据
	currentMarketSlug string
	countdown          string
	btcTargetPrice     float64
	btcRealtimePrice   float64
	
	// UP/DOWN 价格数据
	upBid              float64
	upAsk              float64
	upSpread           float64
	upVelocity         float64
	downBid            float64
	downAsk            float64
	downSpread         float64
	downVelocity       float64
	
	// 智能决策中心数据
	decisionCenter     decisionCenterData
	
	// 旧数据（保留用于兼容）
	openOrdersCount    int
	positionsData      positionsData
	arbitrageAnalysis  *ArbitrageAnalysis
	completedTrades    int
	riskExposure       string

	// UI状态
	width  int
	height int
}

// positionsData 持仓数据
type positionsData struct {
	UpShares      float64
	DownShares    float64
	UpAvgPrice    float64
	DownAvgPrice  float64
	TotalSize     float64
}

// decisionCenterData 智能决策中心数据
type decisionCenterData struct {
	// 开单统计
	UpOrderCount      int     // UP方向开单数量
	DownOrderCount    int     // DOWN方向开单数量
	TotalOrderCount   int     // 总开单数量
	CompletedPairs    int     // 已完成交易对数量
	
	// 持仓信息
	UpShares          float64 // UP持仓数量
	DownShares        float64 // DOWN持仓数量
	UpAvgPrice        float64 // UP平均价格
	DownAvgPrice      float64 // DOWN平均价格
	TotalAvgPrice     float64 // 总持仓均价（加权平均）
	
	// 利润分析
	ProfitIfUpWins    float64 // UP胜出时的利润
	ProfitIfDownWins  float64 // DOWN胜出时的利润
	MinProfit         float64 // 最小利润（无论哪方胜出）
	IsPerfectArbitrage bool  // 是否达到完美套利
	
	// 风险敞口
	RiskExposure      string  // 风险敞口描述
	ExposureSeconds   float64 // 最大敞口时间（秒）
	
	// 开单计划
	HasPlan           bool    // 是否有开单计划
	PlanDirection     string  // 计划方向："UP" 或 "DOWN"
	PlanEntrySize     float64 // 计划Entry订单大小
	PlanHedgeSize     float64 // 计划Hedge订单大小
	PlanEntryPrice    float64 // 计划Entry价格
	PlanHedgePrice    float64 // 计划Hedge价格
	PlanAfterUpProfit float64 // 计划执行后UP胜出的利润
	PlanAfterDownProfit float64 // 计划执行后DOWN胜出的利润
	PlanAfterMinProfit float64 // 计划执行后的最小利润
	PlanReason        string  // 计划原因
	
	// 状态机条件
	StateMachine      stateMachineData // 状态机条件检查结果
}

// stateMachineData 状态机条件数据
type stateMachineData struct {
	// 基础条件
	MarketValid       bool    // 市场是否有效
	BiasReady         bool    // Bias是否就绪
	WarmupPassed      bool    // 是否通过预热期
	CycleEndProtected bool    // 是否在周期结束保护期内
	TradesLimitOK     bool    // 交易次数限制是否OK
	NoPendingHedge    bool    // 是否有未完成的对冲单
	CooldownPassed    bool    // 是否通过冷却期
	
	// UP方向条件
	UpAllowed         bool    // UP是否被允许（bias检查）
	UpVelocityOK      bool    // UP速度计算是否成功
	UpDeltaOK          bool    // UP位移是否满足
	UpVelocityValue   float64 // UP速度值
	UpDeltaValue      float64 // UP位移值
	UpVelocityRequired float64 // UP所需速度
	UpDeltaRequired   int     // UP所需位移
	
	// DOWN方向条件
	DownAllowed       bool    // DOWN是否被允许（bias检查）
	DownVelocityOK    bool    // DOWN速度计算是否成功
	DownDeltaOK       bool    // DOWN位移是否满足
	DownVelocityValue float64 // DOWN速度值
	DownDeltaValue    float64 // DOWN位移值
	DownVelocityRequired float64 // DOWN所需速度
	DownDeltaRequired int     // DOWN所需位移
	
	// 最终选择
	Winner            string  // 最终选择的交易方向（"UP"/"DOWN"/""）
	WinnerReason      string  // 选择原因
	
	// 其他检查（在下单前）
	MarketQualityOK   bool    // 市场质量是否OK
	PriceRangeOK      bool    // 价格范围是否OK
	SpreadOK          bool    // 价差是否OK
	SideCooldownOK    bool    // 方向冷却期是否OK
	InventoryOK       bool    // 库存偏斜检查是否OK
}

// NewDashboardModel 创建新的Dashboard模型
func NewDashboardModel(tradingService *services.TradingService, binanceKlines *services.BinanceFuturesKlines, marketSpec marketspec.MarketSpec, strategy *Strategy) dashboardModel {
	return dashboardModel{
		tradingService: tradingService,
		binanceKlines:  binanceKlines,
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
		// 按键处理
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

	// 头部：周期信息和BTC价格
	sections = append(sections, m.renderHeader())

	// 价格信息
	sections = append(sections, m.renderPrices())

	// 智能决策中心（整合：交易统计、持仓、利润、风险、开单计划）
	sections = append(sections, m.renderDecisionCenter())

	// 底部提示
	footer := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render(fmt.Sprintf("更新时间: %s | 按 'q' 退出 | 按 'r' 刷新",
			time.Now().Format("15:04:05")))

	sections = append(sections, footer)

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

// refreshData 刷新所有数据
func (m *dashboardModel) refreshData() {
	// 更新市场信息
	m.currentMarketSlug = m.tradingService.GetCurrentMarket()
	m.updateCountdown()
	m.updateBTCPrices()
	m.updatePrices()
	m.updateDecisionCenter() // 更新智能决策中心（整合所有数据）
}

// updateCountdown 更新倒计时
func (m *dashboardModel) updateCountdown() {
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

// updateBTCPrices 更新BTC价格
func (m *dashboardModel) updateBTCPrices() {
	// 获取BTC实时价格
	if m.binanceKlines != nil {
		if kline, ok := m.binanceKlines.Latest("1s"); ok && kline.Close > 0 {
			m.btcRealtimePrice = kline.Close
		}
	}

	// 获取BTC目标价（周期开始时的价格，作为目标价）
	if m.currentMarketSlug != "" && m.binanceKlines != nil {
		timestamp, ok := m.marketSpec.TimestampFromSlug(m.currentMarketSlug, time.Now())
		if ok && timestamp > 0 {
			// 获取周期开始时的1m K线（作为目标价）
			cycleStartMs := timestamp * 1000
			if kline, ok := m.binanceKlines.NearestAtOrBefore("1m", cycleStartMs); ok && kline.Open > 0 {
				m.btcTargetPrice = kline.Open
			} else if m.btcRealtimePrice > 0 {
				// 如果无法获取周期开始价格，使用实时价格作为占位
				m.btcTargetPrice = m.btcRealtimePrice
			}
		} else if m.btcRealtimePrice > 0 {
			// 如果无法解析周期，使用实时价格作为占位
			m.btcTargetPrice = m.btcRealtimePrice
		}
	}
}

// updatePrices 更新UP/DOWN价格（优化：优先使用WebSocket缓存，避免频繁API调用）
func (m *dashboardModel) updatePrices() {
	if m.currentMarketSlug == "" {
		return
	}

	// 优化1: 优先使用WebSocket的BestBookSnapshot（内存读取，无网络延迟）
	snap, ok := m.tradingService.BestBookSnapshot()
	if ok {
		// 检查market是否匹配
		curMarket := m.tradingService.GetCurrentMarketInfo()
		if curMarket != nil && curMarket.Slug == m.currentMarketSlug {
			// 使用WebSocket快照数据（最快路径）
			m.upBid = float64(snap.YesBidPips) / 10000.0
			m.upAsk = float64(snap.YesAskPips) / 10000.0
			m.upSpread = m.upAsk - m.upBid
			
			m.downBid = float64(snap.NoBidPips) / 10000.0
			m.downAsk = float64(snap.NoAskPips) / 10000.0
			m.downSpread = m.downAsk - m.downBid
			
			// 从策略中获取速度（通过公开方法）
			if m.strategy != nil {
				m.upVelocity = m.strategy.GetVelocityForDisplay(domain.TokenTypeUp)
				m.downVelocity = m.strategy.GetVelocityForDisplay(domain.TokenTypeDown)
			}
			return // 成功获取，直接返回
		}
	}

	// 优化2: WebSocket不可用时，回退到GetTopOfBook（但使用短超时，避免阻塞）
	// 从持仓中获取market对象（优先）
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
		m.upSpread = m.upAsk - m.upBid
		
		// DOWN价格数据
		m.downBid = noBid.ToDecimal()
		m.downAsk = noAsk.ToDecimal()
		m.downSpread = m.downAsk - m.downBid
		
		// 从策略中获取速度（通过公开方法）
		if m.strategy != nil {
			m.upVelocity = m.strategy.GetVelocityForDisplay(domain.TokenTypeUp)
			m.downVelocity = m.strategy.GetVelocityForDisplay(domain.TokenTypeDown)
		}
	}
	// 如果获取失败，保留上次的值（不更新）
}

// updateTradingStats 更新交易统计
func (m *dashboardModel) updateTradingStats() {
	// 计算开单数量（活跃订单）
	activeOrders := m.tradingService.GetActiveOrders()
	m.openOrdersCount = 0
	for _, order := range activeOrders {
		if order == nil {
			continue
		}
		if m.currentMarketSlug == "" || order.MarketSlug == m.currentMarketSlug {
			if order.Status == domain.OrderStatusOpen || order.Status == domain.OrderStatusPending {
				m.openOrdersCount++
			}
		}
	}

	// 计算已完成的交易对数量（Entry+Hedge都成交的）
	positions := m.tradingService.GetOpenPositionsForMarket(m.currentMarketSlug)
	completedPairs := 0
	for _, pos := range positions {
		if pos == nil || !pos.IsOpen() {
			continue
		}
		if pos.IsHedged() {
			completedPairs++
		}
	}
	m.completedTrades = completedPairs
}

// updatePositions 更新持仓数据
func (m *dashboardModel) updatePositions() {
	m.positionsData = positionsData{}

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
			m.positionsData.UpShares += pos.Size
			if m.positionsData.UpAvgPrice == 0 {
				m.positionsData.UpAvgPrice = avgPrice
			} else {
				// 加权平均
				totalSize := m.positionsData.UpShares
				m.positionsData.UpAvgPrice = (m.positionsData.UpAvgPrice*(totalSize-pos.Size) + avgPrice*pos.Size) / totalSize
			}
		case domain.TokenTypeDown:
			m.positionsData.DownShares += pos.Size
			if m.positionsData.DownAvgPrice == 0 {
				m.positionsData.DownAvgPrice = avgPrice
			} else {
				// 加权平均
				totalSize := m.positionsData.DownShares
				m.positionsData.DownAvgPrice = (m.positionsData.DownAvgPrice*(totalSize-pos.Size) + avgPrice*pos.Size) / totalSize
			}
		}
	}

	m.positionsData.TotalSize = m.positionsData.UpShares + m.positionsData.DownShares
}

// updateArbitrage 更新套利分析
func (m *dashboardModel) updateArbitrage() {
	if m.strategy != nil && m.strategy.arbitrageBrain != nil && m.currentMarketSlug != "" {
		// 从持仓中获取market对象（优先）
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
			}
		}

		if market != nil {
			m.arbitrageAnalysis = m.strategy.arbitrageBrain.AnalyzeMarket(m.currentMarketSlug, market)
		} else {
			// 如果无法获取market，清空分析结果
			m.arbitrageAnalysis = nil
		}
	}
}

// updateRiskExposure 更新风险敞口
func (m *dashboardModel) updateRiskExposure() {
	if m.strategy != nil && m.strategy.riskManager != nil {
		exposures := m.strategy.riskManager.GetExposures()
		if len(exposures) == 0 {
			m.riskExposure = "无风险敞口"
		} else {
			var parts []string
			for _, exp := range exposures {
				parts = append(parts, fmt.Sprintf("Entry=%s 敞口=%.1fs", exp.EntryOrderID, exp.ExposureSeconds))
			}
			m.riskExposure = strings.Join(parts, " | ")
		}
	} else {
		m.riskExposure = "风险管理系统未启用"
	}
}

// updateStateMachine 更新状态机条件
func (m *dashboardModel) updateStateMachine() {
	if m.strategy == nil {
		return
	}
	
	status := m.strategy.GetStateMachineStatus()
	if status == nil {
		return
	}
	
	sm := &m.decisionCenter.StateMachine
	
	// 基础条件
	sm.MarketValid = status.MarketValid
	sm.BiasReady = status.BiasReady
	sm.WarmupPassed = status.WarmupPassed
	sm.CycleEndProtected = status.CycleEndProtected
	sm.TradesLimitOK = status.TradesLimitOK
	sm.NoPendingHedge = status.NoPendingHedge
	sm.CooldownPassed = status.CooldownPassed
	
	// UP方向条件
	sm.UpAllowed = status.UpAllowed
	sm.UpVelocityOK = status.UpVelocityOK
	sm.UpDeltaOK = status.UpDeltaOK
	sm.UpVelocityValue = status.UpVelocityValue
	sm.UpDeltaValue = status.UpDeltaValue
	sm.UpVelocityRequired = status.UpVelocityRequired
	sm.UpDeltaRequired = status.UpDeltaRequired
	
	// DOWN方向条件
	sm.DownAllowed = status.DownAllowed
	sm.DownVelocityOK = status.DownVelocityOK
	sm.DownDeltaOK = status.DownDeltaOK
	sm.DownVelocityValue = status.DownVelocityValue
	sm.DownDeltaValue = status.DownDeltaValue
	sm.DownVelocityRequired = status.DownVelocityRequired
	sm.DownDeltaRequired = status.DownDeltaRequired
	
	// 最终选择
	sm.Winner = status.Winner
	sm.WinnerReason = status.WinnerReason
	
	// 其他检查
	sm.MarketQualityOK = status.MarketQualityOK
	sm.PriceRangeOK = status.PriceRangeOK
	sm.SpreadOK = status.SpreadOK
	sm.SideCooldownOK = status.SideCooldownOK
	sm.InventoryOK = status.InventoryOK
}

// updateDecisionCenter 更新智能决策中心数据（整合所有信息并计算开单计划）
func (m *dashboardModel) updateDecisionCenter() {
	dc := &m.decisionCenter
	*dc = decisionCenterData{} // 重置

	if m.currentMarketSlug == "" {
		return
	}

	// 1. 更新持仓信息
	m.updatePositions()
	dc.UpShares = m.positionsData.UpShares
	dc.DownShares = m.positionsData.DownShares
	dc.UpAvgPrice = m.positionsData.UpAvgPrice
	dc.DownAvgPrice = m.positionsData.DownAvgPrice
	
	// 计算总持仓均价（加权平均）
	if dc.UpShares > 0 || dc.DownShares > 0 {
		totalCost := dc.UpAvgPrice*dc.UpShares + dc.DownAvgPrice*dc.DownShares
		totalSize := dc.UpShares + dc.DownShares
		if totalSize > 0 {
			dc.TotalAvgPrice = totalCost / totalSize
		}
	}

	// 2. 统计开单数量（按方向分别统计）
	// 优先方法：从持仓量反推开单数量（最准确，因为持仓反映了实际成交）
	// 备用方法：从订单中统计（作为验证）
	
	// 获取策略配置的订单大小
	orderSize := 5.0 // 默认值
	if m.strategy != nil && m.strategy.Config.OrderSize > 0 {
		orderSize = m.strategy.Config.OrderSize
	}
	
	// 方法1：从持仓量反推开单数量（最准确）
	// 开单数 = 持仓量 / 每单大小
	if orderSize > 0 {
		if dc.UpShares > 0 {
			dc.UpOrderCount = int(dc.UpShares / orderSize + 0.5) // 四舍五入
		}
		if dc.DownShares > 0 {
			dc.DownOrderCount = int(dc.DownShares / orderSize + 0.5) // 四舍五入
		}
		dc.TotalOrderCount = dc.UpOrderCount + dc.DownOrderCount
	} else {
		// 如果无法获取订单大小，从订单中统计
		allOrders := m.tradingService.GetAllOrders()
		entryOrdersSeen := make(map[string]bool) // 用于去重
		
		for _, order := range allOrders {
			if order == nil {
				continue
			}
			// 只统计当前市场的订单
			if m.currentMarketSlug != "" && order.MarketSlug != m.currentMarketSlug {
				continue
			}
			
			// 统计所有已成交的Entry订单
			if order.IsEntryOrder && 
			   (order.Status == domain.OrderStatusFilled || order.Status == domain.OrderStatusPartial) {
				if !entryOrdersSeen[order.OrderID] {
					entryOrdersSeen[order.OrderID] = true
					if order.TokenType == domain.TokenTypeUp {
						dc.UpOrderCount++
					} else if order.TokenType == domain.TokenTypeDown {
						dc.DownOrderCount++
					}
					dc.TotalOrderCount++
				}
			}
		}
		
		// 如果从订单统计不到，但从持仓有数据，则从持仓推断最小开单数量
		if dc.UpOrderCount == 0 && dc.UpShares > 0 {
			dc.UpOrderCount = 1
			if dc.TotalOrderCount == 0 {
				dc.TotalOrderCount = 1
			}
		}
		if dc.DownOrderCount == 0 && dc.DownShares > 0 {
			dc.DownOrderCount = 1
			if dc.TotalOrderCount == 0 {
				dc.TotalOrderCount = 1
			} else if dc.UpOrderCount == 0 {
				dc.TotalOrderCount = 1
			}
		}
	}

	// 3. 计算已完成交易对数量
	positions := m.tradingService.GetOpenPositionsForMarket(m.currentMarketSlug)
	dc.CompletedPairs = 0
	for _, pos := range positions {
		if pos != nil && pos.IsOpen() && pos.IsHedged() {
			dc.CompletedPairs++
		}
	}

	// 4. 更新套利分析（获取利润信息）
	m.updateArbitrage()
	if m.arbitrageAnalysis != nil {
		dc.ProfitIfUpWins = m.arbitrageAnalysis.ProfitIfUpWins
		dc.ProfitIfDownWins = m.arbitrageAnalysis.ProfitIfDownWins
		dc.MinProfit = m.arbitrageAnalysis.MinProfit
		dc.IsPerfectArbitrage = m.arbitrageAnalysis.IsPerfectArbitrage
	}

	// 5. 更新风险敞口
	m.updateRiskExposure()
	dc.RiskExposure = m.riskExposure
	if m.strategy != nil && m.strategy.riskManager != nil {
		exposures := m.strategy.riskManager.GetExposures()
		maxExposure := 0.0
		for _, exp := range exposures {
			if exp.ExposureSeconds > maxExposure {
				maxExposure = exp.ExposureSeconds
			}
		}
		dc.ExposureSeconds = maxExposure
	}

	// 6. 更新状态机条件
	m.updateStateMachine()
	dc.StateMachine = m.decisionCenter.StateMachine
	
	// 7. 计算开单计划（如果未达到完美套利）
	if !dc.IsPerfectArbitrage && m.strategy != nil {
		m.calculateOrderPlan(dc)
	}
}

// calculateOrderPlan 计算开单计划，使下一对订单成功后能达到完美套利
func (m *dashboardModel) calculateOrderPlan(dc *decisionCenterData) {
	if m.currentMarketSlug == "" || m.strategy == nil {
		return
	}

	// 获取当前市场价格
	if m.upBid <= 0 || m.upAsk <= 0 || m.downBid <= 0 || m.downAsk <= 0 {
		return
	}

	// 获取策略配置
	orderSize := m.strategy.Config.OrderSize
	hedgeSize := m.strategy.Config.HedgeOrderSize
	if hedgeSize <= 0 {
		hedgeSize = orderSize
	}
	hedgeOffsetCents := m.strategy.Config.HedgeOffsetCents
	if hedgeOffsetCents <= 0 {
		hedgeOffsetCents = 3
	}

	// 当前持仓和成本
	currentUpShares := dc.UpShares
	currentDownShares := dc.DownShares
	currentUpCost := dc.UpAvgPrice * currentUpShares
	currentDownCost := dc.DownAvgPrice * currentDownShares
	currentTotalCost := currentUpCost + currentDownCost

	// 尝试两个方向的开单计划
	// 计划1: 开UP方向（Entry UP + Hedge DOWN）
	plan1 := m.calculatePlanForDirection(
		"UP", orderSize, hedgeSize,
		m.upAsk, m.downBid, hedgeOffsetCents,
		currentUpShares, currentDownShares, currentTotalCost,
	)

	// 计划2: 开DOWN方向（Entry DOWN + Hedge UP）
	plan2 := m.calculatePlanForDirection(
		"DOWN", orderSize, hedgeSize,
		m.downAsk, m.upBid, hedgeOffsetCents,
		currentUpShares, currentDownShares, currentTotalCost,
	)

	// 选择最佳计划（优先选择能达到完美套利的计划）
	var bestPlan *orderPlan
	if plan1 != nil && plan1.canAchievePerfectArbitrage {
		bestPlan = plan1
	} else if plan2 != nil && plan2.canAchievePerfectArbitrage {
		bestPlan = plan2
	} else if plan1 != nil && plan1.afterMinProfit > plan2.afterMinProfit {
		bestPlan = plan1
	} else if plan2 != nil {
		bestPlan = plan2
	}

	if bestPlan != nil {
		dc.HasPlan = true
		dc.PlanDirection = bestPlan.direction
		dc.PlanEntrySize = bestPlan.entrySize
		dc.PlanHedgeSize = bestPlan.hedgeSize
		dc.PlanEntryPrice = bestPlan.entryPrice
		dc.PlanHedgePrice = bestPlan.hedgePrice
		dc.PlanAfterUpProfit = bestPlan.afterUpProfit
		dc.PlanAfterDownProfit = bestPlan.afterDownProfit
		dc.PlanAfterMinProfit = bestPlan.afterMinProfit
		dc.PlanReason = bestPlan.reason
	}
}

// orderPlan 开单计划
type orderPlan struct {
	direction                string
	entrySize                float64
	hedgeSize                float64
	entryPrice               float64
	hedgePrice               float64
	afterUpProfit            float64
	afterDownProfit          float64
	afterMinProfit           float64
	canAchievePerfectArbitrage bool
	reason                   string
}

// calculatePlanForDirection 计算某个方向的开单计划
func (m *dashboardModel) calculatePlanForDirection(
	direction string,
	orderSize, hedgeSize float64,
	entryAsk, hedgeBid float64,
	hedgeOffsetCents int,
	currentUpShares, currentDownShares, currentTotalCost float64,
) *orderPlan {
	// Entry价格（吃单，使用ask）
	entryPrice := entryAsk
	entryCost := entryPrice * orderSize

	// Hedge价格（挂单，使用互补价格）
	// hedgePrice = 100 - entryAsk - hedgeOffset（转换为小数）
	entryAskCents := int(entryPrice*100 + 0.5)
	hedgeLimitCents := 100 - entryAskCents - hedgeOffsetCents
	if hedgeLimitCents < 0 {
		hedgeLimitCents = 0
	}
	hedgePrice := float64(hedgeLimitCents) / 100.0
	hedgeCost := hedgePrice * hedgeSize

	// 计算执行后的持仓
	var afterUpShares, afterDownShares, afterTotalCost float64
	if direction == "UP" {
		afterUpShares = currentUpShares + orderSize
		afterDownShares = currentDownShares + hedgeSize
	} else {
		afterDownShares = currentDownShares + orderSize
		afterUpShares = currentUpShares + hedgeSize
	}
	afterTotalCost = currentTotalCost + entryCost + hedgeCost

	// 计算执行后的利润
	afterUpProfit := afterUpShares*1.0 - afterTotalCost
	afterDownProfit := afterDownShares*1.0 - afterTotalCost
	afterMinProfit := min(afterUpProfit, afterDownProfit)
	canAchievePerfectArbitrage := afterMinProfit > 0

	// 生成原因说明
	var reason string
	if canAchievePerfectArbitrage {
		reason = fmt.Sprintf("执行后可达到完美套利（最小利润=%.4f USDC）", afterMinProfit)
	} else {
		reason = fmt.Sprintf("执行后仍无法达到完美套利（最小利润=%.4f USDC，需要继续调整）", afterMinProfit)
	}

	return &orderPlan{
		direction:                direction,
		entrySize:                orderSize,
		hedgeSize:                hedgeSize,
		entryPrice:               entryPrice,
		hedgePrice:               hedgePrice,
		afterUpProfit:            afterUpProfit,
		afterDownProfit:          afterDownProfit,
		afterMinProfit:           afterMinProfit,
		canAchievePerfectArbitrage: canAchievePerfectArbitrage,
		reason:                   reason,
	}
}

// renderHeader 渲染头部
func (m dashboardModel) renderHeader() string {
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("63")).
		Background(lipgloss.Color("236")).
		Padding(0, 1).
		Width(m.width - 2)

	var header strings.Builder
	header.WriteString("📊 Velocity Follow 交易看板\n")
	header.WriteString(strings.Repeat("─", m.width-2) + "\n")
	
	var infoParts []string
	if m.currentMarketSlug != "" {
		infoParts = append(infoParts, fmt.Sprintf("周期: %s", m.currentMarketSlug))
		infoParts = append(infoParts, fmt.Sprintf("倒计时: %s", m.countdown))
	} else {
		infoParts = append(infoParts, "周期: 无")
		infoParts = append(infoParts, "倒计时: --:--")
	}

	if m.btcTargetPrice > 0 {
		infoParts = append(infoParts, fmt.Sprintf("BTC目标价: $%.2f", m.btcTargetPrice))
	}
	if m.btcRealtimePrice > 0 {
		infoParts = append(infoParts, fmt.Sprintf("BTC实时价: $%.2f", m.btcRealtimePrice))
	}

	header.WriteString(strings.Join(infoParts, " | "))

	return headerStyle.Render(header.String())
}

// renderPrices 渲染价格信息
func (m dashboardModel) renderPrices() string {
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
		content.WriteString(fmt.Sprintf("UP:   bid=%.4f ask=%.4f spread=%.4f velocity=%s\n", 
			m.upBid, m.upAsk, m.upSpread, upVelocityStr))
	} else {
		content.WriteString("UP:   bid=0.0000 ask=0.0000 spread=0.0000 velocity=N/A\n")
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
		content.WriteString(fmt.Sprintf("DOWN: bid=%.4f ask=%.4f spread=%.4f velocity=%s", 
			m.downBid, m.downAsk, m.downSpread, downVelocityStr))
	} else {
		content.WriteString("DOWN: bid=0.0000 ask=0.0000 spread=0.0000 velocity=N/A")
	}
	
	return borderStyle.Width(m.width - 4).Render(lipgloss.JoinVertical(lipgloss.Left, title, content.String()))
}

// renderTradingStats 渲染交易统计
func (m dashboardModel) renderTradingStats() string {
	title := titleStyle.Render("📈 交易统计")
	
	var content strings.Builder
	content.WriteString(fmt.Sprintf("开单数量:        %d\n", m.openOrdersCount))
	content.WriteString(fmt.Sprintf("已完成交易对:    %d", m.completedTrades))
	
	return borderStyle.Width(m.width - 4).Render(lipgloss.JoinVertical(lipgloss.Left, title, content.String()))
}

// renderPositions 渲染持仓信息
func (m dashboardModel) renderPositions() string {
	title := titleStyle.Render("💼 持仓信息")
	
	var content strings.Builder
	if m.positionsData.UpShares > 0 {
		content.WriteString(fmt.Sprintf("UP持仓:   %.4f shares (均价: %.4f)\n", m.positionsData.UpShares, m.positionsData.UpAvgPrice))
	} else {
		content.WriteString("UP持仓:   0.0000 shares\n")
	}
	if m.positionsData.DownShares > 0 {
		content.WriteString(fmt.Sprintf("DOWN持仓: %.4f shares (均价: %.4f)\n", m.positionsData.DownShares, m.positionsData.DownAvgPrice))
	} else {
		content.WriteString("DOWN持仓: 0.0000 shares\n")
	}
	content.WriteString(fmt.Sprintf("总持仓:   %.4f shares", m.positionsData.TotalSize))
	
	return borderStyle.Width(m.width - 4).Render(lipgloss.JoinVertical(lipgloss.Left, title, content.String()))
}

// renderArbitrage 渲染套利分析
func (m dashboardModel) renderArbitrage() string {
	title := titleStyle.Render("🧠 套利分析")
	
	var content strings.Builder
	
	if m.arbitrageAnalysis == nil {
		content.WriteString("暂无套利分析数据")
	} else {
		analysis := m.arbitrageAnalysis
		
		// 收益情况
		upProfitStyle := successStyle
		if analysis.ProfitIfUpWins < 0 {
			upProfitStyle = errorStyle
		}
		downProfitStyle := successStyle
		if analysis.ProfitIfDownWins < 0 {
			downProfitStyle = errorStyle
		}
		
		content.WriteString(fmt.Sprintf("UP胜出收益:   %s\n", upProfitStyle.Render(fmt.Sprintf("%.4f USDC", analysis.ProfitIfUpWins))))
		content.WriteString(fmt.Sprintf("DOWN胜出收益: %s\n", downProfitStyle.Render(fmt.Sprintf("%.4f USDC", analysis.ProfitIfDownWins))))
		
		// 锁定状态
		if analysis.IsPerfectArbitrage {
			content.WriteString(successStyle.Render(fmt.Sprintf("✅ 完美套利锁定！最小收益: %.4f USDC (%.2f%%)", 
				analysis.MinProfit, analysis.LockQuality*100)))
		} else if analysis.IsLocked {
			content.WriteString(successStyle.Render(fmt.Sprintf("✅ 完全锁定！最小收益: %.4f USDC", analysis.MinProfit)))
		} else {
			content.WriteString(warningStyle.Render("⚠️ 未完全锁定"))
		}
	}
	
	return borderStyle.Width(m.width - 4).Render(lipgloss.JoinVertical(lipgloss.Left, title, content.String()))
}

// renderRiskExposure 渲染风险敞口
func (m dashboardModel) renderRiskExposure() string {
	title := titleStyle.Render("⚠️ 风险敞口")
	
	content := m.riskExposure
	if content == "" {
		content = "无风险敞口"
	}
	
	return borderStyle.Width(m.width - 4).Render(lipgloss.JoinVertical(lipgloss.Left, title, content))
}

// renderDecisionCenter 渲染智能决策中心（整合所有信息）
func (m dashboardModel) renderDecisionCenter() string {
	title := titleStyle.Render("🧠 智能决策中心")
	dc := m.decisionCenter
	
	var content strings.Builder
	
	// 1. 开单统计
	content.WriteString("📊 开单统计:\n")
	content.WriteString(fmt.Sprintf("  UP开单:   %d | DOWN开单: %d | 总开单: %d | 已完成: %d\n", 
		dc.UpOrderCount, dc.DownOrderCount, dc.TotalOrderCount, dc.CompletedPairs))
	
	// 2. 持仓信息
	content.WriteString("\n💼 持仓信息:\n")
	content.WriteString(fmt.Sprintf("  UP持仓:   %.4f shares (均价: %.4f)\n", dc.UpShares, dc.UpAvgPrice))
	content.WriteString(fmt.Sprintf("  DOWN持仓: %.4f shares (均价: %.4f)\n", dc.DownShares, dc.DownAvgPrice))
	if dc.TotalAvgPrice > 0 {
		content.WriteString(fmt.Sprintf("  总持仓均价: %.4f\n", dc.TotalAvgPrice))
	}
	
	// 3. 利润分析
	content.WriteString("\n💰 利润分析:\n")
	upProfitStyle := successStyle
	if dc.ProfitIfUpWins < 0 {
		upProfitStyle = errorStyle
	}
	downProfitStyle := successStyle
	if dc.ProfitIfDownWins < 0 {
		downProfitStyle = errorStyle
	}
	content.WriteString(fmt.Sprintf("  UP胜出利润:   %s\n", upProfitStyle.Render(fmt.Sprintf("%.4f USDC", dc.ProfitIfUpWins))))
	content.WriteString(fmt.Sprintf("  DOWN胜出利润: %s\n", downProfitStyle.Render(fmt.Sprintf("%.4f USDC", dc.ProfitIfDownWins))))
	
	if dc.IsPerfectArbitrage {
		content.WriteString(successStyle.Render(fmt.Sprintf("  ✅ 完美套利！最小利润: %.4f USDC", dc.MinProfit)))
	} else if dc.MinProfit > 0 {
		content.WriteString(successStyle.Render(fmt.Sprintf("  ✅ 完全锁定！最小利润: %.4f USDC", dc.MinProfit)))
	} else {
		content.WriteString(warningStyle.Render(fmt.Sprintf("  ⚠️ 未完全锁定！最小利润: %.4f USDC", dc.MinProfit)))
	}
	
	// 4. 风险敞口
	content.WriteString("\n\n⚠️ 风险敞口:\n")
	if dc.ExposureSeconds > 0 {
		content.WriteString(fmt.Sprintf("  最大敞口时间: %.1f秒\n", dc.ExposureSeconds))
	}
	if dc.RiskExposure != "" {
		// 截断过长的风险敞口描述
		exposureText := dc.RiskExposure
		if len(exposureText) > 60 {
			exposureText = exposureText[:57] + "..."
		}
		content.WriteString(fmt.Sprintf("  %s", exposureText))
	} else {
		content.WriteString("  无风险敞口")
	}
	
	// 5. 状态机条件
	content.WriteString("\n\n⚙️ 状态机条件:\n")
	sm := dc.StateMachine
	
	// 基础条件
	content.WriteString("  基础条件: ")
	baseOK := sm.MarketValid && sm.BiasReady && sm.WarmupPassed && !sm.CycleEndProtected && 
	          sm.TradesLimitOK && sm.NoPendingHedge && sm.CooldownPassed
	if baseOK {
		content.WriteString(successStyle.Render("✅ 全部满足"))
	} else {
		var failed []string
		if !sm.MarketValid {
			failed = append(failed, "市场无效")
		}
		if !sm.BiasReady {
			failed = append(failed, "Bias未就绪")
		}
		if !sm.WarmupPassed {
			failed = append(failed, "预热中")
		}
		if sm.CycleEndProtected {
			failed = append(failed, "周期结束保护")
		}
		if !sm.TradesLimitOK {
			failed = append(failed, "交易次数限制")
		}
		if !sm.NoPendingHedge {
			failed = append(failed, "有未完成对冲")
		}
		if !sm.CooldownPassed {
			failed = append(failed, "冷却中")
		}
		content.WriteString(warningStyle.Render(fmt.Sprintf("❌ %s", strings.Join(failed, ", "))))
	}
	content.WriteString("\n")
	
	// UP方向条件
	content.WriteString("  UP方向: ")
	upOK := sm.UpAllowed && sm.UpVelocityOK && sm.UpDeltaOK && sm.UpVelocityValue >= sm.UpVelocityRequired
	if upOK {
		content.WriteString(successStyle.Render("✅ 满足"))
	} else {
		var failed []string
		if !sm.UpAllowed {
			failed = append(failed, "被禁止")
		}
		if !sm.UpVelocityOK {
			failed = append(failed, "速度计算失败")
		}
		if !sm.UpDeltaOK {
			failed = append(failed, fmt.Sprintf("位移不足(%.1f < %d)", sm.UpDeltaValue, sm.UpDeltaRequired))
		}
		if sm.UpVelocityValue < sm.UpVelocityRequired {
			failed = append(failed, fmt.Sprintf("速度不足(%.3f < %.3f)", sm.UpVelocityValue, sm.UpVelocityRequired))
		}
		content.WriteString(warningStyle.Render(fmt.Sprintf("❌ %s", strings.Join(failed, ", "))))
	}
	content.WriteString(fmt.Sprintf(" | 速度: %.3f/%.3f c/s | 位移: %.1f/%d c\n", 
		sm.UpVelocityValue, sm.UpVelocityRequired, sm.UpDeltaValue, sm.UpDeltaRequired))
	
	// DOWN方向条件
	content.WriteString("  DOWN方向: ")
	downOK := sm.DownAllowed && sm.DownVelocityOK && sm.DownDeltaOK && sm.DownVelocityValue >= sm.DownVelocityRequired
	if downOK {
		content.WriteString(successStyle.Render("✅ 满足"))
	} else {
		var failed []string
		if !sm.DownAllowed {
			failed = append(failed, "被禁止")
		}
		if !sm.DownVelocityOK {
			failed = append(failed, "速度计算失败")
		}
		if !sm.DownDeltaOK {
			failed = append(failed, fmt.Sprintf("位移不足(%.1f < %d)", sm.DownDeltaValue, sm.DownDeltaRequired))
		}
		if sm.DownVelocityValue < sm.DownVelocityRequired {
			failed = append(failed, fmt.Sprintf("速度不足(%.3f < %.3f)", sm.DownVelocityValue, sm.DownVelocityRequired))
		}
		content.WriteString(warningStyle.Render(fmt.Sprintf("❌ %s", strings.Join(failed, ", "))))
	}
	content.WriteString(fmt.Sprintf(" | 速度: %.3f/%.3f c/s | 位移: %.1f/%d c\n", 
		sm.DownVelocityValue, sm.DownVelocityRequired, sm.DownDeltaValue, sm.DownDeltaRequired))
	
	// 最终选择
	content.WriteString("  最终选择: ")
	if sm.Winner != "" {
		content.WriteString(successStyle.Render(fmt.Sprintf("✅ %s (%s)", sm.Winner, sm.WinnerReason)))
	} else {
		content.WriteString(warningStyle.Render("❌ 无"))
	}
	content.WriteString("\n")
	
	// 其他检查（注意：这些检查在实际下单时才会真正验证）
	content.WriteString("  其他检查: ")
	otherOK := sm.MarketQualityOK && sm.PriceRangeOK && sm.SpreadOK && sm.SideCooldownOK && sm.InventoryOK
	if otherOK {
		content.WriteString(infoStyle.Render("⚠️ 需下单时验证（市场质量/价格范围/价差等）"))
	} else {
		var failed []string
		if !sm.MarketQualityOK {
			failed = append(failed, "市场质量")
		}
		if !sm.PriceRangeOK {
			failed = append(failed, "价格范围")
		}
		if !sm.SpreadOK {
			failed = append(failed, "价差")
		}
		if !sm.SideCooldownOK {
			failed = append(failed, "方向冷却")
		}
		if !sm.InventoryOK {
			failed = append(failed, "库存偏斜")
		}
		content.WriteString(warningStyle.Render(fmt.Sprintf("❌ %s", strings.Join(failed, ", "))))
	}
	content.WriteString("\n")
	content.WriteString(infoStyle.Render("  💡 提示: 如果速度/位移满足但未开单，请查看日志中的'⏭️ 跳过'消息"))
	content.WriteString("\n")
	
	// 6. 开单计划
	content.WriteString("\n🎯 开单计划:\n")
	if dc.HasPlan {
		planStatus := "✅"
		if !dc.IsPerfectArbitrage && dc.PlanAfterMinProfit <= 0 {
			planStatus = "⚠️"
		}
		content.WriteString(fmt.Sprintf("  %s 方向: %s\n", planStatus, dc.PlanDirection))
		content.WriteString(fmt.Sprintf("  Entry: %.4f shares @ %.4f | Hedge: %.4f shares @ %.4f\n",
			dc.PlanEntrySize, dc.PlanEntryPrice, dc.PlanHedgeSize, dc.PlanHedgePrice))
		content.WriteString(fmt.Sprintf("  执行后利润: UP=%.4f USDC, DOWN=%.4f USDC, 最小=%.4f USDC\n",
			dc.PlanAfterUpProfit, dc.PlanAfterDownProfit, dc.PlanAfterMinProfit))
		if dc.PlanReason != "" {
			reasonText := dc.PlanReason
			if len(reasonText) > 70 {
				reasonText = reasonText[:67] + "..."
			}
			content.WriteString(fmt.Sprintf("  %s", reasonText))
		}
	} else {
		if dc.IsPerfectArbitrage {
			content.WriteString(successStyle.Render("  ✅ 已达到完美套利，无需开单"))
		} else {
			content.WriteString(warningStyle.Render("  ⚠️ 暂无开单计划（价格数据不足或无法计算）"))
		}
	}
	
	return borderStyle.Width(m.width - 4).Render(lipgloss.JoinVertical(lipgloss.Left, title, content.String()))
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

// RunDashboard 运行Dashboard（在goroutine中调用）
func (s *Strategy) RunDashboard() {
	if s.TradingService == nil {
		return
	}

	// 获取market spec
	gc := config.Get()
	if gc == nil {
		return
	}
	sp, err := gc.Market.Spec()
	if err != nil {
		return
	}

	// 重定向所有日志输出到文件，避免干扰TUI显示
	// 保存原始的logrus输出
	originalOutput := logrus.StandardLogger().Out
	originalLevel := logrus.GetLevel()
	
	// 创建日志文件
	logDir := "logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		logDir = os.TempDir()
	}
	logFile := filepath.Join(logDir, fmt.Sprintf("dashboard_%s.log", time.Now().Format("20060102_150405")))
	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err == nil {
		// 将所有logrus输出重定向到文件（不输出到终端）
		logrus.SetOutput(file)
		logrus.SetLevel(logrus.DebugLevel)
		logrus.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: "2006-01-02 15:04:05",
			DisableColors:   true, // 禁用颜色，因为写入文件
		})
		defer func() {
			// 恢复原始输出和级别
			logrus.SetOutput(originalOutput)
			logrus.SetLevel(originalLevel)
			file.Close()
		}()
	}

	// 启动Dashboard UI
	model := NewDashboardModel(s.TradingService, s.BinanceFuturesKlines, sp, s)
	program := tea.NewProgram(model, tea.WithAltScreen())

	if _, err := program.Run(); err != nil {
		// 错误信息会写入日志文件
		logrus.Debugf("❌ [%s] Dashboard运行失败: %v", ID, err)
	}
}
