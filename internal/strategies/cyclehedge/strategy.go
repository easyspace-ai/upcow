package cyclehedge

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
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
	"github.com/betbot/gobet/pkg/marketmath"
	"github.com/sirupsen/logrus"
)

const ID = "cyclehedge"

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// priceSnapshot 价格状态快照（原子更新，避免事件丢失）
type priceSnapshot struct {
	UpPrice   *events.PriceChangedEvent
	DownPrice *events.PriceChangedEvent
	Market    *domain.Market
	UpdatedAt time.Time
}

// Strategy：每个周期（15m market）里锁定 1~5c 的 complete-set 收益，并按余额滚动放大。
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	// loop
	loopOnce  sync.Once
	loopCancel context.CancelFunc
	signalC   chan struct{}  // 可选：用于重要变化触发（但主要依赖 tick）
	orderC    chan *domain.Order

	// 价格状态快照（状态快照模式：OnPriceChanged 直接更新，step 读取）
	priceMu sync.RWMutex
	priceSnapshot priceSnapshot

	stateMu sync.Mutex
	marketSlugPrefix string

	// per-cycle state
	currentMarketSlug string
	currentMarket     *domain.Market // 保存完整的 market 对象（参考 updownthreshold 策略）
	cycleStartUnix    int64
	targetNotional    float64
	targetProfitCents int
	targetShares      float64

	yesOrderID string
	noOrderID  string

	firstFillAt time.Time
	lastLogAt   time.Time
	lastCancelAt time.Time // 撤单节流：避免高频重复撤单导致状态乱序/刷爆 API
	lastQuoteAt  time.Time // 报价节流：用于“动态 requote”，避免固定 tick 下每次都重算/撤挂
	closeoutActive bool     // 进入 closeout 窗口后置 true（每周期一次），用于避免重复撤单把补齐挂单撤掉
	lastSupplementAt time.Time // 补齐追价/撤改单节流：避免裸露时 cancel+replace 过频

	// cycle stats (for reporting)
	stats cycleStats

	autoMerge common.AutoMergeController
}

type cycleStats struct {
	MarketSlug string
	CycleStartUnix int64
	CycleEndUnix   int64

	TargetNotionalUSDC float64
	TargetShares       float64

	Quotes int64
	OrdersPlacedYes int64
	OrdersPlacedNo  int64
	Cancels         int64

	TakerCompletes  int64
	Flattens        int64
	CloseoutCancels int64
	MaxSingleSideStops int64

	ProfitChoice map[int]int64 // profitCents -> count
	LastChosenProfit int

	// 成本计算监控
	CostCalculations int64        // 成本计算次数
	CostCalculationErrors int64   // 成本计算错误次数（无法获取成本）
	CostBasisUsed int64           // 使用 CostBasis 的次数
	CostAvgPriceUsed int64        // 使用 AvgPrice 的次数
	CostEntryPriceUsed int64      // 使用 EntryPrice 的次数
	CostSizeMismatches int64      // Size 与 TotalFilledSize 不匹配的次数
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }
func (s *Strategy) Defaults() error { return nil }
func (s *Strategy) Validate() error { return s.Config.Validate() }

func (s *Strategy) Initialize() error {
	if s.signalC == nil {
		s.signalC = make(chan struct{}, 10) // 小缓冲即可，主要用于可选触发（主要依赖 tick）
	}
	if s.orderC == nil {
		s.orderC = make(chan *domain.Order, 256)
	}
	if s.stats.ProfitChoice == nil {
		s.stats.ProfitChoice = make(map[int]int64)
	}

	// 只处理当前 market 前缀，避免误交易
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
	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	session.OnOrderUpdate(s)
	log.Infof("✅ [%s] 已订阅 price/order 事件 (session=%s)", ID, session.Name)
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	// 使用“更短的基础 tick”，在 step 内用 lastQuoteAt 做动态节流（尾盘可加速）。
	// 这样不需要重启 loop/ticker 就能实现随剩余时间变化的 requote 频率。
	tick := time.Duration(s.baseLoopTickMs()) * time.Millisecond
	common.StartLoopOnce(ctx, &s.loopOnce, func(cancel context.CancelFunc) { s.loopCancel = cancel }, tick, s.loop)
	<-ctx.Done()
	return ctx.Err()
}

func (s *Strategy) OnCycle(ctx context.Context, oldMarket *domain.Market, newMarket *domain.Market) {
	if newMarket == nil {
		return
	}
	// 周期结束：先落盘旧周期报表
	if oldMarket != nil {
		s.finalizeAndReport(ctx, oldMarket)
	}
	// 用周期回调快速重置
	now := time.Now()
	s.resetCycle(ctx, now, newMarket)
	
	// 保存完整的 market 对象（参考 updownthreshold 策略的设计）
	s.stateMu.Lock()
	if newMarket != nil {
		cp := *newMarket
		s.currentMarket = &cp
	}
	s.stateMu.Unlock()
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil {
		return nil
	}
	if s.TradingService != nil {
		s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)
	}
	
	// 打印价格更新事件
	priceCents := e.NewPrice.ToCents()
	log.Infof("📈 [%s] 价格更新: market=%s token=%s price=%dc (%.4f) oldPrice=%v", 
		ID, e.Market.Slug, e.TokenType, priceCents, e.NewPrice.ToDecimal(), e.OldPrice)
	
	// 状态快照模式：直接更新状态快照（原子操作）
	s.priceMu.Lock()
	if e.TokenType == domain.TokenTypeUp {
		s.priceSnapshot.UpPrice = e
	} else if e.TokenType == domain.TokenTypeDown {
		s.priceSnapshot.DownPrice = e
	}
	// 更新 market（取最新的）
	if s.priceSnapshot.Market == nil || s.priceSnapshot.Market.Slug != e.Market.Slug {
		cp := *e.Market
		s.priceSnapshot.Market = &cp
	}
	s.priceSnapshot.UpdatedAt = time.Now()
	s.priceMu.Unlock()
	
	// 同时更新 currentMarket（用于兼容性）
	s.stateMu.Lock()
	if s.currentMarket == nil || s.currentMarket.Slug != e.Market.Slug {
		cp := *e.Market
		s.currentMarket = &cp
	}
	s.stateMu.Unlock()
	
	// 可选：发送信号（但主要依赖 tick，信号丢失也无所谓）
	select {
	case s.signalC <- struct{}{}:
	default:
		// 信号丢失也无所谓，tick 会保底执行
	}
	return nil
}

func (s *Strategy) OnOrderUpdate(_ context.Context, order *domain.Order) error {
	if order == nil {
		return nil
	}
	select {
	case s.orderC <- order:
	default:
	}
	common.TrySignal(s.signalC)
	return nil
}

func (s *Strategy) loop(loopCtx context.Context, tickC <-chan time.Time) {
	log.Infof("🔍 [%s] loop 函数启动 (signalC=%v)", ID, s.signalC != nil)
	for {
		select {
		case <-loopCtx.Done():
			log.Infof("🔍 [%s] loop: context done，退出", ID)
			return
		case <-s.signalC:
			log.Infof("🔍 [%s] loop: 收到 signalC 信号，调用 step", ID)
			s.step(loopCtx, time.Now())
		case <-tickC:
			log.Infof("🔍 [%s] loop: 收到 tick 信号，调用 step", ID)
			s.step(loopCtx, time.Now())
		}
	}
}

func (s *Strategy) step(ctx context.Context, now time.Time) {
	log.Infof("🔍 [%s] step 函数被调用", ID)
	
	if s.TradingService == nil {
		log.Infof("🔍 [%s] step: TradingService is nil，返回", ID)
		return
	}

	// 状态快照模式：读取状态快照（原子操作，不丢失数据）
	s.priceMu.RLock()
	snapshot := s.priceSnapshot  // 复制快照
	s.priceMu.RUnlock()

	log.Infof("🔍 [%s] step: 读取价格快照 evUp=%v evDown=%v market=%v", 
		ID, snapshot.UpPrice != nil, snapshot.DownPrice != nil, snapshot.Market != nil)

	// 使用快照中的 market
	var m *domain.Market
	if snapshot.Market != nil {
		// 复制一份，避免竞态
		cp := *snapshot.Market
		m = &cp
		log.Infof("🔍 [%s] step: 使用快照中的 market=%s", ID, m.Slug)
		
		// 同步更新 currentMarket（用于兼容性）
		s.stateMu.Lock()
		if s.currentMarket == nil || s.currentMarket.Slug != m.Slug {
			s.currentMarket = &cp
		}
		s.stateMu.Unlock()
	}
	
	// 如果快照中没有 market，使用保存的 currentMarket 作为 fallback
	if m == nil {
		s.stateMu.Lock()
		if s.currentMarket != nil {
			cp := *s.currentMarket
			m = &cp
			log.Infof("🔍 [%s] step: 使用保存的 currentMarket=%s (fallback)", ID, m.Slug)
		}
		s.stateMu.Unlock()
		
		if m == nil {
			// 完全没有市场信息，返回
			log.Infof("🔍 [%s] step: no market from snapshot and no saved market，返回", ID)
			s.drainOrders()
			return
		}
	}
	
	// 注意：快照中的价格事件（snapshot.UpPrice, snapshot.DownPrice）已保存，
	// 如果需要使用可以在后续逻辑中通过 snapshot 访问

	// 市场过滤
	if !strings.HasPrefix(strings.ToLower(m.Slug), s.marketSlugPrefix) {
		log.Infof("🔍 [%s] step: market slug mismatch: slug=%s prefix=%s，返回", ID, m.Slug, s.marketSlugPrefix)
		s.drainOrders()
		return
	}
	
	log.Infof("🔍 [%s] step: market=%s 继续执行", ID, m.Slug)

	// 周期检测：优先使用 market.Timestamp（从 slug 解析的 period start）
	if m.Timestamp > 0 {
		s.stateMu.Lock()
		needReset := s.cycleStartUnix == 0 || s.cycleStartUnix != m.Timestamp || s.currentMarketSlug != m.Slug
		s.stateMu.Unlock()
		if needReset {
			s.resetCycle(ctx, now, m)
		}
	}

	// closeout window：最后 EntryCutoffSeconds 秒不再“新增建仓/挂单”，但仍允许补齐/回平裸露。
	// 目的：符合“尾盘时间价值变化更快”的现实，避免继续扩张风险；同时避免“停手=裸奔”导致结算风险。
	inCloseout := s.EntryCutoffSeconds > 0 && s.withinEntryCutoff(m)
	if inCloseout {
		// closeout 只做一次“撤单清场”：避免后续补齐挂单也被重复撤掉，导致永远补不齐只能追 taker。
		needCancel := false
		s.stateMu.Lock()
		if !s.closeoutActive {
			s.closeoutActive = true
			needCancel = true
		}
		s.stateMu.Unlock()
		if needCancel {
			_ = s.cancelMarketOrdersThrottled(ctx, now, m, true)
		}
	} else {
		// 离开 closeout（理论上不会发生在同一周期，但为了健壮性兜底）
		s.stateMu.Lock()
		s.closeoutActive = false
		s.stateMu.Unlock()
	}

	// 计算剩余时间（秒）。用于尾盘收敛/动态参数。
	remainingSeconds := s.remainingSeconds(now, m)

	// 盘口质量 + 有效价：统一从 MarketQuality 获取（可供补齐/风控复用）。
	var mq *services.MarketQuality
	{
		// 动态调整盘口质量要求：尾盘放宽标准
		minScore := s.MarketQualityMinScore
		maxSpreadCents := s.MarketQualityMaxSpreadCents
		
		// 尾盘动态调整：结算前 3 分钟放宽标准
		if remainingSeconds > 0 && remainingSeconds <= 180 {
			// 降低最低分数要求（最多降低 10 分）
			if minScore > 60 {
				minScore = minScore - 10
			} else {
				minScore = 60
			}
			// 放宽价差限制（增加 1-2 cents）
			if maxSpreadCents < 10 {
				maxSpreadCents = maxSpreadCents + 2
			}
		} else if remainingSeconds > 0 && remainingSeconds <= 300 {
			// 结算前 5 分钟适度放宽
			if minScore > 65 {
				minScore = minScore - 5
			}
			if maxSpreadCents < 8 {
				maxSpreadCents = maxSpreadCents + 1
			}
		}
		
		log.Infof("🔍 [%s] 调用 GetMarketQuality: market=%s rem=%ds minScore=%d maxSpread=%dc", 
			ID, m.Slug, remainingSeconds, minScore, maxSpreadCents)
		orderCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		got, err := s.TradingService.GetMarketQuality(orderCtx, m, &services.MarketQualityOptions{
			MaxBookAge:     time.Duration(s.MarketQualityMaxBookAgeMs) * time.Millisecond,
			MaxSpreadPips:  maxSpreadCents * 100,
			PreferWS:       true,
			FallbackToREST: true,
			AllowPartialWS: true,
		})
		cancel()
		if err != nil {
			log.Infof("🔍 [%s] GetMarketQuality 错误: market=%s err=%v", ID, m.Slug, err)
		}
		if err == nil && got != nil {
			mq = got
			log.Infof("🔍 [%s] GetMarketQuality 成功: market=%s score=%d rem=%ds", 
				ID, m.Slug, mq.Score, remainingSeconds)
		} else {
			log.Infof("🔍 [%s] GetMarketQuality 返回 nil: market=%s err=%v got=%v", 
				ID, m.Slug, err, got != nil)
		}
		// 质量 gate（避免 stale/wide spread/脏镜像）
		if s.EnableMarketQualityGate != nil && *s.EnableMarketQualityGate {
			if mq == nil {
				log.Infof("🔍 [%s] 盘口质量检查失败: market=%s mq=nil rem=%ds", ID, m.Slug, remainingSeconds)
				return
			}
			if mq.Score < minScore {
				log.Infof("🔍 [%s] 盘口质量检查失败: market=%s score=%d < minScore=%d rem=%ds", 
					ID, m.Slug, mq.Score, minScore, remainingSeconds)
				return
			}
			log.Infof("🔍 [%s] 盘口质量检查通过: market=%s score=%d >= minScore=%d", 
				ID, m.Slug, mq.Score, minScore)
		}
	}

	// 读取 top-of-book
	log.Infof("🔍 [%s] 调用 GetTopOfBook: market=%s", ID, m.Slug)
	orderCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(orderCtx, m)
	cancel()
	if err != nil {
		log.Infof("🔍 [%s] GetTopOfBook 错误: market=%s err=%v", ID, m.Slug, err)
		return
	}
	yesBidC, yesAskC := yesBid.ToCents(), yesAsk.ToCents()
	noBidC, noAskC := noBid.ToCents(), noAsk.ToCents()
	log.Infof("🔍 [%s] GetTopOfBook 成功: market=%s UP(bid=%dc ask=%dc) DOWN(bid=%dc ask=%dc) src=%s", 
		ID, m.Slug, yesBidC, yesAskC, noBidC, noAskC, source)
	if yesBidC <= 0 || yesAskC <= 0 || noBidC <= 0 || noAskC <= 0 {
		log.Infof("🔍 [%s] 盘口数据无效: market=%s UP(bid=%dc ask=%dc) DOWN(bid=%dc ask=%dc)", 
			ID, m.Slug, yesBidC, yesAskC, noBidC, noAskC)
		return
	}

	// 计算有效价格（考虑 Polymarket 订单簿的镜像特性）
	// 核心等价关系：Buy YES @ P ≡ Sell NO @ (1-P)
	// 有效买入价格 = min(直接买入价格, 镜像价格)
	topOfBook := marketmath.TopOfBook{
		YesBidPips: yesBidC * 100,  // cents -> pips (1 cent = 100 pips)
		YesAskPips: yesAskC * 100,
		NoBidPips:  noBidC * 100,
		NoAskPips:  noAskC * 100,
	}
	effectivePrices, err := marketmath.GetEffectivePrices(topOfBook)
	if err != nil {
		log.Warnf("⚠️ [%s] 计算有效价格失败: market=%s err=%v", ID, m.Slug, err)
		return
	}
	
	// 转换为 cents（pips -> cents）
	effectiveBuyYesC := effectivePrices.EffectiveBuyYesPips / 100
	effectiveBuyNoC := effectivePrices.EffectiveBuyNoPips / 100
	
	// 打印实时盘口报价（包含有效价格）
	log.Infof("📊 [%s] 实时盘口: market=%s UP(bid=%dc ask=%dc spread=%dc effBuy=%dc) DOWN(bid=%dc ask=%dc spread=%dc effBuy=%dc) rem=%ds src=%s",
		ID, m.Slug, yesBidC, yesAskC, yesAskC-yesBidC, effectiveBuyYesC, noBidC, noAskC, noAskC-noBidC, effectiveBuyNoC, remainingSeconds, source)

	// 读取当前持仓（shares）
	upShares, downShares := s.currentShares(m.Slug)
	minShares := math.Min(upShares, downShares)
	maxShares := math.Max(upShares, downShares)
	unhedged := maxShares - minShares

	// closeout 窗口：如果没有裸露，就停止本周期新增（只持有到结算）。
	// 注意：若有裸露，则继续走下方“补齐/回平”逻辑（其中也会优先在 closeout 时触发）。
	if inCloseout && unhedged < s.MinUnhedgedShares {
		return
	}

	// 每周期最大单向持仓：到阈值则不再扩大规模（只允许补齐/回平）。
	if s.MaxSingleSideShares > 0 && maxShares >= s.MaxSingleSideShares {
		// 若没有裸露，撤掉挂单，避免继续被动成交扩大规模
		if unhedged < s.MinUnhedgedShares {
			_ = s.cancelMarketOrdersThrottled(ctx, now, m, false)
		}
		s.stateMu.Lock()
		s.stats.MaxSingleSideStops++
		s.stateMu.Unlock()
		s.maybeLog(now, m, fmt.Sprintf("maxSingleSideShares reached: up=%.2f down=%.2f limit=%.2f", upShares, downShares, s.MaxSingleSideShares))
		// 若没有裸露风险：直接停止本周期新增挂单/加仓（只持有到结算）
		if unhedged < s.MinUnhedgedShares {
			return
		}
		// 若仍有裸露：继续让下方“超时补齐/回平”逻辑处理风险
	}

	// 1) 已达到目标：撤单，持有到结算
	s.stateMu.Lock()
	targetShares := s.targetShares
	profitTarget := s.targetProfitCents
	firstFillAt := s.firstFillAt
	s.stateMu.Unlock()

	if targetShares > 0 && minShares >= targetShares {
		s.cancelMarketOrdersThrottled(ctx, now, m, false)
		s.maybeLog(now, m, fmt.Sprintf("locked: profit=%dc targetShares=%.2f got(up=%.2f down=%.2f) src=%s", profitTarget, targetShares, upShares, downShares, source))
		return
	}

	// 2) 单腿裸露：先尝试 maker 补齐；超时则 taker 补齐或回平
	if unhedged >= s.MinUnhedgedShares {
		if firstFillAt.IsZero() {
			s.stateMu.Lock()
			if s.firstFillAt.IsZero() {
				s.firstFillAt = now
			}
			firstFillAt = s.firstFillAt
			s.stateMu.Unlock()
		}
		age := now.Sub(firstFillAt)
		// 尾盘更快：裸露超时随剩余时间收紧（更激进，但更符合尾部波动变快的现实）。
		timeoutSec := s.dynamicUnhedgedTimeoutSeconds(remainingSeconds)

		// 风险预算：裸露超过预算时，不等待 timeout，直接升级到更激进的补齐/回平路径。
		force := false
		if budget := s.dynamicUnhedgedBudgetShares(remainingSeconds); budget > 0 && unhedged >= budget {
			force = true
		}

		// 裸露时先止血：撤掉“多出来那一腿”的挂单，避免继续被动成交把裸露放大。
		// 仅撤 excess leg，不影响 missing leg 的补齐挂单。
		{
			excessTok := domain.TokenTypeUp
			excessOrderID := s.yesOrderID
			if upShares > downShares {
				// excess is UP
			} else {
				excessTok = domain.TokenTypeDown
				excessOrderID = s.noOrderID
			}
			if excessOrderID != "" {
				minIntv := time.Duration(s.dynamicSupplementMinIntervalMs(remainingSeconds)) * time.Millisecond
				s.stateMu.Lock()
				last := s.lastSupplementAt
				allow := last.IsZero() || now.Sub(last) >= minIntv
				if allow {
					s.lastSupplementAt = now
				}
				s.stateMu.Unlock()
				if allow {
					cancelCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
					_ = s.TradingService.CancelOrder(cancelCtx, excessOrderID)
					cancel()
					s.maybeLog(now, m, fmt.Sprintf("unhedged: cancel excess leg order to cap risk: token=%s orderID=%s", excessTok, excessOrderID))
					// 不清本地 orderID：等待 OrderEngine 回流终态，避免 canceling 窗口内堆叠
				}
			}
		}

		// maker 补齐（全程优先，而不只是 closeout）：
		// - 阶段 A（age < window）：缺腿 bestBid 挂单补齐
		// - 阶段 B（window <= age < timeout）：更激进（bid + bump），但仍保持 maker（< ask）
		// - 阶段 C（age >= timeout 或 force 或 closeout & 小裸露）：进入 taker/flatten 兜底
		if s.EnableMakerSupplement && !force && age < time.Duration(timeoutSec)*time.Second && unhedged >= math.Max(s.MinUnhedgedShares, s.MakerSupplementMinShares) {
			windowSec := s.dynamicMakerSupplementWindowSeconds(remainingSeconds, timeoutSec)
			bumpC := 0
			if windowSec > 0 && age >= time.Duration(windowSec)*time.Second {
				bumpC = s.dynamicMakerSupplementBumpCents(remainingSeconds)
			}

			missingTok := domain.TokenTypeUp
			missingAsset := m.YesAssetID
			missingBidC := yesBidC
			missingAskC := yesAskC
			if upShares > downShares {
				missingTok = domain.TokenTypeDown
				missingAsset = m.NoAssetID
				missingBidC = noBidC
				missingAskC = noAskC
			}

			// bump 不能跨价：限定在当前 spread 内（保证还是 maker）
			spreadC := missingAskC - missingBidC
			if spreadC < 0 {
				spreadC = -spreadC
			}
			bumpCap := spreadC - 1
			if bumpCap < 0 {
				bumpCap = 0
			}
			// 若接近预算阈值或尾盘，则在 cap 内尽量更积极一点
			if remainingSeconds > 0 && remainingSeconds <= 180 {
				if bumpC < 2 {
					bumpC = 2
				}
			}
			budget := s.dynamicUnhedgedBudgetShares(remainingSeconds)
			if budget > 0 && unhedged >= budget*0.8 {
				if bumpC < 1 {
					bumpC = 1
				}
			}
			if bumpC > bumpCap {
				bumpC = bumpCap
			}

			priceC := missingBidC + bumpC
			// 更激进但仍保持 maker：尾盘/接近超时/接近预算时，允许直接贴到 ask-1
			if s.EnableMakerSupplementSnapToAskMinusOne && missingAskC > 1 {
				if s.shouldSnapMakerSupplementToAskMinusOne(remainingSeconds, age, timeoutSec, unhedged, budget) {
					priceC = missingAskC - 1
				}
			}
			priceC = clampMakerPriceCents(priceC, missingAskC)
			if priceC > 0 && missingBidC > 0 && missingAskC > 0 {
				// 如果已有缺腿挂单：支持追价（cancel & replace），避免卡在旧 bid 上补不齐。
				var missingOrderID string
				if missingTok == domain.TokenTypeUp {
					missingOrderID = s.yesOrderID
				} else {
					missingOrderID = s.noOrderID
				}
				if missingOrderID != "" {
					if ord, ok := s.TradingService.GetOrder(missingOrderID); ok && ord != nil {
						if ord.IsFinalStatus() {
							// 终态：清理本地记录，允许下面重新挂单
							if missingTok == domain.TokenTypeUp {
								s.yesOrderID = ""
							} else {
								s.noOrderID = ""
							}
						} else if ord.Status == domain.OrderStatusCanceling {
							return
						} else {
							curC := ord.Price.ToCents()
							if curC == priceC {
								return
							}
							minIntv := time.Duration(s.dynamicSupplementMinIntervalMs(remainingSeconds)) * time.Millisecond
							s.stateMu.Lock()
							last := s.lastSupplementAt
							allow := last.IsZero() || now.Sub(last) >= minIntv
							if allow {
								s.lastSupplementAt = now
							}
							s.stateMu.Unlock()
							if allow {
								cancelCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
								_ = s.TradingService.CancelOrder(cancelCtx, missingOrderID)
								cancel()
								s.maybeLog(now, m, fmt.Sprintf("maker_supplement reprice: token=%s %dc->%dc (bid=%dc ask=%dc bump=%dc) orderID=%s",
									missingTok, curC, priceC, missingBidC, missingAskC, bumpC, missingOrderID))
							}
							// 不在同一 tick 里立刻下新单：等待 cancel 回流，避免短时间内双挂
							return
						}
					} else {
						// 查不到：保守清理，允许重新挂单
						if missingTok == domain.TokenTypeUp {
							s.yesOrderID = ""
						} else {
							s.noOrderID = ""
						}
					}
				}

				size := s.clampOrderSize(unhedged)
				if size >= s.MinUnhedgedShares {
					// 节流：避免 cancel->place 或连续 place 过密
					minIntv := time.Duration(s.dynamicSupplementMinIntervalMs(remainingSeconds)) * time.Millisecond
					s.stateMu.Lock()
					last := s.lastSupplementAt
					allow := last.IsZero() || now.Sub(last) >= minIntv
					if allow {
						s.lastSupplementAt = now
					}
					s.stateMu.Unlock()
					if !allow {
						return
					}

					placeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
					ord, err := s.TradingService.PlaceOrder(placeCtx, &domain.Order{
						MarketSlug: m.Slug,
						AssetID:    missingAsset,
						TokenType:  missingTok,
						Side:       types.SideBuy,
						Price:      domain.Price{Pips: priceC * 100}, // 1c = 100 pips
						Size:       size,
						OrderType:  types.OrderTypeGTC,
					})
					cancel()
					if err == nil && ord != nil && ord.OrderID != "" {
						if missingTok == domain.TokenTypeUp {
							s.yesOrderID = ord.OrderID
							s.stateMu.Lock()
							s.stats.OrdersPlacedYes++
							s.stateMu.Unlock()
						} else {
							s.noOrderID = ord.OrderID
							s.stateMu.Lock()
							s.stats.OrdersPlacedNo++
							s.stateMu.Unlock()
						}
						s.maybeLog(now, m, fmt.Sprintf("unhedged->maker_supplement: missing=%s price=%dc (bid=%dc ask=%dc bump=%dc) size=%.2f age=%s rem=%ds",
							missingTok, priceC, missingBidC, missingAskC, bumpC, size, age.Truncate(time.Millisecond), remainingSeconds))
						return
					}
				}
			}
		}

		// 超时/临近结算：执行“补齐或回平”
		if force || age >= time.Duration(timeoutSec)*time.Second || inCloseout {
			if force {
				// 预算触发时先清理挂单，避免继续被动成交扩大裸露
				_ = s.cancelMarketOrdersThrottled(ctx, now, m, false)
			}
			// 风控兜底动作选择：根据“现在补齐 vs 现在回平”的确定性 PnL 估算，选更优的那条路。
			// - 正常情况下：补齐需要满足 minProfitAfterComplete 门槛
			// - force(预算触发) 时：允许补齐略微负收益，只要比 flatten 更划算（且能立刻消除方向风险）
			minProfit := s.dynamicMinProfitAfterCompleteCents(remainingSeconds)
			size := s.clampOrderSize(unhedged)
			if size < s.MinUnhedgedShares {
				return
			}

			// 当前两腿的平均成本（cents/share）
			upAvgC, downAvgC := s.currentAvgCostCents(m.Slug)

			missingTok := domain.TokenTypeUp
			missingAsset := m.YesAssetID
			missingAsk := yesAsk
			missingAskC := yesAskC
			excessTok := domain.TokenTypeUp
			excessAsset := m.YesAssetID
			excessBid := yesBid
			excessBidC := yesBidC
			excessAvgC := upAvgC
			if upShares > downShares {
				// excess is UP (default), missing is DOWN
				missingTok = domain.TokenTypeDown
				missingAsset = m.NoAssetID
				missingAsk = noAsk
				missingAskC = noAskC
				excessTok = domain.TokenTypeUp
				excessAsset = m.YesAssetID
				excessBid = yesBid
				excessBidC = yesBidC
				excessAvgC = upAvgC
			} else {
				// excess is DOWN, missing is UP
				missingTok = domain.TokenTypeUp
				missingAsset = m.YesAssetID
				missingAsk = yesAsk
				missingAskC = yesAskC
				excessTok = domain.TokenTypeDown
				excessAsset = m.NoAssetID
				excessBid = noBid
				excessBidC = noBidC
				excessAvgC = downAvgC
			}

			// 估算（以 unhedged 这部分为对象）：
			// - complete: 买入 missingAsk，结算得到 $1/份；与 excessAvg 组成一套的锁利（确定性）
			// - flatten: 立即卖出 excessBid，结束裸露（确定性）
			completeProfitPerSetC := 100 - excessAvgC - missingAskC
			completeProfitC := float64(completeProfitPerSetC) * size
			flattenProfitC := float64(excessBidC-excessAvgC) * size

			// 是否允许 complete（不 force 时要满足最小利润门槛；force 时只要比 flatten 更优即可）
			allowComplete := s.AllowTakerComplete && (completeProfitPerSetC >= minProfit || (force && completeProfitC >= flattenProfitC))
			allowFlatten := s.AllowFlatten

			// 选更优动作
			doComplete := false
			if allowComplete && allowFlatten {
				doComplete = completeProfitC >= flattenProfitC
			} else if allowComplete {
				doComplete = true
			} else if allowFlatten {
				doComplete = false
			} else {
				return
			}

			if doComplete {
				takerCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
				_, _ = s.TradingService.PlaceOrder(takerCtx, &domain.Order{
					MarketSlug: m.Slug,
					AssetID:    missingAsset,
					TokenType:  missingTok,
					Side:       types.SideBuy,
					Price:      missingAsk,
					Size:       size,
					OrderType:  types.OrderTypeFAK,
				})
				cancel()
				s.stateMu.Lock()
				s.stats.TakerCompletes++
				s.stateMu.Unlock()
				s.maybeLog(now, m, fmt.Sprintf("unhedged->taker_complete(best): need=%.2f missing=%s ask=%dc excessAvg=%dc minProfit=%dc estComplete=%dc estFlatten=%dc",
					size, missingTok, missingAskC, excessAvgC, minProfit, int(completeProfitC+0.5), int(flattenProfitC+0.5)))
				return
			}

			flattenCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			_, _ = s.TradingService.PlaceOrder(flattenCtx, &domain.Order{
				MarketSlug: m.Slug,
				AssetID:    excessAsset,
				TokenType:  excessTok,
				Side:       types.SideSell,
				Price:      excessBid,
				Size:       size,
				OrderType:  types.OrderTypeFAK,
			})
			cancel()
			s.stateMu.Lock()
			s.stats.Flattens++
			s.stateMu.Unlock()
			s.maybeLog(now, m, fmt.Sprintf("unhedged->flatten(best): sell=%.2f token=%s bid=%dc excessAvg=%dc estFlatten=%dc estComplete=%dc",
				size, excessTok, excessBidC, excessAvgC, int(flattenProfitC+0.5), int(completeProfitC+0.5)))
			return
		}
	}

	// 动态 requote：在 closeout 外，按剩余时间加速报价刷新；但不影响上面的“补齐/回平”风险路径。
	if !inCloseout {
		requoteMs := s.dynamicRequoteMs(remainingSeconds)
		if requoteMs > 0 {
			s.stateMu.Lock()
			lastQ := s.lastQuoteAt
			timeSinceLastQuote := now.Sub(lastQ)
			s.stateMu.Unlock()
			if !lastQ.IsZero() && timeSinceLastQuote < time.Duration(requoteMs)*time.Millisecond {
				log.Debugf("🔍 [%s] requote节流: market=%s timeSinceLastQuote=%v < requoteMs=%dms", 
					ID, m.Slug, timeSinceLastQuote, requoteMs)
				return
			}
			s.stateMu.Lock()
			s.lastQuoteAt = now
			s.stateMu.Unlock()
		}
	}

	// 3) 正常建仓：动态选择 profitCents（收益 vs 成交概率）
	// 通过挂 maker 订单（bid 价格）来获取利润，而不是基于有效价格判断
	log.Infof("🔍 [%s] 调用 chooseDynamicProfit: market=%s UP(bid=%dc ask=%dc) DOWN(bid=%dc ask=%dc) rem=%ds", 
		ID, m.Slug, yesBidC, yesAskC, noBidC, noAskC, remainingSeconds)
	chosenProfit, chYesBidC, chNoBidC := s.chooseDynamicProfit(yesBidC, yesAskC, noBidC, noAskC, effectiveBuyYesC, effectiveBuyNoC, remainingSeconds)
	if chosenProfit == 0 {
		// 当前盘口没法用 maker 锁 1~5c：先不做（等待更好时机）
		log.Infof("🔍 [%s] chooseDynamicProfit 返回 0: market=%s UP(bid=%dc ask=%dc) DOWN(bid=%dc ask=%dc) rem=%ds", 
			ID, m.Slug, yesBidC, yesAskC, noBidC, noAskC, remainingSeconds)
		return
	}
	log.Infof("🔍 [%s] chooseDynamicProfit 成功: market=%s profit=%dc UP(bid=%dc) DOWN(bid=%dc)", 
		ID, m.Slug, chosenProfit, chYesBidC, chNoBidC)

	// 4) 计算目标 shares：notional / (1 - profit)
	// 成本 = 100 - profit (cents) => costPerShare = (100-profit)/100
	s.stateMu.Lock()
	tn := s.targetNotional
	s.stateMu.Unlock()
	log.Infof("🔍 [%s] targetNotional 检查: market=%s tn=%.2f", ID, m.Slug, tn)
	if tn <= 0 {
		log.Infof("🔍 [%s] targetNotional <= 0: market=%s tn=%.2f", ID, m.Slug, tn)
		return
	}
	costCents := 100 - chosenProfit
	if costCents <= 0 {
		return
	}
	shares := tn * 100.0 / float64(costCents)
	if shares <= 0 || math.IsInf(shares, 0) || math.IsNaN(shares) {
		return
	}

	// 5) 计算剩余需要挂的 shares
	needUp := math.Max(0, shares-upShares)
	needDown := math.Max(0, shares-downShares)
	log.Debugf("🔍 [%s] 计算需要挂单: market=%s shares=%.2f needUp=%.2f needDown=%.2f", 
		ID, m.Slug, shares, needUp, needDown)

	// ⚠️ 关键修复：确保同时下两腿，避免只下一腿导致裸露风险
	// 核心原则：cyclehedge 策略必须同时下两腿，确保两腿同时成交，避免裸露风险
	// 
	// 如果已经部分成交且有裸露，只允许补齐到对侧，不再扩大总规模
	if unhedged >= s.MinUnhedgedShares {
		log.Debugf("🔍 [%s] 已有裸露: market=%s unhedged=%.2f >= minUnhedged=%.2f", 
			ID, m.Slug, unhedged, s.MinUnhedgedShares)
		// 当已有裸露时，只允许补齐到对侧，不再扩大总规模
		if upShares > downShares {
			needUp = 0
		} else if downShares > upShares {
			needDown = 0
		}
	} else {
		// ⚠️ 关键修复：如果没有裸露，必须确保同时下两腿
		// 即使一腿已经达到目标（need == 0），也应该同时下两腿，确保两腿同时成交
		// 这样可以避免只下一腿导致裸露风险
		// 
		// 修复逻辑：如果只有一腿需要下单（need > 0），但另一腿已经达到目标（need == 0），
		// 应该强制另一腿也下单（即使 need == 0），确保两腿同时成交
		if needUp > 0 && needDown == 0 {
			// UP 需要下单，DOWN 已经达到目标（downShares >= shares）
			// ⚠️ 修复：强制 DOWN 也下单，确保两腿同时成交
			// 即使 DOWN 已经达到目标，也应该同时下两腿，避免只下 UP 导致裸露
			// 设置一个最小单量，确保两腿同时成交
			needDown = math.Max(s.MinUnhedgedShares, shares*0.1) // 至少下目标 shares 的 10% 或最小单量
		} else if needDown > 0 && needUp == 0 {
			// DOWN 需要下单，UP 已经达到目标（upShares >= shares）
			// ⚠️ 修复：强制 UP 也下单，确保两腿同时成交
			// 即使 UP 已经达到目标，也应该同时下两腿，避免只下 DOWN 导致裸露
			// 设置一个最小单量，确保两腿同时成交
			needUp = math.Max(s.MinUnhedgedShares, shares*0.1) // 至少下目标 shares 的 10% 或最小单量
		}
		// 如果两腿都需要下单（needUp > 0 && needDown > 0），这是正常的，同时下两腿
	}

	// 6) 下两腿 GTC（maker）：价格用 cents 构造
	yesPrice := domain.Price{Pips: chYesBidC * 100}
	noPrice := domain.Price{Pips: chNoBidC * 100}

	// 记录本轮目标（用于日志/持仓达到后停止）
	s.stateMu.Lock()
	s.targetShares = shares
	s.targetProfitCents = chosenProfit
	s.stats.LastChosenProfit = chosenProfit
	if s.stats.ProfitChoice == nil {
		s.stats.ProfitChoice = make(map[int]int64)
	}
	s.stats.ProfitChoice[chosenProfit]++
	s.stats.TargetShares = shares
	s.stateMu.Unlock()

	// 如果本次将要下单，先撤掉旧的挂单（避免多单堆叠）
	// 注：TradingService 层有 in-flight 去重，且 CancelOrdersForMarket 会撤掉本周期挂单（含对侧）。
	if (needUp >= s.MinUnhedgedShares || needDown >= s.MinUnhedgedShares) && (s.yesOrderID != "" || s.noOrderID != "") {
		// 只有真的执行了撤单（未被节流）才清理本地 orderID，避免节流窗口内“忘记旧单”导致堆叠挂单。
		if s.cancelMarketOrdersThrottled(ctx, now, m, false) {
			s.yesOrderID, s.noOrderID = "", ""
		}
	}

	// 下 YES
	needUpOK := needUp >= s.MinUnhedgedShares
	needDownOK := needDown >= s.MinUnhedgedShares
	log.Debugf("🔍 [%s] 订单大小检查前: market=%s needUp=%.2f needDown=%.2f needUpOK=%v needDownOK=%v minUnhedged=%.2f", 
		ID, m.Slug, needUp, needDown, needUpOK, needDownOK, s.MinUnhedgedShares)
	if needUpOK {
		needUp = s.clampOrderSize(needUp)
		needUpOK = needUp >= s.MinUnhedgedShares
		log.Debugf("🔍 [%s] clampOrderSize UP: market=%s needUp=%.2f needUpOK=%v", 
			ID, m.Slug, needUp, needUpOK)
	}
	if needDownOK {
		needDown = s.clampOrderSize(needDown)
		needDownOK = needDown >= s.MinUnhedgedShares
		log.Debugf("🔍 [%s] clampOrderSize DOWN: market=%s needDown=%.2f needDownOK=%v", 
			ID, m.Slug, needDown, needDownOK)
	}
	if !needUpOK && !needDownOK {
		log.Debugf("🔍 [%s] 订单大小不足: market=%s needUp=%.2f needDown=%.2f minUnhedged=%.2f", 
			ID, m.Slug, needUp, needDown, s.MinUnhedgedShares)
	}

	placeYes := func() {
		placeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ord, err := s.TradingService.PlaceOrder(placeCtx, &domain.Order{
			MarketSlug: m.Slug,
			AssetID:    m.YesAssetID,
			TokenType:  domain.TokenTypeUp,
			Side:       types.SideBuy,
			Price:      yesPrice,
			Size:       needUp,
			OrderType:  types.OrderTypeGTC,
		})
		cancel()
		if err == nil && ord != nil {
			s.yesOrderID = ord.OrderID
			s.stateMu.Lock()
			s.stats.OrdersPlacedYes++
			s.stateMu.Unlock()
		}
	}
	placeNo := func() {
		placeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ord, err := s.TradingService.PlaceOrder(placeCtx, &domain.Order{
			MarketSlug: m.Slug,
			AssetID:    m.NoAssetID,
			TokenType:  domain.TokenTypeDown,
			Side:       types.SideBuy,
			Price:      noPrice,
			Size:       needDown,
			OrderType:  types.OrderTypeGTC,
		})
		cancel()
		if err == nil && ord != nil {
			s.noOrderID = ord.OrderID
			s.stateMu.Lock()
			s.stats.OrdersPlacedNo++
			s.stateMu.Unlock()
		}
	}

	// 小幅并行：当需要同时下两腿时并发下单，降低“先成交一腿、另一腿来不及挂出”的时间窗。
	// 风险约束仍由上面的 MaxSingleSideShares + 下方的 unhedged 超时补齐/回平兜底。
	if needUpOK && needDownOK {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); placeYes() }()
		go func() { defer wg.Done(); placeNo() }()
		wg.Wait()
	} else if needUpOK {
		placeYes()
	} else if needDownOK {
		placeNo()
	}

	if needUp >= s.MinUnhedgedShares || needDown >= s.MinUnhedgedShares {
		s.stateMu.Lock()
		s.stats.Quotes++
		s.stateMu.Unlock()
		s.maybeLog(now, m, fmt.Sprintf("quote: profit=%dc cost=%dc tn=%.2f shares=%.2f need(up=%.2f down=%.2f) bids(yes=%dc no=%dc) book(yes %d/%d no %d/%d) src=%s",
			chosenProfit, costCents, tn, shares, needUp, needDown, chYesBidC, chNoBidC, yesBidC, yesAskC, noBidC, noAskC, source))
	}
}

func clampMakerPriceCents(priceC, askC int) int {
	// maker buy 需要 price < ask；无法满足时返回 0 让上层走兜底路径
	if priceC <= 0 || askC <= 0 {
		return 0
	}
	if priceC >= askC {
		priceC = askC - 1
	}
	if priceC <= 0 {
		return 0
	}
	return priceC
}

func (s *Strategy) dynamicMakerSupplementWindowSeconds(remainingSeconds, timeoutSec int) int {
	// window 必须小于 timeout，且尾盘更短（更快升级）
	w := s.MakerSupplementWindowSeconds
	if w <= 0 {
		w = 3
	}
	if remainingSeconds > 0 {
		if remainingSeconds <= 180 {
			w = 1
		} else if remainingSeconds <= 300 && w > 2 {
			w = 2
		}
	}
	if timeoutSec <= 1 {
		return 0
	}
	if w >= timeoutSec {
		w = timeoutSec - 1
	}
	if w <= 0 {
		w = 1
	}
	return w
}

func (s *Strategy) dynamicMakerSupplementBumpCents(remainingSeconds int) int {
	b := s.MakerSupplementBumpCents
	if b < 0 {
		b = 0
	}
	// 尾盘更激进一些（仍会被 <ask 约束）
	if remainingSeconds > 0 {
		if remainingSeconds <= 180 {
			if b < 2 {
				b = 2
			}
		}
	}
	return b
}

func (s *Strategy) dynamicSupplementMinIntervalMs(remainingSeconds int) int {
	// 裸露补齐追价的节流：比 requote 更保守一些，避免 cancel+place 过于频繁。
	ms := 700
	if remainingSeconds > 0 {
		if remainingSeconds <= 180 {
			ms = 250
		} else if remainingSeconds <= 300 {
			ms = 400
		}
	}
	minMs := s.baseLoopTickMs()
	if ms < minMs {
		ms = minMs
	}
	return ms
}

func (s *Strategy) dynamicUnhedgedBudgetShares(remainingSeconds int) float64 {
	// 裸露预算：越接近结算越小（更快强制去风险）。
	// - budget=0 表示关闭（保持兼容）
	b := s.MaxUnhedgedSharesBudget
	if b <= 0 {
		return 0
	}
	f := 1.0
	if remainingSeconds > 0 {
		if remainingSeconds <= 180 {
			f = 0.25
		} else if remainingSeconds <= 300 {
			f = 0.5
		}
	}
	b = b * f
	if b < s.MinUnhedgedShares {
		b = s.MinUnhedgedShares
	}
	return b
}

func (s *Strategy) shouldSnapMakerSupplementToAskMinusOne(remainingSeconds int, age time.Duration, timeoutSec int, unhedged float64, budget float64) bool {
	// 目标：在“必须尽快补齐但又不想吃单”的情况下，把 maker 补齐挂到最激进的 ask-1。
	// 触发条件（任一满足即可）：
	// - closeout（<=180s）
	// - 距离超时很近（剩余 < 1s）
	// - 接近预算上限（>= 90%）
	if remainingSeconds > 0 && remainingSeconds <= 180 {
		return true
	}
	if timeoutSec > 0 {
		remain := time.Duration(timeoutSec)*time.Second - age
		if remain <= 1*time.Second {
			return true
		}
	}
	if budget > 0 && unhedged >= budget*0.9 {
		return true
	}
	return false
}

func (s *Strategy) clampOrderSize(size float64) float64 {
	if s == nil {
		return size
	}
	limit := s.MaxOrderSizeShares
	if limit > 0 && size > limit {
		return limit
	}
	return size
}

func (s *Strategy) preferHighPriceFirstToken(yesBidC, noBidC int) (domain.TokenType, bool) {
	if s == nil {
		return "", false
	}
	th := s.PreferHighPriceThresholdCents
	if th <= 0 {
		return "", false
	}
	// 只在“一边明显高于阈值”时启用，避免两边都>=阈值时产生随机偏好
	yesHigh := yesBidC >= th
	noHigh := noBidC >= th
	if yesHigh && !noHigh {
		return domain.TokenTypeUp, true
	}
	if noHigh && !yesHigh {
		return domain.TokenTypeDown, true
	}
	return "", false
}

func (s *Strategy) resetCycle(ctx context.Context, now time.Time, m *domain.Market) {
	s.stateMu.Lock()
	s.currentMarketSlug = m.Slug
	// 保存完整的 market 对象（参考 updownthreshold 策略的设计）
	if m != nil {
		cp := *m
		s.currentMarket = &cp
	} else {
		s.currentMarket = nil
	}
	s.cycleStartUnix = m.Timestamp
	s.targetNotional = 0
	s.targetProfitCents = 0
	s.targetShares = 0
	s.yesOrderID, s.noOrderID = "", ""
	s.firstFillAt = time.Time{}
	s.lastLogAt = time.Time{}
	s.lastCancelAt = time.Time{}
	s.lastQuoteAt = time.Time{}
	s.closeoutActive = false
	s.lastSupplementAt = time.Time{}

	// reset stats for new cycle
	s.stats = cycleStats{
		MarketSlug: m.Slug,
		CycleStartUnix: m.Timestamp,
		TargetNotionalUSDC: 0,
		TargetShares: 0,
		ProfitChoice: make(map[int]int64),
		CostCalculations: 0,
		CostCalculationErrors: 0,
		CostBasisUsed: 0,
		CostAvgPriceUsed: 0,
		CostEntryPriceUsed: 0,
		CostSizeMismatches: 0,
	}
	s.stateMu.Unlock()

	// 周期切换先撤掉本周期旧挂单（保险）
	s.cancelMarketOrdersThrottled(ctx, now, m, false)

	// 刷新余额（用短超时；失败则回退到本地余额）
	bal := 0.0
	{
		refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_ = s.TradingService.RefreshBalance(refreshCtx)
		cancel()
		if b, ok := s.TradingService.GetBalanceUSDC(); ok {
			bal = b
		}
	}

	// 目标 notional：固定 or 按余额滚动
	tn := 0.0
	if s.FixedNotionalUSDC > 0 {
		tn = s.FixedNotionalUSDC
		// 安全护栏：固定 notional 不应超过可用余额（否则必然单边成交/资金锁死）
		alloc := s.BalanceAllocationPct
		if alloc <= 0 || alloc > 1 {
			alloc = 1
		}
		if bal > 0 {
			cap := bal * alloc
			if cap > 0 && tn > cap {
				tn = cap
			}
		}
	} else {
		tn = math.Max(s.MinNotionalUSDC, bal*s.BalanceAllocationPct)
		if tn > s.MaxNotionalUSDC {
			tn = s.MaxNotionalUSDC
		}
		if tn < s.MinNotionalUSDC {
			tn = s.MinNotionalUSDC
		}
	}

	s.stateMu.Lock()
	s.targetNotional = tn
	s.stats.TargetNotionalUSDC = tn
	s.stateMu.Unlock()

	log.Infof("🔄 [%s] 周期重置: market=%s start=%d balance=%.2f targetNotional=%.2f profitRange=[%d,%d]c",
		ID, m.Slug, m.Timestamp, bal, tn, s.ProfitMinCents, s.ProfitMaxCents)
}

// cancelMarketOrdersThrottled 撤单节流：避免在 closeout/锁定阶段每个 tick 都撤一次，造成 API 风暴与状态回退。
func (s *Strategy) cancelMarketOrdersThrottled(ctx context.Context, now time.Time, m *domain.Market, isCloseout bool) bool {
	if s == nil || s.TradingService == nil || m == nil || m.Slug == "" {
		return false
	}
	const minInterval = 2 * time.Second
	s.stateMu.Lock()
	last := s.lastCancelAt
	if !last.IsZero() && now.Sub(last) < minInterval {
		s.stateMu.Unlock()
		return false
	}
	s.lastCancelAt = now
	s.stateMu.Unlock()

	// 只有确实存在本 market 的活跃单才撤（避免无意义 cancel + 400）
	hasActive := false
	for _, o := range s.TradingService.GetActiveOrders() {
		if o != nil && o.MarketSlug == m.Slug {
			hasActive = true
			break
		}
	}
	if !hasActive {
		return false
	}

	cancelCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	s.TradingService.CancelOrdersForMarket(cancelCtx, m.Slug)
	cancel()

	s.stateMu.Lock()
	if isCloseout {
		s.stats.CloseoutCancels++
	} else {
		s.stats.Cancels++
	}
	s.stateMu.Unlock()

	if isCloseout {
		s.maybeLog(now, m, "closeout: cancel & pause entries")
	}
	return true
}

func (s *Strategy) drainOrders() {
	for {
		select {
		case <-s.orderC:
			// no-op: 目前主要依赖 positions/active orders 的本地状态
		default:
			return
		}
	}
}

func (s *Strategy) currentShares(marketSlug string) (up float64, down float64) {
	positions := s.TradingService.GetOpenPositionsForMarket(marketSlug)
	for _, p := range positions {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}
		switch p.TokenType {
		case domain.TokenTypeUp:
			up += p.Size
		case domain.TokenTypeDown:
			down += p.Size
		}
	}
	return up, down
}

// currentAvgCostCents 返回当前两腿的“平均成本（cents/share）”。
// - 优先使用 Position.CostBasis/TotalFilledSize
// - fallback: AvgPrice 或 EntryPrice
// 说明：该均价用于风控兜底时比较“补齐 vs 回平”的确定性损益，不要求绝对精确但要稳定、保守。
func (s *Strategy) currentAvgCostCents(marketSlug string) (upAvgC int, downAvgC int) {
	if s == nil || s.TradingService == nil || marketSlug == "" {
		return 0, 0
	}
	positions := s.TradingService.GetOpenPositionsForMarket(marketSlug)

	upSize, downSize := 0.0, 0.0
	upCost, downCost := 0.0, 0.0

	// 统计信息
	var costBasisCount, avgPriceCount, entryPriceCount, errorCount, sizeMismatchCount int64

	for _, p := range positions {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}

		// 估算该 position 的成本
		size := p.Size
		cost := 0.0
		
		if p.TotalFilledSize > 0 && p.CostBasis > 0 {
			// 成本基础更可靠
			// 注意：TotalFilledSize 可能与 Size 不完全一致（部分平仓/合并等），这里用比例缩放到当前 Size
			if math.Abs(size-p.TotalFilledSize) > 0.01 {
				sizeMismatchCount++
			}
			cost = p.CostBasis * (size / p.TotalFilledSize)
			costBasisCount++
		} else if p.AvgPrice > 0 {
			cost = p.AvgPrice * size
			avgPriceCount++
		} else if p.EntryPrice.Pips > 0 {
			cost = p.EntryPrice.ToDecimal() * size
			entryPriceCount++
		} else {
			errorCount++
			continue
		}

		switch p.TokenType {
		case domain.TokenTypeUp:
			upSize += size
			upCost += cost
		case domain.TokenTypeDown:
			downSize += size
			downCost += cost
		}
	}

	// 更新统计信息
	s.stateMu.Lock()
	s.stats.CostCalculations++
	s.stats.CostCalculationErrors += errorCount
	s.stats.CostBasisUsed += costBasisCount
	s.stats.CostAvgPriceUsed += avgPriceCount
	s.stats.CostEntryPriceUsed += entryPriceCount
	s.stats.CostSizeMismatches += sizeMismatchCount
	s.stateMu.Unlock()

	// 计算平均成本
	if upSize > 0 && upCost > 0 {
		upAvgC = int(upCost/upSize*100 + 0.5)
	}
	if downSize > 0 && downCost > 0 {
		downAvgC = int(downCost/downSize*100 + 0.5)
	}

	// 记录详细日志（仅在成本计算异常或首次计算时）
	if errorCount > 0 || sizeMismatchCount > 0 || (upSize > 0 && upAvgC == 0) || (downSize > 0 && downAvgC == 0) {
		log.Warnf("⚠️ [%s] 成本计算详情: market=%s up(size=%.2f cost=%.2f avg=%dc) down(size=%.2f cost=%.2f avg=%dc) errors=%d mismatches=%d sources(CostBasis=%d AvgPrice=%d EntryPrice=%d)",
			ID, marketSlug, upSize, upCost, upAvgC, downSize, downCost, downAvgC, errorCount, sizeMismatchCount, costBasisCount, avgPriceCount, entryPriceCount)
	}

	return upAvgC, downAvgC
}

func (s *Strategy) maybeLog(now time.Time, m *domain.Market, msg string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.lastLogAt.IsZero() || now.Sub(s.lastLogAt) >= 2*time.Second {
		s.lastLogAt = now
		log.Infof("📌 [%s] %s | market=%s", ID, msg, m.Slug)
	}
}

// chooseMakerBids 选择一组 maker 买价（cents），使得：
// - yesBid <= yesAsk-1
// - noBid  <= noAsk-1
// - yesBid + noBid == 100 - profitCents
// 同时尽量贴近 bestBid（提高成交概率）。
func chooseMakerBids(yesBidC, yesAskC, noBidC, noAskC, profitCents int) (chosenYesBidC, chosenNoBidC int, ok bool) {
	if profitCents <= 0 || profitCents >= 50 {
		return 0, 0, false
	}
	targetSum := 100 - profitCents

	// yesBid 的可行区间：
	// 1) maker: yesBid <= yesAsk-1
	// 2) maker: noBid = targetSum - yesBid <= noAsk-1  => yesBid >= targetSum-(noAsk-1)
	// 3) 正价: yesBid >= 1 且 noBid >= 1 => yesBid <= targetSum-1
	lb := 1
	if v := targetSum - (noAskC - 1); v > lb {
		lb = v
	}
	ub := yesAskC - 1
	if ub > targetSum-1 {
		ub = targetSum - 1
	}
	// 贴近盘口：至少不低于 bestBid（否则太远几乎不成交）
	if yesBidC > lb {
		lb = yesBidC
	}
	if lb > ub {
		return 0, 0, false
	}

	// 首选：yes 贴着 bestBid（或上浮 0），让 no 自动互补
	candYes := lb
	candNo := targetSum - candYes
	if candNo < 1 {
		return 0, 0, false
	}
	// no 也要“别太离谱”：至少不低于 bestBid
	if candNo < noBidC {
		// 为提高 noBid，需要降低 yesBid
		needYes := targetSum - noBidC
		if needYes < lb {
			needYes = lb
		}
		if needYes > ub {
			return 0, 0, false
		}
		candYes = needYes
		candNo = targetSum - candYes
	}
	// maker 校验
	if candYes >= yesAskC || candNo >= noAskC {
		return 0, 0, false
	}
	return candYes, candNo, true
}

func (s *Strategy) withinEntryCutoff(m *domain.Market) bool {
	if s == nil || m == nil || s.EntryCutoffSeconds <= 0 || m.Timestamp <= 0 {
		return false
	}
	dur := time.Duration(s.CycleDurationSeconds) * time.Second
	if dur <= 0 {
		dur = 15 * time.Minute
	}
	end := time.Unix(m.Timestamp, 0).Add(dur)
	remaining := time.Until(end)
	
	// 边界情况处理：
	// 1. 如果周期已结束（remaining <= 0），返回 true（进入 closeout）
	// 2. 如果剩余时间 <= EntryCutoffSeconds，返回 true
	// 3. 如果 EntryCutoffSeconds 大于周期时长，则整个周期都在 closeout（异常情况，记录警告）
	if remaining <= 0 {
		return true
	}
	if s.EntryCutoffSeconds >= s.CycleDurationSeconds {
		log.Warnf("⚠️ [%s] EntryCutoffSeconds(%d) >= CycleDurationSeconds(%d)，整个周期都在 closeout 窗口",
			ID, s.EntryCutoffSeconds, s.CycleDurationSeconds)
		return true
	}
	return remaining <= time.Duration(s.EntryCutoffSeconds)*time.Second
}

func (s *Strategy) remainingSeconds(now time.Time, m *domain.Market) int {
	if s == nil || m == nil || m.Timestamp <= 0 {
		return 0
	}
	durSec := s.CycleDurationSeconds
	if durSec <= 0 {
		durSec = 15 * 60
	}
	elapsed := int(now.Unix() - m.Timestamp)
	if elapsed < 0 {
		elapsed = 0
	}
	rem := durSec - elapsed
	if rem < 0 {
		rem = 0
	}
	return rem
}

func (s *Strategy) baseLoopTickMs() int {
	// 目标：给动态 requote 留出余地，但避免 loop 过于频繁。
	// - 默认每 200ms tick 一次；若用户配置更快，则尊重用户配置。
	ms := s.RequoteMs
	if ms <= 0 {
		ms = 800
	}
	if ms < 200 {
		return ms
	}
	return 200
}

func (s *Strategy) dynamicRequoteMs(remainingSeconds int) int {
	// 基于用户配置的 RequoteMs 做“尾盘加速”。
	ms := s.RequoteMs
	if ms <= 0 {
		ms = 800
	}
	// 尾盘：加速（但下限不小于 baseLoopTick）
	minMs := s.baseLoopTickMs()
	if remainingSeconds > 0 {
		if remainingSeconds <= 180 {
			ms = minMs
		} else if remainingSeconds <= 300 {
			ms = ms / 2
			if ms < minMs {
				ms = minMs
			}
		}
	}
	return ms
}

func (s *Strategy) dynamicUnhedgedTimeoutSeconds(remainingSeconds int) int {
	// 默认：沿用配置；尾盘收紧（更快补齐/回平）。
	timeout := s.UnhedgedTimeoutSeconds
	if timeout <= 0 {
		timeout = 10
	}
	// closeout（用户需求：最后 3 分钟停止新增）窗口内，裸露风险最敏感：更快触发补齐/回平。
	if remainingSeconds > 0 && s.EntryCutoffSeconds > 0 && remainingSeconds <= s.EntryCutoffSeconds {
		if timeout > 2 {
			timeout = 2
		}
		return timeout
	}
	// 结算前 5 分钟开始收紧
	if remainingSeconds > 0 && remainingSeconds <= 300 {
		if timeout > 5 {
			timeout = 5
		}
	}
	return timeout
}

func (s *Strategy) dynamicMinProfitAfterCompleteCents(remainingSeconds int) int {
	// 默认：沿用配置；尾盘更保守一些，避免追单锁亏。
	minProfit := s.MinProfitAfterCompleteCents
	if minProfit < 0 {
		minProfit = 0
	}
	if remainingSeconds > 0 && s.EntryCutoffSeconds > 0 && remainingSeconds <= s.EntryCutoffSeconds {
		// closeout：至少保留 1c（除非用户显式要求更低/更高）
		if minProfit < 1 {
			minProfit = 1
		}
	}
	return minProfit
}

// chooseDynamicProfit 在 profit 区间内根据“收益 vs 成交概率（离盘口距离）”选最优。
// score = profit - (distancePenaltyBps/100)*maxDistanceCents
func (s *Strategy) chooseDynamicProfit(yesBidC, yesAskC, noBidC, noAskC, effectiveBuyYesC, effectiveBuyNoC int, remainingSeconds int) (chosenProfit, chosenYesBidC, chosenNoBidC int) {
	bestScore := -1e9
	bestProfit := 0
	bestYes, bestNo := 0, 0

	penaltyPerCent := float64(s.DistancePenaltyBps) / 100.0
	// 时间敏感：越接近结算，盘口跳变越快、单腿风险越大。
	// 因此尾盘提高“离盘口距离惩罚”，优先选更贴近 bestBid 的报价（提升成交概率，减少挂得太远导致的无效占用）。
	if remainingSeconds > 0 {
		if remainingSeconds <= 180 {
			penaltyPerCent *= 3.0
		} else if remainingSeconds <= 300 {
			penaltyPerCent *= 2.0
		}
	}
	
	// ⚠️ 重要修正：有效价格是市场最优价格，在有效市场中 profit 接近 0。
	// 策略的目标是通过挂 maker 订单（低于 ask 的价格）来获取利润。
	// 因此不需要用有效价格来判断是否有正 profit，而是直接尝试在 profit 范围内选择 maker 订单价格。
	// chooseMakerBids 会检查：yesBid + noBid = 100 - profitCents，并且 yesBid < yesAsk, noBid < noAsk
	// 如果 chooseMakerBids 返回 ok=true，说明可以挂 maker 订单来获得该 profit。
	
	log.Infof("🔍 [%s] chooseDynamicProfit 开始: profitRange=[%d,%d]c UP(bid=%dc ask=%dc) DOWN(bid=%dc ask=%dc)", 
		ID, s.ProfitMinCents, s.ProfitMaxCents, yesBidC, yesAskC, noBidC, noAskC)
	
	triedCount := 0
	for p := s.ProfitMinCents; p <= s.ProfitMaxCents; p++ {
		yb, nb, ok := chooseMakerBids(yesBidC, yesAskC, noBidC, noAskC, p)
		triedCount++
		if !ok {
			log.Infof("🔍 [%s] chooseMakerBids 失败: profit=%dc UP(bid=%dc ask=%dc) DOWN(bid=%dc ask=%dc)", 
				ID, p, yesBidC, yesAskC, noBidC, noAskC)
			continue
		}
		log.Infof("🔍 [%s] chooseMakerBids 成功: profit=%dc UP(bid=%dc->%dc ask=%dc) DOWN(bid=%dc->%dc ask=%dc)", 
			ID, p, yesBidC, yb, yesAskC, noBidC, nb, noAskC)
		// 离当前 best bid 的距离：越远越难成交
		// 使用原始 bid 价格作为参考，因为我们要挂的是 maker 订单（bid 价格）
		dYes := absInt(yesBidC - yb)
		dNo := absInt(noBidC - nb)
		maxD := dYes
		if dNo > maxD {
			maxD = dNo
		}

		score := float64(p)
		if s.EnableDynamicProfit {
			score = float64(p) - penaltyPerCent*float64(maxD)
		}
		if score > bestScore {
			bestScore = score
			bestProfit = p
			bestYes, bestNo = yb, nb
		}
	}
	if bestProfit == 0 {
		log.Infof("🔍 [%s] chooseDynamicProfit 未找到合适profit: 尝试了 %d 个profit值", ID, triedCount)
	}
	return bestProfit, bestYes, bestNo
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

