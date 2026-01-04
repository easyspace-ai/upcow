package winbet

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/execution"
)

func (s *Strategy) exitEnabled() bool {
	if s == nil {
		return false
	}
	return s.TakeProfitCents > 0 || s.StopLossCents > 0 || s.MaxHoldSeconds > 0
}

// tryExitPositions 在满足止盈/止损/超时条件时下 SELL FAK 出场。
// 返回 true 表示本次"已有持仓，因此策略将跳过后续开仓逻辑"（无论是否真的触发了出场）。
func (s *Strategy) tryExitPositions(ctx context.Context, market *domain.Market, now time.Time, positions []*domain.Position) bool {
	if s == nil || s.TradingService == nil || market == nil {
		return false
	}

	// 出场冷却：避免短时间重复下 SELL
	exitCooldown := time.Duration(s.ExitCooldownMs) * time.Millisecond
	if exitCooldown <= 0 {
		exitCooldown = 1500 * time.Millisecond
	}
	s.mu.Lock()
	lastExit := s.lastExitAt
	s.mu.Unlock()
	if !lastExit.IsZero() && now.Sub(lastExit) < exitCooldown {
		return true
	}

	// 只在确实需要评估时才拉 top-of-book（优先 WS，必要时回退 REST）
	orderCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	yesBid, _, noBid, _, _, err := s.TradingService.GetTopOfBook(orderCtx, market)
	if err != nil {
		log.Warnf("⚠️ [%s] 出场检查获取盘口失败: %v", ID, err)
		return true // 有持仓但无法评估：保守起见先不新开仓
	}

	type leg struct {
		name    string
		assetID string
		token   domain.TokenType
		price   domain.Price
		size    float64
		reason  string
	}
	legs := make([]leg, 0, 2)

	// 找到是否双边持仓（用于可选"一次性全平"）
	var upPos, downPos *domain.Position
	for _, p := range positions {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}
		if p.TokenType == domain.TokenTypeUp {
			upPos = p
		} else if p.TokenType == domain.TokenTypeDown {
			downPos = p
		}
	}

	shouldExitBoth := false
	if s.ExitBothSidesIfHedged != nil && *s.ExitBothSidesIfHedged {
		shouldExitBoth = upPos != nil && downPos != nil
	}

	// helper：获取 positionID（用于状态 map key）
	posKey := func(p *domain.Position) string {
		if p == nil {
			return ""
		}
		if p.ID != "" {
			return p.ID
		}
		// 兜底：用 market+token 组合（理论上 Position.ID 一定存在）
		return fmt.Sprintf("%s_%s", p.MarketSlug, p.TokenType)
	}

	type decision struct {
		fullExit   bool
		fullReason string
		partial    []leg
	}

	evalPos := func(p *domain.Position) decision {
		d := decision{partial: make([]leg, 0, 2)}
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			return d
		}
		key := posKey(p)
		bid := yesBid
		assetID := market.YesAssetID
		if p.TokenType == domain.TokenTypeDown {
			bid = noBid
			assetID = market.NoAssetID
		}
		if bid.Pips <= 0 {
			return d
		}
		curC := bid.ToCents()
		avgC := p.EntryPrice.ToCents()
		if p.AvgPrice > 0 {
			avgC = int(p.AvgPrice*100 + 0.5)
		}
		diff := curC - avgC

		// 1) 硬止损 / 超时：优先全平
		if s.StopLossCents > 0 && diff <= -s.StopLossCents {
			d.fullExit = true
			d.fullReason = "stop_loss"
			return d
		}
		if s.MaxHoldSeconds > 0 && !p.EntryTime.IsZero() {
			if now.Sub(p.EntryTime) >= time.Duration(s.MaxHoldSeconds)*time.Second {
				d.fullExit = true
				d.fullReason = "max_hold"
				return d
			}
		}

		// 2) 追踪止盈（trailing）：达到 TrailStart 后开始追踪；跌破 stop 触发全平
		if s.EnableTrailingTakeProfit && s.TrailStartCents > 0 && s.TrailDistanceCents > 0 {
			s.mu.Lock()
			st := s.trailing[key]
			if st == nil {
				st = &trailState{}
				s.trailing[key] = st
			}
			// arm
			if !st.Armed && diff >= s.TrailStartCents {
				st.Armed = true
				st.HighBidCents = curC
				st.StopCents = curC - s.TrailDistanceCents
			}
			// update high/stop
			if st.Armed {
				if curC > st.HighBidCents {
					st.HighBidCents = curC
					st.StopCents = curC - s.TrailDistanceCents
				}
				if st.StopCents > 0 && curC <= st.StopCents {
					d.fullExit = true
					d.fullReason = "trailing_stop"
					s.mu.Unlock()
					return d
				}
			}
			s.mu.Unlock()
		}

		// 3) 硬止盈：达到 takeProfitCents 直接全平（作为最终落袋）
		if s.TakeProfitCents > 0 && diff >= s.TakeProfitCents {
			d.fullExit = true
			d.fullReason = "take_profit"
			return d
		}

		// 4) 分批止盈：达到 level 后卖出 fraction（每个 level 只触发一次）
		if len(s.PartialTakeProfits) > 0 && diff > 0 {
			for i, lv := range s.PartialTakeProfits {
				if diff < lv.ProfitCents {
					continue
				}
				s.mu.Lock()
				doneSet := s.partialTPDone[key]
				if doneSet == nil {
					doneSet = make(map[int]bool)
					s.partialTPDone[key] = doneSet
				}
				already := doneSet[i]
				s.mu.Unlock()
				if already {
					continue
				}

				// 计算卖出数量（按当前剩余持仓比例）
				sellSize := p.Size * lv.Fraction
				if sellSize > p.Size {
					sellSize = p.Size
				}
				// 最小金额保护（SELL 不允许系统自动放大；不足则跳过该 level）
				bidDec := bid.ToDecimal()
				if bidDec <= 0 {
					continue
				}
				minSharesByNotional := s.minOrderSize / bidDec
				if s.minOrderSize <= 0 {
					minSharesByNotional = 0
				}
				if minSharesByNotional > 0 && sellSize*bidDec < s.minOrderSize {
					// 如果当前持仓都不足最小金额，则无法卖，留待后续（或由 maxHold/stopLoss 接管）
					if p.Size*bidDec < s.minOrderSize {
						continue
					}
					// 否则把这次卖出提升到"可卖的最小份额"，但不超过持仓
					if minSharesByNotional <= p.Size {
						sellSize = minSharesByNotional
					}
				}
				if sellSize <= 0 {
					continue
				}

				d.partial = append(d.partial, leg{
					name:    fmt.Sprintf("partial_tp_%s_%d", p.TokenType, i),
					assetID: assetID,
					token:   p.TokenType,
					price:   bid,
					size:    sellSize,
					reason:  fmt.Sprintf("partial_tp_%dc_%0.2f", lv.ProfitCents, lv.Fraction),
				})
			}
		}

		return d
	}

	// 先评估每个仓位的决策
	upDec := evalPos(upPos)
	downDec := evalPos(downPos)

	// exitBoth：任意一侧触发"全平"，则两侧都全平
	if shouldExitBoth && (upDec.fullExit || downDec.fullExit) {
		reason := upDec.fullReason
		if reason == "" {
			reason = downDec.fullReason
		}
		if upPos != nil && upPos.Size > 0 {
			legs = append(legs, leg{name: "exit_sell_up", assetID: market.YesAssetID, token: domain.TokenTypeUp, price: yesBid, size: upPos.Size, reason: reason})
		}
		if downPos != nil && downPos.Size > 0 {
			legs = append(legs, leg{name: "exit_sell_down", assetID: market.NoAssetID, token: domain.TokenTypeDown, price: noBid, size: downPos.Size, reason: reason})
		}
	} else {
		// 非 exitBoth：分别处理全平与分批止盈
		if upDec.fullExit && upPos != nil && upPos.Size > 0 {
			legs = append(legs, leg{name: "exit_sell_up", assetID: market.YesAssetID, token: domain.TokenTypeUp, price: yesBid, size: upPos.Size, reason: upDec.fullReason})
		} else {
			legs = append(legs, upDec.partial...)
		}
		if downDec.fullExit && downPos != nil && downPos.Size > 0 {
			legs = append(legs, leg{name: "exit_sell_down", assetID: market.NoAssetID, token: domain.TokenTypeDown, price: noBid, size: downPos.Size, reason: downDec.fullReason})
		} else {
			legs = append(legs, downDec.partial...)
		}
	}

	if len(legs) == 0 {
		return true // 有持仓但未触发：默认不再叠加开仓
	}

	// 出场前先清理本周期挂单（尤其是未成交的 hedge GTC），避免出场后反向被动成交
	s.TradingService.CancelOrdersForMarket(orderCtx, market.Slug)

	req := execution.MultiLegRequest{
		Name:       ID + "_exit",
		MarketSlug: market.Slug,
		Legs:       make([]execution.LegIntent, 0, len(legs)),
		Hedge:      execution.AutoHedgeConfig{Enabled: false},
	}
	for _, l := range legs {
		if l.size <= 0 || l.price.Pips <= 0 {
			continue
		}
		// 获取市场精度信息（从缓存）
		var exitTickSize types.TickSize
		var exitNegRisk *bool
		if s.currentPrecision != nil {
			if parsed, err := ParseTickSize(s.currentPrecision.TickSize); err == nil {
				exitTickSize = parsed
			}
			exitNegRisk = boolPtr(s.currentPrecision.NegRisk)
		}
		req.Legs = append(req.Legs, execution.LegIntent{
			Name:      l.name,
			AssetID:   l.assetID,
			TokenType: l.token,
			Side:      types.SideSell,
			Price:     l.price,
			Size:      l.size,
			OrderType: types.OrderTypeFAK,
			TickSize:  exitTickSize, // 使用缓存的精度信息
			NegRisk:   exitNegRisk,  // 使用缓存的 neg_risk 信息
		})
		log.Infof("📤 [%s] 出场: reason=%s token=%s bid=%dc size=%.4f market=%s",
			ID, l.reason, l.token, l.price.ToCents(), l.size, market.Slug)
	}
	if len(req.Legs) == 0 {
		return true
	}

	created, execErr := s.TradingService.ExecuteMultiLeg(orderCtx, req)
	if isFailSafeRefusal(execErr) {
		log.Warnf("⏸️ [%s] 系统拒绝出场（fail-safe，预期行为）：err=%v market=%s", ID, execErr, market.Slug)
		return true // 有持仓，但系统暂停交易：明确不再继续开仓
	}
	if execErr == nil && len(created) > 0 {
		// 仅在执行成功后标记分批止盈 level 已触发（避免失败导致"错过 level"）
		for _, l := range legs {
			if !strings.HasPrefix(l.reason, "partial_tp_") {
				continue
			}
			// 从 name 中解析 level idx（partial_tp_{token}_{idx}）
			parts := strings.Split(l.name, "_")
			if len(parts) < 4 {
				continue
			}
			idxStr := parts[len(parts)-1]
			idx, err := strconv.Atoi(idxStr)
			if err != nil {
				continue
			}
			var p *domain.Position
			if l.token == domain.TokenTypeUp {
				p = upPos
			} else {
				p = downPos
			}
			key := posKey(p)
			if key == "" {
				continue
			}
			s.mu.Lock()
			doneSet := s.partialTPDone[key]
			if doneSet == nil {
				doneSet = make(map[int]bool)
				s.partialTPDone[key] = doneSet
			}
			doneSet[idx] = true
			s.mu.Unlock()
		}

		// ✅ 优化：平仓后再次检查持仓，防止Hedge单在平仓过程中成交导致单边持仓
		go func() {
			// 等待一小段时间，让订单状态更新
			time.Sleep(500 * time.Millisecond)

			// 再次检查持仓
			checkCtx, checkCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer checkCancel()

			remainingPositions := s.TradingService.GetOpenPositionsForMarket(market.Slug)
			if len(remainingPositions) == 0 {
				return // 没有剩余持仓，安全
			}

			// 检查是否有单边持仓（只有Hedge单，没有Entry单）
			var hedgeOnlyPositions []*domain.Position
			for _, p := range remainingPositions {
				if p == nil || !p.IsOpen() || p.Size <= 0 {
					continue
				}
				// 检查是否是对冲单持仓（通过EntryOrder判断）
				// 如果EntryOrder为空或已平仓，可能是Hedge单单独持仓
				if p.EntryOrder == nil || p.EntryOrder.Status == domain.OrderStatusFilled {
					// 检查是否有对应的Entry持仓
					hasEntryPos := false
					for _, otherPos := range remainingPositions {
						if otherPos == nil || !otherPos.IsOpen() || otherPos.Size <= 0 {
							continue
						}
						// 如果是对侧持仓，说明是Entry单
						if otherPos.TokenType != p.TokenType && otherPos.MarketSlug == p.MarketSlug {
							hasEntryPos = true
							break
						}
					}
					if !hasEntryPos {
						hedgeOnlyPositions = append(hedgeOnlyPositions, p)
					}
				}
			}

			// 如果发现单边持仓，立即平掉
			if len(hedgeOnlyPositions) > 0 {
				log.Warnf("🚨 [%s] 【风险检测】平仓后发现单边持仓（可能是Hedge单在平仓过程中成交），立即平掉: count=%d",
					ID, len(hedgeOnlyPositions))

				// 获取订单簿价格（需要完整的market对象）
				if market == nil || market.YesAssetID == "" || market.NoAssetID == "" {
					log.Warnf("⚠️ [%s] Market信息不完整，无法平掉单边持仓", ID)
					return
				}

				yesBid, _, noBid, _, _, err := s.TradingService.GetTopOfBook(checkCtx, market)
				if err != nil {
					log.Warnf("⚠️ [%s] 获取订单簿价格失败，无法平掉单边持仓: %v", ID, err)
					return
				}

				// 平掉所有单边持仓
				for _, p := range hedgeOnlyPositions {
					if p.Market == nil {
						log.Warnf("⚠️ [%s] 持仓缺少Market信息，跳过: token=%s", ID, p.TokenType)
						continue
					}

					var exitPrice domain.Price
					var exitAssetID string
					if p.TokenType == domain.TokenTypeUp {
						exitPrice = yesBid
						exitAssetID = p.Market.YesAssetID
					} else {
						exitPrice = noBid
						exitAssetID = p.Market.NoAssetID
					}

					if exitPrice.Pips <= 0 {
						log.Warnf("⚠️ [%s] 订单簿价格无效，无法平掉单边持仓: token=%s", ID, p.TokenType)
						continue
					}

					log.Infof("🔧 [%s] 平掉单边持仓: token=%s size=%.4f price=%dc reason=hedge_only_after_exit",
						ID, p.TokenType, p.Size, exitPrice.ToCents())

					// 创建平仓订单
					exitOrder := &domain.Order{
						MarketSlug: market.Slug,
						AssetID:    exitAssetID,
						TokenType:  p.TokenType,
						Side:       types.SideSell,
						Price:      exitPrice,
						Size:       p.Size,
						OrderType:  types.OrderTypeFAK,
						Status:     domain.OrderStatusPending,
						CreatedAt:  time.Now(),
					}

					// 提交平仓订单
					if _, err := s.TradingService.PlaceOrder(checkCtx, exitOrder); err != nil {
						log.Errorf("❌ [%s] 平掉单边持仓失败: token=%s err=%v", ID, p.TokenType, err)
					} else {
						log.Infof("✅ [%s] 已平掉单边持仓: token=%s size=%.4f", ID, p.TokenType, p.Size)
					}
				}
			}
		}()
	}
	s.mu.Lock()
	s.lastExitAt = now
	s.mu.Unlock()
	return true
}
