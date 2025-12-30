package velocityhedgehold

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy：动量 Entry + 互补价挂 Hedge；对冲成功后持有到结算；未对冲超时/止损才卖出平仓。
type Strategy struct {
	TradingService       *services.TradingService
	BinanceFuturesKlines *services.BinanceFuturesKlines
	Config               `yaml:",inline" json:",inline"`

	autoMerge common.AutoMergeController

	mu sync.Mutex

	// samples：用于速度计算
	samples map[domain.TokenType][]sample

	// 周期状态
	firstSeenAt          time.Time
	lastTriggerAt        time.Time
	tradesCountThisCycle int

	// Binance bias 状态（每周期）
	cycleStartMs int64
	biasReady    bool
	biasToken    domain.TokenType
	biasReason   string

	// 市场过滤
	marketSlugPrefix string

	// 全局约束
	minOrderSize float64 // USDC
	minShareSize float64 // GTC 最小 shares

	// 市场精度信息（从配置加载；可选）
	currentPrecision *MarketPrecisionInfo

	// 监控去重：避免同一 market 重复启动监控 goroutine
	monitoring map[string]bool
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }

func (s *Strategy) Defaults() error { return nil }
func (s *Strategy) Validate() error { return s.Config.Validate() }

func (s *Strategy) Initialize() error {
	if s.samples == nil {
		s.samples = make(map[domain.TokenType][]sample)
	}
	if s.monitoring == nil {
		s.monitoring = make(map[string]bool)
	}

	gc := config.Get()
	if gc == nil {
		return fmt.Errorf("[%s] 全局配置未加载：拒绝启动（避免误交易）", ID)
	}
	sp, err := gc.Market.Spec()
	if err != nil {
		return fmt.Errorf("[%s] 读取 market 配置失败：%w（拒绝启动，避免误交易）", ID, err)
	}

	prefix := strings.TrimSpace(gc.Market.SlugPrefix)
	if prefix == "" {
		prefix = sp.SlugPrefix()
	}
	s.marketSlugPrefix = strings.ToLower(strings.TrimSpace(prefix))
	if s.marketSlugPrefix == "" {
		return fmt.Errorf("[%s] marketSlugPrefix 为空：拒绝启动（避免误交易）", ID)
	}

	s.minOrderSize = gc.MinOrderSize
	s.minShareSize = gc.MinShareSize
	if s.minOrderSize <= 0 {
		s.minOrderSize = 1.1
	}
	if s.minShareSize <= 0 {
		s.minShareSize = 5.0
	}

	// 从配置加载市场精度（如果存在）
	if gc.Market.Precision != nil {
		s.currentPrecision = &MarketPrecisionInfo{
			TickSize:     gc.Market.Precision.TickSize,
			MinOrderSize: gc.Market.Precision.MinOrderSize,
			NegRisk:      gc.Market.Precision.NegRisk,
		}
		log.Infof("✅ [%s] 从配置加载市场精度: tick_size=%s min_order_size=%s neg_risk=%v",
			ID, s.currentPrecision.TickSize, s.currentPrecision.MinOrderSize, s.currentPrecision.NegRisk)
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

func (s *Strategy) OnCycle(ctx context.Context, _ *domain.Market, _ *domain.Market) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = make(map[domain.TokenType][]sample)
	s.firstSeenAt = time.Now()
	s.tradesCountThisCycle = 0
	s.biasReady = false
	s.biasToken = ""
	s.biasReason = ""
	// 不清 lastTriggerAt：避免周期切换瞬间重复触发
	log.Infof("🔄 [%s] 周期切换：交易计数器已重置 tradesCount=0 maxTradesPerCycle=%d", ID, s.MaxTradesPerCycle)
}

func (s *Strategy) shouldHandleMarketEvent(m *domain.Market) bool {
	if s == nil || m == nil || s.TradingService == nil {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(m.Slug), s.marketSlugPrefix) {
		return false
	}
	currentMarketSlug := s.TradingService.GetCurrentMarket()
	if currentMarketSlug != "" && currentMarketSlug != m.Slug {
		return false
	}
	return true
}

func (s *Strategy) updateCycleStartLocked(market *domain.Market) {
	if market == nil || market.Timestamp <= 0 {
		return
	}
	st := market.Timestamp * 1000
	if s.cycleStartMs == 0 || s.cycleStartMs != st {
		s.cycleStartMs = st
		s.biasReady = false
		s.biasToken = ""
		s.biasReason = ""
	}
}

func (s *Strategy) shouldSkipUntilBiasReadyLocked(now time.Time) bool {
	if !s.UseBinanceOpen1mBias {
		return false
	}
	if !s.biasReady && s.cycleStartMs > 0 && s.Open1mMaxWaitSeconds > 0 {
		if now.UnixMilli()-s.cycleStartMs > int64(s.Open1mMaxWaitSeconds)*1000 {
			s.biasReady = true
			s.biasToken = ""
			s.biasReason = "open1m_timeout"
		}
	}
	if !s.biasReady && s.BinanceFuturesKlines != nil && s.cycleStartMs > 0 {
		if k, ok := s.BinanceFuturesKlines.Get("1m", s.cycleStartMs); ok && k.IsClosed && k.Open > 0 {
			bodyBps, wickBps, dirTok := candleStatsBps(k, domain.TokenTypeUp, domain.TokenTypeDown)
			if bodyBps < s.Open1mMinBodyBps {
				s.biasReady = true
				s.biasToken = ""
				s.biasReason = "open1m_body_too_small"
			} else if wickBps > s.Open1mMaxWickBps {
				s.biasReady = true
				s.biasToken = ""
				s.biasReason = "open1m_wick_too_large"
			} else {
				s.biasReady = true
				s.biasToken = dirTok
				s.biasReason = "open1m_ok"
			}
		}
	}
	return s.RequireBiasReady && !s.biasReady
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}
	
	// 🔍 调试日志：记录所有收到的价格事件
	log.Debugf("🔍 [%s] OnPriceChanged 收到价格事件: token=%s price=%.4f market=%s", ID, e.TokenType, e.NewPrice.ToDecimal(), func() string {
		if e.Market != nil {
			return e.Market.Slug
		}
		return "nil"
	}())
	
	s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)

	if !s.shouldHandleMarketEvent(e.Market) {
		log.Debugf("⏭️ [%s] 跳过价格事件（市场不匹配）: token=%s market=%s", ID, e.TokenType, func() string {
			if e.Market != nil {
				return e.Market.Slug
			}
			return "nil"
		}())
		return nil
	}
	now := e.Timestamp
	if now.IsZero() {
		now = time.Now()
	}

	// ===== 恢复/管理已有持仓（重启后也会在这里接管）=====
	// - 已对冲：取消残留挂单，持有到结算
	// - 未对冲：确保 hedge 挂单存在 + 超时/价格止损
	if s.manageExistingExposure(now, e.Market) {
		return nil
	}

	s.mu.Lock()
	if s.firstSeenAt.IsZero() {
		s.firstSeenAt = now
	}
	s.updateCycleStartLocked(e.Market)
	if s.shouldSkipUntilBiasReadyLocked(now) {
		s.mu.Unlock()
		return nil
	}
	if s.WarmupMs > 0 && now.Sub(s.firstSeenAt) < time.Duration(s.WarmupMs)*time.Millisecond {
		s.mu.Unlock()
		return nil
	}

	// 周期尾部保护
	if s.CycleEndProtectionMinutes > 0 && e.Market.Timestamp > 0 {
		cycleDuration := 15 * time.Minute
		if cfg := config.Get(); cfg != nil {
			if spec, err := cfg.Market.Spec(); err == nil {
				cycleDuration = spec.Duration()
			}
		}
		cycleStartTime := time.Unix(e.Market.Timestamp, 0)
		cycleEndTime := cycleStartTime.Add(cycleDuration)
		if now.After(cycleEndTime.Add(-time.Duration(s.CycleEndProtectionMinutes) * time.Minute)) {
			s.mu.Unlock()
			return nil
		}
	}

	if s.MaxTradesPerCycle > 0 && s.tradesCountThisCycle >= s.MaxTradesPerCycle {
		log.Debugf("⏸️ [%s] 已达到每周期最大交易次数限制: count=%d max=%d market=%s", ID, s.tradesCountThisCycle, s.MaxTradesPerCycle, e.Market.Slug)
		s.mu.Unlock()
		return nil
	}
	if !s.lastTriggerAt.IsZero() && now.Sub(s.lastTriggerAt) < time.Duration(s.CooldownMs)*time.Millisecond {
		elapsed := now.Sub(s.lastTriggerAt)
		log.Debugf("⏸️ [%s] 冷却时间未到，跳过触发: elapsed=%dms cooldown=%dms market=%s",
			ID, elapsed.Milliseconds(), s.CooldownMs, e.Market.Slug)
		s.mu.Unlock()
		return nil
	}
	// ✅ 立即更新 lastTriggerAt，防止并发价格事件通过冷却时间检查
	// 注意：即使后续下单失败，冷却时间仍然生效（保守策略）
	s.lastTriggerAt = now

	priceCents := e.NewPrice.ToCents()
	if priceCents <= 0 || priceCents >= 100 {
		s.mu.Unlock()
		return nil
	}
	s.samples[e.TokenType] = append(s.samples[e.TokenType], sample{ts: now, priceCents: priceCents})
	s.pruneLocked(now)

	mUp := s.computeLocked(domain.TokenTypeUp)
	mDown := s.computeLocked(domain.TokenTypeDown)

	// 获取最新价格用于日志（从样本中获取）
	upPrice := latestPriceCents(s.samples[domain.TokenTypeUp])
	downPrice := latestPriceCents(s.samples[domain.TokenTypeDown])
	upSamplesCount := len(s.samples[domain.TokenTypeUp])
	downSamplesCount := len(s.samples[domain.TokenTypeDown])

	// bias 调整阈值（soft）或直接只允许 bias 方向（hard）
	reqMoveUp := s.MinMoveCents
	reqMoveDown := s.MinMoveCents
	reqVelUp := s.MinVelocityCentsPerSec
	reqVelDown := s.MinVelocityCentsPerSec
	if s.UseBinanceOpen1mBias && s.biasToken != "" && s.BiasMode == "soft" {
		if s.biasToken == domain.TokenTypeUp {
			reqMoveDown += s.OppositeBiasMinMoveExtraCents
			reqVelDown *= s.OppositeBiasVelocityMultiplier
		} else if s.biasToken == domain.TokenTypeDown {
			reqMoveUp += s.OppositeBiasMinMoveExtraCents
			reqVelUp *= s.OppositeBiasVelocityMultiplier
		}
	}
	allowUp := true
	allowDown := true
	if s.UseBinanceOpen1mBias && s.biasToken != "" && s.BiasMode == "hard" {
		allowUp = s.biasToken == domain.TokenTypeUp
		allowDown = s.biasToken == domain.TokenTypeDown
	}

	upQualified := allowUp && mUp.ok && mUp.delta >= reqMoveUp && mUp.velocity >= reqVelUp
	downQualified := allowDown && mDown.ok && mDown.delta >= reqMoveDown && mDown.velocity >= reqVelDown

	// 📊 实时价格和速率日志
	var upVelStr, downVelStr string
	if mUp.ok {
		upVelStr = fmt.Sprintf("vel=%.3f(c/s) delta=%dc/%0.1fs", mUp.velocity, mUp.delta, mUp.seconds)
	} else {
		upVelStr = "vel=N/A (insufficient data)"
	}
	if mDown.ok {
		downVelStr = fmt.Sprintf("vel=%.3f(c/s) delta=%dc/%0.1fs", mDown.velocity, mDown.delta, mDown.seconds)
	} else {
		downVelStr = "vel=N/A (insufficient data)"
	}
	
	// 格式化价格显示（显示样本数量）
	var upPriceStr, downPriceStr string
	if upPrice == 0 {
		upPriceStr = fmt.Sprintf("0c (samples=%d)", upSamplesCount)
	} else {
		upPriceStr = fmt.Sprintf("%dc (samples=%d)", upPrice, upSamplesCount)
	}
	if downPrice == 0 {
		downPriceStr = fmt.Sprintf("0c (samples=%d, 未收到DOWN价格更新)", downSamplesCount)
	} else {
		downPriceStr = fmt.Sprintf("%dc (samples=%d)", downPrice, downSamplesCount)
	}
	
	log.Infof("📊 [%s] 价格更新: token=%s price=%dc | UP: price=%s %s [req: move>=%dc vel>=%.3f] qualified=%v | DOWN: price=%s %s [req: move>=%dc vel>=%.3f] qualified=%v | market=%s",
		ID, e.TokenType, priceCents,
		upPriceStr, upVelStr, reqMoveUp, reqVelUp, upQualified,
		downPriceStr, downVelStr, reqMoveDown, reqVelDown, downQualified,
		e.Market.Slug)

	// 选 winner（与 velocityfollow 同步：可选 PreferHigherPrice）
	winner := domain.TokenType("")
	winMet := metrics{}
	if s.PreferHigherPrice && upQualified && downQualified {
		if upPrice > downPrice {
			winner, winMet = domain.TokenTypeUp, mUp
		} else if downPrice > upPrice {
			winner, winMet = domain.TokenTypeDown, mDown
		} else if mUp.velocity >= mDown.velocity {
			winner, winMet = domain.TokenTypeUp, mUp
		} else {
			winner, winMet = domain.TokenTypeDown, mDown
		}
		if s.MinPreferredPriceCents > 0 {
			wp := upPrice
			if winner == domain.TokenTypeDown {
				wp = downPrice
			}
			if wp < s.MinPreferredPriceCents {
				winner = ""
			}
		}
	} else {
		if upQualified {
			winner, winMet = domain.TokenTypeUp, mUp
		}
		if downQualified {
			if winner == "" || mDown.velocity > winMet.velocity {
				winner, winMet = domain.TokenTypeDown, mDown
			}
		}
		if s.PreferHigherPrice && winner != "" && s.MinPreferredPriceCents > 0 {
			wp := upPrice
			if winner == domain.TokenTypeDown {
				wp = downPrice
			}
			if wp < s.MinPreferredPriceCents {
				winner = ""
			}
		}
	}
	if winner == "" {
		s.mu.Unlock()
		return nil
	}

	// 🎯 触发条件满足，准备下单
	log.Infof("🎯 [%s] 触发条件满足: winner=%s vel=%.3f(c/s) delta=%dc/%0.1fs price=%dc market=%s",
		ID, winner, winMet.velocity, winMet.delta, winMet.seconds, latestPriceCents(s.samples[winner]), e.Market.Slug)

	// Binance 1s confirm（可选）
	if s.UseBinanceMoveConfirm {
		if s.BinanceFuturesKlines == nil {
			s.mu.Unlock()
			return nil
		}
		nowMs := now.UnixMilli()
		cur, okCur := s.BinanceFuturesKlines.Latest("1s")
		past, okPast := s.BinanceFuturesKlines.NearestAtOrBefore("1s", nowMs-int64(s.MoveConfirmWindowSeconds)*1000)
		if !okCur || !okPast || past.Close <= 0 {
			s.mu.Unlock()
			return nil
		}
		ret := (cur.Close - past.Close) / past.Close
		retBps := int(math.Abs(ret)*10000 + 0.5)
		dir := domain.TokenTypeDown
		if ret >= 0 {
			dir = domain.TokenTypeUp
		}
		if retBps < s.MinUnderlyingMoveBps || dir != winner {
			s.mu.Unlock()
			return nil
		}
	}

	// 拷贝状态到锁外做 IO
	market := e.Market
	hedgeOffset := s.HedgeOffsetCents
	minOrderSize := s.minOrderSize
	minShareSize := s.minShareSize
	unhedgedMax := s.UnhedgedMaxSeconds
	unhedgedSLCents := s.UnhedgedStopLossCents
	reorderSec := s.HedgeReorderTimeoutSeconds
	biasTok := s.biasToken
	biasReason := s.biasReason
	currentTradesCount := s.tradesCountThisCycle
	maxTradesLimit := s.MaxTradesPerCycle
	s.mu.Unlock()

	// 市场质量 gate
	if s.EnableMarketQualityGate != nil && *s.EnableMarketQualityGate {
		maxSpreadCentsGate := s.MarketQualityMaxSpreadCents
		if maxSpreadCentsGate <= 0 {
			maxSpreadCentsGate = 10
		}
		maxAgeMs := s.MarketQualityMaxBookAgeMs
		if maxAgeMs <= 0 {
			maxAgeMs = 3000
		}
		orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		mq, mqErr := s.TradingService.GetMarketQuality(orderCtx, market, &services.MarketQualityOptions{
			MaxBookAge:     time.Duration(maxAgeMs) * time.Millisecond,
			MaxSpreadPips:  maxSpreadCentsGate * 100,
			PreferWS:       true,
			FallbackToREST: true,
			AllowPartialWS: true,
		})
		if mqErr != nil || mq == nil || mq.Score < s.MarketQualityMinScore {
			return nil
		}
	}

	// 获取盘口并计算 Entry/ Hedge 价格
	orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, yesAsk, _, noAsk, source, err := s.TradingService.GetTopOfBook(orderCtx, market)
	if err != nil {
		return nil
	}

	entryAsset := market.YesAssetID
	hedgeAsset := market.NoAssetID
	entryAsk := yesAsk
	oppAsk := noAsk
	if winner == domain.TokenTypeDown {
		entryAsset = market.NoAssetID
		hedgeAsset = market.YesAssetID
		entryAsk = noAsk
		oppAsk = yesAsk
	}

	entryAskCents := entryAsk.ToCents()
	oppAskCents := oppAsk.ToCents()
	if entryAskCents <= 0 || entryAskCents >= 100 || oppAskCents <= 0 || oppAskCents >= 100 {
		return nil
	}

	hedgeLimitCents := 100 - entryAskCents - hedgeOffset
	if hedgeLimitCents <= 0 || hedgeLimitCents >= 100 {
		return nil
	}
	// 防穿价（保持 maker）
	if hedgeLimitCents >= oppAskCents {
		hedgeLimitCents = oppAskCents - 1
	}
	if hedgeLimitCents <= 0 {
		return nil
	}

	entryPrice := domain.Price{Pips: entryAskCents * 100} // FAK：用实际 ask（taker）
	hedgePrice := domain.Price{Pips: hedgeLimitCents * 100}

	entryPriceDec := entryPrice.ToDecimal()
	hedgePriceDec := hedgePrice.ToDecimal()

	// 下单 shares：Entry 先按期望 size，最终以实际成交为准；Hedge 以后续 entryFilledSize 为准
	entryShares := ensureMinOrderSize(s.OrderSize, entryPriceDec, minOrderSize)
	if entryShares < minShareSize {
		entryShares = minShareSize
	}
	entryShares = adjustSizeForMakerAmountPrecision(entryShares, entryPriceDec)

	log.Infof("⚡ [%s] 准备触发 Entry 订单: side=%s entryAsk=%dc hedgeLimit=%dc vel=%.3f(c/s) move=%dc/%0.1fs market=%s (source=%s) bias=%s(%s) tradesCount=%d/%d",
		ID, winner, entryAskCents, hedgeLimitCents, winMet.velocity, winMet.delta, winMet.seconds, market.Slug, source, string(biasTok), biasReason, currentTradesCount, maxTradesLimit)

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
	}
	s.attachMarketPrecision(entryOrder)
	entryRes, entryErr := s.TradingService.PlaceOrder(orderCtx, entryOrder)
	if entryErr != nil {
		if isFailSafeRefusal(entryErr) {
			return nil
		}
		log.Warnf("⚠️ [%s] Entry 下单失败: err=%v market=%s side=%s entryPrice=%dc size=%.4f", ID, entryErr, market.Slug, winner, entryAskCents, entryShares)
		return nil
	}
	if entryRes == nil || entryRes.OrderID == "" {
		return nil
	}
	log.Infof("✅ [%s] Entry 订单已提交: orderID=%s side=%s price=%dc size=%.4f market=%s",
		ID, entryRes.OrderID, winner, entryAskCents, entryShares, market.Slug)

	// 获取 Entry 实际成交量（必须以此作为 Hedge 目标）
	entryFilledSize := entryRes.FilledSize
	if entryFilledSize <= 0 && s.TradingService != nil {
		if ord, ok := s.TradingService.GetOrder(entryRes.OrderID); ok && ord != nil {
			entryFilledSize = ord.FilledSize
		}
	}
	if entryFilledSize <= 0 {
		// FAK 未成交：直接结束
		return nil
	}
	if entryFilledSize < minShareSize {
		// 不能满足 GTC 最小份额：立即止损平掉碎仓，避免留下无法对冲的敞口
		go s.forceStoploss(context.Background(), market, "entry_fill_too_small", entryRes.OrderID, "")
		return nil
	}

	// Hedge size 按 Entry 实际成交量计算，并做精度/最小金额修正（仍以不超量为原则）
	hedgeShares := entryFilledSize
	if hedgeShares*hedgePriceDec < minOrderSize {
		// 如果最小金额要求导致需要放大 hedgeShares，会造成“过度对冲”；这里选择直接止损退出
		go s.forceStoploss(context.Background(), market, "hedge_min_notional_would_oversize", entryRes.OrderID, "")
		return nil
	}
	hedgeShares = adjustSizeForMakerAmountPrecision(hedgeShares, hedgePriceDec)
	if hedgeShares < minShareSize {
		go s.forceStoploss(context.Background(), market, "hedge_size_precision_too_small", entryRes.OrderID, "")
		return nil
	}

	hedgeOrder := &domain.Order{
		MarketSlug:   market.Slug,
		AssetID:      hedgeAsset,
		TokenType:    opposite(winner),
		Side:         types.SideBuy,
		Price:        hedgePrice,
		Size:         hedgeShares,
		OrderType:    types.OrderTypeGTC,
		IsEntryOrder: false,
		HedgeOrderID: &entryRes.OrderID,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
	}
	s.attachMarketPrecision(hedgeOrder)
	log.Infof("🛡️ [%s] 准备触发 Hedge 订单: side=%s hedgeLimit=%dc size=%.4f entryOrderID=%s entryFilled=%.4f market=%s",
		ID, opposite(winner), hedgeLimitCents, hedgeShares, entryRes.OrderID, entryFilledSize, market.Slug)
	hedgeRes, hedgeErr := s.TradingService.PlaceOrder(orderCtx, hedgeOrder)
	if hedgeErr != nil {
		if isFailSafeRefusal(hedgeErr) {
			// 系统拒绝：保守处理，立即止损退出，避免裸露
			log.Warnf("⚠️ [%s] Hedge 下单失败（系统拒绝）: err=%v entryOrderID=%s market=%s", ID, hedgeErr, entryRes.OrderID, market.Slug)
			go s.forceStoploss(context.Background(), market, "hedge_refused_by_failsafe", entryRes.OrderID, "")
			return nil
		}
		log.Warnf("⚠️ [%s] Hedge 下单失败: err=%v entryOrderID=%s hedgePrice=%dc size=%.4f market=%s", ID, hedgeErr, entryRes.OrderID, hedgeLimitCents, hedgeShares, market.Slug)
		go s.forceStoploss(context.Background(), market, "hedge_place_failed", entryRes.OrderID, "")
		return nil
	}
	if hedgeRes == nil || hedgeRes.OrderID == "" {
		log.Warnf("⚠️ [%s] Hedge 订单ID为空: entryOrderID=%s market=%s", ID, entryRes.OrderID, market.Slug)
		go s.forceStoploss(context.Background(), market, "hedge_order_id_empty", entryRes.OrderID, "")
		return nil
	}
	log.Infof("✅ [%s] Hedge 订单已提交: orderID=%s side=%s price=%dc size=%.4f entryOrderID=%s market=%s",
		ID, hedgeRes.OrderID, opposite(winner), hedgeLimitCents, hedgeShares, entryRes.OrderID, market.Slug)

	s.mu.Lock()
	// lastTriggerAt 已在前面更新，这里只需要更新交易计数
	s.tradesCountThisCycle++
	currentCount := s.tradesCountThisCycle
	maxTrades := s.MaxTradesPerCycle
	s.mu.Unlock()

	log.Infof("✅ [%s] Entry 已成交并已挂 Hedge: entryID=%s filled=%.4f@%dc hedgeID=%s limit=%dc unhedgedMax=%ds sl=%dc tradesCount=%d/%d",
		ID, entryRes.OrderID, entryFilledSize, entryAskCents, hedgeRes.OrderID, hedgeLimitCents, unhedgedMax, unhedgedSLCents, currentCount, maxTrades)

	// 启动监控：直到对冲完成（持有到结算）或触发止损
	entryFilledAt := time.Now()
	if entryRes.FilledAt != nil && !entryRes.FilledAt.IsZero() {
		entryFilledAt = *entryRes.FilledAt
	}
	s.startMonitorIfNeeded(market.Slug, func() {
		s.monitorHedgeAndStoploss(context.Background(), market, winner, entryRes.OrderID, entryAskCents, entryFilledSize, entryFilledAt, hedgeRes.OrderID, hedgeAsset, reorderSec, unhedgedMax, unhedgedSLCents)
	})

	return nil
}

func latestPriceCents(arr []sample) int {
	if len(arr) == 0 {
		return 0
	}
	return arr[len(arr)-1].priceCents
}

func hasAnyOpenPosition(positions []*domain.Position) bool {
	for _, p := range positions {
		if p != nil && p.IsOpen() && p.Size > 0 {
			return true
		}
	}
	return false
}

func (s *Strategy) startMonitorIfNeeded(marketSlug string, fn func()) {
	if s == nil || marketSlug == "" || fn == nil {
		return
	}
	s.mu.Lock()
	if s.monitoring == nil {
		s.monitoring = make(map[string]bool)
	}
	if s.monitoring[marketSlug] {
		s.mu.Unlock()
		return
	}
	s.monitoring[marketSlug] = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			if s.monitoring != nil {
				s.monitoring[marketSlug] = false
			}
			s.mu.Unlock()
		}()
		fn()
	}()
}

func (s *Strategy) attachMarketPrecision(o *domain.Order) {
	if s == nil || o == nil {
		return
	}
	if s.currentPrecision == nil {
		return
	}
	if parsed, err := ParseTickSize(s.currentPrecision.TickSize); err == nil {
		o.TickSize = parsed
	}
	o.NegRisk = boolPtr(s.currentPrecision.NegRisk)
}
