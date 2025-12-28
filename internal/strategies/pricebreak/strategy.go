package pricebreak

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", ID)

func init() {
	bbgo.RegisterStrategy(ID, &Strategy{})
}

// Strategy 价格突破策略
//
// 策略逻辑：
// 1. 监控 up/down 两个方向的代币价格
// 2. 当价格越过 BuyThreshold（默认 70 美分）时买入一定数量
// 3. 当价格跌到 StopLossThreshold（默认 30 美分）时止损卖出
//
// 新架构特性：
// - 使用 TradingService.ExecuteMultiLeg 下单
// - 通过 GetOpenPositionsForMarket 获取持仓
// - 在 OnPriceChanged 中处理买入和止损逻辑
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	mu sync.Mutex

	// 周期状态
	firstSeenAt      time.Time // 首次看到价格的时间（用于预热）
	boughtThisCycle  map[string]bool // 本周期已买入的代币（key: assetID）
	lastActionAt     time.Time // 上次操作时间（用于冷却）

	autoMerge common.AutoMergeController
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }
func (s *Strategy) Defaults() error { return nil }
func (s *Strategy) Validate() error { return s.Config.Validate() }

func (s *Strategy) Initialize() error {
	if s.boughtThisCycle == nil {
		s.boughtThisCycle = make(map[string]bool)
	}
	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	log.Infof("✅ [%s] 策略已订阅价格变化事件 (session=%s)", ID, session.Name)
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	<-ctx.Done()
	return ctx.Err()
}

// OnCycle 周期切换回调
func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, _ *domain.Market) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.firstSeenAt = time.Now()
	s.boughtThisCycle = make(map[string]bool)
	s.lastActionAt = time.Time{}
}

// OnPriceChanged 处理价格变化事件
func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}
	s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)

	// 系统级安全兜底：仅处理当前周期 market 的事件
	cur := s.TradingService.GetCurrentMarket()
	if cur != "" && cur != e.Market.Slug {
		log.Debugf("🔄 [%s] 跳过非当前周期价格事件: eventMarket=%s currentMarket=%s", ID, e.Market.Slug, cur)
		return nil
	}

	// 预热检查：避免刚启动时的脏数据
	s.mu.Lock()
	if s.firstSeenAt.IsZero() {
		s.firstSeenAt = time.Now()
	}
	firstSeenAt := s.firstSeenAt
	s.mu.Unlock()

	if time.Since(firstSeenAt) < time.Duration(s.Config.WarmupMs)*time.Millisecond {
		log.Debugf("⏭️ [%s] 跳过：预热期未结束 (market=%s, elapsed=%v, warmup=%dms)",
			ID, e.Market.Slug, time.Since(firstSeenAt), s.Config.WarmupMs)
		return nil
	}

	// 冷却检查：避免频繁操作
	s.mu.Lock()
	if !s.lastActionAt.IsZero() && time.Since(s.lastActionAt) < 500*time.Millisecond {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	// 1. 检查并处理止损（优先处理）
	if err := s.checkAndHandleStopLoss(orderCtx, e.Market); err != nil {
		log.Warnf("⚠️ [%s] 止损处理失败: %v", ID, err)
	}

	// 2. 检查并处理买入
	if err := s.checkAndHandleBuy(orderCtx, e); err != nil {
		log.Warnf("⚠️ [%s] 买入处理失败: %v", ID, err)
	}

	return nil
}

// checkAndHandleStopLoss 检查并处理止损
func (s *Strategy) checkAndHandleStopLoss(ctx context.Context, market *domain.Market) error {
	// 获取当前持仓
	positions := s.TradingService.GetOpenPositionsForMarket(market.Slug)
	if len(positions) == 0 {
		return nil // 没有持仓，无需止损
	}

	// 获取订单簿价格（同时获取 YES 和 NO 的价格，用于验证）
	yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(ctx, market)
	if err != nil {
		log.Debugf("⏭️ [%s] 止损检查：无法获取订单簿价格: %v", ID, err)
		return nil
	}

	// 打印实时价格信息（用于调试和问题排查）
	yesBidCents := yesBid.ToCents()
	yesAskCents := yesAsk.ToCents()
	noBidCents := noBid.ToCents()
	noAskCents := noAsk.ToCents()
	log.Debugf("📊 [%s] 订单簿价格 (source=%s): YES bid=%dc ask=%dc | NO bid=%dc ask=%dc | market=%s",
		ID, source, yesBidCents, yesAskCents, noBidCents, noAskCents, market.Slug)

	// 检查每个持仓是否需要止损
	for _, pos := range positions {
		if pos == nil || !pos.IsOpen() || pos.Size <= 0 {
			continue
		}

		// 确定 TokenType：优先使用持仓的 TokenType，如果为空则根据 EntryOrder 的 AssetID 推断
		tokenType := pos.TokenType
		if tokenType == "" && pos.EntryOrder != nil && pos.EntryOrder.AssetID != "" {
			if pos.EntryOrder.AssetID == market.YesAssetID {
				tokenType = domain.TokenTypeUp
			} else if pos.EntryOrder.AssetID == market.NoAssetID {
				tokenType = domain.TokenTypeDown
			}
		}
		if tokenType == "" {
			log.Warnf("⚠️ [%s] 无法确定持仓 TokenType，跳过止损: positionID=%s assetID=%s market=%s",
				ID, pos.ID, pos.EntryOrder.AssetID, market.Slug)
			continue
		}

		var currentBid domain.Price
		var assetID string
		var oppositeBid domain.Price // 用于对比验证
		if tokenType == domain.TokenTypeUp {
			currentBid = yesBid
			assetID = market.YesAssetID
			oppositeBid = noBid // NO 的 bid，用于验证
		} else if tokenType == domain.TokenTypeDown {
			currentBid = noBid
			assetID = market.NoAssetID
			oppositeBid = yesBid // YES 的 bid，用于验证
		} else {
			log.Warnf("⚠️ [%s] 未知的 TokenType，跳过止损: tokenType=%s positionID=%s market=%s",
				ID, tokenType, pos.ID, market.Slug)
			continue
		}

		if currentBid.Pips <= 0 {
			log.Debugf("⏭️ [%s] 止损检查：%s bid价格无效 (pips=%d)，跳过", ID, tokenType, currentBid.Pips)
			continue
		}

		currentCents := currentBid.ToCents()
		oppositeCents := oppositeBid.ToCents()

		// 价格合理性检查：如果价格异常低（< 5c），记录详细日志
		if currentCents < 5 {
			log.Warnf("⚠️ [%s] 检测到异常低价: %s bid=%dc (阈值=%dc) | 持仓: size=%.4f entryOrder=%s | 订单簿: YES bid=%dc ask=%dc NO bid=%dc ask=%dc source=%s",
				ID, tokenType, currentCents, s.Config.StopLossThreshold, pos.Size,
				func() string {
					if pos.EntryOrder != nil {
						return pos.EntryOrder.OrderID
					}
					return "nil"
				}(),
				yesBidCents, yesAskCents, noBidCents, noAskCents, source)
			log.Warnf("⚠️ [%s] 价格合理性检查: %s bid=%dc, 对侧(NO) bid=%dc, 价差=%dc",
				ID, tokenType, currentCents, oppositeCents, func() int {
					if currentCents > oppositeCents {
						return currentCents - oppositeCents
					}
					return oppositeCents - currentCents
				}())
		}

		// 检查是否触发止损
		if currentCents <= s.Config.StopLossThreshold {
			// 计算止损订单金额
			orderAmount := currentBid.ToDecimal() * pos.Size
			minOrderSize := 1.1 // 最小订单金额（USDC）

			log.Infof("🛑 [%s] 触发止损: token=%s price=%dc threshold=%dc size=%.4f orderAmount=%.2f USDC market=%s",
				ID, tokenType, currentCents, s.Config.StopLossThreshold, pos.Size, orderAmount, market.Slug)
			log.Infof("📊 [%s] 止损时订单簿详情: YES bid=%dc ask=%dc | NO bid=%dc ask=%dc | source=%s | positionID=%s",
				ID, yesBidCents, yesAskCents, noBidCents, noAskCents, source, pos.ID)

			// 检查订单金额是否满足最小要求
			if orderAmount < minOrderSize {
				log.Warnf("⚠️ [%s] 止损订单金额 %.2f USDC 小于最小要求 %.2f USDC，但仍尝试止损（紧急操作）",
					ID, orderAmount, minOrderSize)
				log.Warnf("⚠️ [%s] 注意：交易所可能拒绝此订单，持仓可能无法及时止损",
					ID)
			}

			// 取消该市场的所有挂单
			s.TradingService.CancelOrdersForMarket(ctx, market.Slug)

			// 创建止损卖出订单（即使金额太小也尝试，止损是紧急操作）
			req := execution.MultiLegRequest{
				Name:       "pricebreak_stop_loss",
				MarketSlug: market.Slug,
				Legs: []execution.LegIntent{{
					Name:      "stop_loss_sell",
					AssetID:   assetID,
					TokenType: tokenType, // ✅ 使用推断后的 TokenType
					Side:      types.SideSell,
					Price:     currentBid,
					Size:      pos.Size,
					OrderType: types.OrderTypeFAK,
				}},
				Hedge: execution.AutoHedgeConfig{Enabled: false},
			}

			_, err := s.TradingService.ExecuteMultiLeg(ctx, req)
			if err != nil {
				estr := strings.ToLower(err.Error())
				if strings.Contains(estr, "trading paused") || strings.Contains(estr, "market mismatch") {
					log.Warnf("⏸️ [%s] 系统拒绝止损下单（fail-safe，预期行为）: %v", ID, err)
					return nil
				}
				// 如果是订单金额太小导致的错误，记录但不返回错误（避免重复触发）
				if strings.Contains(estr, "订单金额") || strings.Contains(estr, "小于最小要求") {
					log.Warnf("⚠️ [%s] 止损订单金额太小，交易所拒绝: %v", ID, err)
					log.Warnf("⚠️ [%s] 持仓可能无法及时止损，请手动处理", ID)
					return nil
				}
				return err
			}

			s.mu.Lock()
			s.lastActionAt = time.Now()
			s.mu.Unlock()

			log.Infof("✅ [%s] 止损订单已提交: token=%s price=%dc size=%.4f",
				ID, tokenType, currentCents, pos.Size)
		}
	}

	return nil
}

// checkAndHandleBuy 检查并处理买入
func (s *Strategy) checkAndHandleBuy(ctx context.Context, e *events.PriceChangedEvent) error {
	market := e.Market

	// 获取当前持仓，避免重复买入
	positions := s.TradingService.GetOpenPositionsForMarket(market.Slug)
	hasUpPosition := false
	hasDownPosition := false
	for _, pos := range positions {
		if pos != nil && pos.IsOpen() && pos.Size > 0 {
			if pos.TokenType == domain.TokenTypeUp {
				hasUpPosition = true
			} else if pos.TokenType == domain.TokenTypeDown {
				hasDownPosition = true
			}
		}
	}

	// 检查 up 和 down 两个方向
	directions := []struct {
		tokenType domain.TokenType
		assetID   string
		name      string
		hasPosition bool
	}{
		{domain.TokenTypeUp, market.YesAssetID, "UP", hasUpPosition},
		{domain.TokenTypeDown, market.NoAssetID, "DOWN", hasDownPosition},
	}

	for _, dir := range directions {
		// 如果已有持仓，跳过
		if dir.hasPosition {
			log.Debugf("⏭️ [%s] %s 已有持仓，跳过买入", ID, dir.name)
			continue
		}

		// 检查是否已在本周期买入过
		s.mu.Lock()
		if s.boughtThisCycle[dir.assetID] {
			s.mu.Unlock()
			continue
		}
		s.mu.Unlock()

		// 获取完整的订单簿价格（用于验证和日志）
		yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(ctx, market)
		if err != nil {
			log.Debugf("⏭️ [%s] 无法获取订单簿价格: %v", ID, err)
			continue
		}

		// 根据方向选择买入价格
		var ask domain.Price
		var askCents int
		if dir.tokenType == domain.TokenTypeUp {
			ask = yesAsk
			askCents = yesAsk.ToCents()
		} else {
			ask = noAsk
			askCents = noAsk.ToCents()
		}

		// 打印订单簿价格信息
		log.Debugf("📊 [%s] 买入检查订单簿 (source=%s): YES bid=%dc ask=%dc | NO bid=%dc ask=%dc | %s ask=%dc market=%s",
			ID, source, yesBid.ToCents(), yesAsk.ToCents(), noBid.ToCents(), noAsk.ToCents(), dir.name, askCents, market.Slug)

		// 检查价格上限（使用 MaxBuyPriceCents 限制）
		if s.Config.MaxBuyPriceCents > 0 && askCents > s.Config.MaxBuyPriceCents {
			log.Debugf("⏭️ [%s] %s 价格超过上限: ask=%dc max=%dc",
				ID, dir.name, askCents, s.Config.MaxBuyPriceCents)
			continue
		}

		// 检查是否触发买入条件（价格越过 BuyThreshold）
		if askCents >= s.Config.BuyThreshold {
			log.Infof("📈 [%s] 触发买入: token=%s price=%dc threshold=%dc size=%.4f market=%s",
				ID, dir.name, askCents, s.Config.BuyThreshold, s.Config.OrderSize, market.Slug)
			log.Infof("📊 [%s] 买入时订单簿详情: YES bid=%dc ask=%dc | NO bid=%dc ask=%dc | source=%s",
				ID, yesBid.ToCents(), yesAsk.ToCents(), noBid.ToCents(), noAsk.ToCents(), source)

			// 创建买入订单
			req := execution.MultiLegRequest{
				Name:       "pricebreak_buy",
				MarketSlug: market.Slug,
				Legs: []execution.LegIntent{{
					Name:      "buy_" + strings.ToLower(dir.name),
					AssetID:   dir.assetID,
					TokenType: dir.tokenType,
					Side:      types.SideBuy,
					Price:     ask,
					Size:      s.Config.OrderSize,
					OrderType: types.OrderTypeFAK,
				}},
				Hedge: execution.AutoHedgeConfig{Enabled: false},
			}

			createdOrders, err := s.TradingService.ExecuteMultiLeg(ctx, req)
			if err != nil {
				estr := strings.ToLower(err.Error())
				if strings.Contains(estr, "trading paused") || strings.Contains(estr, "market mismatch") {
					log.Warnf("⏸️ [%s] 系统拒绝买入下单（fail-safe，预期行为）: %v", ID, err)
					return nil
				}
				log.Warnf("⚠️ [%s] 买入下单失败: %v", ID, err)
				continue
			}

			// 更新状态
			s.mu.Lock()
			s.boughtThisCycle[dir.assetID] = true
			s.lastActionAt = time.Now()
			s.mu.Unlock()

			log.Infof("✅ [%s] 买入订单已提交: token=%s price=%dc size=%.4f orders=%d market=%s",
				ID, dir.name, askCents, s.Config.OrderSize, len(createdOrders), market.Slug)
		}
	}

	return nil
}
