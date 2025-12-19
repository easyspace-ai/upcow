package grid

import (
	"context"
	"fmt"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)
func (s *GridStrategy) checkAndSupplementHedge(ctx context.Context, market *domain.Market) {
	// 检查context是否已取消，如果已取消则快速返回
	select {
	case <-ctx.Done():
		log.Debugf("checkAndSupplementHedge: context已取消，退出")
		return
	default:
	}

	// 使用带超时的锁获取，避免在关闭时死锁
	lockAcquired := make(chan struct{})
	go func() {
		s.mu.Lock()
		close(lockAcquired)
	}()

	select {
	case <-lockAcquired:
		defer s.mu.Unlock()
	case <-ctx.Done():
		log.Debugf("checkAndSupplementHedge: context已取消，退出（等待锁时）")
		return
	case <-time.After(1 * time.Second):
		log.Debugf("checkAndSupplementHedge: 获取锁超时（1秒），可能正在关闭，退出")
		return
	}

	// 检查是否有未对冲的仓位
	if s.activePosition == nil {
		return
	}

	// 如果仓位已完全对冲，不需要补充
	if s.activePosition.IsHedged() {
		return
	}

	// 检查入场订单是否已成交
	entryOrderFilled := s.activePosition.EntryOrder != nil && s.activePosition.EntryOrder.IsFilled()

	// 如果没有入场订单或入场订单未成交，不需要补充对冲
	if !entryOrderFilled {
		return
	}

	// 关键修复：检查是否有对冲订单在待提交列表（pendingHedgeOrders）
	// 如果主单刚成交，OnOrderFilled 的 goroutine 可能正在提交对冲订单
	// 此时不应该重复提交，避免两个对冲订单同时提交
	if len(s.pendingHedgeOrders) > 0 {
		log.Debugf("🛡️ [智能对冲] 检测到待提交的对冲订单（pendingHedgeOrders），等待 OnOrderFilled 提交，跳过补充对冲")
		for entryOrderID, hedgeOrder := range s.pendingHedgeOrders {
			log.Debugf("   待提交对冲订单: 主单ID=%s, 对冲订单ID=%s, %s币 @ %dc",
				entryOrderID[:8], hedgeOrder.OrderID[:8], hedgeOrder.TokenType, hedgeOrder.Price.Cents)
		}
		return
	}

	// 检查是否已有对冲订单在等待成交
	hasPendingHedgeOrder := false
	var existingHedgeOrder *domain.Order
	for _, order := range s.getActiveOrders() {
		if !order.IsEntryOrder && (order.Status == domain.OrderStatusOpen || order.Status == domain.OrderStatusPending) {
			hasPendingHedgeOrder = true
			existingHedgeOrder = order
			break
		}
	}

	// 如果对冲订单已成交，不需要补充
	if s.activePosition.HedgeOrder != nil && s.activePosition.HedgeOrder.IsFilled() {
		return
	}
	
	// 如果已有对冲订单在等待成交或提交中，不需要补充
	if hasPendingHedgeOrder && existingHedgeOrder != nil {
		log.Debugf("🛡️ [智能对冲] 已有对冲订单在等待成交或提交中: orderID=%s, status=%s, 跳过补充对冲",
			existingHedgeOrder.OrderID[:8], existingHedgeOrder.Status)
		return
	}

	// 计算风险敞口
	entryPrice := s.activePosition.EntryPrice
	entrySize := s.activePosition.Size
	entryTokenType := s.activePosition.TokenType

	// 计算理想对冲价格（确保利润目标）
	idealHedgePriceCents := 100 - entryPrice.Cents - s.config.ProfitTarget
	if idealHedgePriceCents < 1 {
		idealHedgePriceCents = 1
	}
	if idealHedgePriceCents > 40 {
		idealHedgePriceCents = 40
	}

	// 获取当前市场价格，动态调整对冲价格
	var currentPrice domain.Price
	var hedgeTokenType domain.TokenType
	var hedgeAssetID string

	if entryTokenType == domain.TokenTypeUp {
		// 入场订单是 UP 币，对冲订单应该是 DOWN 币
		hedgeTokenType = domain.TokenTypeDown
		hedgeAssetID = market.NoAssetID
		currentPrice = domain.Price{Cents: s.currentPriceDown}
	} else {
		// 入场订单是 DOWN 币，对冲订单应该是 UP 币
		hedgeTokenType = domain.TokenTypeUp
		hedgeAssetID = market.YesAssetID
		currentPrice = domain.Price{Cents: s.currentPriceUp}
	}

	// 如果当前价格不可用，跳过
	if currentPrice.Cents <= 0 {
		return
	}

	// 计算最优对冲价格（动态调整，确保能成交）
	optimalHedgePrice := s.calculateOptimalHedgePrice(
		ctx, market, entryPrice, idealHedgePriceCents, hedgeAssetID, currentPrice)

	// 注意：hasPendingHedgeOrder 检查已经在前面完成并返回了
	// 这里不应该再执行，因为如果已有对冲订单，应该已经返回了
	// 保留此代码作为防御性检查，但理论上不应该到达这里
	if hasPendingHedgeOrder && existingHedgeOrder != nil {
		log.Warnf("⚠️ [智能对冲] 检测到已有对冲订单但未在早期返回，可能是并发问题: orderID=%s, status=%s",
			existingHedgeOrder.OrderID[:8], existingHedgeOrder.Status)
		return
	}

	// 计算对冲订单金额和share数量
	_, hedgeShare := s.calculateOrderSize(optimalHedgePrice)

	// 关键修复：在提交对冲订单之前，再次检查是否有对冲订单在待提交列表或已提交
	// 因为可能在检查之后、提交之前，OnOrderFilled 或 checkAndAutoHedge 已经提交了对冲订单
	if len(s.pendingHedgeOrders) > 0 {
		log.Debugf("🛡️ [智能对冲] 提交前检查：检测到待提交的对冲订单（pendingHedgeOrders），跳过补充对冲")
		return
	}

	// 再次检查是否已有对冲订单在等待成交或提交中
	hasPendingHedgeOrderNow := false
	var existingHedgeOrderNow *domain.Order
	for _, order := range s.getActiveOrders() {
		if !order.IsEntryOrder && (order.Status == domain.OrderStatusOpen || order.Status == domain.OrderStatusPending) {
			hasPendingHedgeOrderNow = true
			existingHedgeOrderNow = order
			break
		}
	}

	if hasPendingHedgeOrderNow && existingHedgeOrderNow != nil {
		log.Debugf("🛡️ [智能对冲] 提交前检查：已有对冲订单在等待成交或提交中: orderID=%s, status=%s, 跳过补充对冲",
			existingHedgeOrderNow.OrderID[:8], existingHedgeOrderNow.Status)
		return
	}

	// 风险8修复：使用对冲订单提交锁，确保同一时间只有一个goroutine提交对冲订单
	s.hedgeOrderSubmitMu.Lock()
	defer s.hedgeOrderSubmitMu.Unlock()

	// 在锁内再次检查（防止在获取锁的过程中，其他goroutine已经提交了对冲订单）
	if len(s.pendingHedgeOrders) > 0 {
		log.Debugf("🛡️ [智能对冲] 锁内检查：检测到待提交的对冲订单（pendingHedgeOrders），跳过补充对冲")
		return
	}

	// 再次检查是否已有对冲订单在等待成交或提交中
	hasPendingHedgeOrderInLock := false
	var existingHedgeOrderInLock *domain.Order
	for _, order := range s.getActiveOrders() {
		if !order.IsEntryOrder && (order.Status == domain.OrderStatusOpen || order.Status == domain.OrderStatusPending) {
			hasPendingHedgeOrderInLock = true
			existingHedgeOrderInLock = order
			break
		}
	}

	if hasPendingHedgeOrderInLock && existingHedgeOrderInLock != nil {
		log.Debugf("🛡️ [智能对冲] 锁内检查：已有对冲订单在等待成交或提交中: orderID=%s, status=%s, 跳过补充对冲",
			existingHedgeOrderInLock.OrderID[:8], existingHedgeOrderInLock.Status)
		return
	}

	// 防抖机制：检查距离上次提交对冲订单的时间
	s.lastHedgeOrderSubmitMu.Lock()
	timeSinceLastSubmit := time.Since(s.lastHedgeOrderSubmitTime)
	s.lastHedgeOrderSubmitMu.Unlock()

	const minHedgeSubmitInterval = 2 * time.Second // 最小提交间隔：2秒
	if timeSinceLastSubmit < minHedgeSubmitInterval {
		log.Debugf("🛡️ [智能对冲] 防抖：距离上次提交对冲订单仅 %v，跳过（最小间隔：%v）",
			timeSinceLastSubmit, minHedgeSubmitInterval)
		return
	}

	// 创建或补充对冲订单
	log.Infof("🛡️ [智能对冲] 检测到风险敞口: 入场订单已成交但对冲订单未成交")
	log.Infof("   入场: %s币 @ %dc, 数量=%.4f", entryTokenType, entryPrice.Cents, entrySize)
	log.Infof("   理想对冲价格: %dc, 最优对冲价格: %dc", idealHedgePriceCents, optimalHedgePrice.Cents)

	hedgeOrder := &domain.Order{
		OrderID:      fmt.Sprintf("smart-hedge-%s-%d-%d", hedgeTokenType, optimalHedgePrice.Cents, time.Now().UnixNano()),
		AssetID:      hedgeAssetID,
		Side:         types.SideBuy,
		Price:        optimalHedgePrice,
		Size:         hedgeShare,
		GridLevel:    optimalHedgePrice.Cents,
		TokenType:    hedgeTokenType,
		IsEntryOrder: false,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
	}

	// 关联订单
	hedgeOrder.HedgeOrderID = &s.activePosition.EntryOrder.OrderID
	if s.activePosition.EntryOrder != nil {
		s.activePosition.EntryOrder.PairOrderID = &hedgeOrder.OrderID
	}

	// 提交对冲订单
	if s.tradingService != nil {
		orderCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		if _, err := s.tradingService.PlaceOrder(orderCtx, hedgeOrder); err != nil {
			log.Errorf("🛡️ [智能对冲] 补充对冲订单失败: %v", err)
			return
		}

		// 重构后：activeOrders 由 OrderEngine 管理，无需手动保存
		s.activePosition.HedgeOrder = hedgeOrder

		// 更新最后提交时间
		s.lastHedgeOrderSubmitMu.Lock()
		s.lastHedgeOrderSubmitTime = time.Now()
		s.lastHedgeOrderSubmitMu.Unlock()

		log.Infof("✅ [智能对冲] 已补充对冲订单: %s币 @ %dc, 数量=%.2f",
			hedgeTokenType, optimalHedgePrice.Cents, entrySize)
	} else {
		log.Warnf("交易服务未设置，无法补充对冲订单")
	}
}
func (s *GridStrategy) calculateOptimalHedgePrice(
	ctx context.Context,
	market *domain.Market,
	entryPrice domain.Price,
	idealHedgePriceCents int,
	hedgeAssetID string,
	currentPrice domain.Price,
) domain.Price {
	// 首先尝试使用理想对冲价格
	idealHedgePrice := domain.Price{Cents: idealHedgePriceCents}

	// 如果当前市场价格接近理想价格（差异 <= 3分），使用理想价格
	priceDiff := currentPrice.Cents - idealHedgePriceCents
	if priceDiff < 0 {
		priceDiff = -priceDiff
	}

	if priceDiff <= 3 {
		log.Debugf("🔄 [智能对冲] 使用理想对冲价格: %dc (当前市场价格: %dc, 差异: %dc)",
			idealHedgePriceCents, currentPrice.Cents, priceDiff)
		return idealHedgePrice
	}

	// 如果价格差异较大，需要动态调整
	// 策略：使用当前市场价格，但确保总成本 <= 100，利润目标尽量满足
	if s.tradingService != nil {
		// 获取订单簿的最佳卖价（买入对冲订单需要从卖一价买入）
		_, bestAsk, err := s.tradingService.GetBestPrice(ctx, hedgeAssetID)
		if err == nil && bestAsk > 0 {
			bestAskCents := int(bestAsk * 100)

			// 计算使用最佳卖价后的总成本和利润
			totalCost := entryPrice.Cents + bestAskCents
			profit := 100 - totalCost

			// 如果使用最佳卖价仍能满足利润目标，使用最佳卖价
			if profit >= s.config.ProfitTarget {
				log.Infof("🔄 [智能对冲] 使用订单簿最佳卖价: %dc (总成本: %dc, 利润: %dc)",
					bestAskCents, totalCost, profit)
				return domain.Price{Cents: bestAskCents}
			}

			// 如果使用最佳卖价无法满足利润目标，计算一个折中价格
			// 确保总成本 <= 100，利润目标尽量满足
			maxHedgePriceCents := 100 - entryPrice.Cents - s.config.ProfitTarget
			if maxHedgePriceCents < 1 {
				maxHedgePriceCents = 1
			}

			// 如果最佳卖价超过最大允许价格，使用最大允许价格
			if bestAskCents > maxHedgePriceCents {
				log.Warnf("🔄 [智能对冲] 订单簿最佳卖价 %dc 超过最大允许价格 %dc，使用最大允许价格",
					bestAskCents, maxHedgePriceCents)
				return domain.Price{Cents: maxHedgePriceCents}
			}

			// 使用最佳卖价，但确保不超过最大允许价格
			log.Infof("🔄 [智能对冲] 使用订单簿最佳卖价（折中）: %dc (总成本: %dc, 利润: %dc)",
				bestAskCents, totalCost, profit)
			return domain.Price{Cents: bestAskCents}
		}
	}

	// 如果无法获取订单簿价格，使用理想价格
	log.Warnf("🔄 [智能对冲] 无法获取订单簿价格，使用理想对冲价格: %dc", idealHedgePriceCents)
	return idealHedgePrice
}
func (s *GridStrategy) checkAndAutoHedge(ctx context.Context, market *domain.Market) {
	// 检查context是否已取消，如果已取消则快速返回
	select {
	case <-ctx.Done():
		log.Debugf("checkAndAutoHedge: context已取消，退出")
		return
	default:
	}

	// 使用带超时的锁获取，避免在关闭时死锁
	lockAcquired := make(chan struct{})
	go func() {
		s.mu.Lock()
		close(lockAcquired)
	}()

	select {
	case <-lockAcquired:
		defer s.mu.Unlock()
	case <-ctx.Done():
		log.Debugf("checkAndAutoHedge: context已取消，退出（等待锁时）")
		return
	case <-time.After(1 * time.Second):
		log.Debugf("checkAndAutoHedge: 获取锁超时（1秒），可能正在关闭，退出")
		return
	}

	// 检查是否有持仓
	if s.upHoldings == 0 && s.downHoldings == 0 {
		return
	}

	// 计算实时利润
	upWinProfit := s.upHoldings*1.0 - s.upTotalCost - s.downTotalCost
	downWinProfit := s.downHoldings*1.0 - s.upTotalCost - s.downTotalCost

	// 检查是否已锁定（两个方向利润都为正）
	isLocked := upWinProfit > 0 && downWinProfit > 0

	if isLocked {
		log.Debugf("✅ [自动对冲] 利润已锁定: UP胜=%.4f USDC, DOWN胜=%.4f USDC", upWinProfit, downWinProfit)
		return
	}

	// 未锁定，需要补充对冲订单
	log.Warnf("⚠️ [自动对冲] 检测到未锁定状态: UP胜=%.4f USDC, DOWN胜=%.4f USDC", upWinProfit, downWinProfit)

	// 关键修复：检查是否有对冲订单在待提交列表（pendingHedgeOrders）
	// 如果主单刚成交，OnOrderFilled 的 goroutine 可能正在提交对冲订单
	// 此时不应该重复提交，避免多个对冲订单同时提交
	if len(s.pendingHedgeOrders) > 0 {
		log.Debugf("🛡️ [自动对冲] 检测到待提交的对冲订单（pendingHedgeOrders），等待 OnOrderFilled 提交，跳过自动对冲")
		for entryOrderID, hedgeOrder := range s.pendingHedgeOrders {
			log.Debugf("   待提交对冲订单: 主单ID=%s, 对冲订单ID=%s, %s币 @ %dc",
				entryOrderID[:8], hedgeOrder.OrderID[:8], hedgeOrder.TokenType, hedgeOrder.Price.Cents)
		}
		return
	}

	// 检查是否已有对冲订单在等待成交或提交中
	hasPendingHedgeOrder := false
	var existingHedgeOrder *domain.Order
	for _, order := range s.getActiveOrders() {
		if !order.IsEntryOrder && (order.Status == domain.OrderStatusOpen || order.Status == domain.OrderStatusPending) {
			hasPendingHedgeOrder = true
			existingHedgeOrder = order
			break
		}
	}

	// 如果已有对冲订单在等待成交或提交中，不需要补充
	if hasPendingHedgeOrder && existingHedgeOrder != nil {
		log.Debugf("🛡️ [自动对冲] 已有对冲订单在等待成交或提交中: orderID=%s, status=%s, 跳过自动对冲",
			existingHedgeOrder.OrderID[:8], existingHedgeOrder.Status)
		return
	}

	// 如果UP方向亏损，补充UP订单
	if upWinProfit < 0 && s.currentPriceUp > 0 {
		priceUp := domain.Price{Cents: s.currentPriceUp}
		priceUpDecimal := priceUp.ToDecimal()

		// 计算需要补充的数量：使 upWinProfit >= 0
		// upWinProfit = (upHoldings + dQ) * 1.0 - (upTotalCost + dQ * priceUp) - downTotalCost
		// 0 = (upHoldings + dQ) * 1.0 - (upTotalCost + dQ * priceUp) - downTotalCost
		// dQ = (downTotalCost + upTotalCost - upHoldings) / (1.0 - priceUp)
		need := (s.downTotalCost + s.upTotalCost - s.upHoldings) / (1.0 - priceUpDecimal)

		// 确保金额满足最小要求
		minOrderSize := s.config.MinOrderSize
		if minOrderSize <= 0 {
			minOrderSize = 1.1 // 默认值
		}

		// 如果计算出的数量对应的金额小于最小金额，调整数量
		if need*priceUpDecimal < minOrderSize {
			need = minOrderSize / priceUpDecimal
		}

		dQ := need

		if dQ > 0 {
			log.Infof("🛡️ [自动对冲] UP方向亏损，补充UP订单: 需要=%.4f, 下单=%.4f, 金额=%.2f USDC",
				need, dQ, dQ*priceUpDecimal)

			hedgeOrder := &domain.Order{
				OrderID:      fmt.Sprintf("auto-hedge-up-%d-%d", s.currentPriceUp, time.Now().UnixNano()),
				AssetID:      market.YesAssetID,
				Side:         types.SideBuy,
				Price:        priceUp,
				Size:         dQ,
				TokenType:    domain.TokenTypeUp,
				IsEntryOrder: false,
				Status:       domain.OrderStatusPending,
				CreatedAt:    time.Now(),
			}

			if s.tradingService != nil {
				// 风险8修复：使用对冲订单提交锁，确保同一时间只有一个goroutine提交对冲订单
				s.hedgeOrderSubmitMu.Lock()
				
				// 在锁内再次检查（防止在获取锁的过程中，其他goroutine已经提交了对冲订单）
				if len(s.pendingHedgeOrders) > 0 {
					s.hedgeOrderSubmitMu.Unlock()
					log.Debugf("🛡️ [自动对冲] 锁内检查：检测到待提交的对冲订单（pendingHedgeOrders），跳过UP订单")
					// 继续处理DOWN订单（如果有）
				} else {
					// 再次检查是否已有对冲订单在等待成交或提交中
					hasPendingHedgeOrderInLock := false
					for _, order := range s.getActiveOrders() {
						if !order.IsEntryOrder && order.TokenType == domain.TokenTypeUp &&
							(order.Status == domain.OrderStatusOpen || order.Status == domain.OrderStatusPending) {
							hasPendingHedgeOrderInLock = true
							break
						}
					}

					if hasPendingHedgeOrderInLock {
						s.hedgeOrderSubmitMu.Unlock()
						log.Debugf("🛡️ [自动对冲] 锁内检查：已有UP对冲订单在等待成交或提交中，跳过")
						// 继续处理DOWN订单（如果有）
					} else {
						// 防抖机制：检查距离上次提交对冲订单的时间
						s.lastHedgeOrderSubmitMu.Lock()
						timeSinceLastSubmit := time.Since(s.lastHedgeOrderSubmitTime)
						s.lastHedgeOrderSubmitMu.Unlock()

						const minHedgeSubmitInterval = 2 * time.Second // 最小提交间隔：2秒
						if timeSinceLastSubmit < minHedgeSubmitInterval {
							s.hedgeOrderSubmitMu.Unlock()
							log.Debugf("🛡️ [自动对冲] 防抖：距离上次提交对冲订单仅 %v，跳过UP订单（最小间隔：%v）",
								timeSinceLastSubmit, minHedgeSubmitInterval)
							// 继续处理DOWN订单（如果有）
						} else {
							orderCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
							defer cancel()

							if _, err := s.tradingService.PlaceOrder(orderCtx, hedgeOrder); err != nil {
								s.hedgeOrderSubmitMu.Unlock()
								log.Errorf("🛡️ [自动对冲] 补充UP订单失败: %v", err)
							} else {
								// 更新最后提交时间
								s.lastHedgeOrderSubmitMu.Lock()
								s.lastHedgeOrderSubmitTime = time.Now()
								s.lastHedgeOrderSubmitMu.Unlock()

								s.hedgeOrderSubmitMu.Unlock()
								log.Infof("✅ [自动对冲] 已补充UP订单: 数量=%.4f, 金额=%.2f USDC", dQ, dQ*priceUpDecimal)
							}
						}
					}
				}
			}
		}
	}

	// 如果DOWN方向亏损，补充DOWN订单
	// 注意：这里不需要再次检查 pendingHedgeOrders 和 activeOrders，因为已经在上面统一检查了
	if downWinProfit < 0 && s.currentPriceDown > 0 {
		priceDown := domain.Price{Cents: s.currentPriceDown}
		priceDownDecimal := priceDown.ToDecimal()

		// 计算需要补充的数量：使 downWinProfit >= 0
		// downWinProfit = (downHoldings + dQ) * 1.0 - upTotalCost - (downTotalCost + dQ * priceDown)
		// 0 = (downHoldings + dQ) * 1.0 - upTotalCost - (downTotalCost + dQ * priceDown)
		// dQ = (upTotalCost + downTotalCost - downHoldings) / (1.0 - priceDown)
		need := (s.upTotalCost + s.downTotalCost - s.downHoldings) / (1.0 - priceDownDecimal)

		// 确保金额满足最小要求
		minOrderSize := s.config.MinOrderSize
		if minOrderSize <= 0 {
			minOrderSize = 1.1 // 默认值
		}

		// 如果计算出的数量对应的金额小于最小金额，调整数量
		if need*priceDownDecimal < minOrderSize {
			need = minOrderSize / priceDownDecimal
		}

		dQ := need

		if dQ > 0 {
			log.Infof("🛡️ [自动对冲] DOWN方向亏损，补充DOWN订单: 需要=%.4f, 下单=%.4f, 金额=%.2f USDC",
				need, dQ, dQ*priceDownDecimal)

			hedgeOrder := &domain.Order{
				OrderID:      fmt.Sprintf("auto-hedge-down-%d-%d", s.currentPriceDown, time.Now().UnixNano()),
				AssetID:      market.NoAssetID,
				Side:         types.SideBuy,
				Price:        priceDown,
				Size:         dQ,
				TokenType:    domain.TokenTypeDown,
				IsEntryOrder: false,
				Status:       domain.OrderStatusPending,
				CreatedAt:    time.Now(),
			}

			if s.tradingService != nil {
				// 风险8修复：使用对冲订单提交锁，确保同一时间只有一个goroutine提交对冲订单
				s.hedgeOrderSubmitMu.Lock()
				
				// 在锁内再次检查（防止在获取锁的过程中，其他goroutine已经提交了对冲订单）
				if len(s.pendingHedgeOrders) > 0 {
					s.hedgeOrderSubmitMu.Unlock()
					log.Debugf("🛡️ [自动对冲] 锁内检查：检测到待提交的对冲订单（pendingHedgeOrders），跳过DOWN订单")
					return
				}

				// 再次检查是否已有对冲订单在等待成交或提交中
				hasPendingHedgeOrderInLock := false
				for _, order := range s.getActiveOrders() {
					if !order.IsEntryOrder && order.TokenType == domain.TokenTypeDown &&
						(order.Status == domain.OrderStatusOpen || order.Status == domain.OrderStatusPending) {
						hasPendingHedgeOrderInLock = true
						break
					}
				}

				if hasPendingHedgeOrderInLock {
					s.hedgeOrderSubmitMu.Unlock()
					log.Debugf("🛡️ [自动对冲] 锁内检查：已有DOWN对冲订单在等待成交或提交中，跳过")
					return
				}

				// 防抖机制：检查距离上次提交对冲订单的时间
				s.lastHedgeOrderSubmitMu.Lock()
				timeSinceLastSubmit := time.Since(s.lastHedgeOrderSubmitTime)
				s.lastHedgeOrderSubmitMu.Unlock()

				const minHedgeSubmitInterval = 2 * time.Second // 最小提交间隔：2秒
				if timeSinceLastSubmit < minHedgeSubmitInterval {
					s.hedgeOrderSubmitMu.Unlock()
					log.Debugf("🛡️ [自动对冲] 防抖：距离上次提交对冲订单仅 %v，跳过DOWN订单（最小间隔：%v）",
						timeSinceLastSubmit, minHedgeSubmitInterval)
					return
				}

				orderCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()

				if _, err := s.tradingService.PlaceOrder(orderCtx, hedgeOrder); err != nil {
					s.hedgeOrderSubmitMu.Unlock()
					log.Errorf("🛡️ [自动对冲] 补充DOWN订单失败: %v", err)
				} else {
					// 更新最后提交时间
					s.lastHedgeOrderSubmitMu.Lock()
					s.lastHedgeOrderSubmitTime = time.Now()
					s.lastHedgeOrderSubmitMu.Unlock()

					s.hedgeOrderSubmitMu.Unlock()
					log.Infof("✅ [自动对冲] 已补充DOWN订单: 数量=%.4f, 金额=%.2f USDC", dQ, dQ*priceDownDecimal)
				}
			}
		}
	}
}

// Cleanup 清理资源
