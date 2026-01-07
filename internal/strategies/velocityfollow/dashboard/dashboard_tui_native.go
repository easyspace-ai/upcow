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
	screen       tcell.Screen
	snapshot     *Snapshot
	mu           sync.RWMutex
	renderMu     sync.Mutex
	needsFullClear bool
	updateCh     chan *Snapshot
	stopCh       chan struct{}
	renderTicker *time.Ticker
	width        int
	height       int
	exitCallback func() // 退出回调函数（当收到退出信号时调用）
}

// NewNativeTUI 创建新的原生TUI
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
		// 只保留最新快照：避免 backlog 导致“切周期 UI 不同步”
		updateCh:     make(chan *Snapshot, 1),
		stopCh:       make(chan struct{}),
		renderTicker: time.NewTicker(500 * time.Millisecond), // 500ms 刷新频率（进一步降低刷新频率，减少闪烁）
		needsFullClear: true,
	}

	// 获取初始屏幕尺寸
	tui.width, tui.height = screen.Size()

	return tui, nil
}

// Start 启动原生TUI
// exitCallback: 退出回调函数，当收到退出信号（Ctrl+C等）时调用
func (t *NativeTUI) Start(ctx context.Context, exitCallback func()) error {
	// 保存退出回调
	t.mu.Lock()
	t.exitCallback = exitCallback
	t.mu.Unlock()
	
	if exitCallback == nil {
		nativeLog.Warnf("⚠️ [NativeTUI] 退出回调为 nil，Ctrl+C 可能无法退出")
	} else {
		nativeLog.Infof("✅ [NativeTUI] 已设置退出回调函数")
	}
	
	// 启动事件处理循环
	go t.eventLoop(ctx)
	
	// 启动渲染循环
	go t.renderLoop(ctx)

	return nil
}

// Stop 停止原生TUI
func (t *NativeTUI) Stop() {
	nativeLog.Infof("🛑 [NativeTUI] 正在停止...")
	
	// 关闭停止通道（通知所有 goroutine 退出）
	select {
	case <-t.stopCh:
		// 已经关闭了
	default:
		close(t.stopCh)
	}
	
	// 停止渲染 ticker
	if t.renderTicker != nil {
		t.renderTicker.Stop()
	}
	
	// 关闭屏幕
	if t.screen != nil {
		// 尝试唤醒事件循环，避免 PollEvent/ChannelEvents 卡住
		// 即使失败也不影响 Fini()
		t.screen.PostEvent(tcell.NewEventInterrupt(nil))
		t.screen.Fini()
	}
	
	nativeLog.Infof("🛑 [NativeTUI] 已停止")
}

// UpdateSnapshot 更新快照
func (t *NativeTUI) UpdateSnapshot(snapshot *Snapshot) {
	if snapshot == nil {
		nativeLog.Warnf("⚠️ [NativeTUI] UpdateSnapshot 收到 nil 快照")
		return
	}
	
	// 深拷贝快照，避免引用问题
	// 这样可以确保即使原始快照被修改，TUI 中的快照也不会受影响
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
		MergeCount:        snapshot.MergeCount,
		MergeStatus:        snapshot.MergeStatus,
		MergeAmount:        snapshot.MergeAmount,
		MergeTxHash:        snapshot.MergeTxHash,
		LastMergeTime:      snapshot.LastMergeTime,
		RedeemCount:        snapshot.RedeemCount,
		RedeemStatus:       snapshot.RedeemStatus,
		LastRedeemTime:     snapshot.LastRedeemTime,
		CycleEndTime:       snapshot.CycleEndTime,
		CycleRemainingSec:  snapshot.CycleRemainingSec,
	}
	
	// 深拷贝 RiskManagement
	if snapshot.RiskManagement != nil {
		riskExposures := make([]RiskExposureInfo, len(snapshot.RiskManagement.RiskExposures))
		copy(riskExposures, snapshot.RiskManagement.RiskExposures)
		newSnapshot.RiskManagement = &RiskManagementStatus{
			RiskExposuresCount:    snapshot.RiskManagement.RiskExposuresCount,
			RiskExposures:         riskExposures,
			CurrentAction:         snapshot.RiskManagement.CurrentAction,
			CurrentActionEntry:     snapshot.RiskManagement.CurrentActionEntry,
			CurrentActionHedge:     snapshot.RiskManagement.CurrentActionHedge,
			CurrentActionTime:     snapshot.RiskManagement.CurrentActionTime,
			CurrentActionDesc:      snapshot.RiskManagement.CurrentActionDesc,
			TotalReorders:         snapshot.RiskManagement.TotalReorders,
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
	
	// 深拷贝 DecisionConditions
	if snapshot.DecisionConditions != nil {
		newSnapshot.DecisionConditions = &DecisionConditions{
			UpVelocityOK:       snapshot.DecisionConditions.UpVelocityOK,
			UpVelocityValue:    snapshot.DecisionConditions.UpVelocityValue,
			UpMoveOK:          snapshot.DecisionConditions.UpMoveOK,
			UpMoveValue:       snapshot.DecisionConditions.UpMoveValue,
			DownVelocityOK:    snapshot.DecisionConditions.DownVelocityOK,
			DownVelocityValue: snapshot.DecisionConditions.DownVelocityValue,
			DownMoveOK:        snapshot.DecisionConditions.DownMoveOK,
			DownMoveValue:     snapshot.DecisionConditions.DownMoveValue,
			Direction:         snapshot.DecisionConditions.Direction,
			EntryPriceOK:      snapshot.DecisionConditions.EntryPriceOK,
			EntryPriceValue:   snapshot.DecisionConditions.EntryPriceValue,
			EntryPriceMin:     snapshot.DecisionConditions.EntryPriceMin,
			EntryPriceMax:     snapshot.DecisionConditions.EntryPriceMax,
			TotalCostOK:       snapshot.DecisionConditions.TotalCostOK,
			TotalCostValue:    snapshot.DecisionConditions.TotalCostValue,
			HedgePriceOK:      snapshot.DecisionConditions.HedgePriceOK,
			HedgePriceValue:   snapshot.DecisionConditions.HedgePriceValue,
			HasUnhedgedRisk:   snapshot.DecisionConditions.HasUnhedgedRisk,
			IsProfitLocked:    snapshot.DecisionConditions.IsProfitLocked,
			ProfitIfUpWin:     snapshot.DecisionConditions.ProfitIfUpWin,
			ProfitIfDownWin:   snapshot.DecisionConditions.ProfitIfDownWin,
			CooldownOK:        snapshot.DecisionConditions.CooldownOK,
			CooldownRemaining: snapshot.DecisionConditions.CooldownRemaining,
			WarmupOK:          snapshot.DecisionConditions.WarmupOK,
			WarmupRemaining:   snapshot.DecisionConditions.WarmupRemaining,
			TradesLimitOK:     snapshot.DecisionConditions.TradesLimitOK,
			TradesThisCycle:   snapshot.DecisionConditions.TradesThisCycle,
			MaxTradesPerCycle: snapshot.DecisionConditions.MaxTradesPerCycle,
			MarketValid:       snapshot.DecisionConditions.MarketValid,
			HasPendingHedge:   snapshot.DecisionConditions.HasPendingHedge,
			CanTrade:          snapshot.DecisionConditions.CanTrade,
			BlockReason:       snapshot.DecisionConditions.BlockReason,
		}
	}
	
	// 更新快照并发送到 channel 触发渲染（非阻塞）
	// 注意：不要基于 DecisionConditions 做“早退优化”，否则会漏掉 MarketSlug/周期/价格等顶层字段变化，
	// 导致“切周期 UI 不同步 / UI 数据卡住”。渲染频率由 renderLoop 的限流负责。
	t.mu.Lock()
	t.snapshot = newSnapshot
	t.mu.Unlock()
	
	// 发送到 channel 触发渲染（非阻塞）
	// 关键修复：发送深拷贝的快照，而不是原始快照
	// 注意：不在这里立即调用 render()，避免双重渲染导致闪烁
	// renderLoop 会从 channel 接收快照并触发渲染，renderTicker 也会定期渲染
	// 只保留最新：先 drain，再发送
	drained := false
	for !drained {
		select {
		case <-t.updateCh:
		default:
			drained = true
		}
	}
	select {
	case t.updateCh <- newSnapshot:
		nativeLog.Debugf("✅ [NativeTUI] 已发送快照到 channel: market=%s", newSnapshot.MarketSlug)
	default:
		// 理论上不应发生（buffer=1 且已 drain），兜底交给 renderTicker
		nativeLog.Warnf("⚠️ [NativeTUI] 更新快照失败（channel 满）: market=%s", newSnapshot.MarketSlug)
	}
}

// eventLoop 事件处理循环
func (t *NativeTUI) eventLoop(ctx context.Context) {
	// 使用 tcell 的 ChannelEvents，避免 PollEvent goroutine 在 Stop 时卡死
	eventCh := make(chan tcell.Event, 32)
	go t.screen.ChannelEvents(eventCh, t.stopCh)
	
	for {
		select {
		case <-ctx.Done():
			nativeLog.Infof("🛑 [NativeTUI] 收到 context 取消信号，退出事件循环")
			t.Stop()
			return
		case <-t.stopCh:
			nativeLog.Infof("🛑 [NativeTUI] 收到停止信号，退出事件循环")
			return
		case ev := <-eventCh:
			// 处理键盘事件
			if ev == nil {
				continue
			}
			switch ev := ev.(type) {
			case *tcell.EventKey:
				// 检查各种退出按键
				// 关键修复：正确检测 Ctrl+C
				// tcell 中 Ctrl+C 的检测方式：
				// - ev.Key() == tcell.KeyCtrlC 或
				// - ev.Modifiers() 包含 Ctrl 且 Rune 为 c/C（某些终端）
				// - ev.Rune() == 3（Ctrl+C 的 ASCII 码）
				if ev.Key() == tcell.KeyEscape || 
					ev.Key() == tcell.KeyCtrlC || 
					(ev.Modifiers()&tcell.ModCtrl != 0 && (ev.Rune() == 'c' || ev.Rune() == 'C')) ||
					ev.Rune() == 3 || // Ctrl+C 的 ASCII 码
					ev.Rune() == 'q' || ev.Rune() == 'Q' {
					// 退出
					nativeLog.Infof("🛑 [NativeTUI] 收到退出按键: key=%v rune=%c，退出事件循环", ev.Key(), ev.Rune())

					// 主动向自己发送 SIGINT，确保外层主程序能收到（tcell 有时会拦截 Ctrl+C）。
					_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
					
					// 调用退出回调，通知 Dashboard
					t.mu.RLock()
					callback := t.exitCallback
					t.mu.RUnlock()
					if callback != nil {
						nativeLog.Infof("🛑 [NativeTUI] 调用退出回调")
						callback()
					} else {
						nativeLog.Warnf("⚠️ [NativeTUI] 退出回调为 nil")
					}
					// 立刻恢复终端并停止渲染，避免“按了 Ctrl+C 但程序看起来不退出/终端异常”
					t.Stop()
					return
				}
			case *tcell.EventResize:
				// 屏幕尺寸变化
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

// renderLoop 渲染循环
func (t *NativeTUI) renderLoop(ctx context.Context) {
	// 用于跟踪上次渲染的时间，避免过于频繁的渲染
	lastRenderTime := time.Now()
	minRenderInterval := 200 * time.Millisecond // 最小渲染间隔 200ms
	
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stopCh:
			return
		case snapshot := <-t.updateCh:
			// 更新快照
			t.mu.Lock()
			t.snapshot = snapshot
			t.mu.Unlock()
			
			// 检查是否需要渲染（避免过于频繁）
			now := time.Now()
			if now.Sub(lastRenderTime) >= minRenderInterval {
				// 立即渲染
				t.render()
				lastRenderTime = now
				nativeLog.Debugf("✅ [NativeTUI] 已更新快照并渲染: market=%s", snapshot.MarketSlug)
			} else {
				// 太频繁了，跳过这次渲染，等待下次 ticker
				nativeLog.Debugf("⏸️ [NativeTUI] 渲染过于频繁，跳过: interval=%v", now.Sub(lastRenderTime))
			}
		case <-t.renderTicker.C:
			// 定期渲染（用于倒计时等动态内容）
			// 关键修复：增加渲染间隔检查，避免过于频繁
			now := time.Now()
			if now.Sub(lastRenderTime) >= minRenderInterval {
				t.render()
				lastRenderTime = now
			}
		}
	}
}

// render 渲染UI
func (t *NativeTUI) render() {
	t.renderMu.Lock()
	defer t.renderMu.Unlock()

	t.mu.RLock()
	snap := t.snapshot
	t.mu.RUnlock()

	if snap == nil {
		snap = &Snapshot{}
	}

	// 仅在必要时全屏 Clear（例如 resize）。避免每次都 Clear 导致明显闪烁。
	if t.needsFullClear {
		t.screen.Clear()
		t.needsFullClear = false
	}

	// 避免每次全屏 Clear（会导致明显闪烁）。改为只清理会被覆盖的区域。
	t.clearHeaderArea()

	// 计算布局
	availableWidth := t.width - 4
	if availableWidth < 60 {
		availableWidth = 60
	}
	leftWidth := availableWidth/2 - 1
	rightWidth := availableWidth/2 - 1

	// 渲染标题
	y := 0
	t.renderHeader(snap, y)
	y += 2

	// 渲染左侧内容（带边框）
	t.renderLeftWithBorder(snap, leftWidth, 2, y)

	// 渲染右侧内容（带边框）
	t.renderRightWithBorder(snap, rightWidth, 2+leftWidth+2, y)

	// 显示
	t.screen.Show()
}

func (t *NativeTUI) clearHeaderArea() {
	// 标题行背景是蓝色，必须把整行填满，避免残影
	headerStyle := tcell.StyleDefault.Background(tcell.ColorBlue).Foreground(tcell.ColorWhite)
	for x := 0; x < t.width; x++ {
		if t.height > 0 {
			t.screen.SetContent(x, 0, ' ', nil, headerStyle)
		}
		// 预留的空行也清一下，避免上一次的内容残留
		if t.height > 1 {
			t.screen.SetContent(x, 1, ' ', nil, tcell.StyleDefault)
		}
	}
}

// renderHeader 渲染标题
func (t *NativeTUI) renderHeader(snap *Snapshot, y int) {
	style := tcell.StyleDefault.
		Foreground(tcell.ColorWhite).
		Background(tcell.ColorBlue).
		Bold(true)

	// 实时计算周期倒计时（基于 CycleEndTime 和当前时间）
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

	// 渲染标题（居中）
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

// renderLeftWithBorder 渲染左侧内容（带边框）
func (t *NativeTUI) renderLeftWithBorder(snap *Snapshot, width, startX, startY int) {
	// 绘制边框
	t.drawBorder(startX, startY, width, t.height-startY-2)
	// 清理边框内部区域，避免不 Clear 时残留旧字符
	t.fillRect(startX+1, startY+1, width-2, t.height-startY-4, tcell.StyleDefault)
	
	// 渲染内容（内容区域在边框内）
	t.renderLeft(snap, width-2, startX+1, startY+1)
}

// renderRightWithBorder 渲染右侧内容（带边框）
func (t *NativeTUI) renderRightWithBorder(snap *Snapshot, width, startX, startY int) {
	// 绘制边框
	t.drawBorder(startX, startY, width, t.height-startY-2)
	// 清理边框内部区域，避免不 Clear 时残留旧字符
	t.fillRect(startX+1, startY+1, width-2, t.height-startY-4, tcell.StyleDefault)
	
	// 渲染内容（内容区域在边框内）
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

// drawBorder 绘制边框
func (t *NativeTUI) drawBorder(x, y, width, height int) {
	borderStyle := tcell.StyleDefault.Foreground(tcell.ColorBlue)
	
	// 绘制上边框
	for i := 0; i < width && x+i < t.width; i++ {
		if y < t.height {
			t.screen.SetContent(x+i, y, '─', nil, borderStyle)
		}
	}
	
	// 绘制下边框
	for i := 0; i < width && x+i < t.width; i++ {
		if y+height < t.height {
			t.screen.SetContent(x+i, y+height, '─', nil, borderStyle)
		}
	}
	
	// 绘制左边框
	for i := 0; i < height && y+i < t.height; i++ {
		if x < t.width {
			t.screen.SetContent(x, y+i, '│', nil, borderStyle)
		}
	}
	
	// 绘制右边框
	for i := 0; i < height && y+i < t.height; i++ {
		if x+width-1 < t.width {
			t.screen.SetContent(x+width-1, y+i, '│', nil, borderStyle)
		}
	}
	
	// 绘制四个角
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

// renderLeft 渲染左侧内容
func (t *NativeTUI) renderLeft(snap *Snapshot, width, startX, startY int) {
	y := startY
	x := startX

	// 价格表
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

	// 速度信息
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

	// 持仓信息
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

	// 决策条件
	if snap.DecisionConditions != nil {
		y = t.renderDecisionConditions(snap, x, y, width)
	}
}

// renderRight 渲染右侧内容
func (t *NativeTUI) renderRight(snap *Snapshot, width, startX, startY int) {
	y := startY
	x := startX

	// 盈利信息
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

	// 交易统计
	y = t.renderSection(snap, "Trading Stats", x, y, width, func(snap *Snapshot, y int) int {
		if !snap.LastTriggerTime.IsZero() {
			elapsed := time.Since(snap.LastTriggerTime)
			t.renderText(x+1, y, fmt.Sprintf("Trades:%d Last:%s ago", snap.TradesThisCycle, formatDuration(elapsed)), tcell.ColorDefault)
		} else {
			t.renderText(x+1, y, fmt.Sprintf("Trades:%d Last:-", snap.TradesThisCycle), tcell.ColorDefault)
		}
		return y + 1
	})

	// 订单状态
	y = t.renderSection(snap, "Orders", x, y, width, func(snap *Snapshot, y int) int {
		t.renderText(x+1, y, fmt.Sprintf("Hedges:%d Open:%d", snap.PendingHedges, snap.OpenOrders), tcell.ColorDefault)
		return y + 1
	})

	// 风控状态
	if snap.RiskManagement != nil {
		y = t.renderRiskManagement(snap, x, y, width)
	}

	// 合并和赎回状态
	y = t.renderCapitalOps(snap, x, y, width)
}

// renderSection 渲染一个区块
func (t *NativeTUI) renderSection(snap *Snapshot, title string, x, y, width int, contentFunc func(*Snapshot, int) int) int {
	// 渲染标题
	titleStyle := tcell.StyleDefault.
		Foreground(tcell.ColorWhite).
		Bold(true)
	t.renderText(x+1, y, title, tcell.ColorWhite, titleStyle)
	y++

	// 渲染分隔线
	line := strings.Repeat("─", width-4)
	t.renderText(x+1, y, line, tcell.ColorDefault)
	y++

	// 渲染内容
	y = contentFunc(snap, y)
	y++

	return y
}

// renderText 渲染文本
func (t *NativeTUI) renderText(x, y int, text string, color tcell.Color, styles ...tcell.Style) {
	style := tcell.StyleDefault.Foreground(color)
	if len(styles) > 0 {
		style = styles[0]
	}

	// 关键修复：正确处理宽字符/组合字符（emoji、变体选择符等）
	// 否则会出现错位、残影，甚至 “NNotLLocked / NNotHHedged” 这类重复首字母现象。
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
			// 组合字符（例如 VS16），追加到上一个 base rune
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

// renderDecisionConditions 渲染决策条件
func (t *NativeTUI) renderDecisionConditions(snap *Snapshot, x, y, width int) int {
	dc := snap.DecisionConditions
	if dc == nil {
		return y
	}

	y = t.renderSection(snap, "Decision Conditions", x, y, width, func(snap *Snapshot, y int) int {
		// 总体状态
		if dc.CanTrade {
			t.renderText(x+1, y, "✅ Can Trade", tcell.ColorGreen)
		} else {
			t.renderText(x+1, y, fmt.Sprintf("❌ Cannot Trade: %s", dc.BlockReason), tcell.ColorRed)
		}
		y++

		// 速度条件
		upVelStatus := "❌"
		if dc.UpVelocityOK && dc.UpMoveOK {
			upVelStatus = "✅"
		}
		downVelStatus := "❌"
		if dc.DownVelocityOK && dc.DownMoveOK {
			downVelStatus = "✅"
		}
		t.renderText(x+1, y, fmt.Sprintf("Velocity: UP%s(%.3f/%d) DOWN%s(%.3f/%d) Dir:%s",
			upVelStatus, dc.UpVelocityValue, dc.UpMoveValue,
			downVelStatus, dc.DownVelocityValue, dc.DownMoveValue,
			dc.Direction), tcell.ColorDefault)
		y++

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
		t.renderText(x+1, y, fmt.Sprintf("Price: Entry%s(%.4f) Hedge%s(%.4f) Cost%s(%.4f)",
			entryStatus, dc.EntryPriceValue,
			hedgeStatus, dc.HedgePriceValue,
			totalCostStatus, dc.TotalCostValue), tcell.ColorDefault)
		y++

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
		t.renderText(x+1, y, fmt.Sprintf("Cycle: Cooldown%s Warmup%s Trades%s",
			cooldownStatus, warmupStatus, tradesStatus), tcell.ColorDefault)
		y++

		// 持仓条件
		hedgeRiskStatus := "✅"
		if dc.HasPendingHedge {
			hedgeRiskStatus = "❌"
		}
		profitStatus := "❌"
		if dc.IsProfitLocked {
			profitStatus = "✅"
		}
		t.renderText(x+1, y, fmt.Sprintf("Position: Hedge%s Profit%s(UP:%.4f DOWN:%.4f)",
			hedgeRiskStatus, profitStatus, dc.ProfitIfUpWin, dc.ProfitIfDownWin), tcell.ColorDefault)
		return y + 1
	})

	return y
}

// renderRiskManagement 渲染风控状态
func (t *NativeTUI) renderRiskManagement(snap *Snapshot, x, y, width int) int {
	rm := snap.RiskManagement
	if rm == nil {
		return y
	}

	y = t.renderSection(snap, "Risk Management", x, y, width, func(snap *Snapshot, y int) int {
		// 风险敞口数量
		unhedgedExposures := make([]RiskExposureInfo, 0, len(rm.RiskExposures))
		for _, exp := range rm.RiskExposures {
			if exp.HedgeStatus != "Filled" {
				unhedgedExposures = append(unhedgedExposures, exp)
			}
		}

		if len(unhedgedExposures) > 0 {
			t.renderText(x+1, y, fmt.Sprintf("⚠️ Exposures: %d", len(unhedgedExposures)), tcell.ColorRed)
			y++
			for i, exp := range unhedgedExposures {
				if i >= 3 {
					t.renderText(x+1, y, fmt.Sprintf("  ... and %d more", len(unhedgedExposures)-3), tcell.ColorDefault)
					y++
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
				countdownInfo := fmt.Sprintf("倒计时:%s", countdownStr)
				t.renderText(x+1, y, fmt.Sprintf("  %s%s%s", entryInfo, priceInfo, countdownInfo), tcell.ColorDefault)
				y++
			}
		} else {
			t.renderText(x+1, y, "✅ No Exposures", tcell.ColorGreen)
			y++
		}

		// 当前操作状态
		if rm.CurrentAction != "idle" && rm.CurrentAction != "" {
			actionIcon := "🔄"
			actionColor := tcell.ColorYellow
			switch rm.CurrentAction {
			case "canceling":
				actionIcon = "🛑"
				actionColor = tcell.ColorRed
			case "reordering":
				actionIcon = "🔄"
				actionColor = tcell.ColorYellow
			case "aggressive_hedging":
				actionIcon = "🚨"
				actionColor = tcell.ColorRed
			case "fak_eating":
				actionIcon = "⚡"
				actionColor = tcell.ColorRed
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
			t.renderText(x+1, y, actionLine, actionColor)
			y++

			if rm.CurrentActionEntry != "" {
				t.renderText(x+1, y, fmt.Sprintf("  Entry:%s Hedge:%s",
					truncate(rm.CurrentActionEntry, 8), truncate(rm.CurrentActionHedge, 8)), tcell.ColorDefault)
				y++
			}

			// 显示调价详情（如果正在调价）
			if rm.CurrentAction == "reordering" && rm.RepriceOldPriceCents > 0 {
				y++
				t.renderText(x+1, y, "💰 调价详情:", tcell.ColorYellow)
				y++
				t.renderText(x+1, y, fmt.Sprintf("  原价格: %dc", rm.RepriceOldPriceCents), tcell.ColorDefault)
				y++
				t.renderText(x+1, y, fmt.Sprintf("  新价格: %dc", rm.RepriceNewPriceCents), tcell.ColorDefault)
				y++
				if rm.RepricePriceChangeCents != 0 {
					changeSign := "+"
					if rm.RepricePriceChangeCents < 0 {
						changeSign = ""
					}
					t.renderText(x+1, y, fmt.Sprintf("  价格变化: %s%dc", changeSign, rm.RepricePriceChangeCents), tcell.ColorDefault)
					y++
				}
				if rm.RepriceStrategy != "" {
					t.renderText(x+1, y, fmt.Sprintf("  策略: %s", rm.RepriceStrategy), tcell.ColorDefault)
					y++
				}
				if rm.RepriceEntryCostCents > 0 {
					t.renderText(x+1, y, fmt.Sprintf("  Entry成本: %dc", rm.RepriceEntryCostCents), tcell.ColorDefault)
					y++
				}
				if rm.RepriceMarketAskCents > 0 {
					t.renderText(x+1, y, fmt.Sprintf("  市场ask: %dc", rm.RepriceMarketAskCents), tcell.ColorDefault)
					y++
				}
				if rm.RepriceIdealPriceCents > 0 {
					t.renderText(x+1, y, fmt.Sprintf("  理想价格: %dc", rm.RepriceIdealPriceCents), tcell.ColorDefault)
					y++
				}
				if rm.RepriceTotalCostCents > 0 {
					t.renderText(x+1, y, fmt.Sprintf("  总成本: %dc", rm.RepriceTotalCostCents), tcell.ColorDefault)
					y++
				}
				if rm.RepriceProfitCents != 0 {
					profitColor := tcell.ColorGreen
					if rm.RepriceProfitCents < 0 {
						profitColor = tcell.ColorRed
					}
					t.renderText(x+1, y, fmt.Sprintf("  利润: %dc", rm.RepriceProfitCents), profitColor)
					y++
				}
			}
		} else {
			t.renderText(x+1, y, "✅ Idle", tcell.ColorGreen)
			y++
		}

		// 统计信息
		if rm.TotalReorders > 0 || rm.TotalAggressiveHedges > 0 || rm.TotalFakEats > 0 {
			y++
			t.renderText(x+1, y, fmt.Sprintf("Stats: Reorders:%d Aggressive:%d FAK:%d",
				rm.TotalReorders, rm.TotalAggressiveHedges, rm.TotalFakEats), tcell.ColorDefault)
			y++
		}

		return y
	})

	return y
}

// renderCapitalOps 渲染合并和赎回状态
func (t *NativeTUI) renderCapitalOps(snap *Snapshot, x, y, width int) int {
	y = t.renderSection(snap, "Capital Ops", x, y, width, func(snap *Snapshot, y int) int {
		// 合并状态
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
		t.renderText(x+1, y, mergeLine, tcell.ColorDefault)
		y++

		// 赎回状态
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
		t.renderText(x+1, y, redeemLine, tcell.ColorDefault)
		return y + 1
	})

	return y
}
