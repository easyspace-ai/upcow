package dashboard

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/sirupsen/logrus"
)

// abs 返回整数的绝对值
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

var nativeLog = logrus.WithField("module", "dashboard.native")

// NativeTUI 原生TUI实现（使用 tcell）
type NativeTUI struct {
	screen         tcell.Screen
	snapshot       *Snapshot
	mu             sync.RWMutex
	renderMu       sync.Mutex
	needsFullClear bool
	updateCh       chan *Snapshot
	stopCh         chan struct{}
	renderTicker   *time.Ticker
	width          int
	height         int
	exitCallback   func()
}

func NewNativeTUI() (*NativeTUI, error) {
	screen, err := tcell.NewScreen()
	if err != nil {
		return nil, fmt.Errorf("创建 tcell screen 失败: %w", err)
	}
	if err := screen.Init(); err != nil {
		return nil, fmt.Errorf("初始化 tcell screen 失败: %w", err)
	}
	tui := &NativeTUI{
		screen:       screen,
		snapshot:     &Snapshot{},
		updateCh:     make(chan *Snapshot, 1), // 只保留最新，避免 backlog
		stopCh:       make(chan struct{}),
		renderTicker: time.NewTicker(500 * time.Millisecond),
		needsFullClear: true,
	}
	tui.width, tui.height = screen.Size()
	return tui, nil
}

func (t *NativeTUI) Start(ctx context.Context, exitCallback func()) error {
	t.mu.Lock()
	t.exitCallback = exitCallback
	t.mu.Unlock()

	go t.eventLoop(ctx)
	go t.renderLoop(ctx)
	return nil
}

func (t *NativeTUI) Stop() {
	select {
	case <-t.stopCh:
	default:
		close(t.stopCh)
	}
	if t.renderTicker != nil {
		t.renderTicker.Stop()
	}
	if t.screen != nil {
		t.screen.PostEvent(tcell.NewEventInterrupt(nil))
		t.screen.Fini()
	}
}

func (t *NativeTUI) UpdateSnapshot(snapshot *Snapshot) {
	if snapshot == nil {
		return
	}

	// 深拷贝
	newSnapshot := &Snapshot{
		Title:            snapshot.Title,
		MarketSlug:        snapshot.MarketSlug,
		YesPrice:          snapshot.YesPrice,
		NoPrice:           snapshot.NoPrice,
		YesBid:            snapshot.YesBid,
		YesAsk:            snapshot.YesAsk,
		NoBid:             snapshot.NoBid,
		NoAsk:             snapshot.NoAsk,
		UpVelocity:        snapshot.UpVelocity,
		DownVelocity:      snapshot.DownVelocity,
		UpMove:            snapshot.UpMove,
		DownMove:          snapshot.DownMove,
		Direction:         snapshot.Direction,
		UpSize:            snapshot.UpSize,
		DownSize:          snapshot.DownSize,
		UpCost:            snapshot.UpCost,
		DownCost:          snapshot.DownCost,
		UpAvgPrice:        snapshot.UpAvgPrice,
		DownAvgPrice:      snapshot.DownAvgPrice,
		IsHedged:          snapshot.IsHedged,
		ProfitIfUpWin:     snapshot.ProfitIfUpWin,
		ProfitIfDownWin:   snapshot.ProfitIfDownWin,
		TotalCost:         snapshot.TotalCost,
		IsProfitLocked:    snapshot.IsProfitLocked,
		TradesThisCycle:   snapshot.TradesThisCycle,
		LastTriggerTime:   snapshot.LastTriggerTime,
		PendingHedges:     snapshot.PendingHedges,
		OpenOrders:        snapshot.OpenOrders,
		OMSQueueLen:       snapshot.OMSQueueLen,
		HedgeEWMASec:      snapshot.HedgeEWMASec,
		ReorderBudgetSkips: snapshot.ReorderBudgetSkips,
		FAKBudgetWarnings:  snapshot.FAKBudgetWarnings,
		MarketCooldownRemainingSec: snapshot.MarketCooldownRemainingSec,
		MarketCooldownReason:       snapshot.MarketCooldownReason,
		MergeCount:        snapshot.MergeCount,
		MergeStatus:       snapshot.MergeStatus,
		MergeAmount:       snapshot.MergeAmount,
		MergeTxHash:       snapshot.MergeTxHash,
		LastMergeTime:     snapshot.LastMergeTime,
		RedeemCount:       snapshot.RedeemCount,
		RedeemStatus:      snapshot.RedeemStatus,
		LastRedeemTime:    snapshot.LastRedeemTime,
		CycleEndTime:      snapshot.CycleEndTime,
		CycleRemainingSec: snapshot.CycleRemainingSec,
	}

	if snapshot.RiskManagement != nil {
		riskExposures := make([]RiskExposureInfo, len(snapshot.RiskManagement.RiskExposures))
		copy(riskExposures, snapshot.RiskManagement.RiskExposures)
		newSnapshot.RiskManagement = &RiskManagementStatus{
			RiskExposuresCount:     snapshot.RiskManagement.RiskExposuresCount,
			RiskExposures:          riskExposures,
			CurrentAction:          snapshot.RiskManagement.CurrentAction,
			CurrentActionEntry:     snapshot.RiskManagement.CurrentActionEntry,
			CurrentActionHedge:     snapshot.RiskManagement.CurrentActionHedge,
			CurrentActionTime:      snapshot.RiskManagement.CurrentActionTime,
			CurrentActionDesc:      snapshot.RiskManagement.CurrentActionDesc,
			TotalReorders:          snapshot.RiskManagement.TotalReorders,
			TotalAggressiveHedges:  snapshot.RiskManagement.TotalAggressiveHedges,
			TotalFakEats:           snapshot.RiskManagement.TotalFakEats,
			RepriceOldPriceCents:   snapshot.RiskManagement.RepriceOldPriceCents,
			RepriceNewPriceCents:   snapshot.RiskManagement.RepriceNewPriceCents,
			RepricePriceChangeCents: snapshot.RiskManagement.RepricePriceChangeCents,
			RepriceStrategy:        snapshot.RiskManagement.RepriceStrategy,
			RepriceEntryCostCents:  snapshot.RiskManagement.RepriceEntryCostCents,
			RepriceMarketAskCents:  snapshot.RiskManagement.RepriceMarketAskCents,
			RepriceIdealPriceCents: snapshot.RiskManagement.RepriceIdealPriceCents,
			RepriceTotalCostCents:  snapshot.RiskManagement.RepriceTotalCostCents,
			RepriceProfitCents:     snapshot.RiskManagement.RepriceProfitCents,
		}
	}
	if snapshot.DecisionConditions != nil {
		newSnapshot.DecisionConditions = &DecisionConditions{
			UpVelocityOK:       snapshot.DecisionConditions.UpVelocityOK,
			UpVelocityValue:    snapshot.DecisionConditions.UpVelocityValue,
			UpMoveOK:           snapshot.DecisionConditions.UpMoveOK,
			UpMoveValue:        snapshot.DecisionConditions.UpMoveValue,
			DownVelocityOK:     snapshot.DecisionConditions.DownVelocityOK,
			DownVelocityValue:  snapshot.DecisionConditions.DownVelocityValue,
			DownMoveOK:         snapshot.DecisionConditions.DownMoveOK,
			DownMoveValue:      snapshot.DecisionConditions.DownMoveValue,
			Direction:          snapshot.DecisionConditions.Direction,
			EntryPriceOK:       snapshot.DecisionConditions.EntryPriceOK,
			EntryPriceValue:    snapshot.DecisionConditions.EntryPriceValue,
			EntryPriceMin:      snapshot.DecisionConditions.EntryPriceMin,
			EntryPriceMax:      snapshot.DecisionConditions.EntryPriceMax,
			TotalCostOK:        snapshot.DecisionConditions.TotalCostOK,
			TotalCostValue:     snapshot.DecisionConditions.TotalCostValue,
			HedgePriceOK:       snapshot.DecisionConditions.HedgePriceOK,
			HedgePriceValue:    snapshot.DecisionConditions.HedgePriceValue,
			HasUnhedgedRisk:    snapshot.DecisionConditions.HasUnhedgedRisk,
			IsProfitLocked:     snapshot.DecisionConditions.IsProfitLocked,
			ProfitIfUpWin:      snapshot.DecisionConditions.ProfitIfUpWin,
			ProfitIfDownWin:    snapshot.DecisionConditions.ProfitIfDownWin,
			CooldownOK:         snapshot.DecisionConditions.CooldownOK,
			CooldownRemaining:  snapshot.DecisionConditions.CooldownRemaining,
			WarmupOK:           snapshot.DecisionConditions.WarmupOK,
			WarmupRemaining:    snapshot.DecisionConditions.WarmupRemaining,
			TradesLimitOK:      snapshot.DecisionConditions.TradesLimitOK,
			TradesThisCycle:    snapshot.DecisionConditions.TradesThisCycle,
			MaxTradesPerCycle:  snapshot.DecisionConditions.MaxTradesPerCycle,
			MarketValid:        snapshot.DecisionConditions.MarketValid,
			HasPendingHedge:    snapshot.DecisionConditions.HasPendingHedge,
			CanTrade:           snapshot.DecisionConditions.CanTrade,
			BlockReason:        snapshot.DecisionConditions.BlockReason,
		}
	}

	t.mu.Lock()
	t.snapshot = newSnapshot
	t.mu.Unlock()

	// drain + send latest
	for {
		select {
		case <-t.updateCh:
		default:
			goto drained
		}
	}
drained:
	select {
	case t.updateCh <- newSnapshot:
	default:
	}
}

func (t *NativeTUI) eventLoop(ctx context.Context) {
	eventCh := make(chan tcell.Event, 32)
	go t.screen.ChannelEvents(eventCh, t.stopCh)

	for {
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.stopCh:
			return
		case ev := <-eventCh:
			if ev == nil {
				continue
			}
			switch ev := ev.(type) {
			case *tcell.EventKey:
				if ev.Key() == tcell.KeyEscape ||
					ev.Key() == tcell.KeyCtrlC ||
					(ev.Modifiers()&tcell.ModCtrl != 0 && (ev.Rune() == 'c' || ev.Rune() == 'C')) ||
					ev.Rune() == 3 ||
					ev.Rune() == 'q' || ev.Rune() == 'Q' {

					_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
					t.mu.RLock()
					cb := t.exitCallback
					t.mu.RUnlock()
					if cb != nil {
						cb()
					}
					t.Stop()
					return
				}
			case *tcell.EventResize:
				w, h := t.screen.Size()
				t.renderMu.Lock()
				t.width, t.height = w, h
				t.needsFullClear = true
				t.renderMu.Unlock()
				t.render()
			}
		}
	}
}

func (t *NativeTUI) renderLoop(ctx context.Context) {
	lastRenderTime := time.Now()
	minRenderInterval := 200 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stopCh:
			return
		case snapshot := <-t.updateCh:
			t.mu.Lock()
			t.snapshot = snapshot
			t.mu.Unlock()

			now := time.Now()
			if now.Sub(lastRenderTime) >= minRenderInterval {
				t.render()
				lastRenderTime = now
			}
		case <-t.renderTicker.C:
			now := time.Now()
			if now.Sub(lastRenderTime) >= minRenderInterval {
				t.render()
				lastRenderTime = now
			}
		}
	}
}

func (t *NativeTUI) render() {
	t.renderMu.Lock()
	defer t.renderMu.Unlock()

	t.mu.RLock()
	snap := t.snapshot
	t.mu.RUnlock()
	if snap == nil {
		snap = &Snapshot{}
	}

	if t.needsFullClear {
		t.screen.Clear()
		t.needsFullClear = false
	}
	t.clearHeaderArea()

	availableWidth := t.width - 4
	if availableWidth < 60 {
		availableWidth = 60
	}
	leftWidth := availableWidth/2 - 1
	rightWidth := availableWidth/2 - 1

	y := 0
	t.renderHeader(snap, y)
	y += 2
	t.renderLeftWithBorder(snap, leftWidth, 2, y)
	t.renderRightWithBorder(snap, rightWidth, 2+leftWidth+2, y)
	t.screen.Show()
}

func (t *NativeTUI) clearHeaderArea() {
	headerStyle := tcell.StyleDefault.Background(tcell.ColorBlue).Foreground(tcell.ColorWhite)
	for x := 0; x < t.width; x++ {
		if t.height > 0 {
			t.screen.SetContent(x, 0, ' ', nil, headerStyle)
		}
		if t.height > 1 {
			t.screen.SetContent(x, 1, ' ', nil, tcell.StyleDefault)
		}
	}
}

func (t *NativeTUI) renderHeader(snap *Snapshot, y int) {
	style := tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorBlue).Bold(true)

	cycleInfo := ""
	if !snap.CycleEndTime.IsZero() {
		now := time.Now()
		if now.Before(snap.CycleEndTime) {
			remaining := snap.CycleEndTime.Sub(now)
			minutes := int(remaining.Minutes())
			seconds := int(remaining.Seconds()) % 60
			cycleInfo = fmt.Sprintf(" | Cycle End: %dm%02ds", minutes, seconds)
		} else {
			cycleInfo = fmt.Sprintf(" | Cycle End: %s (已结束)", snap.CycleEndTime.Format("15:04:05"))
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

	titleLen := len(title)
	startX := (t.width - titleLen) / 2
	if startX < 0 {
		startX = 0
	}
	for i, r := range title {
		if startX+i < t.width {
			t.screen.SetContent(startX+i, y, r, nil, style)
		}
	}
}

func (t *NativeTUI) renderLeftWithBorder(snap *Snapshot, width, startX, startY int) {
	t.drawBorder(startX, startY, width, t.height-startY-2)
	t.fillRect(startX+1, startY+1, width-2, t.height-startY-4, tcell.StyleDefault)
	t.renderLeft(snap, width-2, startX+1, startY+1)
}

func (t *NativeTUI) renderRightWithBorder(snap *Snapshot, width, startX, startY int) {
	t.drawBorder(startX, startY, width, t.height-startY-2)
	t.fillRect(startX+1, startY+1, width-2, t.height-startY-4, tcell.StyleDefault)
	t.renderRight(snap, width-2, startX+1, startY+1)
}

func (t *NativeTUI) fillRect(x, y, w, h int, style tcell.Style) {
	if w <= 0 || h <= 0 {
		return
	}
	for yy := 0; yy < h; yy++ {
		if y+yy >= t.height {
			break
		}
		for xx := 0; xx < w; xx++ {
			if x+xx >= t.width {
				break
			}
			t.screen.SetContent(x+xx, y+yy, ' ', nil, style)
		}
	}
}

func (t *NativeTUI) drawBorder(x, y, width, height int) {
	borderStyle := tcell.StyleDefault.Foreground(tcell.ColorBlue)
	for i := 0; i < width && x+i < t.width; i++ {
		if y < t.height {
			t.screen.SetContent(x+i, y, '─', nil, borderStyle)
		}
		if y+height < t.height {
			t.screen.SetContent(x+i, y+height, '─', nil, borderStyle)
		}
	}
	for i := 0; i < height && y+i < t.height; i++ {
		if x < t.width {
			t.screen.SetContent(x, y+i, '│', nil, borderStyle)
		}
		if x+width-1 < t.width {
			t.screen.SetContent(x+width-1, y+i, '│', nil, borderStyle)
		}
	}
	if x < t.width && y < t.height {
		t.screen.SetContent(x, y, '┌', nil, borderStyle)
	}
	if x+width-1 < t.width && y < t.height {
		t.screen.SetContent(x+width-1, y, '┐', nil, borderStyle)
	}
	if x < t.width && y+height < t.height {
		t.screen.SetContent(x, y+height, '└', nil, borderStyle)
	}
	if x+width-1 < t.width && y+height < t.height {
		t.screen.SetContent(x+width-1, y+height, '┘', nil, borderStyle)
	}
}

func (t *NativeTUI) renderSection(snap *Snapshot, title string, x, y, width int, contentFunc func(*Snapshot, int) int) int {
	titleStyle := tcell.StyleDefault.Foreground(tcell.ColorWhite).Bold(true)
	t.renderText(x+1, y, title, tcell.ColorWhite, titleStyle)
	y++
	line := strings.Repeat("─", width-4)
	t.renderText(x+1, y, line, tcell.ColorDefault)
	y++
	y = contentFunc(snap, y)
	y++
	return y
}

func (t *NativeTUI) renderText(x, y int, text string, color tcell.Color, styles ...tcell.Style) {
	style := tcell.StyleDefault.Foreground(color)
	if len(styles) > 0 {
		style = styles[0]
	}
	if y >= t.height {
		return
	}
	pos := 0
	lastBaseX := -1
	var lastBaseRune rune
	var lastStyle tcell.Style
	var combining []rune
	for _, r := range text {
		if x+pos >= t.width {
			break
		}
		w := runewidth.RuneWidth(r)
		if w == 0 {
			if lastBaseX >= 0 {
				combining = append(combining, r)
				t.screen.SetContent(lastBaseX, y, lastBaseRune, combining, lastStyle)
			}
			continue
		}
		lastBaseX = x + pos
		lastBaseRune = r
		lastStyle = style
		combining = combining[:0]
		t.screen.SetContent(lastBaseX, y, lastBaseRune, nil, lastStyle)
		pos += w
	}
}

func (t *NativeTUI) renderLeft(snap *Snapshot, width, startX, startY int) {
	y := startY
	x := startX
	y = t.renderSection(snap, "Price", x, y, width, func(snap *Snapshot, y int) int {
		yesSpread := snap.YesAsk - snap.YesBid
		noSpread := snap.NoAsk - snap.NoBid
		t.renderText(x+1, y, fmt.Sprintf("UP   Price:%7.4f Bid:%7.4f Ask:%7.4f Spread:%6.4f",
			snap.YesPrice, snap.YesBid, snap.YesAsk, yesSpread), tcell.ColorDefault)
		y++
		t.renderText(x+1, y, fmt.Sprintf("DOWN Price:%7.4f Bid:%7.4f Ask:%7.4f Spread:%6.4f",
			snap.NoPrice, snap.NoBid, snap.NoAsk, noSpread), tcell.ColorDefault)
		return y + 1
	})
	y = t.renderSection(snap, "Velocity", x, y, width, func(snap *Snapshot, y int) int {
		t.renderText(x+1, y, fmt.Sprintf("UP   Vel:%7.3f c/s Move:%3d c", snap.UpVelocity, snap.UpMove), tcell.ColorDefault)
		y++
		t.renderText(x+1, y, fmt.Sprintf("DOWN Vel:%7.3f c/s Move:%3d c", snap.DownVelocity, snap.DownMove), tcell.ColorDefault)
		y++
		if snap.Direction != "" {
			t.renderText(x+1, y, fmt.Sprintf("Direction: %s", snap.Direction), tcell.ColorYellow)
		} else {
			t.renderText(x+1, y, "Direction: -", tcell.ColorDefault)
		}
		return y + 1
	})
	y = t.renderSection(snap, "Positions", x, y, width, func(snap *Snapshot, y int) int {
		t.renderText(x+1, y, fmt.Sprintf("UP   Size:%8.4f Cost:$%7.4f Avg:%7.4f",
			snap.UpSize, snap.UpCost, snap.UpAvgPrice), tcell.ColorDefault)
		y++
		t.renderText(x+1, y, fmt.Sprintf("DOWN Size:%8.4f Cost:$%7.4f Avg:%7.4f",
			snap.DownSize, snap.DownCost, snap.DownAvgPrice), tcell.ColorDefault)
		y++
		if snap.IsHedged {
			t.renderText(x+1, y, "Status: ✅ Hedged", tcell.ColorGreen)
		} else {
			t.renderText(x+1, y, "Status: ⚠️ Not Hedged", tcell.ColorRed)
		}
		return y + 1
	})
	if snap.DecisionConditions != nil {
		y = t.renderDecisionConditions(snap, x, y, width)
	}
}

func (t *NativeTUI) renderRight(snap *Snapshot, width, startX, startY int) {
	y := startY
	x := startX
	y = t.renderSection(snap, "Profit", x, y, width, func(snap *Snapshot, y int) int {
		t.renderText(x+1, y, fmt.Sprintf("Cost:$%7.4f UP:$%7.4f DOWN:$%7.4f",
			snap.TotalCost, snap.ProfitIfUpWin, snap.ProfitIfDownWin), tcell.ColorDefault)
		y++
		if snap.IsProfitLocked {
			t.renderText(x+1, y, "Status: ✅ Locked", tcell.ColorGreen)
		} else {
			t.renderText(x+1, y, "Status: ⚠️ Not Locked", tcell.ColorRed)
		}
		return y + 1
	})
	y = t.renderSection(snap, "Trading Stats", x, y, width, func(snap *Snapshot, y int) int {
		if !snap.LastTriggerTime.IsZero() {
			elapsed := time.Since(snap.LastTriggerTime)
			t.renderText(x+1, y, fmt.Sprintf("Trades:%d Last:%s ago", snap.TradesThisCycle, formatDuration(elapsed)), tcell.ColorDefault)
		} else {
			t.renderText(x+1, y, fmt.Sprintf("Trades:%d Last:-", snap.TradesThisCycle), tcell.ColorDefault)
		}
		y++
		if snap.MarketCooldownRemainingSec > 0 {
			reason := snap.MarketCooldownReason
			if strings.TrimSpace(reason) == "" {
				reason = "cooldown"
			}
			t.renderText(x+1, y, fmt.Sprintf("Cooldown:%.0fs (%s)", snap.MarketCooldownRemainingSec, truncate(reason, 18)), tcell.ColorYellow)
			y++
		}
		return y
	})
	y = t.renderSection(snap, "Orders", x, y, width, func(snap *Snapshot, y int) int {
		t.renderText(x+1, y, fmt.Sprintf("Hedges:%d Open:%d", snap.PendingHedges, snap.OpenOrders), tcell.ColorDefault)
		y++
		// 运行指标（紧凑展示）
		if snap.OMSQueueLen > 0 || snap.HedgeEWMASec > 0 || snap.ReorderBudgetSkips > 0 || snap.FAKBudgetWarnings > 0 {
			t.renderText(x+1, y, fmt.Sprintf("Queue:%d EWMA:%.1fs RS:%d FAK:%d",
				snap.OMSQueueLen, snap.HedgeEWMASec, snap.ReorderBudgetSkips, snap.FAKBudgetWarnings), tcell.ColorDefault)
			y++
		}
		return y
	})
	if snap.RiskManagement != nil {
		y = t.renderRiskManagement(snap, x, y, width)
	}
	y = t.renderCapitalOps(snap, x, y, width)
}

func (t *NativeTUI) renderDecisionConditions(snap *Snapshot, x, y, width int) int {
	dc := snap.DecisionConditions
	if dc == nil {
		return y
	}
	y = t.renderSection(snap, "Decision Conditions", x, y, width, func(snap *Snapshot, y int) int {
		if dc.CanTrade {
			t.renderText(x+1, y, "✅ Can Trade", tcell.ColorGreen)
		} else {
			t.renderText(x+1, y, fmt.Sprintf("❌ Cannot Trade: %s", dc.BlockReason), tcell.ColorRed)
		}
		return y + 1
	})
	return y
}

func (t *NativeTUI) renderRiskManagement(snap *Snapshot, x, y, width int) int {
	rm := snap.RiskManagement
	if rm == nil {
		return y
	}
	y = t.renderSection(snap, "Risk Management", x, y, width, func(snap *Snapshot, y int) int {
		unhedged := make([]RiskExposureInfo, 0, len(rm.RiskExposures))
		for _, exp := range rm.RiskExposures {
			if exp.HedgeStatus != "Filled" {
				unhedged = append(unhedged, exp)
			}
		}
		if len(unhedged) > 0 {
			t.renderText(x+1, y, fmt.Sprintf("⚠️ Exposures: %d", len(unhedged)), tcell.ColorRed)
			y++
		} else {
			t.renderText(x+1, y, "✅ No Exposures", tcell.ColorGreen)
			y++
		}
		if rm.CurrentAction != "idle" && rm.CurrentAction != "" {
			t.renderText(x+1, y, fmt.Sprintf("🔄 Action: %s", rm.CurrentAction), tcell.ColorYellow)
			y++
		} else {
			t.renderText(x+1, y, "✅ Idle", tcell.ColorGreen)
			y++
		}
		return y
	})
	return y
}

func (t *NativeTUI) renderCapitalOps(snap *Snapshot, x, y, width int) int {
	y = t.renderSection(snap, "Capital Ops", x, y, width, func(snap *Snapshot, y int) int {
		mergeIcon := "⏸️"
		switch snap.MergeStatus {
		case "merging":
			mergeIcon = "🔄"
		case "completed":
			mergeIcon = "✅"
		case "failed":
			mergeIcon = "❌"
		}
		t.renderText(x+1, y, fmt.Sprintf("Merge:%s %s", mergeIcon, snap.MergeStatus), tcell.ColorDefault)
		y++
		redeemIcon := "⏸️"
		switch snap.RedeemStatus {
		case "redeeming":
			redeemIcon = "🔄"
		case "completed":
			redeemIcon = "✅"
		case "failed":
			redeemIcon = "❌"
		}
		t.renderText(x+1, y, fmt.Sprintf("Redeem:%s %s", redeemIcon, snap.RedeemStatus), tcell.ColorDefault)
		return y + 1
	})
	return y
}

