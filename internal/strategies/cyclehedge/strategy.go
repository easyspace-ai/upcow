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
	"github.com/sirupsen/logrus"
)

const ID = "cyclehedge"

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy：每个周期（15m market）里锁定 1~5c 的 complete-set 收益，并按余额滚动放大。
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	// loop
	loopOnce  sync.Once
	loopCancel context.CancelFunc
	signalC   chan struct{}
	orderC    chan *domain.Order

	priceMu sync.Mutex
	latest  map[domain.TokenType]*events.PriceChangedEvent

	stateMu sync.Mutex
	marketSlugPrefix string

	// per-cycle state
	currentMarketSlug string
	cycleStartUnix    int64
	targetNotional    float64
	targetProfitCents int
	targetShares      float64

	yesOrderID string
	noOrderID  string

	firstFillAt time.Time
	lastLogAt   time.Time
	lastCancelAt time.Time // 撤单节流：避免高频重复撤单导致状态乱序/刷爆 API

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
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }
func (s *Strategy) Defaults() error { return nil }
func (s *Strategy) Validate() error { return s.Config.Validate() }

func (s *Strategy) Initialize() error {
	if s.signalC == nil {
		s.signalC = make(chan struct{}, 1)
	}
	if s.orderC == nil {
		s.orderC = make(chan *domain.Order, 256)
	}
	if s.latest == nil {
		s.latest = make(map[domain.TokenType]*events.PriceChangedEvent)
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
	tick := time.Duration(s.RequoteMs) * time.Millisecond
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
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil {
		return nil
	}
	if s.TradingService != nil {
		s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)
	}
	// fast path：只合并事件
	s.priceMu.Lock()
	s.latest[e.TokenType] = e
	s.priceMu.Unlock()
	common.TrySignal(s.signalC)
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
	for {
		select {
		case <-loopCtx.Done():
			return
		case <-s.signalC:
			s.step(loopCtx, time.Now())
		case <-tickC:
			s.step(loopCtx, time.Now())
		}
	}
}

func (s *Strategy) step(ctx context.Context, now time.Time) {
	if s.TradingService == nil {
		return
	}

	// 合并行情事件（取最新的 market）
	s.priceMu.Lock()
	evUp := s.latest[domain.TokenTypeUp]
	evDown := s.latest[domain.TokenTypeDown]
	s.latest = make(map[domain.TokenType]*events.PriceChangedEvent)
	s.priceMu.Unlock()

	var m *domain.Market
	if evUp != nil && evUp.Market != nil {
		m = evUp.Market
	}
	if m == nil && evDown != nil && evDown.Market != nil {
		m = evDown.Market
	}
	if m == nil {
		// 仍然消费订单更新，避免堆积
		s.drainOrders()
		return
	}

	// 市场过滤
	if !strings.HasPrefix(strings.ToLower(m.Slug), s.marketSlugPrefix) {
		s.drainOrders()
		return
	}

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
		// 先撤掉未成交挂单，降低被动成交扩大规模的概率（节流撤单，避免 API 风暴）。
		s.cancelMarketOrdersThrottled(ctx, now, m, true)
	}

	// 计算剩余时间（秒）。用于尾盘收敛/动态参数。
	remainingSeconds := s.remainingSeconds(now, m)

	// 盘口质量 gate（避免 stale/wide spread）
	if s.EnableMarketQualityGate != nil && *s.EnableMarketQualityGate {
		orderCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		mq, err := s.TradingService.GetMarketQuality(orderCtx, m, &services.MarketQualityOptions{
			MaxBookAge:     time.Duration(s.MarketQualityMaxBookAgeMs) * time.Millisecond,
			MaxSpreadPips:  s.MarketQualityMaxSpreadCents * 100,
			PreferWS:       true,
			FallbackToREST: true,
			AllowPartialWS: true,
		})
		cancel()
		if err != nil || mq == nil || mq.Score < s.MarketQualityMinScore {
			return
		}
	}

	// 读取 top-of-book
	orderCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(orderCtx, m)
	cancel()
	if err != nil {
		return
	}
	yesBidC, yesAskC := yesBid.ToCents(), yesAsk.ToCents()
	noBidC, noAskC := noBid.ToCents(), noAsk.ToCents()
	if yesBidC <= 0 || yesAskC <= 0 || noBidC <= 0 || noAskC <= 0 {
		return
	}

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
			s.cancelMarketOrdersThrottled(ctx, now, m, false)
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

		// 超时/临近结算：执行“补齐或回平”
		if age >= time.Duration(timeoutSec)*time.Second || inCloseout {
			// prefer: taker 补齐（只要不亏/仍有最小利润）
			if s.AllowTakerComplete {
				// 尾盘更严格：补齐后仍需保留的最小利润随时间提高（避免尾盘追单锁亏）。
				minProfit := s.dynamicMinProfitAfterCompleteCents(remainingSeconds)
				if yesAskC+noAskC <= 100-minProfit {
					need := unhedged
					need = s.clampOrderSize(need)
					if need < s.MinUnhedgedShares {
						return
					}
					missingTok := domain.TokenTypeUp
					missingAsset := m.YesAssetID
					missingAsk := yesAsk
					if upShares > downShares {
						// need buy NO
						missingTok = domain.TokenTypeDown
						missingAsset = m.NoAssetID
						missingAsk = noAsk
					}
					takerCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
					_, _ = s.TradingService.PlaceOrder(takerCtx, &domain.Order{
						MarketSlug: m.Slug,
						AssetID:    missingAsset,
						TokenType:  missingTok,
						Side:       types.SideBuy,
						Price:      missingAsk,
						Size:       need,
						OrderType:  types.OrderTypeFAK,
					})
					cancel()
					s.stateMu.Lock()
					s.stats.TakerCompletes++
					s.stateMu.Unlock()
					s.maybeLog(now, m, fmt.Sprintf("unhedged->taker_complete: need=%.2f missing=%s ask=%dc minProfit=%dc", need, missingTok, missingAsk.ToCents(), minProfit))
					return
				}
			}

			// fallback: 回平裸露（卖出多出来的一腿）
			if s.AllowFlatten {
				excessTok := domain.TokenTypeUp
				excessAsset := m.YesAssetID
				excessBid := yesBid
				if upShares > downShares {
					// excess is UP, ok
				} else {
					excessTok = domain.TokenTypeDown
					excessAsset = m.NoAssetID
					excessBid = noBid
				}
				size := s.clampOrderSize(unhedged)
				if size < s.MinUnhedgedShares {
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
				s.maybeLog(now, m, fmt.Sprintf("unhedged->flatten: sell=%.2f token=%s bid=%dc", unhedged, excessTok, excessBid.ToCents()))
				return
			}
		}
	}

	// 3) 正常建仓：动态选择 profitCents（收益 vs 成交概率）
	chosenProfit, chYesBidC, chNoBidC := s.chooseDynamicProfit(yesBidC, yesAskC, noBidC, noAskC, remainingSeconds)
	if chosenProfit == 0 {
		// 当前盘口没法用 maker 锁 1~5c：先不做（等待更好时机）
		return
	}

	// 4) 计算目标 shares：notional / (1 - profit)
	// 成本 = 100 - profit (cents) => costPerShare = (100-profit)/100
	s.stateMu.Lock()
	tn := s.targetNotional
	s.stateMu.Unlock()
	if tn <= 0 {
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

	// ⚠️ 关键修复：确保同时下两腿，避免只下一腿导致裸露风险
	// 核心原则：cyclehedge 策略必须同时下两腿，确保两腿同时成交，避免裸露风险
	// 
	// 如果已经部分成交且有裸露，只允许补齐到对侧，不再扩大总规模
	if unhedged >= s.MinUnhedgedShares {
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
		s.cancelMarketOrdersThrottled(ctx, now, m, false)
		s.yesOrderID, s.noOrderID = "", ""
	}

	// 下 YES
	needUpOK := needUp >= s.MinUnhedgedShares
	needDownOK := needDown >= s.MinUnhedgedShares
	if needUpOK {
		needUp = s.clampOrderSize(needUp)
		needUpOK = needUp >= s.MinUnhedgedShares
	}
	if needDownOK {
		needDown = s.clampOrderSize(needDown)
		needDownOK = needDown >= s.MinUnhedgedShares
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

	// 方向偏好：当需要同时下两腿时，优先下“价格更高且超过阈值”的那一腿，
	// 目的是在短时间裸露时尽量站在胜率更高的一侧。
	if needUpOK && needDownOK {
		if prefer, ok := s.preferHighPriceFirstToken(yesBidC, noBidC); ok {
			if prefer == domain.TokenTypeUp {
				placeYes()
				placeNo()
			} else {
				placeNo()
				placeYes()
			}
		} else {
			placeYes()
			placeNo()
		}
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
	s.cycleStartUnix = m.Timestamp
	s.targetNotional = 0
	s.targetProfitCents = 0
	s.targetShares = 0
	s.yesOrderID, s.noOrderID = "", ""
	s.firstFillAt = time.Time{}
	s.lastLogAt = time.Time{}
	s.lastCancelAt = time.Time{}

	// reset stats for new cycle
	s.stats = cycleStats{
		MarketSlug: m.Slug,
		CycleStartUnix: m.Timestamp,
		TargetNotionalUSDC: 0,
		TargetShares: 0,
		ProfitChoice: make(map[int]int64),
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
func (s *Strategy) cancelMarketOrdersThrottled(ctx context.Context, now time.Time, m *domain.Market, isCloseout bool) {
	if s == nil || s.TradingService == nil || m == nil || m.Slug == "" {
		return
	}
	const minInterval = 2 * time.Second
	s.stateMu.Lock()
	last := s.lastCancelAt
	if !last.IsZero() && now.Sub(last) < minInterval {
		s.stateMu.Unlock()
		return
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
		return
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
	return time.Until(end) <= time.Duration(s.EntryCutoffSeconds)*time.Second
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
func (s *Strategy) chooseDynamicProfit(yesBidC, yesAskC, noBidC, noAskC int, remainingSeconds int) (chosenProfit, chosenYesBidC, chosenNoBidC int) {
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
	for p := s.ProfitMinCents; p <= s.ProfitMaxCents; p++ {
		yb, nb, ok := chooseMakerBids(yesBidC, yesAskC, noBidC, noAskC, p)
		if !ok {
			continue
		}
		// 离盘口距离：越远越难成交
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
	return bestProfit, bestYes, bestNo
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

