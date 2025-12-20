package grid

import (
	"context"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/strategies/common"
)

// OnPriceChanged 处理价格变化事件
// 网格策略规则：
// 1. 同时监控 UP 币和 DOWN 币的价格变化
// 2. 不论 UP 还是 DOWN，只要价格达到 62分（网格层级）就买入该币
// 3. 因为只有涨的币（价格高的币），代表周期结束后才大概率胜出
// 4. 如果买了 UP 币，对冲买入 DOWN 币
// 5. 如果买了 DOWN 币，对冲买入 UP 币
func (s *GridStrategy) OnPriceChanged(ctx context.Context, event *events.PriceChangedEvent) error {
	// 策略内部单线程循环会启动并处理事件；这里仅做合并入队（不做任何业务逻辑）
	if event == nil {
		return nil
	}

	s.priceMu.Lock()
	if s.latestPrice == nil {
		s.latestPrice = make(map[domain.TokenType]*events.PriceChangedEvent)
	}
	s.latestPrice[event.TokenType] = event
	s.priceMu.Unlock()

	// 确保事件循环已启动
	s.startLoop(ctx)

	common.TrySignal(s.priceSignalC)
	return nil
}

// onPriceChangedInternal 内部价格变化处理逻辑（直接回调模式）
func (s *GridStrategy) onPriceChangedInternal(ctx context.Context, event *events.PriceChangedEvent) error {
	startTime := time.Now()

	// 诊断：检查 isPlacingOrder 状态
	s.placeOrderMu.Lock()
	isPlacingOrder := s.isPlacingOrder
	setTime := s.isPlacingOrderSetTime
	s.placeOrderMu.Unlock()

	if isPlacingOrder {
		// 风险13修复：检查是否超时
		const maxPlacingOrderTimeout = 60 * time.Second
		if !setTime.IsZero() {
			timeSinceSet := time.Since(setTime)
			if timeSinceSet > maxPlacingOrderTimeout {
				log.Warnf("⚠️ [价格更新诊断] isPlacingOrder标志已持续%v（超过%v），强制重置: %s @ %dc",
					timeSinceSet, maxPlacingOrderTimeout, event.TokenType, event.NewPrice.Cents)
				s.placeOrderMu.Lock()
				s.isPlacingOrder = false
				s.isPlacingOrderSetTime = time.Time{}
				s.placeOrderMu.Unlock()
			} else {
				log.Warnf("⚠️ [价格更新诊断] onPriceChangedInternal开始处理但 isPlacingOrder=true (已持续%v): %s @ %dc, market=%s",
					timeSinceSet, event.TokenType, event.NewPrice.Cents, event.Market.Slug)
			}
		} else {
			log.Warnf("⚠️ [价格更新诊断] onPriceChangedInternal开始处理但 isPlacingOrder=true (SetTime未设置): %s @ %dc, market=%s",
				event.TokenType, event.NewPrice.Cents, event.Market.Slug)
		}
	} else {
		log.Debugf("📊 [价格更新] onPriceChangedInternal开始处理: %s @ %dc, market=%s",
			event.TokenType, event.NewPrice.Cents, event.Market.Slug)
	}

	// 添加诊断日志：记录价格更新频率（每10次记录一次）
	s.mu.Lock()
	if s.priceUpdateCount == 0 {
		s.priceUpdateCount = 1
		s.lastPriceUpdateLogTime = time.Now()
	} else {
		s.priceUpdateCount++
		if s.priceUpdateCount%10 == 0 {
			elapsed := time.Since(s.lastPriceUpdateLogTime)
			log.Debugf("📊 [价格更新] 已处理%d次价格更新，最近10次耗时=%v", s.priceUpdateCount, elapsed)
			s.lastPriceUpdateLogTime = time.Now()
		}
	}
	s.mu.Unlock()

	s.mu.Lock()

	// 检测周期切换：如果市场 Slug 变化，说明切换到新周期
	if s.currentMarketSlug != event.Market.Slug {
		oldSlug := s.currentMarketSlug
		s.currentMarketSlug = event.Market.Slug
		s.mu.Unlock() // 先解锁，避免在 ResetStateForNewCycle 中再次加锁

		if oldSlug != "" {
			log.Infof("🔄 [周期切换] 检测到新周期: %s → %s", oldSlug, event.Market.Slug)
			// 重置所有状态，与上一个周期完全无关
			s.ResetStateForNewCycle()
		} else {
			// 首次设置，不需要重置
			log.Debugf("📋 [周期切换] 首次设置市场周期: %s", event.Market.Slug)
		}

		// 重新加锁继续处理
		s.mu.Lock()
	}

	s.mu.Unlock()

	// 先更新价格（需要锁保护）
	s.mu.Lock()
	now := time.Now()
	oldPriceUp := s.currentPriceUp
	oldPriceDown := s.currentPriceDown

	// 判断是否为首次价格更新（启动时）
	// 如果旧价格为 0，说明是首次更新（启动时）
	isFirstUpdateUp := (event.TokenType == domain.TokenTypeUp && oldPriceUp == 0)
	isFirstUpdateDown := (event.TokenType == domain.TokenTypeDown && oldPriceDown == 0)

	// 更新当前价格，并保存更新后的价格用于显示
	var newPriceUp, newPriceDown int
	if event.TokenType == domain.TokenTypeUp {
		s.currentPriceUp = event.NewPrice.Cents
		s.lastPriceUpdateUp = now
		newPriceUp = event.NewPrice.Cents
		newPriceDown = s.currentPriceDown // 另一个币种的价格保持不变
	} else if event.TokenType == domain.TokenTypeDown {
		s.currentPriceDown = event.NewPrice.Cents
		s.lastPriceUpdateDown = now
		newPriceDown = event.NewPrice.Cents
		newPriceUp = s.currentPriceUp // 另一个币种的价格保持不变
	}

	// 保存需要的信息（在锁内）
	grid := s.grid
	activePosition := s.activePosition
	s.mu.Unlock() // 尽快释放锁，避免阻塞

	// 强对冲/补仓由 HedgePlan 状态机统一驱动（planTick + planStrongHedge）
	// 这里不再直接调用旧的 ensureMinProfitLocked（避免绕过 plan 造成重复下单/不可追踪）

	// 重构后：从 TradingService 查询活跃订单数量（不需要锁）
	activeOrdersCount := len(s.getActiveOrders())

	// UI/日志输出防抖（不影响交易决策，只防刷屏）
	shouldOutput := true
	if s.displayDebouncer != nil {
		ready, _ := s.displayDebouncer.Ready(now)
		shouldOutput = ready
	}
	if shouldOutput {
		// 显示格式化的价格更新信息到控制台（使用 fmt.Printf 直接输出到终端）
		// 直接传递更新后的价格，避免在 displayGridPosition 中再次读取（可能不一致）
		s.displayGridPosition(event, oldPriceUp, oldPriceDown, newPriceUp, newPriceDown)

		// 同时写入日志文件（使用 log.Infof）
		s.logPriceUpdate(event, oldPriceUp, oldPriceDown)

		// 检测价格更新异常（需要锁，但快速检查）
		s.checkPriceUpdateAnomaly(ctx, now)

		if s.displayDebouncer != nil {
			s.displayDebouncer.Mark(now)
		}
	}

	// 诊断：记录价格更新处理完成时间
	processDuration := time.Since(startTime)
	if processDuration > 50*time.Millisecond {
		log.Debugf("📊 [价格更新诊断] onPriceChangedInternal处理完成: %s @ %dc, 耗时=%v",
			event.TokenType, event.NewPrice.Cents, processDuration)
	}

	// 网格策略同时监控 UP 币和 DOWN 币
	// 不论哪个币，只要价格达到网格层级就买入该币
	if event.TokenType == domain.TokenTypeUp {
		// UP 币价格变化：检查是否达到或超过网格层级
		// 找到价格达到或超过的最高网格层级（因为层级已排序，从后往前找）
		var targetLevel *int
		for i := len(grid.Levels) - 1; i >= 0; i-- {
			level := grid.Levels[i]
			if event.NewPrice.Cents >= level {
				targetLevel = &level
				break // 找到最高的层级
			}
		}

		if targetLevel == nil {
			// 价格低于所有网格层级，不触发
			log.Debugf("UP币价格 %dc (%.4f) 低于所有网格层级，网格层级: %v",
				event.NewPrice.Cents, event.NewPrice.ToDecimal(), grid.Levels)
			return nil
		}

		// 重要：如果当前价格已经高于网格层级，不买入
		// 只有当价格从低于网格层级上涨到网格层级时，才买入
		if event.NewPrice.Cents > *targetLevel {
			log.Debugf("UP币价格 %dc 已高于网格层级 %dc，不买入（价格大于网格层级时不交易）",
				event.NewPrice.Cents, *targetLevel)
			return nil
		}

		// 检查方向（价格从下向上穿越网格层级）
		// 只触发价格变化时的情况，启动时不触发（避免启动时立即下单）
		shouldTrigger := false
		if isFirstUpdateUp {
			// 首次价格更新（启动时）：只记录价格，不触发交易
			// 等待价格变化后再触发，避免启动时立即下单
			log.Infof("🚀 [启动] UP币当前价格: %dc (%.4f)，网格层级: %v，等待价格变化触发交易",
				event.NewPrice.Cents, event.NewPrice.ToDecimal(), grid.Levels)
			return nil
		} else if oldPriceUp > 0 {
			// 有旧价格：检查是否从下向上穿越目标层级
			// 条件：旧价格 < 目标层级 且 新价格 == 目标层级（价格刚好到达网格层级）
			// 这意味着价格从低于层级的位置上涨到了网格层级
			if oldPriceUp < *targetLevel && event.NewPrice.Cents == *targetLevel {
				shouldTrigger = true
				log.Infof("UP币网格层级到达: %dc → %dc (网格层级: %dc)，买入UP币",
					oldPriceUp, event.NewPrice.Cents, *targetLevel)
			}
		}

		if shouldTrigger {
			return s.handleGridLevelReached(ctx, event.Market, domain.TokenTypeUp, *targetLevel, event.NewPrice)
		} else {
			// 价格在网格层级上但没有触发，记录调试信息
			log.Debugf("UP币价格在网格层级 %dc 上，但未触发买入 (OldPrice=%dc, NewPrice=%dc, 已有仓位/订单=%v)",
				*targetLevel, oldPriceUp, event.NewPrice.Cents, activePosition != nil || activeOrdersCount > 0)
		}
	} else if event.TokenType == domain.TokenTypeDown {
		// DOWN 币价格变化：检查是否达到或超过网格层级
		// 如果 DOWN 币价格达到网格层级，说明 DOWN 币在涨，买入 DOWN 币
		// 找到价格达到或超过的最高网格层级（因为层级已排序，从后往前找）
		var targetLevel *int
		for i := len(grid.Levels) - 1; i >= 0; i-- {
			level := grid.Levels[i]
			if event.NewPrice.Cents >= level {
				targetLevel = &level
				break // 找到最高的层级
			}
		}

		if targetLevel == nil {
			// 价格低于所有网格层级，不触发
			log.Debugf("DOWN币价格 %dc (%.4f) 低于所有网格层级，网格层级: %v",
				event.NewPrice.Cents, event.NewPrice.ToDecimal(), grid.Levels)
			return nil
		}

		// 重要：如果当前价格已经高于网格层级，不买入
		// 只有当价格从低于网格层级上涨到网格层级时，才买入
		if event.NewPrice.Cents > *targetLevel {
			log.Debugf("DOWN币价格 %dc 已高于网格层级 %dc，不买入（价格大于网格层级时不交易）",
				event.NewPrice.Cents, *targetLevel)
			return nil
		}

		// 检查方向（价格从下向上穿越网格层级）
		// DOWN 币价格上涨 = DOWN 币在涨，买入 DOWN 币
		// 只触发价格变化时的情况，启动时不触发（避免启动时立即下单）
		shouldTrigger := false
		if isFirstUpdateDown {
			// 首次价格更新（启动时）：只记录价格，不触发交易
			// 等待价格变化后再触发，避免启动时立即下单
			log.Infof("🚀 [启动] DOWN币当前价格: %dc (%.4f)，网格层级: %v，等待价格变化触发交易",
				event.NewPrice.Cents, event.NewPrice.ToDecimal(), grid.Levels)
			return nil
		} else if oldPriceDown > 0 {
			// 有旧价格：检查是否从下向上穿越目标层级
			// 条件：旧价格 < 目标层级 且 新价格 == 目标层级（价格刚好到达网格层级）
			// 这意味着价格从低于层级的位置上涨到了网格层级
			if oldPriceDown < *targetLevel && event.NewPrice.Cents == *targetLevel {
				shouldTrigger = true
				log.Infof("DOWN币网格层级到达: %dc → %dc (网格层级: %dc)，买入DOWN币",
					oldPriceDown, event.NewPrice.Cents, *targetLevel)
			}
		}

		if shouldTrigger {
			return s.handleGridLevelReached(ctx, event.Market, domain.TokenTypeDown, *targetLevel, event.NewPrice)
		} else {
			// 价格在网格层级上但没有触发，记录调试信息
			log.Debugf("DOWN币价格在网格层级 %dc 上，但未触发买入 (OldPrice=%dc, NewPrice=%dc, 已有仓位/订单=%v)",
				*targetLevel, oldPriceDown, event.NewPrice.Cents, activePosition != nil || activeOrdersCount > 0)
		}
	}

	// 记录处理时间（用于性能监控）
	processingTime := time.Since(startTime)
	if processingTime > 100*time.Millisecond {
		log.Warnf("⚠️ [价格更新] 处理时间较长: %s @ %dc, 耗时=%v",
			event.TokenType, event.NewPrice.Cents, processingTime)
	} else {
		log.Debugf("✅ [价格更新] onPriceChangedInternal处理完成: %s @ %dc, 耗时=%v",
			event.TokenType, event.NewPrice.Cents, processingTime)
	}

	return nil
}

// checkPriceUpdateAnomaly 检测价格更新异常
// 如果只有一个币的价格更新，另一个币超过30秒未更新，触发严重错误
func (s *GridStrategy) checkPriceUpdateAnomaly(ctx context.Context, now time.Time) {
	const maxStaleDuration = 30 * time.Second // 最大过期时间：30秒

	// 检查 UP 币和 DOWN 币的更新状态
	upUpdated := !s.lastPriceUpdateUp.IsZero()
	downUpdated := !s.lastPriceUpdateDown.IsZero()

	// 如果两个币都已更新，检查是否过期
	if upUpdated && downUpdated {
		upStale := now.Sub(s.lastPriceUpdateUp) > maxStaleDuration
		downStale := now.Sub(s.lastPriceUpdateDown) > maxStaleDuration

		if upStale && downStale {
			// 两个币都过期，严重错误
			log.Errorf("🚨 [价格更新异常] UP币和DOWN币价格都已过期: UP=%v前更新, DOWN=%v前更新",
				now.Sub(s.lastPriceUpdateUp), now.Sub(s.lastPriceUpdateDown))
		} else if upStale {
			// 只有 UP 币过期
			log.Errorf("🚨 [价格更新异常] UP币价格已过期: %v前更新", now.Sub(s.lastPriceUpdateUp))
		} else if downStale {
			// 只有 DOWN 币过期
			log.Errorf("🚨 [价格更新异常] DOWN币价格已过期: %v前更新", now.Sub(s.lastPriceUpdateDown))
		}
	} else if upUpdated && !downUpdated {
		// 只有 UP 币更新，DOWN 币未更新
		if now.Sub(s.lastPriceUpdateUp) > maxStaleDuration {
			log.Errorf("🚨 [价格更新异常] 只有UP币更新，DOWN币超过%v未更新", maxStaleDuration)
		}
	} else if !upUpdated && downUpdated {
		// 只有 DOWN 币更新，UP 币未更新
		if now.Sub(s.lastPriceUpdateDown) > maxStaleDuration {
			log.Errorf("🚨 [价格更新异常] 只有DOWN币更新，UP币超过%v未更新", maxStaleDuration)
		}
	}
}
