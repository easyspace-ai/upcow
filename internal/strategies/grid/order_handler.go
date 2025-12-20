package grid

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/strategies/common"
)

// calculateOrderSize 根据配置计算订单金额和share数量
// 使用 OrderSize（share数量）下单，确保金额 >= MinOrderSize USDC（交易所最小要求）
func (s *GridStrategy) calculateOrderSize(price domain.Price) (orderAmount float64, share float64) {
	priceDecimal := price.ToDecimal()
	minOrderSize := s.config.MinOrderSize
	if minOrderSize <= 0 {
		minOrderSize = 1.1 // 默认值
	}

	// 使用 OrderSize（按share数量下单）
	share = s.config.OrderSize
	orderAmount = share * priceDecimal

	// 确保最小金额 >= MinOrderSize USDC
	if orderAmount < minOrderSize {
		share = minOrderSize / priceDecimal
		orderAmount = minOrderSize
	}

	return orderAmount, share
}

// OnOrderUpdate 处理订单更新事件（实现 OrderHandler 接口）
// 将订单更新转换为 OrderFilledEvent 并调用 OnOrderFilled
func (s *GridStrategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	// 策略内部单线程循环处理订单更新；这里仅入队（不做任何业务逻辑）
	if order == nil {
		return nil
	}

	select {
	case s.orderC <- orderUpdate{ctx: ctx, order: order}:
		return nil
	default:
		// 极端情况下队列满了：记录错误并丢弃（避免阻塞 Session 分发）
		log.Errorf("❌ [订单更新] 内部队列已满，丢弃订单更新: orderID=%s, status=%s", order.OrderID, order.Status)
		return nil
	}
}

// handleOrderUpdateInternal 在策略单线程 loop 中处理订单更新
func (s *GridStrategy) handleOrderUpdateInternal(loopCtx context.Context, ctx context.Context, order *domain.Order) error {
	if order == nil {
		return nil
	}

	// 只管理本周期：如果 currentMarket 已知，则用 assetID 严格过滤
	s.mu.RLock()
	market := s.currentMarket
	s.mu.RUnlock()
	if market != nil {
		if order.AssetID != market.YesAssetID && order.AssetID != market.NoAssetID {
			return nil
		}
	}

	log.Debugf("📥 [订单更新] 收到订单更新: orderID=%s, status=%s, filledAt=%v",
		order.OrderID, order.Status, order.FilledAt != nil)

	// 如果订单已成交，调用策略的OnOrderFilled方法
	if order.Status == domain.OrderStatusFilled && order.FilledAt != nil {
		log.Debugf("📥 [订单更新] 订单已成交，准备调用OnOrderFilled: orderID=%s, filledAt=%v",
			order.OrderID, order.FilledAt)

		// 获取当前市场（从策略保存的市场引用中获取）
		if market == nil {
			log.Warnf("⚠️ [订单更新] 无法获取市场信息，跳过订单更新处理: orderID=%s", order.OrderID)
			return nil
		}

		// 创建OrderFilledEvent
		event := &events.OrderFilledEvent{
			Order:     order,
			Market:    market,
			Timestamp: *order.FilledAt,
		}

		// 优先使用传入的 ctx；如果已取消则降级用 loopCtx，避免整条链路丢事件
		callCtx := ctx
		if callCtx == nil || callCtx.Err() != nil {
			callCtx = loopCtx
		}

		if err := s.OnOrderFilled(callCtx, event); err != nil {
			log.Errorf("❌ [订单更新] OnOrderFilled处理失败: orderID=%s, error=%v", order.OrderID, err)
			return err
		}
		log.Debugf("✅ [订单更新] OnOrderFilled处理成功: orderID=%s", order.OrderID)
	}
	return nil
}
func (s *GridStrategy) handleGridLevelReached(
	ctx context.Context,
	market *domain.Market,
	tokenType domain.TokenType,
	gridLevel int, // 网格层级价格（例如 62分）
	currentPrice domain.Price,
) error {
	// 下一阶段工程化：统一走 HedgePlan + Executor（单线程 loop，不直接阻塞网络 IO）
	return s.handleGridLevelReachedWithPlan(ctx, market, tokenType, gridLevel, currentPrice)

	/*
			legacy implementation removed:
			- 不再允许策略 loop 里直接同步 PlaceOrder/CancelOrder
			- 统一由 HedgePlan 状态机 + 全局 Executor 串行执行


		log.Infof("🎯 [网格下单] handleGridLevelReached开始处理: %s币, 网格层级=%dc, 当前价格=%dc (%.4f), market=%s",
			tokenType, gridLevel, currentPrice.Cents, currentPrice.ToDecimal(), market.Slug)

		// 第一层防护：检查是否正在下单（全局锁，防止任何并发下单）
		// 同时检查防重复标记，确保原子性操作
		levelKey := fmt.Sprintf("%s:%d", tokenType, gridLevel)
		s.placeOrderMu.Lock()
		defer s.placeOrderMu.Unlock()
		log.Debugf("🔒 [网格下单] 已获取placeOrderMu锁，开始检查下单条件")

		if s.isPlacingOrder {
			// 风险13修复：检查isPlacingOrder是否超时（超过60秒强制重置）
			const maxPlacingOrderTimeout = 60 * time.Second
			if !s.isPlacingOrderSetTime.IsZero() {
				timeSinceSet := time.Since(s.isPlacingOrderSetTime)
				if timeSinceSet > maxPlacingOrderTimeout {
					log.Warnf("⚠️ [防重复] isPlacingOrder标志已持续%v（超过%v），强制重置（防止卡死）: %s:%dc",
						timeSinceSet, maxPlacingOrderTimeout, tokenType, gridLevel)
					s.isPlacingOrder = false
					s.isPlacingOrderSetTime = time.Time{}
				} else {
					log.Warnf("⚠️ [防重复] 正在下单中，跳过网格层级 %s:%dc (isPlacingOrder=true，已持续%v)",
						tokenType, gridLevel, timeSinceSet)
					return nil
				}
			} else {
				log.Warnf("⚠️ [防重复] 正在下单中，跳过网格层级 %s:%dc (isPlacingOrder=true，但SetTime未设置)", tokenType, gridLevel)
				return nil
			}
		}

		// 第二层防护：检查是否已处理过该网格层级（防止重复触发）
		// 注意：这个检查也在下单锁内，确保原子性
		s.processedLevelsMu.Lock()
		if s.processedGridLevels == nil {
			s.processedGridLevels = make(map[string]time.Time)
		}
		lastProcessedTime, alreadyProcessed := s.processedGridLevels[levelKey]
		if alreadyProcessed {
			// 如果距离上次处理时间小于 30 秒，跳过（防止重复触发）
			// 增加时间窗口，因为订单可能需要时间成交
			if time.Since(lastProcessedTime) < 30*time.Second {
				s.processedLevelsMu.Unlock()
				log.Debugf("📌 [防重复] 网格层级 %s:%dc 已在 %v 前处理过，跳过重复触发",
					tokenType, gridLevel, time.Since(lastProcessedTime))
				return nil
			}
		}
		// 立即标记为已处理（防止并发时重复触发）
		// 如果订单提交失败，会在错误处理中清除标记（允许重试）
		s.processedGridLevels[levelKey] = time.Now()
		s.processedLevelsMu.Unlock()
		log.Debugf("📌 [防重复] 网格层级 %s:%dc 已标记为处理中，防止重复触发", tokenType, gridLevel)

		// 设置下单标志（锁已在函数开头获取，这里直接设置，确保原子性）
		s.isPlacingOrder = true
		s.isPlacingOrderSetTime = time.Now()

		// 确保 map 已初始化（防止 nil map panic）
		s.mu.Lock()
		// 重构后：activeOrders 已移除，现在由 OrderEngine 管理
		if false {
			// 重构后：activeOrders 已移除，现在由 OrderEngine 管理
		}
		if s.pendingHedgeOrders == nil {
			s.pendingHedgeOrders = make(map[string]*domain.Order)
		}
		s.mu.Unlock()

		// 先快速检查（需要锁）
		s.mu.RLock()
		roundsThisPeriod := s.roundsThisPeriod
		maxRoundsPerPeriod := s.config.MaxRoundsPerPeriod
		hasActivePosition := s.activePosition != nil
		s.mu.RUnlock()

		// 重构后：从 TradingService 查询活跃订单（不需要锁）
		hasActiveOrders := s.hasActiveOrders()

		// 检查周期限制
		if roundsThisPeriod >= maxRoundsPerPeriod {
			log.Infof("⚠️ [网格下单] 已达到周期最大轮数限制 (%d/%d)，跳过网格层级 %s:%dc",
				roundsThisPeriod, maxRoundsPerPeriod, tokenType, gridLevel)
			return nil
		}
		log.Debugf("✅ [网格下单] 周期限制检查通过: 当前轮数=%d/%d", roundsThisPeriod, maxRoundsPerPeriod)

		// 检查是否已有活跃仓位或订单
		// 规则：一轮里只能一对单（主单+对冲单）全部成交后，再开启下一轮
		log.Debugf("🔍 [网格下单] 检查活跃仓位和订单: hasActivePosition=%v, hasActiveOrders=%v", hasActivePosition, hasActiveOrders)
		if hasActivePosition || hasActiveOrders {
			s.mu.RLock()
			activePosition := s.activePosition
			pendingHedgeOrders := s.pendingHedgeOrders
			s.mu.RUnlock()

			// 重构后：从 TradingService 查询活跃订单（不需要锁）
			activeOrders := s.getActiveOrders()
			activeOrdersMap := make(map[string]*domain.Order)
			for _, order := range activeOrders {
				activeOrdersMap[order.OrderID] = order
			}

			// 1. 检查是否有待提交的对冲订单（主单已提交但未成交，对冲订单还在等待）
			if len(pendingHedgeOrders) > 0 {
				log.Infof("⚠️ [订单顺序] 有待提交的对冲订单（等待主单成交），跳过网格层级 %dc (价格: %dc)", gridLevel, currentPrice.Cents)
				for entryOrderID, hedgeOrder := range pendingHedgeOrders {
					log.Infof("   待提交对冲订单: 主单ID=%s, 对冲订单ID=%s, %s币 @ %dc",
						entryOrderID[:8], hedgeOrder.OrderID[:8], hedgeOrder.TokenType, hedgeOrder.Price.Cents)
				}
				return nil
			}

			// 2. 检查是否有未成交的订单（主单或对冲单）
			if len(activeOrdersMap) > 0 {
				// 检查是否有未成交的主单或对冲单
				hasPendingEntryOrder := false
				hasPendingHedgeOrder := false
				for _, order := range activeOrdersMap {
					if order.Status == domain.OrderStatusPending || order.Status == domain.OrderStatusOpen {
						if order.IsEntryOrder {
							hasPendingEntryOrder = true
						} else {
							hasPendingHedgeOrder = true
						}
					}
				}

				if hasPendingEntryOrder || hasPendingHedgeOrder {
					log.Infof("⚠️ [订单顺序] 有未成交订单，跳过网格层级 %dc (价格: %dc)", gridLevel, currentPrice.Cents)
					if hasPendingEntryOrder {
						log.Infof("   未成交主单: 等待主单成交")
					}
					if hasPendingHedgeOrder {
						log.Infof("   未成交对冲单: 等待对冲单成交")
					}
					for orderID, order := range activeOrdersMap {
						if order.Status == domain.OrderStatusPending || order.Status == domain.OrderStatusOpen {
							orderType := "主单"
							if !order.IsEntryOrder {
								orderType = "对冲单"
							}
							log.Infof("   活跃订单: %s (ID=%s, %s币 @ %dc, 状态=%s)",
								orderType, orderID[:8], order.TokenType, order.Price.Cents, string(order.Status))
						}
					}
					return nil
				}
			}

			// 3. 检查仓位状态
			if activePosition != nil {
				// 检查主单和对冲单是否都已成交
				entryOrderFilled := activePosition.EntryOrder != nil && activePosition.EntryOrder.IsFilled()
				hedgeOrderFilled := activePosition.HedgeOrder != nil && activePosition.HedgeOrder.IsFilled()

				if entryOrderFilled && hedgeOrderFilled {
					// 主单和对冲单都已成交，仓位已完全对冲（锁定利润），清空仓位以允许下一轮
					log.Infof("✅ [订单顺序] 上一轮主单和对冲单都已成交（锁定利润），清空仓位以开启新的一轮")
					s.mu.Lock()
					s.activePosition = nil
					s.mu.Unlock()
					// 继续执行，允许开始新的一轮
				} else {
					// 主单或对冲单未成交，不能开启新的一轮
					log.Infof("⚠️ [订单顺序] 上一轮未完全成交，不能开启新的一轮。跳过网格层级 %dc (价格: %dc)", gridLevel, currentPrice.Cents)
					log.Infof("   主单状态: %v, 对冲单状态: %v",
						entryOrderFilled, hedgeOrderFilled)
					if activePosition.EntryOrder != nil {
						log.Infof("   主单: %s币 @ %dc, 数量=%.2f, 状态=%s",
							activePosition.EntryOrder.TokenType, activePosition.EntryOrder.Price.Cents,
							activePosition.EntryOrder.Size, activePosition.EntryOrder.Status)
					}
					if activePosition.HedgeOrder != nil {
						log.Infof("   对冲单: %s币 @ %dc, 数量=%.2f, 状态=%s",
							activePosition.HedgeOrder.TokenType, activePosition.HedgeOrder.Price.Cents,
							activePosition.HedgeOrder.Size, activePosition.HedgeOrder.Status)
					}
					return nil
				}
			}
		}

		log.Infof("✅ [网格下单] 所有检查通过，准备创建订单: %s币, 网格层级=%dc, 当前价格=%dc",
			tokenType, gridLevel, currentPrice.Cents)

		// 下单锁已在函数开头获取，这里不需要再次获取
		// 下单标志已在函数开头设置，这里不需要再次设置
		defer func() {
			// 风险13修复：确保 isPlacingOrder 标志被重置，并清除设置时间
			// 注意：锁已在函数开头获取，defer函数执行时锁还在被持有（直到第1261行的defer释放），
			// 所以这里可以直接设置标志，不需要再次获取锁
			s.isPlacingOrder = false
			s.isPlacingOrderSetTime = time.Time{}
			log.Debugf("🔄 [下单] isPlacingOrder 标志已重置（handleGridLevelReached defer）")

			// 如果发生panic，清除防重复标记（允许重试）
			if err := recover(); err != nil {
				s.processedLevelsMu.Lock()
				if s.processedGridLevels != nil {
					delete(s.processedGridLevels, levelKey)
					log.Errorf("❌ [下单] 发生panic，已清除防重复标记: %v", err)
				}
				s.processedLevelsMu.Unlock()
				panic(err) // 重新抛出panic
			}
		}()

		// 为下单操作创建带超时的上下文（30秒超时）
		orderCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		// 获取订单簿最佳价格（使用实际成交价格，而不是网格层级价格）
		// 买入订单使用最佳卖价（best ask），确保以最佳价格成交
		var entryPrice domain.Price
		var hedgePrice domain.Price
		var entryOrder *domain.Order
		var hedgeOrder *domain.Order

		if tokenType == domain.TokenTypeUp {
			// UP 币达到网格层级：买入 UP 币，对冲买入 DOWN 币
			// 获取 UP 币的最佳卖价（best ask）
			bestAsk, _, err := s.tradingService.GetBestPrice(orderCtx, market.YesAssetID)
			if err != nil || bestAsk <= 0 {
				log.Warnf("无法获取UP币最佳卖价，使用网格层级价格: %v", err)
				entryPrice = domain.Price{Cents: gridLevel}
			} else {
				bestAskCents := int(bestAsk * 100 + 0.5) // 四舍五入
				// 验证价格合理性：如果获取到的价格异常（小于1分或大于100分），使用网格层级价格
				if bestAskCents < 1 || bestAskCents > 100 {
					log.Warnf("UP币最佳卖价异常: %.4f (%dc)，超出合理范围[1, 100]，使用网格层级价格 %dc",
						bestAsk, bestAskCents, gridLevel)
					entryPrice = domain.Price{Cents: gridLevel}
				} else {
					// 验证价格合理性：如果获取到的价格与网格层级差异过大（超过30分），使用网格层级价格
					priceDiff := bestAskCents - gridLevel
					if priceDiff < 0 {
						priceDiff = -priceDiff
					}
					if priceDiff > 30 {
						log.Warnf("UP币最佳卖价与网格层级差异较大: %.4f (%dc) vs %dc (差异=%dc)，使用网格层级价格",
							bestAsk, bestAskCents, gridLevel, priceDiff)
						entryPrice = domain.Price{Cents: gridLevel}
					} else {
						entryPrice = domain.PriceFromDecimal(bestAsk)
						log.Debugf("使用UP币最佳卖价: %.4f (网格层级: %dc)", bestAsk, gridLevel)
					}
				}
			}

			// 对冲价格计算：基于实际成交价格计算，确保锁定至少 ProfitTarget 的利润
			// 总成本 = entryPrice + hedgePrice
			// 无论哪个胜出，收益 = 100 - (entryPrice + hedgePrice) >= ProfitTarget
			// 所以：hedgePrice <= 100 - entryPrice - ProfitTarget
			hedgePriceCents := 100 - entryPrice.Cents - s.config.ProfitTarget
			if hedgePriceCents < 0 {
				hedgePriceCents = 0
			}
			hedgePrice = domain.Price{Cents: hedgePriceCents}

			log.Infof("网格交易: UP币网格层级=%dc, 买入UP币@%dc (最佳卖价), 对冲买入DOWN币@%dc (锁定利润≥%dc, 总成本=%dc)",
				gridLevel, entryPrice.Cents, hedgePrice.Cents, s.config.ProfitTarget, entryPrice.Cents+hedgePrice.Cents)

			// 计算入场订单金额和share数量
			entryAmount, entryShare := s.calculateOrderSize(entryPrice)

			// 入场订单：买入 UP 币（使用市价单 FAK，吃卖一价）
			entryOrder = &domain.Order{
				OrderID:      fmt.Sprintf("entry-up-%d-%d", gridLevel, time.Now().UnixNano()),
				AssetID:      market.YesAssetID,
				Side:         types.SideBuy,
				Price:        entryPrice,
				Size:         entryShare,
				GridLevel:    gridLevel,
				TokenType:    domain.TokenTypeUp,
				IsEntryOrder: true,
				Status:       domain.OrderStatusPending,
				CreatedAt:    time.Now(),
				OrderType:    types.OrderTypeFAK, // 市价单，吃卖一价
			}

			// 对冲订单：买入 DOWN 币
			if s.config.EnableDoubleSide {
				// 计算对冲订单金额和share数量
				hedgeAmount, hedgeShare := s.calculateOrderSize(hedgePrice)

				hedgeOrder = &domain.Order{
					OrderID:      fmt.Sprintf("hedge-down-%d-%d", gridLevel, time.Now().UnixNano()),
					AssetID:      market.NoAssetID,
					Side:         types.SideBuy,
					Price:        hedgePrice,
					Size:         hedgeShare,
					GridLevel:    gridLevel,
					TokenType:    domain.TokenTypeDown,
					IsEntryOrder: false,
					Status:       domain.OrderStatusPending,
					CreatedAt:    time.Now(),
					OrderType:    types.OrderTypeFAK, // 市价单，吃卖一价
				}

				log.Infof("🔧 [配置检查] EnableDoubleSide=%v, 已创建对冲订单: DOWN币 @ %dc, 数量=%.4f",
					s.config.EnableDoubleSide, hedgePrice.Cents, hedgeShare)
				log.Debugf("订单金额计算: 入场金额=%.2f USDC, share=%.4f; 对冲金额=%.2f USDC, share=%.4f",
					entryAmount, entryShare, hedgeAmount, hedgeShare)
			} else {
				log.Warnf("⚠️ [配置检查] EnableDoubleSide=%v, 未创建对冲订单！", s.config.EnableDoubleSide)
			}
		} else if tokenType == domain.TokenTypeDown {
			// DOWN 币达到网格层级（>= 62分）：买入 DOWN 币（因为 DOWN 币在涨）
			// 获取 DOWN 币的最佳卖价（best ask）
			bestAsk, _, err := s.tradingService.GetBestPrice(orderCtx, market.NoAssetID)
			if err != nil || bestAsk <= 0 {
				log.Warnf("无法获取DOWN币最佳卖价，使用网格层级价格: %v", err)
				entryPrice = domain.Price{Cents: gridLevel}
			} else {
				bestAskCents := int(bestAsk * 100 + 0.5) // 四舍五入
				// 验证价格合理性：如果获取到的价格异常（小于1分或大于100分），使用网格层级价格
				if bestAskCents < 1 || bestAskCents > 100 {
					log.Warnf("DOWN币最佳卖价异常: %.4f (%dc)，超出合理范围[1, 100]，使用网格层级价格 %dc",
						bestAsk, bestAskCents, gridLevel)
					entryPrice = domain.Price{Cents: gridLevel}
				} else {
					// 验证价格合理性：如果获取到的价格与网格层级差异过大（超过30分），使用网格层级价格
					priceDiff := bestAskCents - gridLevel
					if priceDiff < 0 {
						priceDiff = -priceDiff
					}
					if priceDiff > 30 {
						log.Warnf("DOWN币最佳卖价与网格层级差异较大: %.4f (%dc) vs %dc (差异=%dc)，使用网格层级价格",
							bestAsk, bestAskCents, gridLevel, priceDiff)
						entryPrice = domain.Price{Cents: gridLevel}
					} else {
						entryPrice = domain.PriceFromDecimal(bestAsk)
						log.Debugf("使用DOWN币最佳卖价: %.4f (网格层级: %dc)", bestAsk, gridLevel)
					}
				}
			}

			// 对冲价格计算：基于实际成交价格计算，确保锁定至少 ProfitTarget 的利润
			// 总成本 = entryPrice + hedgePrice
			// 无论哪个胜出，收益 = 100 - (entryPrice + hedgePrice) >= ProfitTarget
			// 所以：hedgePrice <= 100 - entryPrice - ProfitTarget
			hedgePriceCents := 100 - entryPrice.Cents - s.config.ProfitTarget
			if hedgePriceCents < 0 {
				hedgePriceCents = 0
			}
			hedgePrice = domain.Price{Cents: hedgePriceCents}

			log.Infof("网格交易: DOWN币价格达到%dc（网格层级），买入DOWN币@%dc，对冲买入UP币@%dc (锁定利润≥%dc, 总成本=%dc)",
				gridLevel, entryPrice.Cents, hedgePrice.Cents, s.config.ProfitTarget, entryPrice.Cents+hedgePrice.Cents)

			// 计算入场订单金额和share数量
			entryAmount, entryShare := s.calculateOrderSize(entryPrice)

			// 入场订单：买入 DOWN 币（使用市价单 FAK，吃卖一价）
			entryOrder = &domain.Order{
				OrderID:      fmt.Sprintf("entry-down-%d-%d", gridLevel, time.Now().UnixNano()),
				AssetID:      market.NoAssetID,
				Side:         types.SideBuy,
				Price:        entryPrice,
				Size:         entryShare,
				GridLevel:    hedgePriceCents, // 记录对应的 UP 币网格层级
				TokenType:    domain.TokenTypeDown,
				IsEntryOrder: true,
				Status:       domain.OrderStatusPending,
				CreatedAt:    time.Now(),
				OrderType:    types.OrderTypeFAK, // 市价单，吃卖一价
			}

			// 对冲订单：买入 UP 币
			if s.config.EnableDoubleSide {
				// 计算对冲订单金额和share数量
				hedgeAmount, hedgeShare := s.calculateOrderSize(hedgePrice)

				hedgeOrder = &domain.Order{
					OrderID:      fmt.Sprintf("hedge-up-%d-%d", gridLevel, time.Now().UnixNano()),
					AssetID:      market.YesAssetID,
					Side:         types.SideBuy,
					Price:        hedgePrice,
					Size:         hedgeShare,
					GridLevel:    hedgePriceCents,
					TokenType:    domain.TokenTypeUp,
					IsEntryOrder: false,
					Status:       domain.OrderStatusPending,
					CreatedAt:    time.Now(),
					OrderType:    types.OrderTypeFAK, // 市价单，吃卖一价
				}

				log.Infof("🔧 [配置检查] EnableDoubleSide=%v, 已创建对冲订单: UP币 @ %dc, 数量=%.4f",
					s.config.EnableDoubleSide, hedgePrice.Cents, hedgeShare)
				log.Debugf("订单金额计算: 入场金额=%.2f USDC, share=%.4f; 对冲金额=%.2f USDC, share=%.4f",
					entryAmount, entryShare, hedgeAmount, hedgeShare)
			} else {
				log.Warnf("⚠️ [配置检查] EnableDoubleSide=%v, 未创建对冲订单！", s.config.EnableDoubleSide)
			}
		} else {
			return fmt.Errorf("不支持的 token 类型: %s", tokenType)
		}

		// 提交入场订单
		if s.tradingService == nil {
			log.Errorf("❌ 交易服务未设置，无法下单！请检查策略初始化")
			// 重构后：activeOrders 已移除，订单由 OrderEngine 管理
			return fmt.Errorf("交易服务未设置，无法下单")
		}

		log.Infof("📤 [网格下单] 准备提交%s币入场订单: orderID=%s, assetID=%s, 价格=%dc (%.4f), 数量=%.4f",
			tokenType, entryOrder.OrderID, entryOrder.AssetID, entryPrice.Cents, entryPrice.ToDecimal(), entryOrder.Size)

		// 保存原始订单ID，用于更新 pendingHedgeOrders 的 key
		originalOrderID := entryOrder.OrderID

		createdOrder, err := s.tradingService.PlaceOrder(orderCtx, entryOrder)
		if err != nil {
			log.Errorf("❌ [网格下单] %s币买入订单失败: %v", tokenType, err)
			// 检查是否是超时错误
			if orderCtx.Err() == context.DeadlineExceeded {
				log.Errorf("❌ [网格下单] 下单超时（30秒），可能网络问题或API响应慢")
			}
			// 检查是否是队列已满错误
			if strings.Contains(err.Error(), "队列已满") {
				log.Errorf("❌ [网格下单] 订单队列已满，无法添加订单，可能订单处理速度跟不上")
			}
			// 重构后：activeOrders 已移除，订单由 OrderEngine 管理，无需手动清理
			// 清理对应的待提交对冲订单（主单失败，对冲订单也不应该提交）
			if hedgeOrder != nil {
				delete(s.pendingHedgeOrders, entryOrder.OrderID)
				log.Debugf("🧹 [订单顺序] 主单失败，已清理对应的待提交对冲订单: 主单ID=%s", entryOrder.OrderID)
			}
			// 订单提交失败，清除防重复标记（允许重试）
			s.processedLevelsMu.Lock()
			if s.processedGridLevels != nil {
				delete(s.processedGridLevels, levelKey)
				log.Debugf("🔄 [防重复] 订单提交失败，已清除防重复标记，允许重试: %s:%dc", tokenType, gridLevel)
			}
			s.processedLevelsMu.Unlock()
			return fmt.Errorf("%s币买入订单失败: %w", tokenType, err)
		}

		// 风险1修复：原子化更新订单ID和数量（如果服务器返回了新的订单ID或调整了数量）
		if createdOrder != nil {
			// 在锁内原子化更新所有相关映射
			s.mu.Lock()

			// 检查订单数量是否被调整
			originalSize := entryOrder.Size
			if createdOrder.Size != originalSize {
				log.Warnf("⚠️ [订单调整] 入场订单数量被调整: %.4f → %.4f shares", originalSize, createdOrder.Size)

				// 同步调整对冲订单数量，保持对冲比例一致
				if hedgeOrder != nil {
					// 计算调整比例
					adjustmentRatio := createdOrder.Size / originalSize
					originalHedgeSize := hedgeOrder.Size
					adjustedHedgeSize := hedgeOrder.Size * adjustmentRatio

					// 确保对冲订单数量满足最小值要求
					const minShareSize = 5.0
					if adjustedHedgeSize < minShareSize {
						adjustedHedgeSize = minShareSize
						log.Warnf("⚠️ [订单调整] 对冲订单数量调整后小于最小值，使用最小值: %.4f → %.4f shares",
							hedgeOrder.Size*adjustmentRatio, adjustedHedgeSize)
					}

					hedgeOrder.Size = adjustedHedgeSize
					log.Infof("🔄 [订单调整] 对冲订单数量已同步调整: %.4f → %.4f shares (调整比例: %.4f)",
						originalHedgeSize, adjustedHedgeSize, adjustmentRatio)
				}

				// 更新入场订单数量
				entryOrder.Size = createdOrder.Size
			}

			// 风险1修复：原子化更新订单ID和相关映射
			if createdOrder.OrderID != originalOrderID {
				entryOrder.OrderID = createdOrder.OrderID
				log.Infof("🔄 [订单ID变更] 订单ID已更新: %s → %s", originalOrderID, createdOrder.OrderID)

				// 重构后：activeOrders 已移除，订单由 OrderEngine 管理，无需手动更新映射
				log.Debugf("🔄 [订单ID变更] 订单ID已更新: %s → %s (由 OrderEngine 管理)", originalOrderID, createdOrder.OrderID)

				// 原子化更新 pendingHedgeOrders 的 key（如果存在）
				if hedgeOrder != nil {
					if existingHedgeOrder, exists := s.pendingHedgeOrders[originalOrderID]; exists {
						delete(s.pendingHedgeOrders, originalOrderID)
						s.pendingHedgeOrders[createdOrder.OrderID] = existingHedgeOrder
						log.Infof("🔄 [订单ID变更] pendingHedgeOrders映射已更新: %s → %s", originalOrderID, createdOrder.OrderID)
					} else {
						log.Warnf("⚠️ [订单ID变更] pendingHedgeOrders中未找到原始订单ID: %s，可能已被删除", originalOrderID)
					}
				}
			}

			s.mu.Unlock()
		} else {
			// 重构后：activeOrders 已移除，订单由 OrderEngine 管理
			log.Debugf("订单已提交到 OrderEngine: %s", entryOrder.OrderID)
		}
		entryAmount := entryOrder.Price.ToDecimal() * entryOrder.Size
		log.Infof("✅ [网格下单] %s币买入订单已提交（市价单FAK，吃卖一价）: orderID=%s, 价格=%dc (%.4f), 数量=%.4f, 金额=%.2f USDC",
			tokenType, entryOrder.OrderID, entryPrice.Cents, entryPrice.ToDecimal(), entryOrder.Size, entryAmount)

		// 订单提交成功，防重复标记已在函数开头设置，这里只需要确认
		log.Debugf("📌 [防重复] 网格层级 %s:%dc 订单已提交成功，30秒内不会重复触发", tokenType, gridLevel)

		// 保存对冲订单到待提交列表（等待主单成交后再提交）
		if hedgeOrder != nil {
			log.Infof("⏳ [订单顺序] 对冲订单已创建，等待主单成交后再提交: EnableDoubleSide=%v", s.config.EnableDoubleSide)
			hedgeOrder.HedgeOrderID = &entryOrder.OrderID
			entryOrder.PairOrderID = &hedgeOrder.OrderID

			// 将对冲订单保存到待提交列表，关联到主单的 OrderID（使用更新后的ID）
			s.pendingHedgeOrders[entryOrder.OrderID] = hedgeOrder
			log.Infof("📋 [订单顺序] 对冲订单已保存到待提交列表: 主单ID=%s, 对冲订单ID=%s, 价格=%dc (%.4f), 数量=%.4f",
				entryOrder.OrderID, hedgeOrder.OrderID, hedgeOrder.Price.Cents, hedgeOrder.Price.ToDecimal(), hedgeOrder.Size)
		} else {
			log.Warnf("⚠️ [调试] hedgeOrder为nil！EnableDoubleSide=%v, 未创建对冲订单", s.config.EnableDoubleSide)
		}

		// 只有至少一个订单成功提交，才增加轮数
		if s.hasActiveOrders() {
			s.roundsThisPeriod++
		}
		return nil
	}

	*/

}

func (s *GridStrategy) OnOrderFilled(ctx context.Context, event *events.OrderFilledEvent) error {
	log.Debugf("📥 [订单成交] OnOrderFilled开始处理: orderID=%s, status=%s", event.Order.OrderID, event.Order.Status)

	// 风险10修复：订单成交事件去重
	// 使用订单ID + 成交时间的组合作为去重key
	if event.Order.FilledAt == nil {
		log.Warnf("⚠️ [订单成交去重] 订单成交事件缺少FilledAt时间戳: orderID=%s", event.Order.OrderID)
		// 如果没有FilledAt，使用当前时间（但这不是理想情况）
		now := time.Now()
		event.Order.FilledAt = &now
	}

	// 检查是否已处理过该订单成交事件
	s.processedFilledOrdersMu.Lock()
	// 确保 map 已初始化（防止 nil map panic）
	if s.processedFilledOrders == nil {
		s.processedFilledOrders = make(map[string]time.Time)
	}
	if existingFilledAt, exists := s.processedFilledOrders[event.Order.OrderID]; exists {
		// 检查是否是同一个成交事件（相同的时间戳，允许1秒误差）
		timeDiff := existingFilledAt.Sub(*event.Order.FilledAt)
		if timeDiff < 0 {
			timeDiff = -timeDiff
		}
		if timeDiff < time.Second {
			s.processedFilledOrdersMu.Unlock()
			log.Warnf("⚠️ [订单成交去重] 订单成交事件已处理过，跳过重复处理: orderID=%s, filledAt=%v, 时间差=%v",
				event.Order.OrderID, event.Order.FilledAt, timeDiff)
			return nil
		}
		// 如果是不同的成交时间，可能是部分成交或新的成交事件，记录警告但继续处理
		log.Warnf("⚠️ [订单成交去重] 订单有多个成交时间戳: orderID=%s, 旧时间=%v, 新时间=%v, 时间差=%v",
			event.Order.OrderID, existingFilledAt, event.Order.FilledAt, timeDiff)
	}
	// 记录已处理的订单成交事件
	s.processedFilledOrders[event.Order.OrderID] = *event.Order.FilledAt

	// 清理旧的记录（保留最近1小时的记录，避免内存泄漏）
	now := time.Now()
	for orderID, filledAt := range s.processedFilledOrders {
		if now.Sub(filledAt) > time.Hour {
			delete(s.processedFilledOrders, orderID)
		}
	}
	s.processedFilledOrdersMu.Unlock()

	// 第一步：在锁内快速完成订单查找和状态更新（最小化持锁时间）
	var order *domain.Order
	var originalOrderID string
	var exists bool
	var hedgeOrder *domain.Order
	var hasPendingHedge bool

	// 复制需要的数据，避免在锁外访问共享状态
	var market *domain.Market
	var config *GridStrategyConfig

	func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		// 保存市场引用（用于后续处理）
		market = s.currentMarket
		config = s.config

		// 首先通过订单 ID 查找
		// 重构后：从 OrderEngine 查询订单
		activeOrders := s.getActiveOrders()
		order = nil
		exists = false
		for _, o := range activeOrders {
			if o.OrderID == event.Order.OrderID {
				order = o
				exists = true
				break
			}
		}

		// 如果通过订单 ID 找不到，尝试通过属性匹配（处理订单 ID 不匹配的情况）
		if !exists {
			// 利用业务规则优化匹配：
			// - 入场订单价格范围：60-90（网格层级）
			// - 对冲订单价格范围：1-40（因为总成本 <= 100，且要保证利润目标）

			// 重构后：从 OrderEngine 查询活跃订单进行匹配
			activeOrders := s.getActiveOrders()
			activeOrdersMap := make(map[string]*domain.Order)
			for _, o := range activeOrders {
				activeOrdersMap[o.OrderID] = o
			}

			// 首先尝试精确匹配：assetID + side + price
			for localOrderID, localOrder := range activeOrdersMap {
				if localOrder.AssetID == event.Order.AssetID &&
					localOrder.Side == event.Order.Side &&
					localOrder.Price.Cents == event.Order.Price.Cents {
					// 找到匹配的订单
					log.Infof("🔄 [策略] 通过精确属性匹配找到订单: 本地ID=%s, 事件ID=%s, assetID=%s, side=%s, price=%dc",
						localOrderID, event.Order.OrderID, event.Order.AssetID, event.Order.Side, event.Order.Price.Cents)

					// 保存原始订单ID（用于查找对冲订单）
					originalOrderID = localOrderID

					// 更新订单 ID
					order = localOrder
					order.OrderID = event.Order.OrderID

					// 重构后：activeOrders 由 OrderEngine 管理，无需手动更新映射
					// 如果 pendingHedgeOrders 中有这个本地订单ID，需要更新key
					if hedgeOrder, hasHedge := s.pendingHedgeOrders[localOrderID]; hasHedge {
						delete(s.pendingHedgeOrders, localOrderID)
						s.pendingHedgeOrders[event.Order.OrderID] = hedgeOrder
						log.Debugf("🔄 [订单顺序] 更新对冲订单映射: 本地ID=%s → 真实ID=%s", localOrderID, event.Order.OrderID)
					}

					exists = true
					break
				}
			}

			// 如果精确匹配失败，尝试通过业务规则匹配（允许价格略有差异）
			if !exists {
				for localOrderID, localOrder := range activeOrdersMap {
					// 检查 assetID 和 side 是否匹配
					if localOrder.AssetID != event.Order.AssetID || localOrder.Side != event.Order.Side {
						continue
					}

					// 利用业务规则验证价格范围
					// 注意：只匹配网格层级范围内的价格，避免误匹配手工订单
					priceMatched := false
					if localOrder.IsEntryOrder {
						// 入场订单：价格必须在网格层级范围内
						// 检查价格是否在网格层级列表中（允许±2分的差异）
						isInGridLevels := false
						for _, level := range s.grid.Levels {
							priceDiff := localOrder.Price.Cents - level
							if priceDiff < 0 {
								priceDiff = -priceDiff
							}
							if priceDiff <= 2 {
								isInGridLevels = true
								break
							}
						}

						// 只有价格在网格层级范围内，且事件价格也在范围内时，才匹配
						if isInGridLevels {
							priceDiff := localOrder.Price.Cents - event.Order.Price.Cents
							if priceDiff < 0 {
								priceDiff = -priceDiff
							}
							if priceDiff <= 2 {
								priceMatched = true
							}
						}
					} else {
						// 对冲订单：价格应该在 1-40 之间（基于利润目标计算）
						// 允许价格略有差异（±2分）
						if localOrder.Price.Cents >= 1 && localOrder.Price.Cents <= 40 &&
							event.Order.Price.Cents >= 1 && event.Order.Price.Cents <= 40 {
							priceDiff := localOrder.Price.Cents - event.Order.Price.Cents
							if priceDiff < 0 {
								priceDiff = -priceDiff
							}
							if priceDiff <= 2 {
								priceMatched = true
							}
						}
					}

					if priceMatched {
						// 找到匹配的订单（通过业务规则）
						log.Infof("🔄 [策略] 通过业务规则匹配找到订单: 本地ID=%s, 事件ID=%s, assetID=%s, side=%s, 本地价格=%dc, 事件价格=%dc, 订单类型=%s",
							localOrderID, event.Order.OrderID, event.Order.AssetID, event.Order.Side,
							localOrder.Price.Cents, event.Order.Price.Cents,
							map[bool]string{true: "入场", false: "对冲"}[localOrder.IsEntryOrder])

						// 保存原始订单ID（用于查找对冲订单）
						originalOrderID = localOrderID

						// 更新订单 ID 和价格（使用事件中的价格，因为这是服务器返回的实际价格）
						order = localOrder
						order.OrderID = event.Order.OrderID
						order.Price = event.Order.Price

						// 重构后：activeOrders 由 OrderEngine 管理，无需手动更新映射

						// 如果 pendingHedgeOrders 中有这个本地订单ID，需要更新key
						if hedgeOrder, hasHedge := s.pendingHedgeOrders[localOrderID]; hasHedge {
							delete(s.pendingHedgeOrders, localOrderID)
							s.pendingHedgeOrders[event.Order.OrderID] = hedgeOrder
							log.Debugf("🔄 [订单顺序] 更新对冲订单映射: 本地ID=%s → 真实ID=%s", localOrderID, event.Order.OrderID)
						}

						exists = true
						break
					}
				}
			}
		} else {
			// 如果直接找到了订单，使用订单ID作为原始ID
			originalOrderID = event.Order.OrderID
		}

		// 如果订单不在策略的 activeOrders 中，可能是手动订单，需要接管
		// 但只处理价格在网格层级范围内的订单，避免误处理其他手工订单
		if !exists && event.Order.Side == types.SideBuy {
			// 检查价格是否在网格层级范围内（允许±2分的差异）
			isInGridLevels := false
			for _, level := range s.grid.Levels {
				priceDiff := event.Order.Price.Cents - level
				if priceDiff < 0 {
					priceDiff = -priceDiff
				}
				if priceDiff <= 2 {
					isInGridLevels = true
					break
				}
			}

			if !isInGridLevels {
				// 价格不在网格层级范围内，不是我们的订单，跳过处理
				log.Debugf("🔍 [手动订单检测] 订单价格 %dc 不在网格层级范围内 %v，跳过处理: orderID=%s",
					event.Order.Price.Cents, s.grid.Levels, event.Order.OrderID)
				return // 从匿名函数返回
			}

			log.Infof("🔍 [手动订单检测] 检测到订单不在策略订单列表中，可能是手动订单: orderID=%s, assetID=%s, side=%s, price=%.4f, size=%.2f",
				event.Order.OrderID, event.Order.AssetID, event.Order.Side, event.Order.Price.ToDecimal(), event.Order.Size)

			// 识别 TokenType（通过 assetID 和 market 比较）
			var tokenType domain.TokenType
			if event.Market != nil {
				if event.Order.AssetID == event.Market.YesAssetID {
					tokenType = domain.TokenTypeUp
					log.Debugf("🔍 [手动订单检测] 识别为 UP币: assetID=%s == market.YesAssetID=%s",
						event.Order.AssetID, event.Market.YesAssetID)
				} else if event.Order.AssetID == event.Market.NoAssetID {
					tokenType = domain.TokenTypeDown
					log.Debugf("🔍 [手动订单检测] 识别为 DOWN币: assetID=%s == market.NoAssetID=%s",
						event.Order.AssetID, event.Market.NoAssetID)
				} else {
					// 无法识别 token 类型，跳过处理
					log.Warnf("⚠️ [手动订单] 无法识别 token 类型，跳过处理: assetID=%s, market.YesAssetID=%s, market.NoAssetID=%s",
						event.Order.AssetID, event.Market.YesAssetID, event.Market.NoAssetID)
					return // 从匿名函数返回
				}
			} else {
				// 没有 market 信息，无法处理手动订单
				log.Warnf("⚠️ [手动订单] 缺少 market 信息，无法处理手动订单: orderID=%s", event.Order.OrderID)
				return // 从匿名函数返回
			}

			// 创建订单对象（手动订单）
			order = &domain.Order{
				OrderID:      event.Order.OrderID,
				AssetID:      event.Order.AssetID,
				Side:         event.Order.Side,
				Price:        event.Order.Price,
				Size:         event.Order.Size,
				TokenType:    tokenType,
				IsEntryOrder: true, // 手动订单视为入场订单
				Status:       domain.OrderStatusFilled,
				CreatedAt:    event.Order.CreatedAt,
			}
			now := time.Now()
			order.FilledAt = &now

			log.Infof("🤖 [手动订单接管] ✅ 已接管手动买入订单: %s币 @ %dc (%.4f), 数量=%.2f, orderID=%s",
				tokenType, order.Price.Cents, order.Price.ToDecimal(), order.Size, order.OrderID)

			// 标记为已找到，继续处理
			exists = true
			originalOrderID = order.OrderID
		}

		// 如果找到了订单，更新订单状态和查找对冲订单（仍在锁内，快速操作）
		if exists {
			now := time.Now()
			// 只有订单状态不是已成交时才更新（手动订单可能已经是已成交状态）
			if order.Status != domain.OrderStatusFilled {
				order.Status = domain.OrderStatusFilled
				order.FilledAt = &now
			}
			// 重构后：activeOrders 由 OrderEngine 管理，无需手动删除

			// 查找对冲订单（如果存在）
			lookupOrderID := originalOrderID
			if lookupOrderID == "" {
				lookupOrderID = order.OrderID
			}
			if hedgeOrder, hasPendingHedge = s.pendingHedgeOrders[lookupOrderID]; hasPendingHedge {
				delete(s.pendingHedgeOrders, lookupOrderID)
			}

		}
	}() // 锁内操作结束

	// 如果没有找到订单，直接返回
	if !exists || order == nil {
		log.Debugf("📥 [订单成交] 订单未找到或已处理，跳过: orderID=%s", event.Order.OrderID)
		return nil
	}

	// 第二步：在锁外处理复杂业务逻辑（在策略单线程 loop 内执行，避免并发竞态）
	{
		// 风险13修复：确保 isPlacingOrder 标志在订单成交处理开始时重置
		// 这可以防止订单立即成交后，标志未重置导致后续价格更新被阻塞
		s.placeOrderMu.Lock()
		if s.isPlacingOrder {
			log.Infof("🔄 [订单成交处理] 检测到 isPlacingOrder=true，重置标志（订单已成交）")
			s.isPlacingOrder = false
			s.isPlacingOrderSetTime = time.Time{}
		}
		s.placeOrderMu.Unlock()

		defer func() {
			// 风险13修复：确保在处理结束时再次检查并重置标志
			s.placeOrderMu.Lock()
			if s.isPlacingOrder {
				log.Warnf("⚠️ [订单成交处理] 结束时检测到 isPlacingOrder=true，强制重置")
				s.isPlacingOrder = false
				s.isPlacingOrderSetTime = time.Time{}
			}
			s.placeOrderMu.Unlock()

			if r := recover(); r != nil {
				log.Errorf("❌ [订单成交处理] 发生panic: %v", r)
				log.Errorf("   堆栈信息: %s", string(debug.Stack()))
			}
		}()

		// 显示订单成交信息
		orderType := "入场"
		if !order.IsEntryOrder {
			orderType = "对冲"
		}
		log.Infof("✅ %s订单已成交: %s币 @ %dc (%.4f), 数量=%.2f, 网格层级=%dc",
			orderType, order.TokenType, order.Price.Cents, order.Price.ToDecimal(), order.Size, order.GridLevel)

		// 如果是入场订单成交（买入订单），创建或更新仓位
		if order.IsEntryOrder && order.Side == types.SideBuy {
			// 更新双向持仓跟踪（需要在锁内更新）
			cost := order.Size * order.Price.ToDecimal()
			func() {
				s.mu.Lock()
				defer s.mu.Unlock()

				if order.TokenType == domain.TokenTypeUp {
					s.upTotalCost += cost
					s.upHoldings += order.Size
					log.Debugf("📊 [持仓跟踪] UP币: 成本+=%.8f, 持仓+=%.8f, 总成本=%.8f, 总持仓=%.8f",
						cost, order.Size, s.upTotalCost, s.upHoldings)
				} else if order.TokenType == domain.TokenTypeDown {
					s.downTotalCost += cost
					s.downHoldings += order.Size
					log.Debugf("📊 [持仓跟踪] DOWN币: 成本+=%.8f, 持仓+=%.8f, 总成本=%.8f, 总持仓=%.8f",
						cost, order.Size, s.downTotalCost, s.downHoldings)
				}

				now := time.Now()
				if s.activePosition == nil {
					// 创建新仓位
					s.activePosition = &domain.Position{
						ID:         fmt.Sprintf("grid-%s-%d", order.TokenType, now.UnixNano()),
						Market:     market,
						EntryOrder: order,
						EntryPrice: order.Price,
						EntryTime:  now,
						Size:       order.Size,
						TokenType:  order.TokenType,
						Status:     domain.PositionStatusOpen,
						Unhedged:   true,
					}
					log.Infof("📊 新仓位已创建: %s币 @ %dc, 数量=%.2f", order.TokenType, order.Price.Cents, order.Size)
				} else {
					// 更新现有仓位（如果入场订单已存在，更新其状态）
					// 注意：只有在仓位存在但订单ID不同时才更新（避免覆盖已成交的订单）
					if s.activePosition.EntryOrder == nil || s.activePosition.EntryOrder.OrderID != order.OrderID {
						s.activePosition.EntryOrder = order
						s.activePosition.EntryPrice = order.Price
						s.activePosition.Size = order.Size
						log.Debugf("📊 仓位已更新: %s币 @ %dc, 数量=%.2f", order.TokenType, order.Price.Cents, order.Size)
					}
				}
			}()

			// ✅ 主单已成交，现在可以提交对冲订单了
			// 使用从锁内获取的对冲订单信息
			if hasPendingHedge && hedgeOrder != nil {
				log.Infof("✅ [订单顺序] 主单已成交，现在提交对冲订单: 主单ID=%s, 对冲订单ID=%s",
					order.OrderID, hedgeOrder.OrderID)
			} else if config.EnableDoubleSide {
				// 如果是手动订单且启用了双向对冲，自动创建对冲订单
				log.Infof("🤖 [手动订单对冲] 📋 手动订单已成交，开始自动创建对冲订单")
				log.Infof("   主单信息: %s币 @ %dc (%.4f), 数量=%.2f, orderID=%s",
					order.TokenType, order.Price.Cents, order.Price.ToDecimal(), order.Size, order.OrderID)
				log.Infof("   配置信息: EnableDoubleSide=%v, ProfitTarget=%dc",
					config.EnableDoubleSide, config.ProfitTarget)

				// 计算对冲价格（确保利润目标）
				hedgePriceCents := 100 - order.Price.Cents - config.ProfitTarget
				if hedgePriceCents < 0 {
					hedgePriceCents = 0
					log.Warnf("⚠️ [手动订单对冲] 对冲价格计算结果为负数，调整为0")
				}
				hedgePrice := domain.Price{Cents: hedgePriceCents}

				// 确定对冲 token 类型和 assetID
				var hedgeTokenType domain.TokenType
				var hedgeAssetID string
				if order.TokenType == domain.TokenTypeUp {
					hedgeTokenType = domain.TokenTypeDown
					hedgeAssetID = market.NoAssetID
				} else {
					hedgeTokenType = domain.TokenTypeUp
					hedgeAssetID = market.YesAssetID
				}

				log.Debugf("🤖 [手动订单对冲] 对冲方向: %s币 → %s币, assetID=%s",
					order.TokenType, hedgeTokenType, hedgeAssetID)

				// 计算对冲订单数量（需要在锁内访问s.config，但我们已经复制了config）
				hedgeAmount, hedgeShare := s.calculateOrderSize(hedgePrice)
				log.Debugf("🤖 [手动订单对冲] 对冲订单计算: 价格=%dc (%.4f), 数量=%.4f, 金额=%.2f USDC",
					hedgePriceCents, hedgePrice.ToDecimal(), hedgeShare, hedgeAmount)

				// 创建对冲订单
				hedgeOrder = &domain.Order{
					OrderID:      fmt.Sprintf("auto-hedge-%s-%d-%d", hedgeTokenType, hedgePriceCents, time.Now().UnixNano()),
					AssetID:      hedgeAssetID,
					Side:         types.SideBuy,
					Price:        hedgePrice,
					Size:         hedgeShare,
					GridLevel:    hedgePriceCents,
					TokenType:    hedgeTokenType,
					IsEntryOrder: false,
					Status:       domain.OrderStatusPending,
					CreatedAt:    time.Now(),
					OrderType:    types.OrderTypeFAK, // 市价单，吃卖一价
				}

				hasPendingHedge = true
				log.Infof("🤖 [手动订单对冲] ✅ 对冲订单已创建: %s币 @ %dc (%.4f), 数量=%.4f, 金额=%.2f USDC",
					hedgeTokenType, hedgePrice.Cents, hedgePrice.ToDecimal(), hedgeShare, hedgeAmount)
				log.Infof("   总成本: %.2f USDC (主单: %.2f + 对冲: %.2f), 锁定利润≥%dc",
					order.Size*order.Price.ToDecimal()+hedgeAmount, order.Size*order.Price.ToDecimal(), hedgeAmount, config.ProfitTarget)
			} else {
				log.Warnf("⚠️ [手动订单对冲] EnableDoubleSide=%v，未启用双向对冲，不会自动创建对冲订单", config.EnableDoubleSide)
			}

			if hasPendingHedge && hedgeOrder != nil {
				// 风险8修复：使用对冲订单提交锁，确保同一时间只有一个goroutine提交对冲订单
				s.hedgeOrderSubmitMu.Lock()

				// 在锁内再次检查（防止在获取锁的过程中，其他goroutine已经提交了对冲订单）
				if len(s.pendingHedgeOrders) == 0 {
					s.hedgeOrderSubmitMu.Unlock()
					log.Debugf("📋 [订单顺序] 锁内检查：对冲订单已不在待提交列表中，可能已被其他goroutine提交，跳过")
					return nil
				}

				// 重构后：activeOrders 由 OrderEngine 管理，无需手动添加
				// 从待提交列表中删除
				func() {
					s.mu.Lock()
					defer s.mu.Unlock()
					delete(s.pendingHedgeOrders, order.OrderID)
					log.Debugf("📋 [订单顺序] 对冲订单已从待提交列表中移除，开始提交: 主单ID=%s, 对冲订单ID=%s",
						order.OrderID[:8], hedgeOrder.OrderID[:8])
				}()

				// 判断是对冲订单类型（策略创建的还是手动订单自动创建的）
				isManualHedge := strings.HasPrefix(hedgeOrder.OrderID, "auto-hedge-")
				hedgeType := "策略"
				if isManualHedge {
					hedgeType = "手动订单自动"
				}

				// 提交对冲订单（在当前goroutine中执行，避免嵌套goroutine）
				hedgeOrderToSubmit := hedgeOrder
				if s.tradingService == nil {
					log.Errorf("❌ [%s对冲] 交易服务未设置，无法提交对冲订单", hedgeType)
					// 重构后：activeOrders 由 OrderEngine 管理
					s.hedgeOrderSubmitMu.Unlock()
					return nil
				}

				if isManualHedge {
					log.Infof("📤 [手动订单对冲] 🚀 准备提交对冲订单: %s币 @ %dc (%.4f), 数量=%.4f, orderID=%s",
						hedgeOrderToSubmit.TokenType, hedgeOrderToSubmit.Price.Cents, hedgeOrderToSubmit.Price.ToDecimal(), hedgeOrderToSubmit.Size, hedgeOrderToSubmit.OrderID)
				} else {
					log.Infof("📤 [网格下单] 准备提交%s币对冲订单: orderID=%s, assetID=%s, 价格=%dc (%.4f), 数量=%.4f",
						hedgeOrderToSubmit.TokenType, hedgeOrderToSubmit.OrderID, hedgeOrderToSubmit.AssetID, hedgeOrderToSubmit.Price.Cents, hedgeOrderToSubmit.Price.ToDecimal(), hedgeOrderToSubmit.Size)
				}

				// 使用新的 context，避免使用已取消的 ctx
				// 设置超时保护，确保不会无限期阻塞
				hedgeCtx, hedgeCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer hedgeCancel()

				// 保存原始订单ID，用于更新 activeOrders 的 key
				originalHedgeOrderID := hedgeOrderToSubmit.OrderID

				// 诊断：记录对冲订单提交开始时间
				hedgeOrderStartTime := time.Now()
				log.Debugf("📤 [对冲订单提交] 开始提交对冲订单: orderID=%s, 开始时间=%v",
					hedgeOrderToSubmit.OrderID, hedgeOrderStartTime)

				// 下一阶段工程化：对冲下单通过 Executor 串行执行，策略 loop 不直接阻塞网络 IO
				planID := fmt.Sprintf("grid-hedge-%d", time.Now().UnixNano())
				if s.plan != nil {
					planID = s.plan.ID
					s.plan.State = PlanHedgeSubmitting
				}
				if err := s.submitPlaceOrderCmd(context.Background(), planID, gridCmdPlaceHedge, hedgeOrderToSubmit); err != nil {
					log.Errorf("❌ [网格下单] %s币对冲买入订单提交失败（执行器）: %v", hedgeOrderToSubmit.TokenType, err)
					s.mu.Lock()
					if s.activePosition != nil {
						s.activePosition.Unhedged = true
					}
					s.mu.Unlock()
					s.hedgeOrderSubmitMu.Unlock()
					return nil
				}
				// 提交成功：等待 cmdResult + 订单更新驱动后续状态
				s.hedgeOrderSubmitMu.Unlock()
				return nil

				createdHedgeOrder, err := s.tradingService.PlaceOrder(hedgeCtx, hedgeOrderToSubmit)

				// 诊断：记录对冲订单提交耗时
				hedgeOrderDuration := time.Since(hedgeOrderStartTime)
				if hedgeOrderDuration > 1*time.Second {
					log.Warnf("⚠️ [对冲订单提交诊断] 对冲订单提交耗时较长: orderID=%s, 耗时=%v",
						hedgeOrderToSubmit.OrderID, hedgeOrderDuration)
				} else {
					log.Debugf("📤 [对冲订单提交] 对冲订单提交完成: orderID=%s, 耗时=%v",
						hedgeOrderToSubmit.OrderID, hedgeOrderDuration)
				}
				if err != nil {
					log.Errorf("❌ [网格下单] %s币对冲买入订单失败: %v", hedgeOrderToSubmit.TokenType, err)
					// 检查是否是超时错误
					if hedgeCtx.Err() == context.DeadlineExceeded {
						log.Errorf("❌ [网格下单] 对冲订单超时（30秒），可能网络问题或API响应慢")
					}
					// 检查是否是队列已满错误
					if strings.Contains(err.Error(), "队列已满") {
						log.Errorf("❌ [网格下单] 订单队列已满，无法添加对冲订单")
					}
					// 重构后：activeOrders 由 OrderEngine 管理，无需手动清理
					s.mu.Lock()
					// 如果仓位存在，标记为未对冲（因为对冲订单失败）
					if s.activePosition != nil {
						s.activePosition.Unhedged = true
						log.Warnf("⚠️ [对冲失败] 仓位标记为未对冲，因为对冲订单提交失败: 主单ID=%s, 对冲订单ID=%s",
							s.activePosition.EntryOrder.OrderID, hedgeOrderToSubmit.OrderID)
					}
					s.mu.Unlock()
					// 释放对冲订单提交锁
					s.hedgeOrderSubmitMu.Unlock()
				} else {
					// 无论订单是否立即成交，服务器都会返回订单ID，必须使用服务器返回的订单ID
					if createdHedgeOrder != nil {
						// 更新订单ID（使用服务器返回的订单ID，这是权威的）
						if createdHedgeOrder.OrderID != originalHedgeOrderID {
							log.Debugf("🔄 [订单顺序] 对冲订单ID已更新: %s → %s", originalHedgeOrderID, createdHedgeOrder.OrderID)
						}
						hedgeOrderToSubmit.OrderID = createdHedgeOrder.OrderID

						// 更新订单状态（使用服务器返回的状态）
						hedgeOrderToSubmit.Status = createdHedgeOrder.Status
						if createdHedgeOrder.FilledAt != nil {
							hedgeOrderToSubmit.FilledAt = createdHedgeOrder.FilledAt
						}
						if createdHedgeOrder.Size > 0 {
							hedgeOrderToSubmit.Size = createdHedgeOrder.Size
						}

						log.Debugf("📋 [对冲订单] 服务器返回订单ID: %s, 状态: %s, 数量: %.4f",
							createdHedgeOrder.OrderID, createdHedgeOrder.Status, createdHedgeOrder.Size)
					} else {
						log.Warnf("⚠️ [对冲订单] PlaceOrder返回的createdHedgeOrder为nil，无法更新订单ID")
					}

					s.mu.Lock()
					// 重构后：activeOrders 由 OrderEngine 管理，无需手动保存
					// 确保从待提交列表中删除（双重保障）
					delete(s.pendingHedgeOrders, order.OrderID)
					s.mu.Unlock()

					// 更新最后提交时间（防抖机制）
					if s.hedgeSubmitDebouncer == nil {
						s.hedgeSubmitDebouncer = common.NewDebouncer(2 * time.Second)
					}
					s.hedgeSubmitDebouncer.MarkNow()
					// 释放对冲订单提交锁
					s.hedgeOrderSubmitMu.Unlock()

					// 计算订单金额（在锁外执行，避免死锁）
					// 注意：calculateOrderSize访问s.config，但config是只读的，不需要锁
					var hedgeAmount float64
					if config.EnableDoubleSide { // 使用复制的config
						hedgeAmount, _ = s.calculateOrderSize(hedgeOrderToSubmit.Price)
					} else {
						// 如果config为nil，使用订单价格和数量计算金额
						hedgeAmount = hedgeOrderToSubmit.Price.ToDecimal() * hedgeOrderToSubmit.Size
						log.Warnf("⚠️ [对冲订单] config为nil，使用订单价格和数量计算金额: %.2f USDC", hedgeAmount)
					}

					// 输出成功日志
					if isManualHedge {
						log.Infof("✅ [手动订单对冲] 🎯 对冲订单已提交（市价单FAK，吃卖一价）: %s币 @ %dc (%.4f), 数量=%.4f, 金额=%.2f USDC, orderID=%s",
							hedgeOrderToSubmit.TokenType, hedgeOrderToSubmit.Price.Cents, hedgeOrderToSubmit.Price.ToDecimal(), hedgeOrderToSubmit.Size, hedgeAmount, hedgeOrderToSubmit.OrderID)
						log.Infof("   📊 仓位状态: 主单已成交 ✅ | 对冲订单已提交 ⏳ | 等待对冲订单成交...")
					} else {
						log.Infof("✅ [网格下单] %s币对冲买入订单已提交（市价单FAK，吃卖一价）: orderID=%s, 价格=%dc (%.4f), 数量=%.4f, 金额=%.2f USDC",
							hedgeOrderToSubmit.TokenType, hedgeOrderToSubmit.OrderID, hedgeOrderToSubmit.Price.Cents, hedgeOrderToSubmit.Price.ToDecimal(), hedgeOrderToSubmit.Size, hedgeAmount)
					}
				}
			} else {
				log.Debugf("📋 [订单顺序] 主单已成交，但没有待提交的对冲订单: 主单ID=%s", order.OrderID)
			}
		}

		// 如果是对冲订单成交（买入订单），更新仓位的对冲订单状态
		if !order.IsEntryOrder && order.Side == types.SideBuy {
			isManualHedge := strings.HasPrefix(order.OrderID, "auto-hedge-")
			if isManualHedge {
				log.Infof("🤖 [手动订单对冲] 📥 收到对冲订单成交事件: %s币 @ %dc (%.4f), 数量=%.2f, orderID=%s",
					order.TokenType, order.Price.Cents, order.Price.ToDecimal(), order.Size, order.OrderID)
			}

			// 严格检查：必须主单先成交，对冲单才能成交
			// 检查主单是否已成交（需要在锁内检查）
			entryOrderFilled := false
			var entryOrder *domain.Order
			func() {
				s.mu.RLock()
				defer s.mu.RUnlock()

				if s.activePosition != nil && s.activePosition.EntryOrder != nil {
					entryOrder = s.activePosition.EntryOrder
					entryOrderFilled = entryOrder.IsFilled()
					if isManualHedge {
						log.Debugf("🤖 [手动订单对冲] 主单状态: 已成交=%v, orderID=%s",
							entryOrderFilled, entryOrder.OrderID)
					}
				} else {
					// 检查 activeOrders 中是否有主单
					for _, o := range s.getActiveOrders() {
						if o.IsEntryOrder {
							entryOrder = o
							entryOrderFilled = o.IsFilled()
							break
						}
					}
				}
			}()

			// 如果主单未成交，对冲单先成交了，需要取消对冲单
			if !entryOrderFilled {
				log.Warnf("🚨 [订单顺序错误] 对冲订单先成交，但主单未成交！必须取消对冲单，等待主单先成交")
				log.Warnf("   对冲订单: %s币 @ %dc (%.4f), 数量=%.2f", order.TokenType, order.Price.Cents, order.Price.ToDecimal(), order.Size)
				log.Warnf("   主单状态: %v", entryOrder != nil)

				// 回滚持仓跟踪数据（因为对冲单不应该先成交）
				cost := order.Size * order.Price.ToDecimal()
				func() {
					s.mu.Lock()
					defer s.mu.Unlock()

					if order.TokenType == domain.TokenTypeUp {
						s.upTotalCost -= cost
						s.upHoldings -= order.Size
						if s.upTotalCost < 0 {
							s.upTotalCost = 0
						}
						if s.upHoldings < 0 {
							s.upHoldings = 0
						}
						log.Debugf("📊 [持仓跟踪回滚] UP币(对冲): 成本-=%.8f, 持仓-=%.8f, 总成本=%.8f, 总持仓=%.8f",
							cost, order.Size, s.upTotalCost, s.upHoldings)
					} else if order.TokenType == domain.TokenTypeDown {
						s.downTotalCost -= cost
						s.downHoldings -= order.Size
						if s.downTotalCost < 0 {
							s.downTotalCost = 0
						}
						if s.downHoldings < 0 {
							s.downHoldings = 0
						}
						log.Debugf("📊 [持仓跟踪回滚] DOWN币(对冲): 成本-=%.8f, 持仓-=%.8f, 总成本=%.8f, 总持仓=%.8f",
							cost, order.Size, s.downTotalCost, s.downHoldings)
					}
				}()

				// 创建卖出订单来取消对冲单（卖出已买入的对冲代币）
				if s.tradingService != nil {
					// 获取当前价格用于卖出（需要在锁内读取）
					var currentPrice domain.Price
					var assetID string
					func() {
						s.mu.RLock()
						defer s.mu.RUnlock()

						if order.TokenType == domain.TokenTypeUp {
							currentPrice = domain.Price{Cents: s.currentPriceUp}
							assetID = market.YesAssetID
						} else {
							currentPrice = domain.Price{Cents: s.currentPriceDown}
							assetID = market.NoAssetID
						}
					}()

					// 如果当前价格不可用，使用订单价格
					if currentPrice.Cents <= 0 {
						currentPrice = order.Price
					}

					// 获取订单簿的最佳买价（用于卖出）
					bestBid, _, err := s.tradingService.GetBestPrice(ctx, assetID)
					if err != nil {
						log.Errorf("获取订单簿失败: %v", err)
						bestBid = currentPrice.ToDecimal()
					}

					if bestBid <= 0 {
						bestBid = currentPrice.ToDecimal()
					}

					sellPrice := domain.PriceFromDecimal(bestBid)
					log.Warnf("🔄 [取消对冲单] 创建卖出订单: 卖出%s币@%.4f (%dc), 数量=%.2f",
						order.TokenType, bestBid, sellPrice.Cents, order.Size)

					// 创建卖出订单
					sellOrder := &domain.Order{
						OrderID:      fmt.Sprintf("cancel-hedge-%s-%d-%d", order.TokenType, sellPrice.Cents, time.Now().UnixNano()),
						AssetID:      assetID,
						Side:         types.SideSell,
						Price:        sellPrice,
						Size:         order.Size,
						TokenType:    order.TokenType,
						IsEntryOrder: false,
						Status:       domain.OrderStatusPending,
						CreatedAt:    time.Now(),
					}

					// 提交卖出订单（使用新的context，避免使用已取消的ctx）
					orderCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()

					if _, err := s.tradingService.PlaceOrder(orderCtx, sellOrder); err != nil {
						log.Errorf("❌ [取消对冲单] 卖出订单提交失败: %v", err)
						// 即使卖出订单失败，也不创建仓位，因为对冲单不应该先成交
					} else {
						// 重构后：activeOrders 由 OrderEngine 管理，无需手动更新
						log.Warnf("✅ [取消对冲单] 卖出订单已提交: orderID=%s, 价格=%dc (%.4f), 数量=%.2f",
							sellOrder.OrderID, sellPrice.Cents, sellPrice.ToDecimal(), order.Size)
					}
				} else {
					log.Errorf("❌ [取消对冲单] 交易服务未设置，无法创建卖出订单")
				}

				// 不创建仓位，不更新仓位状态，因为对冲单不应该先成交
				// 等待主单成交后，会通过 checkAndSupplementHedge 重新提交对冲单
				log.Warnf("⏳ [订单顺序] 等待主单成交后，将重新提交对冲单")
				return nil
			}

			// 主单已成交，正常处理对冲单
			// 更新双向持仓跟踪（需要在锁内更新）
			cost := order.Size * order.Price.ToDecimal()
			func() {
				s.mu.Lock()
				defer s.mu.Unlock()

				if order.TokenType == domain.TokenTypeUp {
					s.upTotalCost += cost
					s.upHoldings += order.Size
					log.Debugf("📊 [持仓跟踪] UP币(对冲): 成本+=%.8f, 持仓+=%.8f, 总成本=%.8f, 总持仓=%.8f",
						cost, order.Size, s.upTotalCost, s.upHoldings)
				} else if order.TokenType == domain.TokenTypeDown {
					s.downTotalCost += cost
					s.downHoldings += order.Size
					log.Debugf("📊 [持仓跟踪] DOWN币(对冲): 成本+=%.8f, 持仓+=%.8f, 总成本=%.8f, 总持仓=%.8f",
						cost, order.Size, s.downTotalCost, s.downHoldings)
				}

				if s.activePosition == nil {
					// 如果仓位不存在，这不应该发生（因为主单应该先成交）
					log.Errorf("❌ [订单顺序错误] 对冲订单成交但仓位不存在，主单应该先成交")
					// 仍然创建仓位，但记录错误
					now := time.Now()
					s.activePosition = &domain.Position{
						ID:         fmt.Sprintf("grid-%s-%d", order.TokenType, now.UnixNano()),
						Market:     market,
						HedgeOrder: order,
						EntryTime:  now,
						Size:       0, // 对冲订单不增加仓位大小
						TokenType:  order.TokenType,
						Status:     domain.PositionStatusOpen,
						Unhedged:   true,
					}
					log.Warnf("📊 新仓位已创建（异常情况）: %s币 @ %dc", order.TokenType, order.Price.Cents)
				} else {
					// 更新仓位的对冲订单状态
					s.activePosition.HedgeOrder = order
				}
			}()

			// 检查仓位是否已完全对冲（入场订单和对冲订单都已成交）
			// 需要在锁内检查
			var isHedged bool
			var entryOrderInfo, hedgeOrderInfo string
			var isManualPosition bool
			func() {
				s.mu.RLock()
				defer s.mu.RUnlock()

				if s.activePosition != nil && s.activePosition.IsHedged() {
					isHedged = true
					if s.activePosition.EntryOrder != nil {
						entryOrderInfo = fmt.Sprintf("%s币 @ %dc, 数量=%.2f, 状态=%s",
							s.activePosition.EntryOrder.TokenType, s.activePosition.EntryOrder.Price.Cents,
							s.activePosition.EntryOrder.Size, s.activePosition.EntryOrder.Status)
						// 检查是否是手动订单（通过检查订单是否不在策略的 activeOrders 中）
						isManualPosition = !strings.HasPrefix(s.activePosition.EntryOrder.OrderID, "entry-") &&
							!strings.HasPrefix(s.activePosition.EntryOrder.OrderID, "hedge-")
					}
					if s.activePosition.HedgeOrder != nil {
						hedgeOrderInfo = fmt.Sprintf("%s币 @ %dc, 数量=%.2f, 状态=%s",
							s.activePosition.HedgeOrder.TokenType, s.activePosition.HedgeOrder.Price.Cents,
							s.activePosition.HedgeOrder.Size, s.activePosition.HedgeOrder.Status)
						if !isManualPosition {
							isManualPosition = strings.HasPrefix(s.activePosition.HedgeOrder.OrderID, "auto-hedge-")
						}
					}
				}
			}()

			if isHedged {
				if isManualPosition {
					log.Infof("🎯 [手动订单对冲] ✅ 仓位已完全对冲（主单和对冲单都已成交），锁定利润！")
				} else {
					log.Infof("🎯 [订单顺序] 仓位已完全对冲（主单和对冲单都已成交），锁定利润，清空仓位以允许下一轮交易")
				}
				if entryOrderInfo != "" {
					log.Infof("   主单: %s", entryOrderInfo)
				}
				if hedgeOrderInfo != "" {
					log.Infof("   对冲单: %s", hedgeOrderInfo)
				}

				// 显示锁定利润（在清空前）
				s.displayHoldingsAndProfit()

				// 清空仓位（但不清空双向持仓跟踪，因为可能还有未平仓的持仓）
				// 注意：双向持仓跟踪会持续累积，直到市场周期结束或手动清空
				func() {
					s.mu.Lock()
					defer s.mu.Unlock()

					// 保存主单ID用于清理 activeOrders
					entryOrderID := ""
					if s.activePosition != nil && s.activePosition.EntryOrder != nil {
						entryOrderID = s.activePosition.EntryOrder.OrderID
					}
					s.activePosition = nil
					// 确保 activeOrders 中没有残留的订单（主单和对冲单）
					activeOrdersMap := make(map[string]*domain.Order)
					for _, o := range s.getActiveOrders() {
						activeOrdersMap[o.OrderID] = o
					}
					for _, o := range activeOrdersMap {
						if o.OrderID == order.OrderID || (entryOrderID != "" && o.OrderID == entryOrderID) {
							// 重构后：activeOrders 由 OrderEngine 管理，无需手动删除
						}
					}
				}()

				if isManualPosition {
					log.Infof("✅ [手动订单对冲] 🎊 仓位已清空，手动订单对冲流程完成！可以开始下一轮交易")
				} else {
					log.Infof("✅ [订单顺序] 仓位已清空，可以开始下一轮交易")
				}

				// 注意：这里不清空轮数，因为轮数会持续累积直到达到 max_rounds_per_period
				// 当仓位清空后，handleGridLevelReached 会检查是否可以开始新的一轮
			} else {
				// 需要在锁内检查仓位状态
				func() {
					s.mu.RLock()
					defer s.mu.RUnlock()

					log.Debugf("📋 [订单顺序] 对冲单已成交，但主单或对冲单未完全成交，等待全部成交")
					if s.activePosition != nil {
						if s.activePosition.EntryOrder != nil {
							log.Debugf("   主单状态: %s", s.activePosition.EntryOrder.Status)
						}
						if s.activePosition.HedgeOrder != nil {
							log.Debugf("   对冲单状态: %s", s.activePosition.HedgeOrder.Status)
						}
					}
				}()
			}
		}

		// 如果是止损订单成交（卖出订单），清空仓位，允许下一轮
		if !order.IsEntryOrder && order.Side == types.SideSell {
			// 检查是否是取消对冲单的卖出订单
			isCancelHedgeOrder := strings.HasPrefix(order.OrderID, "cancel-hedge-")

			if isCancelHedgeOrder {
				// 这是取消对冲单的卖出订单，不需要减少持仓跟踪数据
				// 因为在对冲单先成交时已经回滚了持仓跟踪数据
				log.Infof("✅ [取消对冲单] 卖出订单已成交: %s币 @ %dc (%.4f), 数量=%.2f",
					order.TokenType, order.Price.Cents, order.Price.ToDecimal(), order.Size)
				log.Infof("   对冲单已取消，等待主单成交后重新提交对冲单")
			} else {
				// 这是止损订单，减少持仓（卖出）
				func() {
					s.mu.Lock()
					defer s.mu.Unlock()

					if s.activePosition != nil {
						log.Warnf("🛑 止损订单已成交，清空仓位以允许下一轮交易")
						if order.TokenType == domain.TokenTypeUp {
							s.upHoldings -= order.Size
							if s.upHoldings < 0 {
								s.upHoldings = 0
							}
						} else if order.TokenType == domain.TokenTypeDown {
							s.downHoldings -= order.Size
							if s.downHoldings < 0 {
								s.downHoldings = 0
							}
						}
						s.activePosition = nil
					}
				}()
			}
		}

		// 显示当前仓位情况和订单信息（需要在锁内读取）
		func() {
			s.mu.RLock()
			defer s.mu.RUnlock()

			// 显示当前仓位情况
			if s.activePosition != nil {
				posInfo := s.formatPositionInfo()
				log.Infof("  %s", posInfo)
			}

			// 显示剩余待成交订单
			if s.hasActiveOrders() {
				ordersInfo := s.formatOrdersInfo()
				if ordersInfo != "" {
					log.Infof("  %s", ordersInfo)
				}
			} else {
				// 如果没有待成交订单，且仓位已清空，记录可以开始下一轮
				if s.activePosition == nil {
					log.Infof("✅ 所有订单已处理完成，可以开始下一轮交易 (当前轮数: %d/%d)",
						s.roundsThisPeriod, config.MaxRoundsPerPeriod)
				}
			}

			// 显示双向持仓和利润信息到终端
			s.displayHoldingsAndProfit()
			s.displayStrategyStatus()
		}()
	}

	log.Debugf("📥 [订单成交] OnOrderFilled处理完成: orderID=%s", event.Order.OrderID)

	return nil
}
