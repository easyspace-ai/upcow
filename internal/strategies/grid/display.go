package grid

import (
	"fmt"
	"strings"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
)

// displayGridPosition 显示网格位置信息
func (s *GridStrategy) displayGridPosition(event *events.PriceChangedEvent, oldPriceUp, oldPriceDown, newPriceUp, newPriceDown int) {
	if s.grid == nil {
		log.Warnf("⚠️ 网格未初始化，跳过显示")
		// 即使 grid 为 nil，也显示基本信息
		log.Infof("✅ Price updated (网格未初始化): %s=%dc", event.TokenType, event.NewPrice.Cents)
		return
	}
	
	// 参数验证
	if event == nil {
		return
	}

	// 直接使用传入的价格，避免再次读取（可能不一致）
	currentPriceUp := newPriceUp
	currentPriceDown := newPriceDown

	// 更新价格后，显示两个币种的完整信息
	var lines []string

	// UP 币信息（如果价格已更新）
	if currentPriceUp > 0 {
		// 如果是 UP 币价格变化，显示价格变化；否则只显示当前价格
		isUpChanged := event.TokenType == domain.TokenTypeUp
		upEvent := event
		if !isUpChanged {
			// 创建一个包含当前价格和旧价格的事件（用于显示价格变化）
			var oldPrice *domain.Price
			if oldPriceUp > 0 {
				oldPrice = &domain.Price{Cents: oldPriceUp}
			}
			upEvent = &events.PriceChangedEvent{
				Market:    event.Market,
				TokenType: domain.TokenTypeUp,
				OldPrice:  oldPrice,
				NewPrice:  domain.Price{Cents: currentPriceUp},
				Timestamp: event.Timestamp,
			}
		}
		upLine := s.formatGridPosition("UP", currentPriceUp, isUpChanged || oldPriceUp > 0, upEvent)
		lines = append(lines, upLine)
	} else {
		// 即使价格未更新，也显示等待状态
		lines = append(lines, "UP:   等待价格更新...")
	}

	// DOWN 币信息（如果价格已更新）
	if currentPriceDown > 0 {
		// 如果是 DOWN 币价格变化，显示价格变化；否则只显示当前价格
		isDownChanged := event.TokenType == domain.TokenTypeDown
		downEvent := event
		if !isDownChanged {
			// 创建一个包含当前价格和旧价格的事件（用于显示价格变化）
			var oldPrice *domain.Price
			if oldPriceDown > 0 {
				oldPrice = &domain.Price{Cents: oldPriceDown}
			}
			downEvent = &events.PriceChangedEvent{
				Market:    event.Market,
				TokenType: domain.TokenTypeDown,
				OldPrice:  oldPrice,
				NewPrice:  domain.Price{Cents: currentPriceDown},
				Timestamp: event.Timestamp,
			}
		}
		downLine := s.formatGridPosition("DOWN", currentPriceDown, isDownChanged || oldPriceDown > 0, downEvent)
		lines = append(lines, downLine)
	} else {
		// 即使价格未更新，也显示等待状态
		lines = append(lines, "DOWN: 等待价格更新...")
	}

	// 输出到日志（避免 stdout 未被采集导致“看不到实时信息”）
	log.Infof("✅ Price updated:")
	for _, line := range lines {
		log.Infof("   %s", line)
	}

	// 显示双向持仓和利润信息（内部会短暂 RLock）
	s.displayHoldingsAndProfit()
	// 显示策略状态信息（内部会短暂 RLock + 读活跃订单）
	s.displayStrategyStatus()

	// 仓位和订单信息写入日志文件（交易相关信息）
	if s.activePosition != nil {
		posInfo := s.formatPositionInfo()
		log.Infof("💼 %s", posInfo)
	}

	// 重构后：从 TradingService 查询活跃订单
	activeOrders := s.getActiveOrders()
	if len(activeOrders) > 0 {
		ordersInfo := s.formatOrdersInfo()
		if ordersInfo != "" {
			log.Infof("📋 %s", ordersInfo)
		}
	}
}

// displayStrategyStatus 在终端显示策略状态信息
func (s *GridStrategy) displayStrategyStatus() {
	// 显示轮数信息
	roundInfo := fmt.Sprintf("📊 轮数: %d/%d", s.roundsThisPeriod, s.config.MaxRoundsPerPeriod)

	// 判断当前状态
	var statusInfo string
	var statusEmoji string

	if s.isPlacingOrder {
		statusInfo = "正在下单中..."
		statusEmoji = "⏳"
	} else if s.activePosition != nil {
		// 有仓位，检查是否已对冲
		if s.activePosition.IsHedged() {
			statusInfo = "已对冲完成，等待清仓"
			statusEmoji = "✅"
		} else {
			// 检查是否有待成交订单
			hasEntryOrder := false
			hasHedgeOrder := false
			// 重构后：从 TradingService 查询活跃订单
			activeOrders := s.getActiveOrders()
			for _, order := range activeOrders {
				if order.IsEntryOrder && (order.Status == domain.OrderStatusPending || order.Status == domain.OrderStatusOpen) {
					hasEntryOrder = true
				}
				if !order.IsEntryOrder && (order.Status == domain.OrderStatusPending || order.Status == domain.OrderStatusOpen) {
					hasHedgeOrder = true
				}
			}

			if hasEntryOrder && !hasHedgeOrder {
				statusInfo = "入场订单待成交，等待对冲"
				statusEmoji = "⏳"
			} else if !hasEntryOrder && hasHedgeOrder {
				statusInfo = "对冲订单待成交"
				statusEmoji = "⏳"
			} else if hasEntryOrder && hasHedgeOrder {
				statusInfo = "入场和对冲订单均待成交"
				statusEmoji = "⏳"
			} else {
				statusInfo = "仓位已建立，待对冲"
				statusEmoji = "⚠️"
			}
		}
	} else if s.hasActiveOrders() {
		// 有订单但没有仓位
		statusInfo = "订单待成交中..."
		statusEmoji = "⏳"
	} else {
		// 没有仓位和订单
		if s.roundsThisPeriod >= s.config.MaxRoundsPerPeriod {
			statusInfo = "已达到最大轮数限制"
			statusEmoji = "🔒"
		} else {
			statusInfo = "等待网格触发，可开启新一轮"
			statusEmoji = "🟢"
		}
	}

	// 显示订单状态
	var orderStatusLines []string
	activeOrders := s.getActiveOrders()
	if len(activeOrders) > 0 {
		for _, order := range activeOrders {
			if order.Status == domain.OrderStatusPending || order.Status == domain.OrderStatusOpen {
				orderType := "入场"
				if !order.IsEntryOrder {
					orderType = "对冲"
				}
				orderStatusLines = append(orderStatusLines, fmt.Sprintf("%s订单: %s币 @ %dc [%s]",
					orderType, order.TokenType, order.Price.Cents, order.Status))
			}
		}
	} else {
		orderStatusLines = append(orderStatusLines, "无待成交订单")
	}

	// 显示持仓状态
	var positionStatus string
	if s.activePosition != nil {
		pos := s.activePosition
		hedgeStatus := "⚠️ 未对冲"
		if pos.IsHedged() {
			hedgeStatus = "✅ 已对冲"
		}

		// 计算盈亏（如果有当前价格）
		profitInfo := ""
		// 安全读取当前价格
		s.mu.RLock()
		currentPriceUp := s.currentPriceUp
		currentPriceDown := s.currentPriceDown
		s.mu.RUnlock()
		
		if pos.TokenType == domain.TokenTypeUp && currentPriceUp > 0 {
			currentPrice := domain.Price{Cents: currentPriceUp}
			profit := pos.CalculateProfit(currentPrice)
			if profit > 0 {
				profitInfo = fmt.Sprintf(" | 利润: +%dc", profit)
			} else if profit < 0 {
				profitInfo = fmt.Sprintf(" | 亏损: %dc", profit)
			}
		} else if pos.TokenType == domain.TokenTypeDown && currentPriceDown > 0 {
			currentPrice := domain.Price{Cents: currentPriceDown}
			profit := pos.CalculateProfit(currentPrice)
			if profit > 0 {
				profitInfo = fmt.Sprintf(" | 利润: +%dc", profit)
			} else if profit < 0 {
				profitInfo = fmt.Sprintf(" | 亏损: %dc", profit)
			}
		}

		positionStatus = fmt.Sprintf("%s币 @ %dc, 数量=%.2f | %s%s",
			pos.TokenType, pos.EntryPrice.Cents, pos.Size, hedgeStatus, profitInfo)
	} else {
		positionStatus = "无持仓"
	}

	// 输出到日志（同一日志流可见）
	log.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Infof("   %s %s", statusEmoji, statusInfo)
	log.Infof("   %s", roundInfo)
	log.Infof("   📋 订单: %s", strings.Join(orderStatusLines, " | "))
	log.Infof("   💼 持仓: %s", positionStatus)

	// 显示双向持仓和利润信息（注意：这里会导致与 displayGridPosition 的调用产生重复输出，
	// 但能保证无论调用链如何都可见；如需去重可再做一次优化）
	s.displayHoldingsAndProfit()

	log.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
}

// displayHoldingsAndProfit 显示双向持仓和利润信息
func (s *GridStrategy) displayHoldingsAndProfit() {
	s.mu.RLock()
	upTotalCost := s.upTotalCost
	upHoldings := s.upHoldings
	downTotalCost := s.downTotalCost
	downHoldings := s.downHoldings
	s.mu.RUnlock()

	// 计算均价
	var upAvgPrice float64
	if upHoldings > 0 {
		upAvgPrice = upTotalCost / upHoldings
	}

	var downAvgPrice float64
	if downHoldings > 0 {
		downAvgPrice = downTotalCost / downHoldings
	}

	// 计算利润
	// UP胜利润 = UP持仓量 * 1 USDC - UP总成本 - DOWN总成本
	upWinProfit := upHoldings*1.0 - upTotalCost - downTotalCost

	// DOWN胜利润 = DOWN持仓量 * 1 USDC - UP总成本 - DOWN总成本
	downWinProfit := downHoldings*1.0 - upTotalCost - downTotalCost

	// 输出到日志（同一日志流可见）
	log.Infof("   📊 双向持仓:")
	log.Infof("      UP:   总成本=%.8f USDC, 持仓=%.8f, 均价=%.8f", upTotalCost, upHoldings, upAvgPrice)
	log.Infof("      DOWN: 总成本=%.8f USDC, 持仓=%.8f, 均价=%.8f", downTotalCost, downHoldings, downAvgPrice)
	log.Infof("      💰 利润: UP胜=%.8f USDC, DOWN胜=%.8f USDC", upWinProfit, downWinProfit)
}

// formatOrdersInfo 格式化待成交订单信息
func (s *GridStrategy) formatOrdersInfo() string {
	// 重构后：从 TradingService 查询活跃订单
	activeOrders := s.getActiveOrders()
	if len(activeOrders) == 0 {
		return ""
	}

	var orderLines []string
	for _, order := range activeOrders {
		if order.Status == domain.OrderStatusPending || order.Status == domain.OrderStatusOpen {
			orderType := "入场"
			if !order.IsEntryOrder {
				orderType = "对冲"
			}
			orderLines = append(orderLines, fmt.Sprintf("%s订单: %s币 @ %dc, 数量=%.2f [%s]",
				orderType, order.TokenType, order.Price.Cents, order.Size, order.Status))
		}
	}

	if len(orderLines) > 0 {
		return fmt.Sprintf("📋 待成交订单: %s", strings.Join(orderLines, " | "))
	}

	return ""
}

// 其他显示和日志方法（logPriceUpdate, logTokenPriceUpdate, logPositionAndProfit, 
// formatGridPosition, formatPositionInfo）保留在 strategy.go 中，稍后可以继续拆分

