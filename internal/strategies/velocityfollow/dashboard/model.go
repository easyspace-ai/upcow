package dashboard

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sirupsen/logrus"
)

var modelLog = logrus.WithField("module", "dashboard.model")

// UpdateMsg 更新消息（导出以便 dashboard.go 使用）
type UpdateMsg struct {
	Snapshot *Snapshot
}

// updateMsg 内部使用的更新消息类型（与 UpdateMsg 相同，但用于类型匹配）
type updateMsg struct {
	snapshot *Snapshot
}

// model Bubble Tea model
type model struct {
	snapshot *Snapshot
	updateCh <-chan *Snapshot
	width    int
	height   int
}

// newModel 创建新的 model
func newModel(updateCh <-chan *Snapshot) model {
	return model{
		snapshot: &Snapshot{},
		updateCh: updateCh,
	}
}

// Init 初始化
func (m model) Init() tea.Cmd {
	return tea.Batch(
		m.waitForUpdate(),
		m.tick(),
	)
}

// Update 处理消息
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			// Bubble Tea 会拦截 Ctrl+C，使得外层主程序可能收不到 SIGINT。
			// 主动向自己发送一次 SIGINT，确保整套程序能走统一的优雅退出链路。
			_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case updateMsg:
		// 诊断日志：确认消息到达
		if msg.snapshot != nil {
			modelLog.Debugf("📊 [Model.Update] 收到 updateMsg: market=%s", msg.snapshot.MarketSlug)
		} else {
			modelLog.Debugf("📊 [Model.Update] 收到 updateMsg: snapshot=nil")
		}
		m.snapshot = msg.snapshot
		// 继续等待下一个更新，同时保持 tick 运行以定期刷新 UI
		// 注意：tick 现在也会检查 channel，所以不需要单独调用 waitForUpdate
		return m, m.tick()
	case UpdateMsg:
		// 处理导出的 UpdateMsg 类型（从 program.Send() 发送）
		if msg.Snapshot != nil {
			modelLog.Debugf("📊 [Model.Update] 收到 UpdateMsg: market=%s", msg.Snapshot.MarketSlug)
		} else {
			modelLog.Debugf("📊 [Model.Update] 收到 UpdateMsg: snapshot=nil")
		}
		m.snapshot = msg.Snapshot
		return m, m.tick()
	case tickMsg:
		// 定期刷新 UI，即使没有数据更新也要刷新（确保 UI 响应）
		// 诊断日志：确认 tick 消息到达
		modelLog.Debugf("📊 [Model.Update] 收到 tickMsg: time=%v", time.Time(msg))
		// 在 tick 时也检查 channel 中是否有待处理的更新
		// 使用 Batch 同时等待更新和下一个 tick
		return m, tea.Batch(m.waitForUpdate(), m.tick())
	}
	return m, nil
}

// View 渲染视图
func (m model) View() string {
	if m.snapshot == nil {
		return "等待数据..."
	}

	snap := m.snapshot

	// 计算可用宽度（左右各留 2 个字符边距）
	availableWidth := m.width - 4
	if availableWidth < 60 {
		availableWidth = 60
	}
	leftWidth := availableWidth/2 - 1
	rightWidth := availableWidth/2 - 1

	// 左侧：价格、速度、持仓
	left := m.renderLeft(snap, leftWidth)

	// 右侧：盈利、交易统计、订单状态、合并/赎回
	right := m.renderRight(snap, rightWidth)

	// 合并左右两栏
	content := lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)

	// 添加标题
	header := m.renderHeader(snap)

	// 组合
	return lipgloss.JoinVertical(lipgloss.Left, header, content)
}

// renderHeader 渲染标题
func (m model) renderHeader(snap *Snapshot) string {
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("39")).
		Padding(0, 1)

	// 显示周期倒计时
	cycleInfo := ""
	if !snap.CycleEndTime.IsZero() {
		now := time.Now()
		if now.Before(snap.CycleEndTime) {
			remaining := snap.CycleEndTime.Sub(now)
			minutes := int(remaining.Minutes())
			seconds := int(remaining.Seconds()) % 60
			cycleInfo = fmt.Sprintf(" | Cycle End: %dm%02ds", minutes, seconds)
		} else {
			cycleInfo = fmt.Sprintf(" | Cycle End: %s", snap.CycleEndTime.Format("15:04:05"))
		}
	}

	titlePrefix := snap.Title
	if strings.TrimSpace(titlePrefix) == "" {
		titlePrefix = "Strategy Dashboard"
	}
	title := fmt.Sprintf("%s | Market: %s | Time: %s%s",
		titlePrefix,
		snap.MarketSlug,
		time.Now().Format("15:04:05"),
		cycleInfo)
	return headerStyle.Render(title)
}

// renderLeft 渲染左侧内容
func (m model) renderLeft(snap *Snapshot, width int) string {
	var lines []string

	// 价格表
	lines = append(lines, m.renderPriceTable(snap, width))
	lines = append(lines, "")

	// 速度信息
	lines = append(lines, m.renderVelocity(snap, width))
	lines = append(lines, "")

	// 持仓信息
	lines = append(lines, m.renderPositions(snap, width))
	lines = append(lines, "")

	// 决策条件（移到左下角，放在最后）
	if snap.DecisionConditions != nil {
		lines = append(lines, m.renderDecisionConditions(snap, width))
	}

	content := strings.Join(lines, "\n")
	return lipgloss.NewStyle().
		Width(width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("39")).
		Padding(0, 1).
		Render(content)
}

// renderRight 渲染右侧内容
func (m model) renderRight(snap *Snapshot, width int) string {
	var lines []string

	// 盈利信息
	lines = append(lines, m.renderProfit(snap, width))
	lines = append(lines, "")

	// 交易统计
	lines = append(lines, m.renderTradingStats(snap, width))
	lines = append(lines, "")

	// 订单状态
	lines = append(lines, m.renderOrderStatus(snap, width))
	lines = append(lines, "")

	// 风控状态
	if snap.RiskManagement != nil {
		lines = append(lines, m.renderRiskManagement(snap, width))
		lines = append(lines, "")
	}

	// 合并和赎回状态
	lines = append(lines, m.renderCapitalOps(snap, width))

	content := strings.Join(lines, "\n")
	return lipgloss.NewStyle().
		Width(width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("39")).
		Padding(0, 1).
		Render(content)
}

// renderPriceTable 渲染价格表
func (m model) renderPriceTable(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))

	var lines []string
	lines = append(lines, titleStyle.Render("Price"))
	lines = append(lines, strings.Repeat("─", width-4))

	yesSpread := snap.YesAsk - snap.YesBid
	noSpread := snap.NoAsk - snap.NoBid

	// UP 信息一行显示（紧凑格式）
	lines = append(lines, fmt.Sprintf("UP   Price:%7.4f Bid:%7.4f Ask:%7.4f Spread:%6.4f",
		snap.YesPrice, snap.YesBid, snap.YesAsk, yesSpread))

	// DOWN 信息一行显示（紧凑格式）
	lines = append(lines, fmt.Sprintf("DOWN Price:%7.4f Bid:%7.4f Ask:%7.4f Spread:%6.4f",
		snap.NoPrice, snap.NoBid, snap.NoAsk, noSpread))

	return strings.Join(lines, "\n")
}

// renderVelocity 渲染速度信息
func (m model) renderVelocity(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	directionStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("226")) // 黄色

	var lines []string
	lines = append(lines, titleStyle.Render("Velocity"))
	lines = append(lines, strings.Repeat("─", width-4))

	// UP 速度信息一行显示
	lines = append(lines, fmt.Sprintf("UP   Vel:%7.3f c/s Move:%3d c", snap.UpVelocity, snap.UpMove))

	// DOWN 速度信息一行显示
	lines = append(lines, fmt.Sprintf("DOWN Vel:%7.3f c/s Move:%3d c", snap.DownVelocity, snap.DownMove))

	// 方向信息
	if snap.Direction != "" {
		lines = append(lines, directionStyle.Render(fmt.Sprintf("Direction: %s", snap.Direction)))
	} else {
		lines = append(lines, "Direction: -")
	}

	return strings.Join(lines, "\n")
}

// renderPositions 渲染持仓信息
func (m model) renderPositions(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	hedgedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("46"))     // 绿色
	notHedgedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // 红色

	var lines []string
	lines = append(lines, titleStyle.Render("Positions"))
	lines = append(lines, strings.Repeat("─", width-4))

	// UP 持仓信息一行显示
	lines = append(lines, fmt.Sprintf("UP   Size:%8.4f Cost:$%7.4f Avg:%7.4f",
		snap.UpSize, snap.UpCost, snap.UpAvgPrice))

	// DOWN 持仓信息一行显示
	lines = append(lines, fmt.Sprintf("DOWN Size:%8.4f Cost:$%7.4f Avg:%7.4f",
		snap.DownSize, snap.DownCost, snap.DownAvgPrice))

	// 对冲状态
	if snap.IsHedged {
		lines = append(lines, hedgedStyle.Render("Status: ✅ Hedged"))
	} else {
		lines = append(lines, notHedgedStyle.Render("Status: ⚠️ Not Hedged"))
	}

	return strings.Join(lines, "\n")
}

// renderProfit 渲染盈利信息
func (m model) renderProfit(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	lockedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("46"))     // 绿色
	notLockedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // 红色

	var lines []string
	lines = append(lines, titleStyle.Render("Profit"))
	lines = append(lines, strings.Repeat("─", width-4))

	// 盈利信息一行显示
	lines = append(lines, fmt.Sprintf("Cost:$%7.4f UP:$%7.4f DOWN:$%7.4f",
		snap.TotalCost, snap.ProfitIfUpWin, snap.ProfitIfDownWin))

	// 锁定状态
	if snap.IsProfitLocked {
		lines = append(lines, lockedStyle.Render("Status: ✅ Locked"))
	} else {
		lines = append(lines, notLockedStyle.Render("Status: ⚠️ Not Locked"))
	}

	return strings.Join(lines, "\n")
}

// renderTradingStats 渲染交易统计
func (m model) renderTradingStats(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))

	var lines []string
	lines = append(lines, titleStyle.Render("Trading Stats"))
	lines = append(lines, strings.Repeat("─", width-4))

	// 交易统计一行显示
	if !snap.LastTriggerTime.IsZero() {
		elapsed := time.Since(snap.LastTriggerTime)
		lines = append(lines, fmt.Sprintf("Trades:%d Last:%s ago", snap.TradesThisCycle, formatDuration(elapsed)))
	} else {
		lines = append(lines, fmt.Sprintf("Trades:%d Last:-", snap.TradesThisCycle))
	}

	return strings.Join(lines, "\n")
}

// renderOrderStatus 渲染订单状态
func (m model) renderOrderStatus(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))

	var lines []string
	lines = append(lines, titleStyle.Render("Orders"))
	lines = append(lines, strings.Repeat("─", width-4))

	// 订单状态一行显示
	lines = append(lines, fmt.Sprintf("Hedges:%d Open:%d", snap.PendingHedges, snap.OpenOrders))

	return strings.Join(lines, "\n")
}

// renderCapitalOps 渲染合并和赎回状态
func (m model) renderCapitalOps(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))

	var lines []string
	lines = append(lines, titleStyle.Render("Capital Ops"))
	lines = append(lines, strings.Repeat("─", width-4))

	// 合并状态（尽量紧凑）
	mergeIcon := "⏸️"
	switch snap.MergeStatus {
	case "merging":
		mergeIcon = "🔄"
	case "completed":
		mergeIcon = "✅"
	case "failed":
		mergeIcon = "❌"
	}
	mergeLine := fmt.Sprintf("Merge:%s %s", mergeIcon, snap.MergeStatus)
	if snap.MergeAmount > 0 {
		mergeLine += fmt.Sprintf(" $%.2f", snap.MergeAmount)
	}
	if snap.MergeTxHash != "" {
		mergeLine += " " + truncate(snap.MergeTxHash, 8)
	}
	if !snap.LastMergeTime.IsZero() {
		elapsed := time.Since(snap.LastMergeTime)
		mergeLine += fmt.Sprintf(" %s", formatDuration(elapsed))
	}
	// 显示 merge 次数
	if snap.MergeCount > 0 {
		mergeLine += fmt.Sprintf(" Count:%d", snap.MergeCount)
	}
	lines = append(lines, mergeLine)

	// 赎回状态（尽量紧凑）
	redeemIcon := "⏸️"
	switch snap.RedeemStatus {
	case "redeeming":
		redeemIcon = "🔄"
	case "completed":
		redeemIcon = "✅"
	case "failed":
		redeemIcon = "❌"
	}
	redeemLine := fmt.Sprintf("Redeem:%s %s", redeemIcon, snap.RedeemStatus)
	if snap.RedeemCount > 0 {
		redeemLine += fmt.Sprintf(" (%d)", snap.RedeemCount)
	}
	if !snap.LastRedeemTime.IsZero() {
		elapsed := time.Since(snap.LastRedeemTime)
		redeemLine += fmt.Sprintf(" %s", formatDuration(elapsed))
	}
	lines = append(lines, redeemLine)

	return strings.Join(lines, "\n")
}

// renderDecisionConditions 渲染决策条件
func (m model) renderDecisionConditions(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	canTradeStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("46"))
	cannotTradeStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))

	dc := snap.DecisionConditions
	if dc == nil {
		return ""
	}

	var lines []string
	lines = append(lines, titleStyle.Render("Decision Conditions"))
	lines = append(lines, strings.Repeat("─", width-4))

	// 总体状态
	if dc.CanTrade {
		lines = append(lines, canTradeStyle.Render("✅ Can Trade"))
	} else {
		lines = append(lines, cannotTradeStyle.Render(fmt.Sprintf("❌ Cannot Trade: %s", dc.BlockReason)))
	}
	lines = append(lines, "")

	// 速度条件
	upVelStatus := "❌"
	if dc.UpVelocityOK && dc.UpMoveOK {
		upVelStatus = "✅"
	}
	downVelStatus := "❌"
	if dc.DownVelocityOK && dc.DownMoveOK {
		downVelStatus = "✅"
	}
	lines = append(lines, fmt.Sprintf("Velocity: UP%s(%.3f/%d) DOWN%s(%.3f/%d) Dir:%s",
		upVelStatus, dc.UpVelocityValue, dc.UpMoveValue,
		downVelStatus, dc.DownVelocityValue, dc.DownMoveValue,
		dc.Direction))

	// 价格条件
	entryStatus := "❌"
	if dc.EntryPriceOK {
		entryStatus = "✅"
	}
	totalCostStatus := "❌"
	if dc.TotalCostOK {
		totalCostStatus = "✅"
	}
	hedgeStatus := "❌"
	if dc.HedgePriceOK {
		hedgeStatus = "✅"
	}
	lines = append(lines, fmt.Sprintf("Price: Entry%s(%.4f) Hedge%s(%.4f) Cost%s(%.4f)",
		entryStatus, dc.EntryPriceValue,
		hedgeStatus, dc.HedgePriceValue,
		totalCostStatus, dc.TotalCostValue))

	// 周期条件
	cooldownStatus := "✅"
	if !dc.CooldownOK {
		cooldownStatus = fmt.Sprintf("❌(%.1fs)", dc.CooldownRemaining)
	}
	warmupStatus := "✅"
	if !dc.WarmupOK {
		warmupStatus = fmt.Sprintf("❌(%.1fs)", dc.WarmupRemaining)
	}
	tradesStatus := "✅"
	if !dc.TradesLimitOK {
		tradesStatus = fmt.Sprintf("❌(%d/%d)", dc.TradesThisCycle, dc.MaxTradesPerCycle)
	}
	lines = append(lines, fmt.Sprintf("Cycle: Cooldown%s Warmup%s Trades%s",
		cooldownStatus, warmupStatus, tradesStatus))

	// 持仓条件
	hedgeRiskStatus := "✅"
	if dc.HasPendingHedge {
		hedgeRiskStatus = "❌"
	}
	profitStatus := "❌"
	if dc.IsProfitLocked {
		profitStatus = "✅"
	}
	lines = append(lines, fmt.Sprintf("Position: Hedge%s Profit%s(UP:%.4f DOWN:%.4f)",
		hedgeRiskStatus, profitStatus, dc.ProfitIfUpWin, dc.ProfitIfDownWin))

	return strings.Join(lines, "\n")
}

// renderRiskManagement 渲染风控状态
func (m model) renderRiskManagement(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	warningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // 红色
	infoStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("226"))    // 黄色
	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("46"))  // 绿色

	rm := snap.RiskManagement
	if rm == nil {
		return ""
	}

	var lines []string
	lines = append(lines, titleStyle.Render("Risk Management"))
	lines = append(lines, strings.Repeat("─", width-4))

	// 风险敞口数量（再次过滤，确保只显示未对冲的）
	unhedgedExposures := make([]RiskExposureInfo, 0, len(rm.RiskExposures))
	for _, exp := range rm.RiskExposures {
		// 只显示未对冲的风险敞口（HedgeStatus != Filled）
		// HedgeStatus 是字符串类型，直接比较
		if exp.HedgeStatus != "Filled" {
			unhedgedExposures = append(unhedgedExposures, exp)
		}
	}

	if len(unhedgedExposures) > 0 {
		lines = append(lines, warningStyle.Render(fmt.Sprintf("⚠️ Exposures: %d", len(unhedgedExposures))))
		// 显示每个风险敞口（理论上只应该有一条）
		for i, exp := range unhedgedExposures {
			if i >= 3 { // 最多显示3个（理论上不应该超过1个）
				lines = append(lines, fmt.Sprintf("  ... and %d more", len(unhedgedExposures)-3))
				break
			}

			// 格式化倒计时
			countdownStr := formatDuration(time.Duration(exp.CountdownSeconds) * time.Second)
			if exp.CountdownSeconds <= 0 {
				countdownStr = "超时"
			}

			// 构建显示信息
			entryInfo := fmt.Sprintf("Entry:%s(%.2f) ", truncate(exp.EntryOrderID, 8), float64(exp.EntryPriceCents)/100.0)

			// 显示价格信息
			priceInfo := ""
			if exp.OriginalHedgePriceCents > 0 {
				if exp.NewHedgePriceCents > 0 {
					// 重新下单了，显示原价和新价
					priceInfo = fmt.Sprintf("原价:%.2f→新价:%.2f ",
						float64(exp.OriginalHedgePriceCents)/100.0,
						float64(exp.NewHedgePriceCents)/100.0)
				} else {
					// 未重新下单，只显示原价
					priceInfo = fmt.Sprintf("原价:%.2f ", float64(exp.OriginalHedgePriceCents)/100.0)
				}
			}

			// 显示倒计时
			countdownInfo := fmt.Sprintf("倒计时:%s", countdownStr)

			lines = append(lines, fmt.Sprintf("  %s%s%s",
				entryInfo, priceInfo, countdownInfo))
		}
	} else {
		lines = append(lines, successStyle.Render("✅ No Exposures"))
	}

	// 当前操作状态
	if rm.CurrentAction != "idle" && rm.CurrentAction != "" {
		actionIcon := "🔄"
		actionColor := infoStyle
		switch rm.CurrentAction {
		case "canceling":
			actionIcon = "🛑"
			actionColor = warningStyle
		case "reordering":
			actionIcon = "🔄"
			actionColor = infoStyle
		case "aggressive_hedging":
			actionIcon = "🚨"
			actionColor = warningStyle
		case "fak_eating":
			actionIcon = "⚡"
			actionColor = warningStyle
		}

		actionTime := ""
		if !rm.CurrentActionTime.IsZero() {
			elapsed := time.Since(rm.CurrentActionTime)
			actionTime = fmt.Sprintf(" (%s)", formatDuration(elapsed))
		}

		actionLine := fmt.Sprintf("%s Action: %s%s", actionIcon, rm.CurrentAction, actionTime)
		if rm.CurrentActionDesc != "" {
			actionLine += fmt.Sprintf(" - %s", rm.CurrentActionDesc)
		}
		lines = append(lines, actionColor.Render(actionLine))

		if rm.CurrentActionEntry != "" {
			lines = append(lines, fmt.Sprintf("  Entry:%s Hedge:%s",
				truncate(rm.CurrentActionEntry, 8), truncate(rm.CurrentActionHedge, 8)))
		}

		// 显示调价详情（如果正在调价）
		if rm.CurrentAction == "reordering" && rm.RepriceOldPriceCents > 0 {
			lines = append(lines, "")
			lines = append(lines, infoStyle.Render("💰 调价详情:"))
			lines = append(lines, fmt.Sprintf("  原价格: %dc", rm.RepriceOldPriceCents))
			lines = append(lines, fmt.Sprintf("  新价格: %dc", rm.RepriceNewPriceCents))
			if rm.RepricePriceChangeCents != 0 {
				changeSign := "+"
				if rm.RepricePriceChangeCents < 0 {
					changeSign = ""
				}
				lines = append(lines, fmt.Sprintf("  价格变化: %s%dc", changeSign, rm.RepricePriceChangeCents))
			}
			if rm.RepriceStrategy != "" {
				lines = append(lines, fmt.Sprintf("  策略: %s", rm.RepriceStrategy))
			}
			if rm.RepriceEntryCostCents > 0 {
				lines = append(lines, fmt.Sprintf("  Entry成本: %dc", rm.RepriceEntryCostCents))
			}
			if rm.RepriceMarketAskCents > 0 {
				lines = append(lines, fmt.Sprintf("  市场ask: %dc", rm.RepriceMarketAskCents))
			}
			if rm.RepriceIdealPriceCents > 0 {
				lines = append(lines, fmt.Sprintf("  理想价格: %dc", rm.RepriceIdealPriceCents))
			}
			if rm.RepriceTotalCostCents > 0 {
				lines = append(lines, fmt.Sprintf("  总成本: %dc", rm.RepriceTotalCostCents))
			}
			if rm.RepriceProfitCents != 0 {
				profitColor := successStyle
				if rm.RepriceProfitCents < 0 {
					profitColor = warningStyle
				}
				lines = append(lines, profitColor.Render(fmt.Sprintf("  利润: %dc", rm.RepriceProfitCents)))
			}
		}
	} else {
		lines = append(lines, successStyle.Render("✅ Idle"))
	}

	// 统计信息
	if rm.TotalReorders > 0 || rm.TotalAggressiveHedges > 0 || rm.TotalFakEats > 0 {
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("Stats: Reorders:%d Aggressive:%d FAK:%d",
			rm.TotalReorders, rm.TotalAggressiveHedges, rm.TotalFakEats))
	}

	return strings.Join(lines, "\n")
}

// waitForUpdate 等待更新
// 改进：使用阻塞方式读取 channel，但会跳过旧数据，只保留最新的
func (m model) waitForUpdate() tea.Cmd {
	return func() tea.Msg {
		// 诊断日志：确认 waitForUpdate 被调用
		modelLog.Debugf("📊 [Model.waitForUpdate] 开始等待更新")

		// 先读取一个快照（阻塞等待）
		snap := <-m.updateCh

		// 诊断日志：成功读取到快照
		if snap != nil {
			modelLog.Debugf("📊 [Model.waitForUpdate] 读取到快照: market=%s", snap.MarketSlug)
		} else {
			modelLog.Debugf("📊 [Model.waitForUpdate] 读取到快照: snapshot=nil")
		}

		// 如果 channel 中还有更多快照，继续读取直到最后一个（只保留最新的）
		// 使用非阻塞的方式检查是否有更多数据
		for {
			select {
			case latestSnap := <-m.updateCh:
				// 有更新的快照，使用最新的
				if latestSnap != nil {
					modelLog.Debugf("📊 [Model.waitForUpdate] 读取到更新的快照: market=%s", latestSnap.MarketSlug)
				}
				snap = latestSnap
			default:
				// 没有更多快照了，返回最新的
				return updateMsg{snapshot: snap}
			}
		}
	}
}

// tickMsg 定时器消息
type tickMsg time.Time

// tick 定时器命令
// 改进：增加 tick 频率到 50ms，确保 UI 及时更新
// 注意：tick 函数本身不能直接读取 channel，需要通过 waitForUpdate 来处理
func (m model) tick() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}
