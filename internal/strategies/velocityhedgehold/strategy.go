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
	// signalSamples：用于“单边绝对变化/盘口跳变”信号计算（避免依赖两边都必须到达）
	signalSamples []sample

	// 周期状态
	firstSeenAt          time.Time
	lastTriggerAt        time.Time
	tradesCountThisCycle int

	// Binance bias 状态（每周期）
	cycleStartMs int64
	biasReady    bool
	biasToken    domain.TokenType
	biasReason   string

	// Binance fast bias（秒级）状态：用于“胜率更高的一方”优先过滤
	fastBiasReady     bool
	fastBiasToken     domain.TokenType
	fastBiasReason    string
	fastBiasRetBps    int
	fastBiasUpdatedAt time.Time

	// 市场过滤
	marketSlugPrefix string

	// 全局约束
	minOrderSize float64 // USDC
	minShareSize float64 // GTC 最小 shares

	// 市场精度信息（从配置加载；可选）
	currentPrecision *MarketPrecisionInfo

	// 监控去重：避免同一 market 重复启动监控 goroutine
	monitoring map[string]bool

	// 待处理的 Entry 订单：等待成交后提交 Hedge
	// key: entryOrderID, value: pendingEntryInfo
	pendingEntries   map[string]*pendingEntryInfo
	pendingEntriesMu sync.Mutex
}

// pendingEntryInfo 存储待处理 Entry 订单的信息，用于在订单成交后提交 Hedge
type pendingEntryInfo struct {
	market          *domain.Market
	winner          domain.TokenType
	entryAskCents   int
	hedgeLimitCents int
	hedgePrice      domain.Price
	hedgeAsset      string
	entryShares     float64
	hedgeOffset     int
	minOrderSize    float64
	minShareSize    float64
	unhedgedMax     int
	unhedgedSLCents int
	reorderSec      int
	createdAt       time.Time
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
	if s.pendingEntries == nil {
		s.pendingEntries = make(map[string]*pendingEntryInfo)
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
	session.OnOrderUpdate(s)
	log.Infof("✅ [%s] 策略已订阅价格变化和订单更新事件 (session=%s)", ID, session.Name)

	// 注册 TradingService 的订单更新回调（兜底）
	if s.TradingService != nil {
		handler := services.OrderUpdateHandlerFunc(s.OnOrderUpdate)
		s.TradingService.OnOrderUpdate(handler)
		log.Infof("✅ [%s] 已注册 TradingService 订单更新回调", ID)
	}
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *Strategy) OnCycle(ctx context.Context, _ *domain.Market, _ *domain.Market) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = make(map[domain.TokenType][]sample)
	s.signalSamples = nil
	s.firstSeenAt = time.Now()
	s.tradesCountThisCycle = 0
	s.biasReady = false
	s.biasToken = ""
	s.biasReason = ""
	s.fastBiasReady = false
	s.fastBiasToken = ""
	s.fastBiasReason = ""
	s.fastBiasRetBps = 0
	s.fastBiasUpdatedAt = time.Time{}
	// 不清 lastTriggerAt：避免周期切换瞬间重复触发
	log.Infof("🔄 [%s] 周期切换：交易计数器已重置 tradesCount=0 maxTradesPerCycle=%d", ID, s.MaxTradesPerCycle)

	// 清理待处理的 Entry 订单（周期切换时清理）
	s.pendingEntriesMu.Lock()
	s.pendingEntries = make(map[string]*pendingEntryInfo)
	s.pendingEntriesMu.Unlock()
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
	if !s.UseBinanceOpen1mBias && !s.UseBinanceFastBias {
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
	// fast bias：只要我们至少计算过一次（无论 token 是否为空），就认为 ready（用于启动期门控）
	if s.UseBinanceFastBias && s.fastBiasReady {
		return false
	}
	return s.RequireBiasReady && !s.biasReady
}

// activeBiasLocked 选择当前使用的 bias（优先 fast bias，其次 open1m bias）。
func (s *Strategy) activeBiasLocked(now time.Time) (domain.TokenType, string) {
	if s == nil {
		return "", ""
	}
	if s.UseBinanceFastBias && s.fastBiasToken != "" {
		return s.fastBiasToken, s.fastBiasReason
	}
	if s.UseBinanceOpen1mBias && s.biasToken != "" {
		return s.biasToken, s.biasReason
	}
	return "", ""
}

// updateFastBiasLocked 使用 Binance 1s Kline 计算短窗方向 bias（用于“胜率更高一方”过滤）。
func (s *Strategy) updateFastBiasLocked(now time.Time) {
	if s == nil || !s.UseBinanceFastBias || s.BinanceFuturesKlines == nil {
		return
	}
	// 标记 ready：只要尝试过计算（避免 RequireBiasReady 卡死在“永远等不到 1m 收盘”）
	s.fastBiasReady = true

	win := s.FastBiasWindowSeconds
	if win <= 0 {
		win = 30
	}
	minBps := s.FastBiasMinMoveBps
	if minBps <= 0 {
		minBps = 15
	}
	hold := s.FastBiasMinHoldSeconds
	if hold <= 0 {
		hold = 2
	}

	cur, okCur := s.BinanceFuturesKlines.Latest("1s")
	past, okPast := s.BinanceFuturesKlines.NearestAtOrBefore("1s", now.UnixMilli()-int64(win)*1000)
	if !okCur || !okPast || past.Close <= 0 || cur.Close <= 0 {
		return
	}

	ret := (cur.Close - past.Close) / past.Close
	retBps := int(math.Abs(ret)*10000 + 0.5)
	dir := domain.TokenTypeDown
	if ret >= 0 {
		dir = domain.TokenTypeUp
	}

	// 抗抖：bias 至少保持 hold 秒，避免 1s 噪声来回翻转造成过度交易
	if s.fastBiasToken != "" && !s.fastBiasUpdatedAt.IsZero() && now.Sub(s.fastBiasUpdatedAt) < time.Duration(hold)*time.Second {
		// 在 hold 时间内，只更新强度，不换方向（除非完全清空）
		s.fastBiasRetBps = retBps
		s.fastBiasReason = "fast_bias_hold"
		return
	}

	if retBps >= minBps {
		s.fastBiasToken = dir
		s.fastBiasReason = "fast_bias_ok"
		s.fastBiasRetBps = retBps
		s.fastBiasUpdatedAt = now
	} else {
		s.fastBiasToken = ""
		s.fastBiasReason = "fast_bias_too_small"
		s.fastBiasRetBps = retBps
		s.fastBiasUpdatedAt = now
	}
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}
	log.Infof("🔍 [%s] OnPriceChanged 收到价格事件: token=%s price=%.4f market=%s", ID, e.TokenType, e.NewPrice.ToDecimal(), func() string {
		if e.Market != nil {
			return e.Market.Slug
		}
		return "nil"
	}())
	// 🔍 调试日志：记录所有收到的价格事件
	//log.Debugf("🔍 [%s] OnPriceChanged 收到价格事件: token=%s price=%.4f market=%s", ID, e.TokenType, e.NewPrice.ToDecimal(), func() string {
	//	if e.Market != nil {
	//		return e.Market.Slug
	//	}
	//	return "nil"
	//}())

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
	// 注意：即使当前有持仓需要管理，我们也希望“监控日志”能持续拿到 book/signal 信息。
	// entry 逻辑会在稍后根据 manageExistingExposure 决定是否继续。

	s.mu.Lock()
	if s.firstSeenAt.IsZero() {
		s.firstSeenAt = now
	}
	s.updateCycleStartLocked(e.Market)
	// 秒级 fast bias：每次 tick 都尝试更新（不要求“等到 1m 收盘”）
	s.updateFastBiasLocked(now)
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

	// 1) 记录原始 event price（用于兼容日志/双边统计）
	priceCents := e.NewPrice.ToCents()
	if priceCents > 0 && priceCents < 100 {
		s.samples[e.TokenType] = append(s.samples[e.TokenType], sample{ts: now, priceCents: priceCents})
	}

	// 2) 构造“信号侧价格”（可来自 bestBook 或 event，单边绝对变化）
	signalTok := domain.TokenTypeUp
	if strings.EqualFold(s.SignalToken, "down") {
		signalTok = domain.TokenTypeDown
	}
	signalCents := s.signalPriceCentsLocked(now, signalTok, e)
	if signalCents > 0 && signalCents < 100 {
		s.signalSamples = append(s.signalSamples, sample{ts: now, priceCents: signalCents})
	}

	s.pruneLocked(now)
	s.pruneSignalLocked(now)

	// 3) 若有持仓需要管理，先走管理逻辑（不进入开仓/加仓触发）
	if s.manageExistingExposure(now, e.Market) {
		// 仍然会继续走日志输出（监控），但不触发交易
	}

	// 4) 计算信号
	var mUp, mDown metrics
	winner := domain.TokenType("")
	winMet := metrics{}
	if strings.EqualFold(s.SignalMode, "legacy") {
		// 旧逻辑：分别计算 UP/DOWN 的上行速度
		mUp = s.computeLocked(domain.TokenTypeUp)
		mDown = s.computeLocked(domain.TokenTypeDown)
	} else {
		// 新逻辑：单边绝对变化（双向）/盘口跳变
		win, met := s.computeSignalLocked(signalTok)
		winner = win
		winMet = met
		// 为保持日志结构，mUp/mDown 仍给出“是否可用”的占位（不作为触发依据）
		mUp = metrics{}
		mDown = metrics{}
	}

	// 获取最新价格用于日志（从样本中获取）
	upPrice := latestPriceCents(s.samples[domain.TokenTypeUp])
	downPrice := latestPriceCents(s.samples[domain.TokenTypeDown])
	upSamplesCount := len(s.samples[domain.TokenTypeUp])
	downSamplesCount := len(s.samples[domain.TokenTypeDown])

	// 如果 DOWN 未到达但 UP 有值，给出“镜像推导”监控（不参与下单）
	impliedDown := 0
	if downSamplesCount == 0 && upPrice > 0 && upPrice < 100 {
		impliedDown = 100 - upPrice
	}

	// 选择当前“方向 bias”（优先 fast bias，其次 open1m bias）
	activeBiasTok, activeBiasReason := s.activeBiasLocked(now)

	// bias 调整阈值（soft）或直接只允许 bias 方向（hard）
	reqMoveUp := s.MinMoveCents
	reqMoveDown := s.MinMoveCents
	reqVelUp := s.MinVelocityCentsPerSec
	reqVelDown := s.MinVelocityCentsPerSec
	if (s.UseBinanceFastBias || s.UseBinanceOpen1mBias) && activeBiasTok != "" && s.BiasMode == "soft" {
		if activeBiasTok == domain.TokenTypeUp {
			reqMoveDown += s.OppositeBiasMinMoveExtraCents
			reqVelDown *= s.OppositeBiasVelocityMultiplier
		} else if activeBiasTok == domain.TokenTypeDown {
			reqMoveUp += s.OppositeBiasMinMoveExtraCents
			reqVelUp *= s.OppositeBiasVelocityMultiplier
		}
	}
	allowUp := true
	allowDown := true
	if (s.UseBinanceFastBias || s.UseBinanceOpen1mBias) && activeBiasTok != "" && s.BiasMode == "hard" {
		allowUp = activeBiasTok == domain.TokenTypeUp
		allowDown = activeBiasTok == domain.TokenTypeDown
	}

	upQualified := allowUp && mUp.ok && mUp.delta >= reqMoveUp && mUp.velocity >= reqVelUp
	downQualified := allowDown && mDown.ok && mDown.delta >= reqMoveDown && mDown.velocity >= reqVelDown

	// 新信号模式：用 winMet/winner 覆盖 qualified 判定
	if !strings.EqualFold(s.SignalMode, "legacy") {
		upQualified = false
		downQualified = false
		if winner == domain.TokenTypeUp {
			upQualified = winMet.ok && winMet.delta >= s.MinMoveCents && winMet.velocity >= s.MinVelocityCentsPerSec
		} else if winner == domain.TokenTypeDown {
			downQualified = winMet.ok && winMet.delta >= s.MinMoveCents && winMet.velocity >= s.MinVelocityCentsPerSec
		}
	}

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
		if impliedDown > 0 {
			downPriceStr = fmt.Sprintf("0c (samples=%d, implied=%dc)", downSamplesCount, impliedDown)
		} else {
			downPriceStr = fmt.Sprintf("0c (samples=%d, 未收到DOWN价格更新)", downSamplesCount)
		}
	} else {
		downPriceStr = fmt.Sprintf("%dc (samples=%d)", downPrice, downSamplesCount)
	}

	// 盘口快照（监控/风控）：来自 WS bestBook（零 IO），用于观测 bid/ask 是否镜像与是否过旧
	bookStr := s.bestBookLogStr(now)

	log.Infof("📊 [%s] 价格更新: token=%s price=%dc | UP: price=%s %s [req: move>=%dc vel>=%.3f] qualified=%v | DOWN: price=%s %s [req: move>=%dc vel>=%.3f] qualified=%v | %s | market=%s",
		ID, e.TokenType, priceCents,
		upPriceStr, upVelStr, reqMoveUp, reqVelUp, upQualified,
		downPriceStr, downVelStr, reqMoveDown, reqVelDown, downQualified,
		bookStr, e.Market.Slug)

	// 选 winner
	if strings.EqualFold(s.SignalMode, "legacy") {
		// legacy：与 velocityfollow 同步：可选 PreferHigherPrice
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
	biasTok := activeBiasTok
	biasReason := activeBiasReason
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
		// FAK 未立即成交：保存订单信息，等待订单更新事件触发 Hedge 提交
		s.pendingEntriesMu.Lock()
		s.pendingEntries[entryRes.OrderID] = &pendingEntryInfo{
			market:          market,
			winner:          winner,
			entryAskCents:   entryAskCents,
			hedgeLimitCents: hedgeLimitCents,
			hedgePrice:      hedgePrice,
			hedgeAsset:      hedgeAsset,
			entryShares:     entryShares,
			hedgeOffset:     hedgeOffset,
			minOrderSize:    minOrderSize,
			minShareSize:    minShareSize,
			unhedgedMax:     unhedgedMax,
			unhedgedSLCents: unhedgedSLCents,
			reorderSec:      reorderSec,
			createdAt:       now,
		}
		s.pendingEntriesMu.Unlock()
		log.Infof("⏳ [%s] Entry 订单未立即成交，已保存待处理信息，等待订单更新事件: orderID=%s market=%s", ID, entryRes.OrderID, market.Slug)
		return nil
	}
	// 提交 Hedge 订单（提取为独立函数，可在 OnOrderUpdate 中复用）
	return s.submitHedgeOrder(ctx, orderCtx, market, winner, entryRes.OrderID, entryFilledSize, entryAskCents, hedgeLimitCents, hedgePrice, hedgeAsset, minOrderSize, minShareSize, unhedgedMax, unhedgedSLCents, reorderSec, now, entryRes.FilledAt)
}

// submitHedgeOrder 提交 Hedge 订单的通用逻辑
func (s *Strategy) submitHedgeOrder(ctx context.Context, orderCtx context.Context, market *domain.Market, winner domain.TokenType, entryOrderID string, entryFilledSize float64, entryAskCents int, hedgeLimitCents int, hedgePrice domain.Price, hedgeAsset string, minOrderSize float64, minShareSize float64, unhedgedMax int, unhedgedSLCents int, reorderSec int, now time.Time, entryFilledAt *time.Time) error {
	if entryFilledSize < minShareSize {
		// 不能满足 GTC 最小份额：立即止损平掉碎仓，避免留下无法对冲的敞口
		go s.forceStoploss(context.Background(), market, "entry_fill_too_small", entryOrderID, "")
		return nil
	}

	hedgePriceDec := hedgePrice.ToDecimal()
	// Hedge size 按 Entry 实际成交量计算，并做精度/最小金额修正（仍以不超量为原则）
	hedgeShares := entryFilledSize
	if hedgeShares*hedgePriceDec < minOrderSize {
		// 如果最小金额要求导致需要放大 hedgeShares，会造成“过度对冲”；这里选择直接止损退出
		if s.AllowModerateOverHedge {
			// 允许适度过度对冲：计算需要放大的倍数
			requiredMultiplier := minOrderSize / (hedgeShares * hedgePriceDec)
			enlargedHedgeShares := hedgeShares * requiredMultiplier

			// 检查是否在允许的过度对冲范围内
			maxAllowedHedgeShares := entryFilledSize * (1.0 + s.MaxOverHedgeRatio)
			if enlargedHedgeShares <= maxAllowedHedgeShares {
				hedgeShares = enlargedHedgeShares
				log.Infof("⚠️ [%s] 允许适度过度对冲以满足最小金额：entry=%.4f hedge=%.4f (放大%.1f%%, 过度对冲%.1f%%) entryOrderID=%s market=%s",
					ID, entryFilledSize, hedgeShares, (requiredMultiplier-1)*100, ((hedgeShares-entryFilledSize)/entryFilledSize)*100, entryOrderID, market.Slug)
			} else {
				// 过度对冲超过允许范围，仍然止损
				log.Warnf("🚨 [%s] 过度对冲超过允许范围：entry=%.4f required=%.4f maxAllowed=%.4f (%.1f%%) entryOrderID=%s market=%s",
					ID, entryFilledSize, enlargedHedgeShares, maxAllowedHedgeShares, s.MaxOverHedgeRatio*100, entryOrderID, market.Slug)
				go s.forceStoploss(context.Background(), market, "hedge_min_notional_would_oversize", entryOrderID, "")
				return nil
			}
		} else {
			// 不允许过度对冲：直接止损退出（保守策略）
			go s.forceStoploss(context.Background(), market, "hedge_min_notional_would_oversize", entryOrderID, "")
			return nil
		}
	}
	hedgeShares = adjustSizeForMakerAmountPrecision(hedgeShares, hedgePriceDec)
	if hedgeShares < minShareSize {
		go s.forceStoploss(context.Background(), market, "hedge_size_precision_too_small", entryOrderID, "")
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
		HedgeOrderID: &entryOrderID,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
	}
	s.attachMarketPrecision(hedgeOrder)
	log.Infof("🛡️ [%s] 准备触发 Hedge 订单: side=%s hedgeLimit=%dc size=%.4f entryOrderID=%s entryFilled=%.4f market=%s",
		ID, opposite(winner), hedgeLimitCents, hedgeShares, entryOrderID, entryFilledSize, market.Slug)
	hedgeRes, hedgeErr := s.TradingService.PlaceOrder(orderCtx, hedgeOrder)

	// 无论Hedge订单是否成功，Entry订单已成交，都应该递增交易计数
	s.mu.Lock()
	s.tradesCountThisCycle++
	currentCount := s.tradesCountThisCycle
	maxTrades := s.MaxTradesPerCycle
	s.mu.Unlock()

	if hedgeErr != nil {
		if isFailSafeRefusal(hedgeErr) {
			// 系统拒绝：保守处理，立即止损退出，避免裸露
			log.Warnf("⚠️ [%s] Hedge 下单失败（系统拒绝）: err=%v entryOrderID=%s market=%s tradesCount=%d/%d", ID, hedgeErr, entryOrderID, market.Slug, currentCount, maxTrades)
			go s.forceStoploss(context.Background(), market, "hedge_refused_by_failsafe", entryOrderID, "")
			return nil
		}
		log.Warnf("⚠️ [%s] Hedge 下单失败: err=%v entryOrderID=%s hedgePrice=%dc size=%.4f market=%s tradesCount=%d/%d", ID, hedgeErr, entryOrderID, hedgeLimitCents, hedgeShares, market.Slug, currentCount, maxTrades)
		// Hedge订单提交失败，启动监控以处理未对冲持仓
		entryFilledAtTime := now
		if entryFilledAt != nil && !entryFilledAt.IsZero() {
			entryFilledAtTime = *entryFilledAt
		}
		s.startMonitorIfNeeded(market.Slug, func() {
			// Hedge订单ID为空，监控会检测到未对冲并触发止损
			s.monitorHedgeAndStoploss(context.Background(), market, winner, entryOrderID, entryAskCents, entryFilledSize, entryFilledAtTime, "", hedgeAsset, reorderSec, unhedgedMax, unhedgedSLCents)
		})
		go s.forceStoploss(context.Background(), market, "hedge_place_failed", entryOrderID, "")
		return nil
	}
	if hedgeRes == nil || hedgeRes.OrderID == "" {
		log.Warnf("⚠️ [%s] Hedge 订单ID为空: entryOrderID=%s market=%s tradesCount=%d/%d", ID, entryOrderID, market.Slug, currentCount, maxTrades)
		// Hedge订单ID为空，启动监控以处理未对冲持仓
		entryFilledAtTime := now
		if entryFilledAt != nil && !entryFilledAt.IsZero() {
			entryFilledAtTime = *entryFilledAt
		}
		s.startMonitorIfNeeded(market.Slug, func() {
			// Hedge订单ID为空，监控会检测到未对冲并触发止损
			s.monitorHedgeAndStoploss(context.Background(), market, winner, entryOrderID, entryAskCents, entryFilledSize, entryFilledAtTime, "", hedgeAsset, reorderSec, unhedgedMax, unhedgedSLCents)
		})
		go s.forceStoploss(context.Background(), market, "hedge_order_id_empty", entryOrderID, "")
		return nil
	}
	log.Infof("✅ [%s] Hedge 订单已提交: orderID=%s side=%s price=%dc size=%.4f entryOrderID=%s market=%s",
		ID, hedgeRes.OrderID, opposite(winner), hedgeLimitCents, hedgeShares, entryOrderID, market.Slug)

	log.Infof("✅ [%s] Entry 已成交并已挂 Hedge: entryID=%s filled=%.4f@%dc hedgeID=%s limit=%dc unhedgedMax=%ds sl=%dc tradesCount=%d/%d",
		ID, entryOrderID, entryFilledSize, entryAskCents, hedgeRes.OrderID, hedgeLimitCents, unhedgedMax, unhedgedSLCents, currentCount, maxTrades)

	// 启动监控：直到对冲完成（持有到结算）或触发止损
	entryFilledAtTime := now
	if entryFilledAt != nil && !entryFilledAt.IsZero() {
		entryFilledAtTime = *entryFilledAt
	}
	s.startMonitorIfNeeded(market.Slug, func() {
		s.monitorHedgeAndStoploss(context.Background(), market, winner, entryOrderID, entryAskCents, entryFilledSize, entryFilledAtTime, hedgeRes.OrderID, hedgeAsset, reorderSec, unhedgedMax, unhedgedSLCents)
	})

	return nil
}

// OnOrderUpdate 处理订单更新事件，当 Entry 订单成交时自动提交 Hedge 订单
func (s *Strategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	if order == nil || order.OrderID == "" {
		return nil
	}

	// 只处理 Entry 订单
	if !order.IsEntryOrder {
		return nil
	}

	// 只处理当前市场的订单
	if order.MarketSlug != "" && !strings.HasPrefix(strings.ToLower(order.MarketSlug), s.marketSlugPrefix) {
		return nil
	}

	// 检查是否有待处理的 Entry 订单信息
	s.pendingEntriesMu.Lock()
	pendingInfo, exists := s.pendingEntries[order.OrderID]
	s.pendingEntriesMu.Unlock()

	if !exists {
		// 没有待处理信息，说明订单在提交时已立即成交，已在 OnPriceChanged 中处理
		return nil
	}

	// 只处理成交的订单
	if order.Status != domain.OrderStatusFilled || order.FilledSize <= 0 {
		return nil
	}

	entryFilledSize := order.FilledSize
	log.Infof("✅ [%s] Entry 订单已成交（通过订单更新回调）: orderID=%s filledSize=%.4f market=%s", ID, order.OrderID, entryFilledSize, order.MarketSlug)

	// 从待处理信息中获取参数
	market := pendingInfo.market
	winner := pendingInfo.winner
	entryAskCents := pendingInfo.entryAskCents
	hedgeLimitCents := pendingInfo.hedgeLimitCents
	hedgePrice := pendingInfo.hedgePrice
	hedgeAsset := pendingInfo.hedgeAsset
	minOrderSize := pendingInfo.minOrderSize
	minShareSize := pendingInfo.minShareSize
	unhedgedMax := pendingInfo.unhedgedMax
	unhedgedSLCents := pendingInfo.unhedgedSLCents
	reorderSec := pendingInfo.reorderSec

	// 创建订单上下文（使用独立的context，避免使用已取消的ctx）
	orderCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 提交 Hedge 订单
	entryFilledAt := order.FilledAt
	if entryFilledAt == nil {
		t := time.Now()
		entryFilledAt = &t
	}
	err := s.submitHedgeOrder(context.Background(), orderCtx, market, winner, order.OrderID, entryFilledSize, entryAskCents, hedgeLimitCents, hedgePrice, hedgeAsset, minOrderSize, minShareSize, unhedgedMax, unhedgedSLCents, reorderSec, time.Now(), entryFilledAt)

	// 清理待处理信息
	s.pendingEntriesMu.Lock()
	delete(s.pendingEntries, order.OrderID)
	s.pendingEntriesMu.Unlock()

	return err
}

func (s *Strategy) pruneSignalLocked(now time.Time) {
	window := time.Duration(s.WindowSeconds) * time.Second
	if window <= 0 {
		window = 10 * time.Second
	}
	cut := now.Add(-window)
	arr := s.signalSamples
	i := 0
	for i < len(arr) && arr[i].ts.Before(cut) {
		i++
	}
	if i > 0 {
		arr = arr[i:]
	}
	if len(arr) > 512 {
		arr = arr[len(arr)-512:]
	}
	s.signalSamples = arr
}

// computeSignalLocked: 单边绝对变化（双向）
// - delta>0：买 signalTok
// - delta<0：买 opposite(signalTok)
// - velocity 用 abs(delta)/dt
func (s *Strategy) computeSignalLocked(signalTok domain.TokenType) (winner domain.TokenType, met metrics) {
	arr := s.signalSamples
	if len(arr) < 2 {
		return "", metrics{}
	}
	first := arr[0]
	last := arr[len(arr)-1]
	dt := last.ts.Sub(first.ts).Seconds()
	if dt <= 0.001 {
		return "", metrics{}
	}
	rawDelta := last.priceCents - first.priceCents
	if rawDelta == 0 {
		return "", metrics{}
	}
	absDelta := rawDelta
	if absDelta < 0 {
		absDelta = -absDelta
	}
	vel := float64(absDelta) / dt
	if math.IsNaN(vel) || math.IsInf(vel, 0) {
		return "", metrics{}
	}
	winner = signalTok
	if rawDelta < 0 {
		winner = opposite(signalTok)
	}
	return winner, metrics{ok: true, delta: absDelta, seconds: dt, velocity: vel}
}

func (s *Strategy) bestBookLogStr(now time.Time) string {
	if s == nil || s.TradingService == nil {
		return "book=na"
	}
	snap, ok := s.TradingService.BestBookSnapshot()
	if !ok {
		return "book=na"
	}
	// pips -> cents（四舍五入到 1c）
	p2c := func(p uint16) int {
		if p == 0 {
			return 0
		}
		return (int(p) + 50) / 100
	}
	upBid := p2c(snap.YesBidPips)
	upAsk := p2c(snap.YesAskPips)
	downBid := p2c(snap.NoBidPips)
	downAsk := p2c(snap.NoAskPips)
	ageMs := int64(0)
	if !snap.UpdatedAt.IsZero() {
		ageMs = now.Sub(snap.UpdatedAt).Milliseconds()
	}
	// 镜像偏离（监控）：NO_bid 应≈100-YES_ask，NO_ask 应≈100-YES_bid
	d1 := 0
	d2 := 0
	if upAsk > 0 && downBid > 0 {
		d1 = downBid - (100 - upAsk)
		if d1 < 0 {
			d1 = -d1
		}
	}
	if upBid > 0 && downAsk > 0 {
		d2 = downAsk - (100 - upBid)
		if d2 < 0 {
			d2 = -d2
		}
	}
	return fmt.Sprintf("book: UP bid/ask=%d/%d DOWN bid/ask=%d/%d age=%dms mirrorΔ=%d/%d",
		upBid, upAsk, downBid, downAsk, ageMs, d1, d2)
}

// signalPriceCentsLocked 根据 signalSource 选择“信号侧价格”。
// - best_*：来自 WS bestBook（盘口跳变）
// - event：来自 PriceChangedEvent.NewPrice（或在收到对侧事件时按互补推导）
func (s *Strategy) signalPriceCentsLocked(now time.Time, signalTok domain.TokenType, e *events.PriceChangedEvent) int {
	// 1) bestBook 路径（盘口跳变）
	if strings.HasPrefix(strings.ToLower(s.SignalSource), "best_") && s.TradingService != nil {
		snap, ok := s.TradingService.BestBookSnapshot()
		if ok {
			p2c := func(p uint16) int {
				if p == 0 {
					return 0
				}
				return (int(p) + 50) / 100
			}
			switch strings.ToLower(s.SignalSource) {
			case "best_bid":
				if signalTok == domain.TokenTypeUp {
					return p2c(snap.YesBidPips)
				}
				return p2c(snap.NoBidPips)
			case "best_ask":
				if signalTok == domain.TokenTypeUp {
					return p2c(snap.YesAskPips)
				}
				return p2c(snap.NoAskPips)
			default: // best_mid
				var bid, ask int
				if signalTok == domain.TokenTypeUp {
					bid = p2c(snap.YesBidPips)
					ask = p2c(snap.YesAskPips)
				} else {
					bid = p2c(snap.NoBidPips)
					ask = p2c(snap.NoAskPips)
				}
				if bid > 0 && ask > 0 {
					return (bid + ask + 1) / 2
				}
				// 单边盘口时：退回 event（避免永远 0）
			}
		}
	}

	// 2) event 路径（含互补推导）
	if e == nil {
		return 0
	}
	c := e.NewPrice.ToCents()
	if c <= 0 || c >= 100 {
		return 0
	}
	if e.TokenType == signalTok {
		return c
	}
	// 收到对侧事件时，用互补推导当前信号侧价格（允许“只盯一边”也能连续更新）
	return 100 - c
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
