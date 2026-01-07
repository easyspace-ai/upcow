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

type updateMsg struct {
	snapshot *Snapshot
}

type model struct {
	snapshot *Snapshot
	updateCh <-chan *Snapshot
	width    int
	height   int
}

func newModel(updateCh <-chan *Snapshot) model {
	return model{
		snapshot: &Snapshot{},
		updateCh: updateCh,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		m.waitForUpdate(),
		m.tick(),
	)
}

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
		m.snapshot = msg.snapshot
		return m, m.tick()
	case UpdateMsg:
		m.snapshot = msg.Snapshot
		return m, m.tick()
	case tickMsg:
		return m, tea.Batch(m.waitForUpdate(), m.tick())
	}
	return m, nil
}

func (m model) View() string {
	if m.snapshot == nil {
		return "等待数据..."
	}

	snap := m.snapshot
	availableWidth := m.width - 4
	if availableWidth < 60 {
		availableWidth = 60
	}
	leftWidth := availableWidth/2 - 1
	rightWidth := availableWidth/2 - 1

	left := m.renderLeft(snap, leftWidth)
	right := m.renderRight(snap, rightWidth)

	content := lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)
	header := m.renderHeader(snap)
	return lipgloss.JoinVertical(lipgloss.Left, header, content)
}

func (m model) renderHeader(snap *Snapshot) string {
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("39")).
		Padding(0, 1)

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

func (m model) renderLeft(snap *Snapshot, width int) string {
	var lines []string
	lines = append(lines, m.renderPriceTable(snap, width))
	lines = append(lines, "")
	lines = append(lines, m.renderVelocity(snap, width))
	lines = append(lines, "")
	lines = append(lines, m.renderPositions(snap, width))
	lines = append(lines, "")
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

func (m model) renderRight(snap *Snapshot, width int) string {
	var lines []string
	lines = append(lines, m.renderProfit(snap, width))
	lines = append(lines, "")
	lines = append(lines, m.renderTradingStats(snap, width))
	lines = append(lines, "")
	lines = append(lines, m.renderOrderStatus(snap, width))
	lines = append(lines, "")
	if snap.RiskManagement != nil {
		lines = append(lines, m.renderRiskManagement(snap, width))
		lines = append(lines, "")
	}
	lines = append(lines, m.renderCapitalOps(snap, width))
	content := strings.Join(lines, "\n")
	return lipgloss.NewStyle().
		Width(width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("39")).
		Padding(0, 1).
		Render(content)
}

func (m model) renderPriceTable(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	var lines []string
	lines = append(lines, titleStyle.Render("Price"))
	lines = append(lines, strings.Repeat("─", width-4))
	yesSpread := snap.YesAsk - snap.YesBid
	noSpread := snap.NoAsk - snap.NoBid
	lines = append(lines, fmt.Sprintf("UP   Price:%7.4f Bid:%7.4f Ask:%7.4f Spread:%6.4f",
		snap.YesPrice, snap.YesBid, snap.YesAsk, yesSpread))
	lines = append(lines, fmt.Sprintf("DOWN Price:%7.4f Bid:%7.4f Ask:%7.4f Spread:%6.4f",
		snap.NoPrice, snap.NoBid, snap.NoAsk, noSpread))
	return strings.Join(lines, "\n")
}

func (m model) renderVelocity(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	directionStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("226"))
	var lines []string
	lines = append(lines, titleStyle.Render("Velocity"))
	lines = append(lines, strings.Repeat("─", width-4))
	lines = append(lines, fmt.Sprintf("UP   Vel:%7.3f c/s Move:%3d c", snap.UpVelocity, snap.UpMove))
	lines = append(lines, fmt.Sprintf("DOWN Vel:%7.3f c/s Move:%3d c", snap.DownVelocity, snap.DownMove))
	if snap.Direction != "" {
		lines = append(lines, directionStyle.Render(fmt.Sprintf("Direction: %s", snap.Direction)))
	} else {
		lines = append(lines, "Direction: -")
	}
	return strings.Join(lines, "\n")
}

func (m model) renderPositions(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	hedgedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("46"))
	notHedgedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	var lines []string
	lines = append(lines, titleStyle.Render("Positions"))
	lines = append(lines, strings.Repeat("─", width-4))
	lines = append(lines, fmt.Sprintf("UP   Size:%8.4f Cost:$%7.4f Avg:%7.4f", snap.UpSize, snap.UpCost, snap.UpAvgPrice))
	lines = append(lines, fmt.Sprintf("DOWN Size:%8.4f Cost:$%7.4f Avg:%7.4f", snap.DownSize, snap.DownCost, snap.DownAvgPrice))
	if snap.IsHedged {
		lines = append(lines, hedgedStyle.Render("Status: ✅ Hedged"))
	} else {
		lines = append(lines, notHedgedStyle.Render("Status: ⚠️ Not Hedged"))
	}
	return strings.Join(lines, "\n")
}

func (m model) renderProfit(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	lockedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("46"))
	notLockedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	var lines []string
	lines = append(lines, titleStyle.Render("Profit"))
	lines = append(lines, strings.Repeat("─", width-4))
	lines = append(lines, fmt.Sprintf("Cost:$%7.4f UP:$%7.4f DOWN:$%7.4f", snap.TotalCost, snap.ProfitIfUpWin, snap.ProfitIfDownWin))
	if snap.IsProfitLocked {
		lines = append(lines, lockedStyle.Render("Status: ✅ Locked"))
	} else {
		lines = append(lines, notLockedStyle.Render("Status: ⚠️ Not Locked"))
	}
	return strings.Join(lines, "\n")
}

func (m model) renderTradingStats(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	var lines []string
	lines = append(lines, titleStyle.Render("Trading Stats"))
	lines = append(lines, strings.Repeat("─", width-4))
	if !snap.LastTriggerTime.IsZero() {
		elapsed := time.Since(snap.LastTriggerTime)
		lines = append(lines, fmt.Sprintf("Trades:%d Last:%s ago", snap.TradesThisCycle, formatDuration(elapsed)))
	} else {
		lines = append(lines, fmt.Sprintf("Trades:%d Last:-", snap.TradesThisCycle))
	}
	return strings.Join(lines, "\n")
}

func (m model) renderOrderStatus(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	var lines []string
	lines = append(lines, titleStyle.Render("Orders"))
	lines = append(lines, strings.Repeat("─", width-4))
	lines = append(lines, fmt.Sprintf("Hedges:%d Open:%d", snap.PendingHedges, snap.OpenOrders))
	return strings.Join(lines, "\n")
}

func (m model) renderCapitalOps(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	var lines []string
	lines = append(lines, titleStyle.Render("Capital Ops"))
	lines = append(lines, strings.Repeat("─", width-4))
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
	if snap.MergeCount > 0 {
		mergeLine += fmt.Sprintf(" Count:%d", snap.MergeCount)
	}
	lines = append(lines, mergeLine)

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
	if dc.CanTrade {
		lines = append(lines, canTradeStyle.Render("✅ Can Trade"))
	} else {
		lines = append(lines, cannotTradeStyle.Render(fmt.Sprintf("❌ Cannot Trade: %s", dc.BlockReason)))
	}
	lines = append(lines, "")

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
	lines = append(lines, fmt.Sprintf("Cycle: Cooldown%s Warmup%s Trades%s", cooldownStatus, warmupStatus, tradesStatus))

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

func (m model) renderRiskManagement(snap *Snapshot, width int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	warningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	infoStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("226"))
	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("46"))

	rm := snap.RiskManagement
	if rm == nil {
		return ""
	}
	var lines []string
	lines = append(lines, titleStyle.Render("Risk Management"))
	lines = append(lines, strings.Repeat("─", width-4))

	unhedged := make([]RiskExposureInfo, 0, len(rm.RiskExposures))
	for _, exp := range rm.RiskExposures {
		if exp.HedgeStatus != "Filled" {
			unhedged = append(unhedged, exp)
		}
	}
	if len(unhedged) > 0 {
		lines = append(lines, warningStyle.Render(fmt.Sprintf("⚠️ Exposures: %d", len(unhedged))))
		for i, exp := range unhedged {
			if i >= 3 {
				lines = append(lines, fmt.Sprintf("  ... and %d more", len(unhedged)-3))
				break
			}
			countdownStr := formatDuration(time.Duration(exp.CountdownSeconds) * time.Second)
			if exp.CountdownSeconds <= 0 {
				countdownStr = "超时"
			}
			entryInfo := fmt.Sprintf("Entry:%s(%.2f) ", truncate(exp.EntryOrderID, 8), float64(exp.EntryPriceCents)/100.0)
			priceInfo := ""
			if exp.OriginalHedgePriceCents > 0 {
				if exp.NewHedgePriceCents > 0 {
					priceInfo = fmt.Sprintf("原价:%.2f→新价:%.2f ",
						float64(exp.OriginalHedgePriceCents)/100.0,
						float64(exp.NewHedgePriceCents)/100.0)
				} else {
					priceInfo = fmt.Sprintf("原价:%.2f ", float64(exp.OriginalHedgePriceCents)/100.0)
				}
			}
			lines = append(lines, fmt.Sprintf("  %s%s倒计时:%s", entryInfo, priceInfo, countdownStr))
		}
	} else {
		lines = append(lines, successStyle.Render("✅ No Exposures"))
	}

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
	} else {
		lines = append(lines, successStyle.Render("✅ Idle"))
	}

	if rm.TotalReorders > 0 || rm.TotalAggressiveHedges > 0 || rm.TotalFakEats > 0 {
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("Stats: Reorders:%d Aggressive:%d FAK:%d",
			rm.TotalReorders, rm.TotalAggressiveHedges, rm.TotalFakEats))
	}
	return strings.Join(lines, "\n")
}

func (m model) waitForUpdate() tea.Cmd {
	return func() tea.Msg {
		snap := <-m.updateCh
		for {
			select {
			case latest := <-m.updateCh:
				snap = latest
			default:
				return updateMsg{snapshot: snap}
			}
		}
	}
}

type tickMsg time.Time

func (m model) tick() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

