package rangeboth

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/marketspec"
)

// Dashboard 实时终端显示组件
type Dashboard struct {
	tradingService *services.TradingService
	marketSpec     *marketspec.MarketSpec
	strategy       *Strategy // 用于获取波动幅度数据
	updateInterval time.Duration
	ctx            context.Context
	cancel         context.CancelFunc
	mu             sync.Mutex
	running        bool
	firstRender    bool // 是否是首次渲染
	lineCount      int  // 记录上次渲染的行数
}

// NewDashboard 创建新的Dashboard实例
func NewDashboard(tradingService *services.TradingService, marketSpec *marketspec.MarketSpec) *Dashboard {
	ctx, cancel := context.WithCancel(context.Background())
	return &Dashboard{
		tradingService: tradingService,
		marketSpec:     marketSpec,
		updateInterval: 1 * time.Second, // 每秒更新一次
		ctx:            ctx,
		cancel:         cancel,
	}
}

// SetStrategy 设置策略引用（用于获取波动幅度数据）
func (d *Dashboard) SetStrategy(strategy *Strategy) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.strategy = strategy
}

// UpdateMarketSpec 更新市场规格（周期切换时调用）
func (d *Dashboard) UpdateMarketSpec(marketSpec *marketspec.MarketSpec) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.marketSpec = marketSpec
}

// IsRunning 检查Dashboard是否正在运行
func (d *Dashboard) IsRunning() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running
}

// Start 启动Dashboard
func (d *Dashboard) Start() {
	d.mu.Lock()
	
	// 如果已经在运行，先停止（处理周期切换的情况）
	if d.running {
		d.running = false
		if d.cancel != nil {
			d.cancel()
		}
		// 等待一小段时间，让旧的 goroutine 退出
		d.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
		d.mu.Lock()
	}
	
	// 重新创建 context（重要：周期切换时需要新的 context）
	ctx, cancel := context.WithCancel(context.Background())
	d.ctx = ctx
	d.cancel = cancel
	d.running = true
	d.firstRender = true // 重置首次渲染标志
	d.lineCount = 0      // 重置行数计数
	
	d.mu.Unlock()

	go d.loop()
}

// Stop 停止Dashboard
func (d *Dashboard) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.running {
		return
	}
	d.running = false
	if d.cancel != nil {
		d.cancel()
	}
}

// loop 主循环
func (d *Dashboard) loop() {
	ticker := time.NewTicker(d.updateInterval)
	defer ticker.Stop()

	// 首次立即显示
	d.render()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			d.render()
		}
	}
}

// render 渲染显示内容
func (d *Dashboard) render() {
	d.mu.Lock()
	isFirstRender := d.firstRender
	d.mu.Unlock()

	if isFirstRender {
		// 首次渲染：清屏并移动到顶部
		fmt.Print("\033[2J\033[H")
		d.mu.Lock()
		d.firstRender = false
		d.mu.Unlock()
	} else {
		// 后续更新：移动到顶部，不清屏（减少闪烁）
		fmt.Print("\033[H")
	}

	// 获取当前市场信息
	currentMarketSlug := d.tradingService.GetCurrentMarket()

	// 使用缓冲区收集所有输出
	var buf strings.Builder

	// 显示周期信息
	d.renderCycleInfoToBuffer(&buf, currentMarketSlug)

	// 显示波动幅度
	d.renderVolatilityToBuffer(&buf)

	// 显示已成交订单
	d.renderFilledOrdersToBuffer(&buf, currentMarketSlug)

	// 显示未成交挂单
	d.renderPendingOrdersToBuffer(&buf, currentMarketSlug)

	// 显示收益计算
	d.renderProfitToBuffer(&buf, currentMarketSlug)

	// 输出缓冲区内容
	output := buf.String()
	fmt.Print(output)

	// 如果行数减少，清除多余的行
	lines := strings.Count(output, "\n")
	d.mu.Lock()
	if d.lineCount > lines {
		// 清除多余的行
		for i := 0; i < d.lineCount-lines; i++ {
			fmt.Print("\033[K\n") // 清除当前行并换行
		}
		fmt.Print("\033[K") // 清除最后一行
		// 移动回正确位置
		fmt.Printf("\033[%dA", d.lineCount-lines)
	}
	d.lineCount = lines
	d.mu.Unlock()

	// 刷新输出
	os.Stdout.Sync()
}

// renderCycleInfoToBuffer 显示周期信息到缓冲区
func (d *Dashboard) renderCycleInfoToBuffer(buf *strings.Builder, marketSlug string) {
	buf.WriteString("╔════════════════════════════════════════════════════════════════════════════════╗\n")
	buf.WriteString("║                           📊 实时交易监控面板                                  ║\n")
	buf.WriteString("╠════════════════════════════════════════════════════════════════════════════════╣\n")
	
	if marketSlug == "" {
		buf.WriteString("║ 当前周期: 无                                                                    ║\n")
		buf.WriteString("║ 剩余时间: --                                                                  ║\n")
	} else {
		buf.WriteString(fmt.Sprintf("║ 当前周期: %-70s ║\n", marketSlug))
		
		// 计算剩余时间
		var remainingTime string
		if d.marketSpec != nil {
			// 从slug提取时间戳
			timestamp, ok := d.marketSpec.TimestampFromSlug(marketSlug, time.Now())
			if ok && timestamp > 0 {
				cycleDuration := d.marketSpec.Duration()
				cycleEndTime := time.Unix(timestamp, 0).Add(cycleDuration)
				now := time.Now()
				remaining := cycleEndTime.Sub(now)
				
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
		buf.WriteString(fmt.Sprintf("║ 剩余时间: %-70s ║\n", remainingTime))
	}
	
	buf.WriteString("╚════════════════════════════════════════════════════════════════════════════════╝\n")
	buf.WriteString("\n")
}

// renderVolatilityToBuffer 显示波动幅度到缓冲区
func (d *Dashboard) renderVolatilityToBuffer(buf *strings.Builder) {
	buf.WriteString("╔════════════════════════════════════════════════════════════════════════════════╗\n")
	buf.WriteString("║  📊 波动幅度监控                                                               ║\n")
	buf.WriteString("╠════════════════════════════════════════════════════════════════════════════════╣\n")
	
	d.mu.Lock()
	strategy := d.strategy
	d.mu.Unlock()
	
	if strategy == nil {
		buf.WriteString("║ 策略未初始化，无法获取波动数据                                                      ║\n")
		buf.WriteString("╚════════════════════════════════════════════════════════════════════════════════╝\n")
		buf.WriteString("\n")
		return
	}
	
	snapshot := strategy.GetVolatilitySnapshot()
	
	// 显示观察窗口配置
	buf.WriteString(fmt.Sprintf("║ 观察窗口: %d秒 | 最大允许波动: %d分                                              ║\n",
		snapshot.LookbackSeconds, snapshot.MaxRangeCents))
	buf.WriteString("╠════════════════════════════════════════════════════════════════════════════════╣\n")
	
	// 显示UP波动
	upStatus := "❌ 不稳定"
	upColor := "\033[31m" // 红色
	if snapshot.UpStable {
		upStatus = "✅ 稳定"
		upColor = "\033[32m" // 绿色
	}
	resetColor := "\033[0m"
	
	if snapshot.SampleCountUp > 0 {
		buf.WriteString(fmt.Sprintf("║ UP方向:   样本数=%d | 价格范围: %d-%d分 | 波动幅度: %d分 | %s%s%s              ║\n",
			snapshot.SampleCountUp,
			snapshot.UpMinCents,
			snapshot.UpMaxCents,
			snapshot.UpRangeCents,
			upColor, upStatus, resetColor))
	} else {
		buf.WriteString("║ UP方向:   暂无数据                                                              ║\n")
	}
	
	// 显示DOWN波动
	downStatus := "❌ 不稳定"
	downColor := "\033[31m" // 红色
	if snapshot.DownStable {
		downStatus = "✅ 稳定"
		downColor = "\033[32m" // 绿色
	}
	
	if snapshot.SampleCountDown > 0 {
		buf.WriteString(fmt.Sprintf("║ DOWN方向: 样本数=%d | 价格范围: %d-%d分 | 波动幅度: %d分 | %s%s%s            ║\n",
			snapshot.SampleCountDown,
			snapshot.DownMinCents,
			snapshot.DownMaxCents,
			snapshot.DownRangeCents,
			downColor, downStatus, resetColor))
	} else {
		buf.WriteString("║ DOWN方向: 暂无数据                                                            ║\n")
	}
	
	// 显示整体状态
	overallStatus := "❌ 不满足条件"
	overallColor := "\033[31m"
	if snapshot.UpStable && snapshot.DownStable {
		overallStatus = "✅ 满足条件，可以下单"
		overallColor = "\033[32m"
	} else if snapshot.UpStable || snapshot.DownStable {
		overallStatus = "⚠️  仅单边满足条件"
		overallColor = "\033[33m" // 黄色
	}
	
	buf.WriteString("╠════════════════════════════════════════════════════════════════════════════════╣\n")
	buf.WriteString(fmt.Sprintf("║ 整体状态: %s%s%s                                                          ║\n",
		overallColor, overallStatus, resetColor))
	
	buf.WriteString("╚════════════════════════════════════════════════════════════════════════════════╝\n")
	buf.WriteString("\n")
}

// renderFilledOrders 显示已成交订单
func (d *Dashboard) renderFilledOrders(marketSlug string) {
	fmt.Println("╔════════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║  ✅ 已成交订单                                                                   ║")
	fmt.Println("╠════════════════════════════════════════════════════════════════════════════════╣")
	
	filledOrders := make([]*domain.Order, 0)
	
	// 1. 从活跃订单中查找部分成交的订单
	allOrders := d.tradingService.GetActiveOrders()
	for _, order := range allOrders {
		if order == nil {
			continue
		}
		// 只显示当前周期的订单
		if marketSlug != "" && order.MarketSlug != marketSlug {
			continue
		}
		// 显示部分成交的订单
		if order.Status == domain.OrderStatusPartial {
			filledOrders = append(filledOrders, order)
		}
	}
	
	// 2. 从持仓中提取已成交的订单（EntryOrder 和 HedgeOrder）
	positions := d.tradingService.GetOpenPositionsForMarket(marketSlug)
	for _, pos := range positions {
		if pos == nil {
			continue
		}
		// 提取 EntryOrder（如果已成交）
		if pos.EntryOrder != nil && pos.EntryOrder.IsFilled() {
			// 检查是否已经添加过（避免重复）
			exists := false
			for _, o := range filledOrders {
				if o.OrderID == pos.EntryOrder.OrderID {
					exists = true
					break
				}
			}
			if !exists {
				filledOrders = append(filledOrders, pos.EntryOrder)
			}
		}
		// 提取 HedgeOrder（如果已成交）
		if pos.HedgeOrder != nil && pos.HedgeOrder.IsFilled() {
			// 检查是否已经添加过（避免重复）
			exists := false
			for _, o := range filledOrders {
				if o.OrderID == pos.HedgeOrder.OrderID {
					exists = true
					break
				}
			}
			if !exists {
				filledOrders = append(filledOrders, pos.HedgeOrder)
			}
		}
	}
	
	// 按成交时间排序（最新的在前）
	sort.Slice(filledOrders, func(i, j int) bool {
		if filledOrders[i].FilledAt == nil {
			return false
		}
		if filledOrders[j].FilledAt == nil {
			return true
		}
		return filledOrders[i].FilledAt.After(*filledOrders[j].FilledAt)
	})
	
	if len(filledOrders) == 0 {
		fmt.Println("║ 暂无已成交订单                                                                  ║")
	} else {
		fmt.Println("║ 订单ID          │ 方向 │ 价格(分) │ 数量    │ 成交时间                        ║")
		fmt.Println("╠═════════════════╪══════╪══════════╪═════════╪════════════════════════════════╣")
		
		// 最多显示10条
		maxDisplay := len(filledOrders)
		if maxDisplay > 10 {
			maxDisplay = 10
		}
		
		for i := 0; i < maxDisplay; i++ {
			order := filledOrders[i]
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
			
			fmt.Printf("║ %-15s │ %-4s │ %-8s │ %-7s │ %-30s ║\n",
				orderID, tokenType, price, size, filledTime)
		}
		
		if len(filledOrders) > 10 {
			fmt.Printf("║ ... 还有 %d 条订单未显示                                                      ║\n", len(filledOrders)-10)
		}
	}
	
	fmt.Println("╚════════════════════════════════════════════════════════════════════════════════╝")
	fmt.Println()
}

// renderPendingOrders 显示未成交挂单
func (d *Dashboard) renderPendingOrders(marketSlug string) {
	fmt.Println("╔════════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║  ⏳ 未成交挂单                                                                   ║")
	fmt.Println("╠════════════════════════════════════════════════════════════════════════════════╣")
	
	allOrders := d.tradingService.GetActiveOrders()
	pendingOrders := make([]*domain.Order, 0)
	
	for _, order := range allOrders {
		if order == nil {
			continue
		}
		// 只显示当前周期的订单
		if marketSlug != "" && order.MarketSlug != marketSlug {
			continue
		}
		// 显示未成交的订单（Pending, Open, Partial）
		if order.Status == domain.OrderStatusPending ||
			order.Status == domain.OrderStatusOpen ||
			order.Status == domain.OrderStatusPartial {
			pendingOrders = append(pendingOrders, order)
		}
	}
	
	// 按创建时间排序（最新的在前）
	sort.Slice(pendingOrders, func(i, j int) bool {
		return pendingOrders[i].CreatedAt.After(pendingOrders[j].CreatedAt)
	})
	
	if len(pendingOrders) == 0 {
		fmt.Println("║ 暂无未成交挂单                                                                  ║")
	} else {
		fmt.Println("║ 订单ID          │ 方向 │ 价格(分) │ 数量    │ 状态    │ 创建时间                ║")
		fmt.Println("╠═════════════════╪══════╪══════════╪═════════╪══════════╪══════════════════════╣")
		
		for _, order := range pendingOrders {
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
			
			fmt.Printf("║ %-15s │ %-4s │ %-8s │ %-7s │ %-8s │ %-22s ║\n",
				orderID, tokenType, price, size, status, createdTime)
		}
	}
	
	fmt.Println("╚════════════════════════════════════════════════════════════════════════════════╝")
	fmt.Println()
}

// renderProfit 显示收益计算
func (d *Dashboard) renderProfit(marketSlug string) {
	fmt.Println("╔════════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║  💰 收益计算                                                                     ║")
	fmt.Println("╠════════════════════════════════════════════════════════════════════════════════╣")
	
	if marketSlug == "" {
		fmt.Println("║ 当前周期: 无，无法计算收益                                                      ║")
		fmt.Println("╚════════════════════════════════════════════════════════════════════════════╝")
		return
	}
	
	// 获取当前周期的持仓
	positions := d.tradingService.GetOpenPositionsForMarket(marketSlug)
	
	// 计算UP和DOWN的持仓和成本
	var upShares, downShares float64
	var upCost, downCost float64
	
	for _, pos := range positions {
		if pos == nil || !pos.IsOpen() {
			continue
		}
		
		// 使用 Size（当前持仓数量）而不是 TotalFilledSize（累计成交数量）
		// Size 会随着买入/卖出变化，TotalFilledSize 只累加不减少
		currentSize := pos.Size
		if currentSize <= 0 {
			continue // 跳过已清空的持仓
		}
		
		if pos.TokenType == domain.TokenTypeUp {
			upShares += currentSize
			// 计算当前持仓的成本
			// 优先使用 AvgPrice（平均价格），它是基于 CostBasis 和 TotalFilledSize 计算的
			if pos.AvgPrice > 0 && currentSize > 0 {
				// AvgPrice 已经是小数形式（如0.497表示49.7分），直接乘以当前持仓数量
				upCost += pos.AvgPrice * currentSize
			} else if pos.CostBasis > 0 && pos.TotalFilledSize > 0 {
				// 如果没有 AvgPrice，使用 CostBasis 和 TotalFilledSize 计算平均价格
				avgPrice := pos.CostBasis / pos.TotalFilledSize
				upCost += avgPrice * currentSize
			} else if pos.EntryPrice.Pips > 0 && currentSize > 0 {
				// 使用EntryPrice作为fallback（价格是小数形式，如0.497表示49.7分）
				upCost += pos.EntryPrice.ToDecimal() * currentSize
			}
		} else if pos.TokenType == domain.TokenTypeDown {
			downShares += currentSize
			// 计算当前持仓的成本
			if pos.AvgPrice > 0 && currentSize > 0 {
				downCost += pos.AvgPrice * currentSize
			} else if pos.CostBasis > 0 && pos.TotalFilledSize > 0 {
				avgPrice := pos.CostBasis / pos.TotalFilledSize
				downCost += avgPrice * currentSize
			} else if pos.EntryPrice.Pips > 0 && currentSize > 0 {
				downCost += pos.EntryPrice.ToDecimal() * currentSize
			}
		}
	}
	
	// 如果没有持仓，也从已成交订单计算（fallback）
	if upShares == 0 && downShares == 0 {
		allOrders := d.tradingService.GetActiveOrders()
		for _, order := range allOrders {
			if order == nil {
				continue
			}
			if order.MarketSlug != marketSlug {
				continue
			}
			if order.Status != domain.OrderStatusFilled {
				continue
			}
			
			if order.TokenType == domain.TokenTypeUp {
				upShares += order.FilledSize
				if order.FilledPrice != nil {
					// FilledPrice.ToDecimal() 返回小数形式（如0.50表示50分）
					upCost += order.FilledPrice.ToDecimal() * order.FilledSize
				} else if order.Price.Pips > 0 {
					upCost += order.Price.ToDecimal() * order.FilledSize
				}
			} else if order.TokenType == domain.TokenTypeDown {
				downShares += order.FilledSize
				if order.FilledPrice != nil {
					downCost += order.FilledPrice.ToDecimal() * order.FilledSize
				} else if order.Price.Pips > 0 {
					downCost += order.Price.ToDecimal() * order.FilledSize
				}
			}
		}
	}
	
	totalCost := upCost + downCost
	
	// 计算收益
	// 如果UP获胜：收益 = UP持仓 * $1 - 总成本
	// 如果DOWN获胜：收益 = DOWN持仓 * $1 - 总成本
	profitIfUpWin := upShares*1.0 - totalCost
	profitIfDownWin := downShares*1.0 - totalCost
	
	// 计算均价（成本/持仓数量）
	var upAvgPrice, downAvgPrice float64
	if upShares > 0 {
		upAvgPrice = upCost / upShares
	}
	if downShares > 0 {
		downAvgPrice = downCost / downShares
	}
	
	fmt.Printf("║ UP持仓:   %-10.4f 成本: $%-10.4f 均价: %.2fc                                    ║\n", upShares, upCost, upAvgPrice*100)
	fmt.Printf("║ DOWN持仓: %-10.4f 成本: $%-10.4f 均价: %.2fc                                  ║\n", downShares, downCost, downAvgPrice*100)
	fmt.Printf("║ 总成本:   $%-10.4f                                                          ║\n", totalCost)
	fmt.Println("╠════════════════════════════════════════════════════════════════════════════════╣")
	
	// 根据收益显示不同颜色（使用ANSI颜色码）
	upWinColor := "\033[32m" // 绿色
	downWinColor := "\033[32m" // 绿色
	resetColor := "\033[0m"
	
	if profitIfUpWin < 0 {
		upWinColor = "\033[31m" // 红色
	}
	if profitIfDownWin < 0 {
		downWinColor = "\033[31m" // 红色
	}
	
	fmt.Printf("║ 如果UP获胜:   %s$%-10.4f%s                                                      ║\n",
		upWinColor, profitIfUpWin, resetColor)
	fmt.Printf("║ 如果DOWN获胜: %s$%-10.4f%s                                                      ║\n",
		downWinColor, profitIfDownWin, resetColor)
	
	// 计算最小收益（无论哪方获胜）
	minProfit := profitIfUpWin
	if profitIfDownWin < profitIfUpWin {
		minProfit = profitIfDownWin
	}
	
	minProfitColor := "\033[32m"
	if minProfit < 0 {
		minProfitColor = "\033[31m"
	}
	
	fmt.Printf("║ 最小收益:     %s$%-10.4f%s (无论哪方获胜)                                    ║\n",
		minProfitColor, minProfit, resetColor)
	
	fmt.Println("╚════════════════════════════════════════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("更新时间: %s\n", time.Now().Format("2006-01-02 15:04:05"))
}

// renderFilledOrdersToBuffer 显示已成交订单到缓冲区
func (d *Dashboard) renderFilledOrdersToBuffer(buf *strings.Builder, marketSlug string) {
	buf.WriteString("╔════════════════════════════════════════════════════════════════════════════════╗\n")
	buf.WriteString("║  ✅ 已成交订单                                                                   ║\n")
	buf.WriteString("╠════════════════════════════════════════════════════════════════════════════════╣\n")
	
	filledOrders := make([]*domain.Order, 0)
	
	// 1. 从活跃订单中查找部分成交的订单
	allOrders := d.tradingService.GetActiveOrders()
	for _, order := range allOrders {
		if order == nil {
			continue
		}
		if marketSlug != "" && order.MarketSlug != marketSlug {
			continue
		}
		if order.Status == domain.OrderStatusPartial {
			filledOrders = append(filledOrders, order)
		}
	}
	
	// 2. 从持仓中提取已成交的订单
	positions := d.tradingService.GetOpenPositionsForMarket(marketSlug)
	for _, pos := range positions {
		if pos == nil {
			continue
		}
		if pos.EntryOrder != nil && pos.EntryOrder.IsFilled() {
			exists := false
			for _, o := range filledOrders {
				if o.OrderID == pos.EntryOrder.OrderID {
					exists = true
					break
				}
			}
			if !exists {
				filledOrders = append(filledOrders, pos.EntryOrder)
			}
		}
		if pos.HedgeOrder != nil && pos.HedgeOrder.IsFilled() {
			exists := false
			for _, o := range filledOrders {
				if o.OrderID == pos.HedgeOrder.OrderID {
					exists = true
					break
				}
			}
			if !exists {
				filledOrders = append(filledOrders, pos.HedgeOrder)
			}
		}
	}
	
	sort.Slice(filledOrders, func(i, j int) bool {
		if filledOrders[i].FilledAt == nil {
			return false
		}
		if filledOrders[j].FilledAt == nil {
			return true
		}
		return filledOrders[i].FilledAt.After(*filledOrders[j].FilledAt)
	})
	
	if len(filledOrders) == 0 {
		buf.WriteString("║ 暂无已成交订单                                                                  ║\n")
	} else {
		buf.WriteString("║ 订单ID          │ 方向 │ 价格(分) │ 数量    │ 成交时间                        ║\n")
		buf.WriteString("╠═════════════════╪══════╪══════════╪═════════╪════════════════════════════════╣\n")
		
		maxDisplay := len(filledOrders)
		if maxDisplay > 10 {
			maxDisplay = 10
		}
		
		for i := 0; i < maxDisplay; i++ {
			order := filledOrders[i]
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
			
			buf.WriteString(fmt.Sprintf("║ %-15s │ %-4s │ %-8s │ %-7s │ %-30s ║\n",
				orderID, tokenType, price, size, filledTime))
		}
		
		if len(filledOrders) > 10 {
			buf.WriteString(fmt.Sprintf("║ ... 还有 %d 条订单未显示                                                      ║\n", len(filledOrders)-10))
		}
	}
	
	buf.WriteString("╚════════════════════════════════════════════════════════════════════════════════╝\n")
	buf.WriteString("\n")
}

// renderPendingOrdersToBuffer 显示未成交挂单到缓冲区
func (d *Dashboard) renderPendingOrdersToBuffer(buf *strings.Builder, marketSlug string) {
	buf.WriteString("╔════════════════════════════════════════════════════════════════════════════════╗\n")
	buf.WriteString("║  ⏳ 未成交挂单                                                                   ║\n")
	buf.WriteString("╠════════════════════════════════════════════════════════════════════════════════╣\n")
	
	allOrders := d.tradingService.GetActiveOrders()
	pendingOrders := make([]*domain.Order, 0)
	
	for _, order := range allOrders {
		if order == nil {
			continue
		}
		if marketSlug != "" && order.MarketSlug != marketSlug {
			continue
		}
		if order.Status == domain.OrderStatusPending ||
			order.Status == domain.OrderStatusOpen ||
			order.Status == domain.OrderStatusPartial {
			pendingOrders = append(pendingOrders, order)
		}
	}
	
	sort.Slice(pendingOrders, func(i, j int) bool {
		return pendingOrders[i].CreatedAt.After(pendingOrders[j].CreatedAt)
	})
	
	if len(pendingOrders) == 0 {
		buf.WriteString("║ 暂无未成交挂单                                                                  ║\n")
	} else {
		buf.WriteString("║ 订单ID          │ 方向 │ 价格(分) │ 数量    │ 状态    │ 创建时间                ║\n")
		buf.WriteString("╠═════════════════╪══════╪══════════╪═════════╪══════════╪══════════════════════╣\n")
		
		for _, order := range pendingOrders {
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
			
			buf.WriteString(fmt.Sprintf("║ %-15s │ %-4s │ %-8s │ %-7s │ %-8s │ %-22s ║\n",
				orderID, tokenType, price, size, status, createdTime))
		}
	}
	
	buf.WriteString("╚════════════════════════════════════════════════════════════════════════════════╝\n")
	buf.WriteString("\n")
}

// renderProfitToBuffer 显示收益计算到缓冲区
func (d *Dashboard) renderProfitToBuffer(buf *strings.Builder, marketSlug string) {
	buf.WriteString("╔════════════════════════════════════════════════════════════════════════════════╗\n")
	buf.WriteString("║  💰 收益计算                                                                     ║\n")
	buf.WriteString("╠════════════════════════════════════════════════════════════════════════════════╣\n")
	
	if marketSlug == "" {
		buf.WriteString("║ 当前周期: 无，无法计算收益                                                      ║\n")
		buf.WriteString("╚════════════════════════════════════════════════════════════════════════════╝\n")
		return
	}
	
	positions := d.tradingService.GetOpenPositionsForMarket(marketSlug)
	
	var upShares, downShares float64
	var upCost, downCost float64
	
	for _, pos := range positions {
		if pos == nil || !pos.IsOpen() {
			continue
		}
		
		// 使用 Size（当前持仓数量）而不是 TotalFilledSize
		currentSize := pos.Size
		if currentSize <= 0 {
			continue
		}
		
		if pos.TokenType == domain.TokenTypeUp {
			upShares += currentSize
			// 优先使用 AvgPrice 计算当前持仓成本
			if pos.AvgPrice > 0 && currentSize > 0 {
				upCost += pos.AvgPrice * currentSize
			} else if pos.CostBasis > 0 && pos.TotalFilledSize > 0 {
				avgPrice := pos.CostBasis / pos.TotalFilledSize
				upCost += avgPrice * currentSize
			} else if pos.EntryPrice.Pips > 0 && currentSize > 0 {
				upCost += pos.EntryPrice.ToDecimal() * currentSize
			}
		} else if pos.TokenType == domain.TokenTypeDown {
			downShares += currentSize
			if pos.AvgPrice > 0 && currentSize > 0 {
				downCost += pos.AvgPrice * currentSize
			} else if pos.CostBasis > 0 && pos.TotalFilledSize > 0 {
				avgPrice := pos.CostBasis / pos.TotalFilledSize
				downCost += avgPrice * currentSize
			} else if pos.EntryPrice.Pips > 0 && currentSize > 0 {
				downCost += pos.EntryPrice.ToDecimal() * currentSize
			}
		}
	}
	
	if upShares == 0 && downShares == 0 {
		allOrders := d.tradingService.GetActiveOrders()
		for _, order := range allOrders {
			if order == nil {
				continue
			}
			if order.MarketSlug != marketSlug {
				continue
			}
			if order.Status != domain.OrderStatusFilled {
				continue
			}
			
			if order.TokenType == domain.TokenTypeUp {
				upShares += order.FilledSize
				if order.FilledPrice != nil {
					upCost += order.FilledPrice.ToDecimal() * order.FilledSize
				} else if order.Price.Pips > 0 {
					upCost += order.Price.ToDecimal() * order.FilledSize
				}
			} else if order.TokenType == domain.TokenTypeDown {
				downShares += order.FilledSize
				if order.FilledPrice != nil {
					downCost += order.FilledPrice.ToDecimal() * order.FilledSize
				} else if order.Price.Pips > 0 {
					downCost += order.Price.ToDecimal() * order.FilledSize
				}
			}
		}
	}
	
	totalCost := upCost + downCost
	profitIfUpWin := upShares*1.0 - totalCost
	profitIfDownWin := downShares*1.0 - totalCost
	
	// 计算均价（成本/持仓数量）
	var upAvgPrice, downAvgPrice float64
	if upShares > 0 {
		upAvgPrice = upCost / upShares
	}
	if downShares > 0 {
		downAvgPrice = downCost / downShares
	}
	
	buf.WriteString(fmt.Sprintf("║ UP持仓:   %-10.4f 成本: $%-10.4f 均价: %.2fc                                    ║\n", upShares, upCost, upAvgPrice*100))
	buf.WriteString(fmt.Sprintf("║ DOWN持仓: %-10.4f 成本: $%-10.4f 均价: %.2fc                                  ║\n", downShares, downCost, downAvgPrice*100))
	buf.WriteString(fmt.Sprintf("║ 总成本:   $%-10.4f                                                          ║\n", totalCost))
	buf.WriteString("╠════════════════════════════════════════════════════════════════════════════════╣\n")
	
	upWinColor := "\033[32m"
	downWinColor := "\033[32m"
	resetColor := "\033[0m"
	
	if profitIfUpWin < 0 {
		upWinColor = "\033[31m"
	}
	if profitIfDownWin < 0 {
		downWinColor = "\033[31m"
	}
	
	buf.WriteString(fmt.Sprintf("║ 如果UP获胜:   %s$%-10.4f%s                                                      ║\n",
		upWinColor, profitIfUpWin, resetColor))
	buf.WriteString(fmt.Sprintf("║ 如果DOWN获胜: %s$%-10.4f%s                                                      ║\n",
		downWinColor, profitIfDownWin, resetColor))
	
	minProfit := profitIfUpWin
	if profitIfDownWin < profitIfUpWin {
		minProfit = profitIfDownWin
	}
	
	minProfitColor := "\033[32m"
	if minProfit < 0 {
		minProfitColor = "\033[31m"
	}
	
	buf.WriteString(fmt.Sprintf("║ 最小收益:     %s$%-10.4f%s (无论哪方获胜)                                    ║\n",
		minProfitColor, minProfit, resetColor))
	
	buf.WriteString("╚════════════════════════════════════════════════════════════════════════════════╝\n")
	buf.WriteString("\n")
	buf.WriteString(fmt.Sprintf("更新时间: %s\n", time.Now().Format("2006-01-02 15:04:05")))
}
