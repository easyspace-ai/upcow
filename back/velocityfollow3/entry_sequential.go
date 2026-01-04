package velocityfollow

import (
	"context"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
)

// executeSequential 顺序下单模式（新架构特性）
//
// 执行流程：
// 1. 下 Entry 订单（FAK，立即成交或取消）
// 2. 等待 Entry 订单成交（轮询检查订单状态）
// 3. Entry 成交后，下 Hedge 订单（GTC 限价单）
//
// 优势：
// - 风险低：确保 Entry 成交后再下 Hedge
// - 适合 FAK 订单：FAK 订单通常立即成交
//
// 参数：
// - SequentialCheckIntervalMs: 检查订单状态的间隔（默认 50ms）
// - SequentialMaxWaitMs: 最大等待时间（默认 1000ms）
func (s *Strategy) executeSequential(ctx context.Context, market *domain.Market, winner domain.TokenType,
	entryAsset, hedgeAsset string, entryPrice, hedgePrice domain.Price, entryShares, hedgeShares float64,
	entryAskCents, hedgeAskCents int, winMet metrics, biasTok, biasReason string) error {
	// 使用更短的超时时间（10秒），快速失败，避免阻塞策略
	orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// ===== 顺序下单：先买主单（Entry），成交后再下对冲单（Hedge）=====
	// ⚠️ 重要：FAK 买入订单必须在下单前再次验证订单簿价格和流动性
	// 因为价格可能在获取订单簿和下单之间发生变化
	// 策略：使用卖二价作为缓冲，提高下单成功率
	// - 卖一价（asks[0]）是最优价格，但可能很快被吃掉
	// - 卖二价（asks[1]）是次优价格，更稳定，有更大的价格缓冲空间
	// - 使用卖二价下单，即使卖一价被吃掉，仍然可以匹配到卖二价
	secondLevelPrice, hasSecondLevel := s.TradingService.GetSecondLevelPrice(orderCtx, entryAsset, types.SideBuy)
	_, actualAsk, err := s.TradingService.GetBestPrice(orderCtx, entryAsset)

	if err != nil {
		log.Warnf("⚠️ [%s] 下单前获取订单簿价格失败，使用原价格: err=%v", ID, err)
	} else if actualAsk > 0 {
		// 优先使用卖二价（如果存在且合理）
		targetPrice := actualAsk
		targetPriceName := "卖一价"

		if hasSecondLevel && secondLevelPrice > 0 && secondLevelPrice <= actualAsk*1.02 {
			// 卖二价存在且不超过卖一价的 2%，使用卖二价
			targetPrice = secondLevelPrice
			targetPriceName = "卖二价"
			log.Infof("💰 [%s] 使用卖二价作为缓冲: 卖一价=%.4f, 卖二价=%.4f (价格缓冲=%.2f%%)",
				ID, actualAsk, secondLevelPrice, (secondLevelPrice-actualAsk)/actualAsk*100)
		}

		// 对于买入订单，需要检查 ask 价格
		targetPriceCents := int(targetPrice*100 + 0.5)
		entryPriceCents := int(entryPrice.ToDecimal()*100 + 0.5)
		priceDiffCents := targetPriceCents - entryPriceCents

		if priceDiffCents > 0 {
			// 订单簿的 ask 价格高于我们的价格
			// 如果价格偏差 <= 5c，调整价格为订单簿的 ask 价格
			// 如果价格偏差 > 5c，跳过这次下单（市场波动太大）
			if priceDiffCents <= 5 {
				log.Warnf("⚠️ [%s] 订单簿价格变化：原价格=%dc, %s=%dc (偏差=%dc)，调整为订单簿价格",
					ID, entryPriceCents, targetPriceName, targetPriceCents, priceDiffCents)
				entryPrice = domain.PriceFromDecimal(targetPrice)
			} else {
				log.Warnf("⚠️ [%s] 订单簿价格变化过大：原价格=%dc, %s=%dc (偏差=%dc > 5c)，跳过下单",
					ID, entryPriceCents, targetPriceName, targetPriceCents, priceDiffCents)
				return nil // 跳过这次下单
			}
		} else if priceDiffCents < 0 {
			// 订单簿的 ask 价格低于我们的价格，这是正常的，可以使用我们的价格
			log.Debugf("💰 [%s] 订单簿价格更好：我们的价格=%dc, %s=%dc，使用我们的价格",
				ID, entryPriceCents, targetPriceName, targetPriceCents)
		} else {
			// 价格一致
			log.Debugf("💰 [%s] 订单簿价格一致：价格=%dc (%s)", ID, entryPriceCents, targetPriceName)
		}
	}

	// ⚠️ 重要：价格调整后，需要重新进行精度调整
	// 因为价格可能从有效价格调整为实际订单簿价格（卖一价或卖二价）
	// 精度调整必须使用实际下单价格，确保 maker amount = size × price 是 2 位小数
	entryPriceDec := entryPrice.ToDecimal()
	entrySharesAdjusted := adjustSizeForMakerAmountPrecision(entryShares, entryPriceDec)
	if entrySharesAdjusted != entryShares {
		log.Infof("🔧 [%s] Entry size 精度调整（价格调整后）: %.4f -> %.4f (maker amount: %.2f -> %.2f, price=%.4f)",
			ID, entryShares, entrySharesAdjusted, entryShares*entryPriceDec, entrySharesAdjusted*entryPriceDec, entryPriceDec)
		entryShares = entrySharesAdjusted
	}

	// 检查订单簿流动性（使用 REST API 获取完整订单簿）
	hasLiquidity, actualPrice, availableSize := s.TradingService.CheckOrderBookLiquidity(
		orderCtx, entryAsset, types.SideBuy, entryPrice.ToDecimal(), entryShares)
	if !hasLiquidity {
		log.Warnf("⚠️ [%s] 订单簿无流动性：价格=%dc, size=%.4f，跳过下单",
			ID, int(entryPrice.ToDecimal()*100+0.5), entryShares)
		return nil // 跳过这次下单
	}

	// 如果可用数量不足，记录警告但仍尝试下单（FAK 允许部分成交）
	if availableSize < entryShares {
		log.Warnf("⚠️ [%s] 订单簿流动性不足：需要=%.4f, 可用=%.4f, 实际价格=%.4f，FAK订单将尝试部分成交",
			ID, entryShares, availableSize, actualPrice)
		// FAK 订单允许部分成交，所以继续下单
	} else {
		log.Infof("✅ [%s] 订单簿流动性充足：需要=%.4f, 可用=%.4f, 实际价格=%.4f",
			ID, entryShares, availableSize, actualPrice)
	}

	// 主单：价格 >= minPreferredPriceCents 的订单（FAK，立即成交或取消）
	log.Infof("📤 [%s] 步骤1: 下主单 Entry (side=%s price=%dc size=%.4f FAK)",
		ID, winner, int(entryPrice.ToDecimal()*100+0.5), entryShares)

	// 获取市场精度信息（从缓存）
	var tickSize types.TickSize
	var negRisk *bool
	if s.currentPrecision != nil {
		if parsed, err := ParseTickSize(s.currentPrecision.TickSize); err == nil {
			tickSize = parsed
		}
		negRisk = boolPtr(s.currentPrecision.NegRisk)
	}

	entryOrder := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      entryAsset,
		TokenType:    winner,
		Side:         types.SideBuy,
		Price:        entryPrice,
		Size:         entryShares,
		OrderType:    types.OrderTypeFAK,
		IsEntryOrder: true,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
		TickSize:     tickSize, // 使用缓存的精度信息
		NegRisk:      negRisk,  // 使用缓存的 neg_risk 信息
	}

	entryOrderResult, execErr := s.TradingService.PlaceOrder(orderCtx, entryOrder)
	if execErr != nil {
		if isFailSafeRefusal(execErr) {
			log.Warnf("⏸️ [%s] 系统拒绝下单（fail-safe，预期行为）：entry err=%v market=%s", ID, execErr, market.Slug)
			return nil
		}
		log.Warnf("⚠️ [%s] 主单下单失败: err=%v side=%s market=%s", ID, execErr, winner, market.Slug)
		return nil
	}

	if entryOrderResult == nil || entryOrderResult.OrderID == "" {
		log.Warnf("⚠️ [%s] 主单下单失败: 订单ID为空", ID)
		return nil
	}

	// ✅ 修复竞态条件：立即更新 lastEntryOrderID，防止第二次交易在订单提交后、状态更新前触发
	entryOrderID := entryOrderResult.OrderID
	s.mu.Lock()
	s.lastEntryOrderID = entryOrderID
	s.lastEntryOrderStatus = entryOrderResult.Status
	s.mu.Unlock()

	log.Infof("✅ [%s] 主单已提交: orderID=%s status=%s",
		ID, entryOrderID, entryOrderResult.Status)

	// 等待主单成交（FAK 订单要么立即成交，要么立即取消）
	// 优化：使用更短的检查间隔和更长的等待时间，同时使用订单更新回调来检测成交
	maxWaitTime := time.Duration(s.Config.SequentialMaxWaitMs) * time.Millisecond
	if maxWaitTime <= 0 {
		maxWaitTime = 2000 * time.Millisecond // 默认 2 秒
	}
	checkInterval := time.Duration(s.Config.SequentialCheckIntervalMs) * time.Millisecond
	if checkInterval <= 0 {
		checkInterval = 20 * time.Millisecond // 默认 20ms（更频繁）
	}
	entryFilled := false

	// ✅ 修复：在纸交易模式下，FAK 订单应该立即成交
	// 因为 io_executor 在纸交易模式下会将 FAK 订单状态设置为 filled
	if s.TradingService != nil && s.TradingService.IsDryRun() && entryOrderResult.OrderType == types.OrderTypeFAK {
		// 纸交易模式：FAK 订单立即成交
		entryFilled = true
		log.Infof("✅ [%s] 主单已成交（纸交易模式，FAK 订单立即成交）: orderID=%s",
			ID, entryOrderID)
	}

	// 先检查一次订单状态（可能已经成交）
	// ⚠️ 重要：优先检查 entryOrderResult 的状态，因为它可能已经通过 WebSocket 更新
	if !entryFilled && entryOrderResult != nil {
		if entryOrderResult.Status == domain.OrderStatusFilled {
			entryFilled = true
			log.Infof("✅ [%s] 主单已成交（通过订单结果）: orderID=%s filledSize=%.4f",
				ID, entryOrderID, entryOrderResult.FilledSize)
		} else if entryOrderResult.Status == domain.OrderStatusFailed ||
			entryOrderResult.Status == domain.OrderStatusCanceled {
			log.Warnf("⚠️ [%s] 主单失败/取消（通过订单结果）: orderID=%s status=%s",
				ID, entryOrderID, entryOrderResult.Status)
			return nil
		}
	}

	// 如果订单结果中没有成交信息，再检查本地订单状态（包含已成交订单）
	// ⚠️ 修复：GetActiveOrders 只包含 openOrders，订单一旦 filled 会从列表移除，导致“误判未成交”。
	if !entryFilled && s.TradingService != nil {
		if ord, ok := s.TradingService.GetOrder(entryOrderID); ok && ord != nil {
			if ord.Status == domain.OrderStatusFilled {
				entryFilled = true
				log.Infof("✅ [%s] 主单已成交（立即检查）: orderID=%s filledSize=%.4f",
					ID, ord.OrderID, ord.FilledSize)
			} else if ord.Status == domain.OrderStatusFailed || ord.Status == domain.OrderStatusCanceled {
				log.Warnf("⚠️ [%s] 主单失败/取消（立即检查）: orderID=%s status=%s",
					ID, ord.OrderID, ord.Status)
				return nil
			}
		}
	}

	// 如果未成交，轮询检查订单状态（使用更短的间隔）
	if !entryFilled {
		deadline := time.Now().Add(maxWaitTime)
		checkCount := 0
		for time.Now().Before(deadline) {
			checkCount++
			// 查询订单状态（包含已成交/已取消）
			if s.TradingService != nil {
				if ord, ok := s.TradingService.GetOrder(entryOrderID); ok && ord != nil {
					if ord.Status == domain.OrderStatusFilled {
						entryFilled = true
						log.Infof("✅ [%s] 主单已成交（轮询检查，第%d次）: orderID=%s filledSize=%.4f",
							ID, checkCount, ord.OrderID, ord.FilledSize)
					} else if ord.Status == domain.OrderStatusFailed || ord.Status == domain.OrderStatusCanceled {
						log.Warnf("⚠️ [%s] 主单失败/取消（轮询检查，第%d次）: orderID=%s status=%s",
							ID, checkCount, ord.OrderID, ord.Status)
						return nil
					}
				}
			}

			if entryFilled {
				break
			}

			// 等待一小段时间后再次检查（使用更短的间隔）
			time.Sleep(checkInterval)
		}

		if !entryFilled {
			log.Debugf("🔄 [%s] 主单轮询检查完成（共检查%d次）: orderID=%s 未在预期时间内成交",
				ID, checkCount, entryOrderID)
		}
	}

	if !entryFilled {
		log.Warnf("⚠️ [%s] 主单未在预期时间内成交: orderID=%s (可能部分成交或仍在处理中)",
			ID, entryOrderID)
		// 即使主单未完全成交，也继续下对冲单（使用实际成交数量）
		// 但为了安全，我们仍然继续执行
	}

	// ✅ 修复：若 Entry 下单前发生了价格上调（例如使用卖二价缓冲），必须同步重算 Hedge 互补挂单价，
	// 否则可能出现 entryPrice 上调后 totalCost > 100c 的结构性必亏。
	{
		entryCentsNow := int(entryPrice.ToDecimal()*100 + 0.5)
		if entryCentsNow > 0 && entryCentsNow < 100 && s.HedgeOffsetCents > 0 {
			newHedgeLimit := 100 - entryCentsNow - s.HedgeOffsetCents
			if newHedgeLimit > 0 && newHedgeLimit < 100 {
				// 防止穿价：确保买单价格 < 当前 ask
				if s.TradingService != nil {
					_, bestAsk, err := s.TradingService.GetBestPrice(orderCtx, hedgeAsset)
					if err == nil && bestAsk > 0 {
						askCents := int(bestAsk*100 + 0.5)
						if newHedgeLimit >= askCents {
							newHedgeLimit = askCents - 1
						}
					}
				}
				if newHedgeLimit > 0 && newHedgeLimit < 100 && newHedgeLimit != hedgeAskCents {
					log.Infof("💰 [%s] Hedge 价格随 Entry 调整而重算: entry=%dc hedge(old)=%dc -> hedge(new)=%dc (offset=%dc)",
						ID, entryCentsNow, hedgeAskCents, newHedgeLimit, s.HedgeOffsetCents)
					hedgeAskCents = newHedgeLimit
					hedgePrice = domain.Price{Pips: hedgeAskCents * 100}
				}
			}
		}
	}

	// ===== 步骤2: 主单成交后，下对冲单（Hedge）=====
	log.Infof("📤 [%s] 步骤2: 下对冲单 Hedge (side=%s price=%dc size=%.4f GTC)",
		ID, opposite(winner), hedgeAskCents, hedgeShares)

	hedgeOrder := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      hedgeAsset,
		TokenType:    opposite(winner),
		Side:         types.SideBuy,
		Price:        hedgePrice,
		Size:         hedgeShares,
		OrderType:    types.OrderTypeGTC,
		IsEntryOrder: false,
		HedgeOrderID: &entryOrderID, // 关联主单ID
		Status:       domain.OrderStatusPending,
		TickSize:     tickSize, // 使用缓存的精度信息
		NegRisk:      negRisk,  // 使用缓存的 neg_risk 信息
		CreatedAt:    time.Now(),
	}

	hedgeOrderResult, hedgeErr := s.TradingService.PlaceOrder(orderCtx, hedgeOrder)
	hedgeOrderID := ""
	if hedgeErr != nil {
		// 系统级 fail-safe：如果主单未成交且系统拒绝对冲腿下单，则视为“预期跳过”，不进入风险逻辑
		if isFailSafeRefusal(hedgeErr) && !entryFilled {
			log.Warnf("⏸️ [%s] 系统拒绝对冲单（fail-safe，预期行为）：hedge err=%v market=%s", ID, hedgeErr, market.Slug)
			return nil
		}
		log.Errorf("❌ [%s] 对冲单下单失败: err=%v (主单已成交，需要处理)",
			ID, hedgeErr)

		// ⚠️ 重要：如果 Entry 订单已成交，但 Hedge 订单失败，这是一个高风险情况
		// 选项1：如果 Entry 订单还未完全成交，尝试取消 Entry 订单
		// 选项2：记录未对冲的 Entry 订单，提醒手动处理
		if entryFilled {
			// Entry 订单已成交，无法取消，记录未对冲风险
			log.Errorf("🚨 [%s] 【风险警告】Entry 订单已成交但 Hedge 订单失败！Entry orderID=%s, 需要手动对冲！",
				ID, entryOrderID)
			log.Errorf("🚨 [%s] Entry 订单详情: side=%s, price=%dc, size=%.4f, filledSize=%.4f",
				ID, winner, entryAskCents, entryShares, entryShares)
			log.Errorf("🚨 [%s] 建议：立即手动下 Hedge 订单对冲风险，或取消 Entry 订单（如果可能）",
				ID)

			// 记录未对冲的 Entry 订单到策略状态中，方便后续查询
			s.mu.Lock()
			if s.unhedgedEntries == nil {
				s.unhedgedEntries = make(map[string]*domain.Order)
			}
			if entryOrderResult != nil {
				s.unhedgedEntries[entryOrderID] = entryOrderResult
				log.Errorf("🚨 [%s] 已记录未对冲的 Entry 订单到策略状态: orderID=%s",
					ID, entryOrderID)
			}
			s.mu.Unlock()
		} else {
			// Entry 订单未成交或部分成交，尝试取消 Entry 订单
			log.Warnf("⚠️ [%s] Entry 订单未完全成交，尝试取消 Entry 订单以避免未对冲风险: orderID=%s",
				ID, entryOrderID)
			go func(orderID string) {
				if err := s.TradingService.CancelOrder(context.Background(), orderID); err != nil {
					log.Warnf("⚠️ [%s] 取消 Entry 订单失败: orderID=%s err=%v", ID, orderID, err)
				} else {
					log.Infof("✅ [%s] 已取消 Entry 订单（Hedge 订单失败）: orderID=%s", ID, orderID)
				}
			}(entryOrderID)
		}

		// 主单已成交，对冲单失败，这是一个风险情况
		execErr = hedgeErr
		return nil // 返回错误，不再继续执行
	} else if hedgeOrderResult != nil && hedgeOrderResult.OrderID != "" {
		hedgeOrderID = hedgeOrderResult.OrderID
		log.Infof("✅ [%s] 对冲单已提交: orderID=%s status=%s (关联主单=%s)",
			ID, hedgeOrderResult.OrderID, hedgeOrderResult.Status, entryOrderID)
	} else {
		log.Errorf("❌ [%s] 对冲单下单失败: 订单ID为空 (主单已成交，需要手动处理)",
			ID)
		// 同样处理：记录未对冲风险或取消 Entry 订单
		if entryFilled {
			log.Errorf("🚨 [%s] 【风险警告】Entry 订单已成交但 Hedge 订单ID为空！Entry orderID=%s",
				ID, entryOrderID)
			s.mu.Lock()
			if s.unhedgedEntries == nil {
				s.unhedgedEntries = make(map[string]*domain.Order)
			}
			if entryOrderResult != nil {
				s.unhedgedEntries[entryOrderID] = entryOrderResult
			}
			s.mu.Unlock()
		} else {
			go func(orderID string) {
				_ = s.TradingService.CancelOrder(context.Background(), orderID)
			}(entryOrderID)
		}
		return nil
	}

	// 更新订单关联关系（如果对冲单成功）
	// entryOrderResult 一定不为 nil（因为如果为 nil，execErr 不为 nil，函数会提前返回）
	if hedgeOrderID != "" {
		entryOrderResult.HedgeOrderID = &hedgeOrderID
	}

	// ===== 主单成交后：实时计算盈亏并监控对冲单 =====
	if entryFilled {
		entryFilledTime := time.Now()
		entryFilledSize := entryShares
		if entryOrderResult.FilledSize > 0 {
			entryFilledSize = entryOrderResult.FilledSize
		}

		// 实时计算盈亏：如果 UP/DOWN 各自 win 时的收益与亏损
		// 使用实际成交价格（从 Trade 消息获取），而不是下单时的价格

		// Entry 成本：优先使用实际成交价格，如果没有则使用实际下单价格（不是有效价格）
		// ⚠️ 重要：entryPrice 是实际下单价格（可能已被调整为订单簿价格），entryAskCents 是有效价格（用于成本估算）
		// 如果 FilledPrice 为空，应该使用实际下单价格 entryPrice，而不是有效价格 entryAskCents
		var entryActualPriceCents int
		entryOrderPriceCents := int(entryPrice.ToDecimal()*100 + 0.5) // 实际下单价格
		if entryOrderResult.FilledPrice != nil {
			entryActualPriceCents = entryOrderResult.FilledPrice.ToCents()
			log.Debugf("💰 [%s] Entry 使用实际成交价格: %dc (下单价格: %dc, 有效价格: %dc)", ID, entryActualPriceCents, entryOrderPriceCents, entryAskCents)
		} else {
			entryActualPriceCents = entryOrderPriceCents // 使用实际下单价格，而不是有效价格
			log.Debugf("💰 [%s] Entry 使用实际下单价格: %dc (有效价格: %dc, 实际成交价格未获取)", ID, entryOrderPriceCents, entryAskCents)
		}
		entryCost := float64(entryActualPriceCents) / 100.0 * entryFilledSize

		// 计算如果 UP win 时的盈亏
		var upWinProfit, downWinProfit float64
		if winner == domain.TokenTypeUp {
			// Entry 是 UP，如果 UP win：收益 = entryFilledSize * $1 - entryCost
			upWinProfit = entryFilledSize*1.0 - entryCost
			// 如果 DOWN win：亏损 = -entryCost（对冲单未成交时）
			downWinProfit = -entryCost
		} else {
			// Entry 是 DOWN，如果 DOWN win：收益 = entryFilledSize * $1 - entryCost
			downWinProfit = entryFilledSize*1.0 - entryCost
			// 如果 UP win：亏损 = -entryCost（对冲单未成交时）
			upWinProfit = -entryCost
		}

		// 计算 Hedge 订单成本（无论是否已成交）
		// 如果对冲单已成交，使用实际成交价格；如果未成交，使用下单价格
		if hedgeOrderID != "" && s.TradingService != nil {
			var hedgeOrder *domain.Order
			if ord, ok := s.TradingService.GetOrder(hedgeOrderID); ok {
				hedgeOrder = ord
			}

			if hedgeOrder != nil {
				// 获取 Hedge 订单的实际成交数量
				hedgeFilledSize := hedgeOrder.FilledSize
				if hedgeFilledSize <= 0 {
					// 如果未成交，使用下单时的 size（因为我们需要承担这个成本）
					hedgeFilledSize = hedgeShares
				}

				// 优先使用实际成交价格，如果没有则使用实际下单价格（不是有效价格）
				// ⚠️ 重要：hedgePrice 是实际下单价格（有效价格），hedgeAskCents 也是有效价格
				// 对于 GTC 订单，下单价格就是有效价格，所以可以直接使用 hedgeAskCents
				// 但如果 FilledPrice 存在，应该优先使用实际成交价格
				var hedgeActualPriceCents int
				hedgeOrderPriceCents := int(hedgePrice.ToDecimal()*100 + 0.5) // 实际下单价格（对于GTC订单，这就是有效价格）
				if hedgeOrder.FilledPrice != nil {
					hedgeActualPriceCents = hedgeOrder.FilledPrice.ToCents()
					log.Debugf("💰 [%s] Hedge 使用实际成交价格: %dc (下单价格: %dc, 有效价格: %dc)", ID, hedgeActualPriceCents, hedgeOrderPriceCents, hedgeAskCents)
				} else {
					hedgeActualPriceCents = hedgeOrderPriceCents // 使用实际下单价格（对于GTC订单，这就是有效价格）
					if hedgeOrder.Status == domain.OrderStatusFilled {
						log.Debugf("💰 [%s] Hedge 使用下单价格: %dc (实际成交价格未获取，但订单已成交)", ID, hedgeOrderPriceCents)
					} else {
						log.Debugf("💰 [%s] Hedge 使用下单价格: %dc (订单未成交，使用下单价格计算成本)", ID, hedgeOrderPriceCents)
					}
				}

				hedgeCost := float64(hedgeActualPriceCents) / 100.0 * hedgeFilledSize
				totalCost := entryCost + hedgeCost

				// 记录价格对比（如果实际价格与下单价格不同）
				if hedgeOrder.Status == domain.OrderStatusFilled && hedgeActualPriceCents != hedgeAskCents {
					log.Infof("💰 [%s] 对冲单价格差异: 下单价格=%dc, 实际成交价格=%dc, 差异=%dc",
						ID, hedgeAskCents, hedgeActualPriceCents, hedgeActualPriceCents-hedgeAskCents)
				}

				// 重新计算盈亏（考虑 Hedge 成本）
				if winner == domain.TokenTypeUp {
					// Entry UP + Hedge DOWN，无论哪边 win，总成本 = entryCost + hedgeCost
					// UP win: 收益 = entryFilledSize * $1 - totalCost
					// DOWN win: 收益 = hedgeFilledSize * $1 - totalCost
					upWinProfit = entryFilledSize*1.0 - totalCost
					downWinProfit = hedgeFilledSize*1.0 - totalCost
				} else {
					// Entry DOWN + Hedge UP
					downWinProfit = entryFilledSize*1.0 - totalCost
					upWinProfit = hedgeFilledSize*1.0 - totalCost
				}

				// 记录 Hedge 订单状态
				if hedgeOrder.Status == domain.OrderStatusFilled {
					log.Debugf("💰 [%s] Hedge 订单已成交，使用实际成交价格计算成本", ID)
				} else {
					log.Debugf("💰 [%s] Hedge 订单未成交（status=%s），使用下单价格计算成本", ID, hedgeOrder.Status)
				}
			} else {
				// Hedge 订单未找到，使用下单价格计算成本（保守估计）
				log.Debugf("💰 [%s] Hedge 订单未找到，使用下单价格计算成本: price=%dc size=%.4f", ID, hedgeAskCents, hedgeShares)
				hedgeCost := float64(hedgeAskCents) / 100.0 * hedgeShares
				totalCost := entryCost + hedgeCost

				// 重新计算盈亏（考虑 Hedge 成本）
				if winner == domain.TokenTypeUp {
					upWinProfit = entryFilledSize*1.0 - totalCost
					downWinProfit = hedgeShares*1.0 - totalCost
				} else {
					downWinProfit = entryFilledSize*1.0 - totalCost
					upWinProfit = hedgeShares*1.0 - totalCost
				}
			}
		}

		// 计算 Hedge 成本（用于日志显示）
		hedgeCostDisplay := 0.0
		if hedgeOrderID != "" && s.TradingService != nil {
			if ord, ok := s.TradingService.GetOrder(hedgeOrderID); ok && ord != nil {
				hedgeFilledSize := ord.FilledSize
				if hedgeFilledSize <= 0 {
					hedgeFilledSize = hedgeShares
				}
				var hedgeActualPriceCents int
				if ord.FilledPrice != nil {
					hedgeActualPriceCents = ord.FilledPrice.ToCents()
				} else {
					hedgeActualPriceCents = hedgeAskCents
				}
				hedgeCostDisplay = float64(hedgeActualPriceCents) / 100.0 * hedgeFilledSize
			}
		}
		totalCostDisplay := entryCost + hedgeCostDisplay

		log.Infof("💰 [%s] 主单成交后实时盈亏计算: Entry=%s @ %dc(有效)/%dc(下单)/%dc(实际) size=%.4f cost=$%.2f | Hedge cost=$%.2f | Total cost=$%.2f | UP win: $%.2f | DOWN win: $%.2f",
			ID, winner, entryAskCents, entryOrderPriceCents, entryActualPriceCents, entryFilledSize, entryCost, hedgeCostDisplay, totalCostDisplay, upWinProfit, downWinProfit)

		// 启动对冲单重下监控（如果对冲单未成交）
		if hedgeOrderID != "" && s.HedgeReorderTimeoutSeconds > 0 {
			// 使用 Entry 实际下单价格（不是“信号时刻的 ask”）作为对冲成本约束基准
			go s.monitorAndReorderHedge(ctx, market, entryOrderID, hedgeOrderID, hedgeAsset, hedgePrice, hedgeShares, entryFilledTime, entryFilledSize, entryOrderPriceCents, winner)
		}
	}

	var tradesCount int
	var pendingCount int
	// entryOrderResult 一定不为 nil（因为如果为 nil，execErr 不为 nil，函数会提前返回）
	if execErr == nil {
		now := time.Now()
		// 只在更新共享状态时持锁，避免阻塞订单更新回调/行情分发（性能关键）
		s.mu.Lock()
		s.lastTriggerAt = now
		// 注意：lastTriggerSide 和 lastTriggerSideAt 已经在上面提前更新了
		// ⚠️ 重要：不再在这里增加交易计数，只有 Entry + Hedge 都成交后才算完成一次交易
		// 交易计数会在 OnOrderUpdate 回调中，当 Hedge 订单成交时增加
		s.tradedThisCycle = true
		// s.tradesCountThisCycle++ // 已移除：只有 Hedge 成交后才增加计数

		// 更新订单跟踪状态
		s.lastEntryOrderID = entryOrderResult.OrderID
		s.lastEntryOrderStatus = entryOrderResult.Status
		if entryFilled {
			s.lastEntryOrderStatus = domain.OrderStatusFilled
		}
		if hedgeOrderID != "" {
			s.lastHedgeOrderID = hedgeOrderID
		}
		tradesCount = s.tradesCountThisCycle
		if s.pendingTrades != nil {
			pendingCount = len(s.pendingTrades)
		}
		s.mu.Unlock()

		log.Infof("⚡ [%s] 触发(顺序): side=%s ask=%dc hedge=%dc vel=%.3f(c/s) move=%dc/%0.1fs bias=%s(%s) market=%s trades=%d(已完成)+%d(进行中)/%d",
			ID, winner, entryAskCents, hedgeAskCents, winMet.velocity, winMet.delta, winMet.seconds, biasTok, biasReason, market.Slug, tradesCount, pendingCount, s.MaxTradesPerCycle)
		if biasTok != "" || biasReason != "" {
			log.Infof("🧭 [%s] bias: token=%s reason=%s cycleStartMs=%d", ID, biasTok, biasReason, s.cycleStartMs)
		}

		// 额外：打印 Binance 1s/1m 最新 K 线（用于你观察“开盘 1 分钟”关系）
		if s.BinanceFuturesKlines != nil {
			if k1m, ok := s.BinanceFuturesKlines.Latest("1m"); ok {
				log.Infof("📊 [%s] Binance 1m kline: sym=%s o=%.2f c=%.2f h=%.2f l=%.2f closed=%v startMs=%d",
					ID, k1m.Symbol, k1m.Open, k1m.Close, k1m.High, k1m.Low, k1m.IsClosed, k1m.StartTimeMs)
			}
			if k1s, ok := s.BinanceFuturesKlines.Latest("1s"); ok {
				log.Infof("📊 [%s] Binance 1s kline: sym=%s o=%.2f c=%.2f closed=%v startMs=%d",
					ID, k1s.Symbol, k1s.Open, k1s.Close, k1s.IsClosed, k1s.StartTimeMs)
			}
		}
	} else {
		log.Warnf("⚠️ [%s] 下单失败: err=%v side=%s market=%s", ID, execErr, winner, market.Slug)
	}
	return nil
}
