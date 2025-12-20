package pairlock

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/strategies"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/internal/strategies/orderutil"
	strategyports "github.com/betbot/gobet/internal/strategies/ports"
	"github.com/betbot/gobet/pkg/bbgo"
)

const ID = "pairlock"

var log = logrus.WithField("strategy", ID)

func init() {
	bbgo.RegisterStrategyWithAdapter(ID, &PairLockStrategy{}, &PairLockConfigAdapter{})
}

type tokenKey string

const (
	upKey   tokenKey = "up"
	downKey tokenKey = "down"
)

type priceEvent struct {
	ctx   context.Context
	event *events.PriceChangedEvent
}

type orderUpdate struct {
	ctx   context.Context
	order *domain.Order
}

type cmdKind string

const (
	cmdPlaceYes   cmdKind = "place_yes"
	cmdPlaceNo    cmdKind = "place_no"
	cmdSupplement cmdKind = "supplement"
	cmdCancel     cmdKind = "cancel"
	cmdFlatten    cmdKind = "flatten"
)

type cmdResult struct {
	kind    cmdKind
	planID  string
	order   *domain.Order // template
	created *domain.Order
	err     error
}

// PairLockStrategy 周期内多轮“成对锁定”策略
//
// 核心：
// - 观察 YES/NO 两腿的 bestAsk
// - 当 yesAsk + noAsk <= 100 - ProfitTargetCents 时，买入等量 YES + NO（FAK）
// - 如果一腿成交另一腿没成交，进入补齐逻辑，补齐成功即完成一轮；补齐失败则暂停策略（避免裸露风险）
type PairLockStrategy struct {
	Executor bbgo.CommandExecutor

	config         *PairLockStrategyConfig
	tradingService strategyports.PairLockTradingService

	// 单线程 loop
	loopOnce     sync.Once
	loopCancel   context.CancelFunc
	priceSignalC chan struct{}
	priceMu      sync.Mutex
	latestPrice  map[tokenKey]*priceEvent
	orderC       chan orderUpdate
	cmdResultC   chan cmdResult

	// market / cycle
	currentMarket    *domain.Market
	marketGuard      common.MarketSlugGuard
	roundsThisPeriod int
	attemptCooldown  *common.Debouncer

	// last seen price (用于 slippage cap)
	lastSeenUpCents   int
	lastSeenDownCents int

	// plans：默认串行只会有 0/1 个；开启并行后允许多个
	plans map[string]*pairLockPlan

	// 快速归属：orderID -> planID（用于 order update 快速定位轮次）
	orderIDToPlanID map[string]string

	paused bool

	// 双向持仓累计（用于日志与收益估算）
	upTotalCost   float64
	upHoldings    float64
	downTotalCost float64
	downHoldings  float64

	// 订单去重（防止重复 fill 事件导致重复计数）
	processedFilledOrders   map[string]time.Time
	processedFilledOrdersMu sync.Mutex

	// 订单增量累计：orderID -> 上次已统计的 filledSize
	lastCountedFilled map[string]float64
}

func (s *PairLockStrategy) SetTradingService(ts strategyports.PairLockTradingService) {
	s.tradingService = ts
}

func (s *PairLockStrategy) ID() string   { return ID }
func (s *PairLockStrategy) Name() string { return ID }

func (s *PairLockStrategy) Defaults() error { return nil }

func (s *PairLockStrategy) Validate() error {
	if s.config == nil {
		return fmt.Errorf("策略配置未设置")
	}
	return s.config.Validate()
}

func (s *PairLockStrategy) Initialize(ctx context.Context, conf strategies.StrategyConfig) error {
	cfg, ok := conf.(*PairLockStrategyConfig)
	if !ok {
		return fmt.Errorf("无效的配置类型")
	}
	s.config = cfg
	if err := s.Validate(); err != nil {
		return err
	}
	if s.attemptCooldown == nil {
		s.attemptCooldown = common.NewDebouncer(time.Duration(s.config.CooldownMs) * time.Millisecond)
	} else {
		s.attemptCooldown.SetInterval(time.Duration(s.config.CooldownMs) * time.Millisecond)
		s.attemptCooldown.Reset()
	}

	// init channels/maps
	if s.priceSignalC == nil {
		s.priceSignalC = make(chan struct{}, 1)
	}
	if s.latestPrice == nil {
		s.latestPrice = make(map[tokenKey]*priceEvent)
	}
	if s.orderC == nil {
		s.orderC = make(chan orderUpdate, 4096)
	}
	if s.cmdResultC == nil {
		s.cmdResultC = make(chan cmdResult, 4096)
	}
	if s.processedFilledOrders == nil {
		s.processedFilledOrders = make(map[string]time.Time)
	}
	if s.lastCountedFilled == nil {
		s.lastCountedFilled = make(map[string]float64)
	}
	if s.plans == nil {
		s.plans = make(map[string]*pairLockPlan)
	}
	if s.orderIDToPlanID == nil {
		s.orderIDToPlanID = make(map[string]string)
	}

	log.Infof("pairlock 策略已初始化: orderSize=%.4f, minOrder=%.2f, profitTarget=%dc, maxRounds=%d, cooldown=%dms, maxSupplementAttempts=%d, slippageCap=%dc",
		s.config.OrderSize,
		s.config.MinOrderSize,
		s.config.ProfitTargetCents,
		s.config.MaxRoundsPerPeriod,
		s.config.CooldownMs,
		s.config.MaxSupplementAttempts,
		s.config.EntryMaxBuySlippageCents,
	)
	log.Infof("pairlock 并行配置: enable_parallel=%v, max_concurrent_plans=%d",
		s.config.EnableParallel, s.config.MaxConcurrentPlans)
	if s.config.EnableParallel {
		log.Infof("pairlock 风控: max_total_unhedged_shares=%.4f (保守口径：在途轮次 targetSize 总和)",
			s.config.MaxTotalUnhedgedShares)
	}
	log.Infof("pairlock 失败策略: on_fail_action=%s, max_plan_age_seconds=%d, fail_max_sell_slippage_cents=%d",
		s.config.OnFailAction, s.config.MaxPlanAgeSeconds, s.config.FailMaxSellSlippageCents)
	log.Infof("pairlock flatten 阈值: fail_flatten_min_shares=%.4f", s.config.FailFlattenMinShares)

	return nil
}

// Subscribe 订阅会话事件
func (s *PairLockStrategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	session.OnOrderUpdate(s)
	log.Infof("pairlock 策略已订阅价格与订单事件")
}

func (s *PairLockStrategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	s.startLoop(ctx)
	log.Infof("pairlock 策略已启动")
	return nil
}

func (s *PairLockStrategy) Shutdown(ctx context.Context, wg *sync.WaitGroup) {
	log.Infof("pairlock 策略开始关闭...")
	if s.loopCancel != nil {
		s.loopCancel()
	}
	log.Infof("pairlock 策略关闭完成")
}

// OnPriceChanged 快路径：只入队合并信号
func (s *PairLockStrategy) OnPriceChanged(ctx context.Context, ev *events.PriceChangedEvent) error {
	if ev == nil {
		return nil
	}
	s.startLoop(ctx)

	key := downKey
	if ev.TokenType == domain.TokenTypeUp {
		key = upKey
	}
	s.priceMu.Lock()
	s.latestPrice[key] = &priceEvent{ctx: ctx, event: ev}
	s.priceMu.Unlock()
	common.TrySignal(s.priceSignalC)
	return nil
}

func (s *PairLockStrategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	if order == nil {
		return nil
	}
	select {
	case s.orderC <- orderUpdate{ctx: ctx, order: order}:
	default:
		log.Errorf("pairlock: 内部订单队列已满，丢弃更新: orderID=%s status=%s", order.OrderID, order.Status)
	}
	return nil
}

func (s *PairLockStrategy) onPriceChangedInternal(loopCtx context.Context, ctx context.Context, ev *events.PriceChangedEvent) error {
	if ev == nil || ev.Market == nil || s.config == nil {
		return nil
	}

	// 周期切换：market slug 变化则重置
	if s.marketGuard.Update(ev.Market.Slug) {
		s.resetForNewCycle()
	}
	s.currentMarket = ev.Market

	// 记录最近观测价（用于 slippage cap）
	if ev.TokenType == domain.TokenTypeUp {
		s.lastSeenUpCents = ev.NewPrice.Cents
	} else if ev.TokenType == domain.TokenTypeDown {
		s.lastSeenDownCents = ev.NewPrice.Cents
	}

	if s.paused {
		return nil
	}
	if s.tradingService == nil {
		return nil
	}
	if s.roundsThisPeriod >= s.config.MaxRoundsPerPeriod {
		return nil
	}
	if s.inflightPlans() >= s.maxConcurrentPlans() {
		return nil
	}
	if !s.canStartMorePlans() {
		return nil
	}

	// cooldown
	if s.attemptCooldown != nil {
		ready, _ := s.attemptCooldown.ReadyNow()
		if !ready {
			return nil
		}
	}

	// 并行模式：一次信号允许尽量补满并发额度（但仍受 cooldown 限制）
	for s.inflightPlans() < s.maxConcurrentPlans() && s.roundsThisPeriod < s.config.MaxRoundsPerPeriod {
		if !s.canStartMorePlans() {
			break
		}
		if err := s.tryStartNewPlan(loopCtx); err != nil {
			// 不因为一次失败就中断循环（除非策略被标记 paused）
			break
		}
		// cooldown：避免一次循环内过快连续开轮（interval<=0 则永远 Ready）
		if s.attemptCooldown != nil {
			ready, _ := s.attemptCooldown.ReadyNow()
			if !ready {
				break
			}
		}
	}
	return nil
}

func (s *PairLockStrategy) tryStartNewPlan(ctx context.Context) error {
	market := s.currentMarket
	if market == nil || !market.IsValid() {
		return nil
	}
	if s.Executor == nil {
		// 你们的工程化方向是“交易 IO 走 Executor”，这里直接强约束，避免 loop 阻塞
		return fmt.Errorf("pairlock: Executor 未设置")
	}
	if s.attemptCooldown != nil {
		s.attemptCooldown.MarkNow()
	}

	// quote 两腿 bestAsk（可选 slippage cap）
	yesMax := 0
	noMax := 0
	if s.config.EntryMaxBuySlippageCents > 0 {
		if s.lastSeenUpCents > 0 {
			yesMax = s.lastSeenUpCents + s.config.EntryMaxBuySlippageCents
		}
		if s.lastSeenDownCents > 0 {
			noMax = s.lastSeenDownCents + s.config.EntryMaxBuySlippageCents
		}
	}

	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	yesAsk, err := orderutil.QuoteBuyPrice(orderCtx, s.tradingService, market.YesAssetID, yesMax)
	if err != nil {
		return nil
	}
	noAsk, err := orderutil.QuoteBuyPrice(orderCtx, s.tradingService, market.NoAssetID, noMax)
	if err != nil {
		return nil
	}

	totalCents := yesAsk.Cents + noAsk.Cents
	maxTotal := 100 - s.config.ProfitTargetCents
	if totalCents > maxTotal {
		return nil
	}

	// 计算统一 size：同时满足两腿最小金额
	size := s.calcUnifiedSize(yesAsk, noAsk)
	if size <= 0 {
		return nil
	}
	// 全局未锁定预算（保守口径：新增一个 in-flight 轮次，最坏会增加 size 的未对冲风险）
	if !s.canStartPlanWithSize(size) {
		return nil
	}

	now := time.Now()
	planID := fmt.Sprintf("%s-%d", market.Slug, now.UnixNano())
	yesOrder := orderutil.NewOrder(market.Slug, market.YesAssetID, types.SideBuy, yesAsk, size, domain.TokenTypeUp, true, types.OrderTypeFAK)
	yesOrder.OrderID = fmt.Sprintf("pairlock-yes-%d", now.UnixNano())
	noOrder := orderutil.NewOrder(market.Slug, market.NoAssetID, types.SideBuy, noAsk, size, domain.TokenTypeDown, true, types.OrderTypeFAK)
	noOrder.OrderID = fmt.Sprintf("pairlock-no-%d", now.UnixNano())

	p := &pairLockPlan{
		ID:          planID,
		MarketSlug:  market.Slug,
		CreatedAt:   now,
		TargetSize:  size,
		YesTemplate: yesOrder,
		NoTemplate:  noOrder,
		State:       planSubmitting,
		StateAt:     now,
		OrderIDs: map[string]tokenKey{
			yesOrder.OrderID: upKey,
			noOrder.OrderID:  downKey,
		},
	}
	s.plans[planID] = p
	s.orderIDToPlanID[yesOrder.OrderID] = planID
	s.orderIDToPlanID[noOrder.OrderID] = planID

	log.Infof("🎯 [pairlock] 开始新一轮: rounds=%d/%d, yesAsk=%dc, noAsk=%dc, total=%dc, maxTotal=%dc, size=%.4f",
		s.roundsThisPeriod+1, s.config.MaxRoundsPerPeriod, yesAsk.Cents, noAsk.Cents, totalCents, maxTotal, size)

	// 提交两个下单命令（串行执行，但不阻塞策略 loop）
	if err := s.submitPlaceCmd(planID, cmdPlaceYes, yesOrder); err != nil {
		p.State = planFailed
		p.LastError = err.Error()
		s.paused = true
		return err
	}
	if err := s.submitPlaceCmd(planID, cmdPlaceNo, noOrder); err != nil {
		p.State = planFailed
		p.LastError = err.Error()
		s.paused = true
		return err
	}

	// 认为本轮已“开启”（即已投递到执行器）
	s.roundsThisPeriod++
	p.State = planWaiting
	p.StateAt = time.Now()
	return nil
}

func (s *PairLockStrategy) submitPlaceCmd(planID string, kind cmdKind, order *domain.Order) error {
	ok := s.Executor.Submit(bbgo.Command{
		Name:    fmt.Sprintf("pairlock_%s_%s", kind, planID),
		Timeout: 25 * time.Second,
		Do: func(runCtx context.Context) {
			created, err := s.tradingService.PlaceOrder(runCtx, order)
			select {
			case s.cmdResultC <- cmdResult{kind: kind, planID: planID, order: order, created: created, err: err}:
			default:
			}
		},
	})
	if !ok {
		return fmt.Errorf("执行器队列已满，无法提交下单命令")
	}
	return nil
}

func (s *PairLockStrategy) onCmdResultInternal(ctx context.Context, res cmdResult) error {
	p := s.plans[res.planID]
	if p == nil {
		return nil
	}
	if res.err != nil {
		s.failPlan(ctx, p, fmt.Sprintf("cmd_failed:%s", res.kind), res.err)
		return nil
	}
	if res.created == nil {
		return nil
	}
	// 记录真实订单ID（服务器返回）
	switch res.kind {
	case cmdPlaceYes:
		p.YesCreatedID = res.created.OrderID
		if p.OrderIDs != nil {
			p.OrderIDs[res.created.OrderID] = upKey
		}
		s.orderIDToPlanID[res.created.OrderID] = p.ID
		// 防止“order update 先到、cmd result 后到”导致本轮漏记
		if s.lastCountedFilled != nil && p.OrderIDs != nil {
			if already := s.lastCountedFilled[res.created.OrderID]; already > 0 {
				p.YesFilled += already
			}
		}
	case cmdPlaceNo:
		p.NoCreatedID = res.created.OrderID
		if p.OrderIDs != nil {
			p.OrderIDs[res.created.OrderID] = downKey
		}
		s.orderIDToPlanID[res.created.OrderID] = p.ID
		if s.lastCountedFilled != nil && p.OrderIDs != nil {
			if already := s.lastCountedFilled[res.created.OrderID]; already > 0 {
				p.NoFilled += already
			}
		}
	case cmdSupplement:
		// 补齐单：也纳入本轮关联集合（靠 created orderID）
		if p.OrderIDs != nil {
			// template.TokenType up/down 可直接映射
			if res.order != nil {
				if res.order.TokenType == domain.TokenTypeUp {
					p.OrderIDs[res.created.OrderID] = upKey
				} else if res.order.TokenType == domain.TokenTypeDown {
					p.OrderIDs[res.created.OrderID] = downKey
				}
			}
		}
		s.orderIDToPlanID[res.created.OrderID] = p.ID
		if s.lastCountedFilled != nil && res.order != nil {
			if already := s.lastCountedFilled[res.created.OrderID]; already > 0 {
				if res.order.TokenType == domain.TokenTypeUp {
					p.YesFilled += already
				} else if res.order.TokenType == domain.TokenTypeDown {
					p.NoFilled += already
				}
			}
		}
	}
	return nil
}

func (s *PairLockStrategy) onOrderUpdateInternal(loopCtx context.Context, ctx context.Context, order *domain.Order) error {
	if order == nil || s.currentMarket == nil {
		return nil
	}
	// 只处理当前 market 的两种 asset
	if order.AssetID != s.currentMarket.YesAssetID && order.AssetID != s.currentMarket.NoAssetID {
		return nil
	}

	// 去重：对 filled 事件做强去重，避免重复记账
	if order.Status == domain.OrderStatusFilled && order.FilledAt != nil {
		if s.isFilledDuplicate(order.OrderID, *order.FilledAt) {
			return nil
		}
	}

	// 统一：按 orderID 做“增量累计”，避免 partial/重复 update 造成重复记账
	executed := order.FilledSize
	if executed <= 0 && (order.Status == domain.OrderStatusFilled || order.Status == domain.OrderStatusPartial) {
		executed = order.Size
	}
	if executed < 0 {
		executed = 0
	}
	prev := 0.0
	if s.lastCountedFilled != nil {
		prev = s.lastCountedFilled[order.OrderID]
	}
	delta := executed - prev
	if delta > 0 {
		// 先更新全局持仓/成本（这反映了本 market 内的累计持仓）
		if order.AssetID == s.currentMarket.YesAssetID {
			s.upHoldings += delta
			s.upTotalCost += delta * order.Price.ToDecimal()
		} else if order.AssetID == s.currentMarket.NoAssetID {
			s.downHoldings += delta
			s.downTotalCost += delta * order.Price.ToDecimal()
		}
		if s.lastCountedFilled != nil {
			s.lastCountedFilled[order.OrderID] = executed
		}
	}

	// plan 内累计：定位到对应 plan
	if planID, ok := s.orderIDToPlanID[order.OrderID]; ok && delta > 0 {
		if p := s.plans[planID]; p != nil && p.OrderIDs != nil && p.State != planFailed {
			if side, ok := p.OrderIDs[order.OrderID]; ok {
				if side == upKey {
					p.YesFilled += delta
				} else if side == downKey {
					p.NoFilled += delta
				}
			}
			// 成功匹配完毕：如果两腿都 >= TargetSize，完成本轮
			if p.YesFilled+1e-8 >= p.TargetSize && p.NoFilled+1e-8 >= p.TargetSize {
				log.Infof("✅ [pairlock] 本轮完成: plan=%s size=%.4f, lockedProfit≈%.4f USDC（按到期1.0估算）",
					p.ID, p.TargetSize, s.estimateLockedProfit())
				p.State = planCompleted
				p.StateAt = time.Now()
				// 清理索引
				for oid := range p.OrderIDs {
					delete(s.orderIDToPlanID, oid)
				}
				delete(s.plans, p.ID)
			}
		}
	}

	return nil
}

func (s *PairLockStrategy) onTick(ctx context.Context) {
	if s.paused || s.currentMarket == nil || s.config == nil {
		return
	}

	// 遍历所有 in-flight plan 进行补齐
	for _, p := range s.plans {
		if p == nil || p.State == planFailed || p.State == planCompleted {
			continue
		}
		// 超时失败
		maxAge := time.Duration(s.config.MaxPlanAgeSeconds) * time.Second
		if maxAge > 0 && !p.CreatedAt.IsZero() && time.Since(p.CreatedAt) > maxAge {
			s.failPlan(ctx, p, "plan_timeout", nil)
			return
		}
		imb := p.imbalance()
		if imb <= 0 {
			continue
		}
		if p.SupplementAttempts >= s.config.MaxSupplementAttempts {
			s.failPlan(ctx, p, "supplement_exhausted", nil)
			return
		}
		if !p.LastSupplementAt.IsZero() && time.Since(p.LastSupplementAt) < 2*time.Second {
			continue
		}

		needYes := p.YesFilled < p.NoFilled
		needNo := p.NoFilled < p.YesFilled
		if !needYes && !needNo {
			continue
		}

		otherPriceCents := 0
		if needYes && p.NoTemplate != nil {
			otherPriceCents = p.NoTemplate.Price.Cents
		}
		if needNo && p.YesTemplate != nil {
			otherPriceCents = p.YesTemplate.Price.Cents
		}
		if otherPriceCents <= 0 {
			continue
		}

		maxPriceCents := 100 - s.config.ProfitTargetCents - otherPriceCents
		if maxPriceCents < 0 {
			maxPriceCents = 0
		}
		if maxPriceCents <= 0 {
			p.SupplementAttempts++
			p.LastSupplementAt = time.Now()
			continue
		}

		assetID := s.currentMarket.YesAssetID
		tokenType := domain.TokenTypeUp
		if needNo {
			assetID = s.currentMarket.NoAssetID
			tokenType = domain.TokenTypeDown
		}

		orderCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		price, err := orderutil.QuoteBuyPrice(orderCtx, s.tradingService, assetID, maxPriceCents)
		cancel()
		if err != nil {
			p.SupplementAttempts++
			p.LastSupplementAt = time.Now()
			continue
		}

		needSize := imb
		if needSize > p.TargetSize {
			needSize = p.TargetSize
		}

		now := time.Now()
		supp := orderutil.NewOrder(s.currentMarket.Slug, assetID, types.SideBuy, price, needSize, tokenType, true, types.OrderTypeFAK)
		supp.OrderID = fmt.Sprintf("pairlock-supp-%s-%d", tokenType, now.UnixNano())

		if p.OrderIDs != nil {
			if tokenType == domain.TokenTypeUp {
				p.OrderIDs[supp.OrderID] = upKey
			} else if tokenType == domain.TokenTypeDown {
				p.OrderIDs[supp.OrderID] = downKey
			}
		}
		s.orderIDToPlanID[supp.OrderID] = p.ID

		if s.Executor == nil {
			return
		}
		p.State = planSupplementing
		p.SupplementAttempts++
		p.LastSupplementAt = time.Now()

		_ = s.submitPlaceCmd(p.ID, cmdSupplement, supp)
	}
}

func (s *PairLockStrategy) resetForNewCycle() {
	s.currentMarket = nil
	s.roundsThisPeriod = 0
	if s.attemptCooldown != nil {
		s.attemptCooldown.Reset()
	}
	s.lastSeenUpCents = 0
	s.lastSeenDownCents = 0
	s.plans = make(map[string]*pairLockPlan)
	s.orderIDToPlanID = make(map[string]string)
	s.paused = false

	s.upTotalCost = 0
	s.upHoldings = 0
	s.downTotalCost = 0
	s.downHoldings = 0

	s.lastCountedFilled = make(map[string]float64)
}

func (s *PairLockStrategy) failPlan(ctx context.Context, p *pairLockPlan, reason string, err error) {
	if p == nil || s.config == nil {
		return
	}
	p.State = planFailed
	p.StateAt = time.Now()
	if err != nil {
		p.LastError = err.Error()
	} else {
		p.LastError = reason
	}

	log.Errorf("❌ [pairlock] 轮次失败: plan=%s reason=%s err=%v yesFilled=%.4f noFilled=%.4f target=%.4f action=%s",
		p.ID, reason, err, p.YesFilled, p.NoFilled, p.TargetSize, s.config.OnFailAction)

	// 失败动作：默认都要暂停（最安全）
	switch s.config.OnFailAction {
	case "pause":
		s.paused = true
		return
	case "cancel_pause":
		s.cancelPlanOpenOrders(p)
		s.paused = true
		return
	case "flatten_pause":
		s.cancelPlanOpenOrders(p)
		s.flattenExposure(ctx, p)
		s.paused = true
		return
	default:
		s.paused = true
		return
	}
}

func (s *PairLockStrategy) cancelPlanOpenOrders(p *pairLockPlan) {
	if p == nil || s.Executor == nil || s.tradingService == nil || s.currentMarket == nil {
		return
	}
	// best-effort：逐个 cancel（失败忽略），避免阻塞策略 loop
	for orderID := range p.OrderIDs {
		oid := orderID
		_ = s.Executor.Submit(bbgo.Command{
			Name:    fmt.Sprintf("pairlock_cancel_%s_%s", p.ID, oid),
			Timeout: 10 * time.Second,
			Do: func(runCtx context.Context) {
				_ = s.tradingService.CancelOrder(runCtx, oid)
			},
		})
	}
}

func (s *PairLockStrategy) flattenExposure(ctx context.Context, p *pairLockPlan) {
	if p == nil || s.Executor == nil || s.tradingService == nil || s.currentMarket == nil {
		return
	}
	// 只回平“超出的那一腿”
	overYes := p.YesFilled - p.NoFilled
	overNo := p.NoFilled - p.YesFilled
	var assetID string
	var tokenType domain.TokenType
	var size float64
	var refCents int
	if overYes > 0 {
		assetID = s.currentMarket.YesAssetID
		tokenType = domain.TokenTypeUp
		size = overYes
		refCents = s.lastSeenUpCents
	} else if overNo > 0 {
		assetID = s.currentMarket.NoAssetID
		tokenType = domain.TokenTypeDown
		size = overNo
		refCents = s.lastSeenDownCents
	} else {
		return
	}
	if size <= 0 {
		return
	}
	if s.config != nil && size < s.config.FailFlattenMinShares {
		log.Warnf("⚠️ [pairlock] flatten 跳过（差额过小）: plan=%s token=%s diff=%.4f < min=%.4f",
			p.ID, tokenType, size, s.config.FailFlattenMinShares)
		return
	}

	minSell := 0
	if s.config.FailMaxSellSlippageCents > 0 && refCents > 0 {
		minSell = refCents - s.config.FailMaxSellSlippageCents
		if minSell < 0 {
			minSell = 0
		}
	}

	orderCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	price, qerr := orderutil.QuoteSellPrice(orderCtx, s.tradingService, assetID, minSell)
	cancel()
	if qerr != nil {
		log.Errorf("⚠️ [pairlock] flatten 获取卖价失败: plan=%s err=%v", p.ID, qerr)
		return
	}

	now := time.Now()
	sell := orderutil.NewOrder(s.currentMarket.Slug, assetID, types.SideSell, price, size, tokenType, false, types.OrderTypeFAK)
	sell.OrderID = fmt.Sprintf("pairlock-flatten-%s-%d", tokenType, now.UnixNano())

	// 提交卖出回平（best-effort）
	_ = s.Executor.Submit(bbgo.Command{
		Name:    fmt.Sprintf("pairlock_flatten_%s", p.ID),
		Timeout: 25 * time.Second,
		Do: func(runCtx context.Context) {
			_, _ = s.tradingService.PlaceOrder(runCtx, sell)
		},
	})
}

func (s *PairLockStrategy) inflightPlans() int {
	n := 0
	for _, p := range s.plans {
		if p == nil {
			continue
		}
		if p.State == planSubmitting || p.State == planWaiting || p.State == planSupplementing {
			n++
		}
	}
	return n
}

func (s *PairLockStrategy) maxConcurrentPlans() int {
	if s.config == nil {
		return 1
	}
	if !s.config.EnableParallel {
		return 1
	}
	if s.config.MaxConcurrentPlans <= 0 {
		return 1
	}
	return s.config.MaxConcurrentPlans
}

func (s *PairLockStrategy) inflightTargetShares() float64 {
	sum := 0.0
	for _, p := range s.plans {
		if p == nil {
			continue
		}
		if p.State == planSubmitting || p.State == planWaiting || p.State == planSupplementing {
			sum += p.TargetSize
		}
	}
	return sum
}

func (s *PairLockStrategy) canStartMorePlans() bool {
	if s.config == nil || !s.config.EnableParallel {
		return true
	}
	limit := s.config.MaxTotalUnhedgedShares
	if limit <= 0 {
		// 并行模式下策略侧会设置保守默认；这里兜底放行
		return true
	}
	return s.inflightTargetShares() < limit-1e-9
}

func (s *PairLockStrategy) canStartPlanWithSize(size float64) bool {
	if s.config == nil || !s.config.EnableParallel {
		return true
	}
	limit := s.config.MaxTotalUnhedgedShares
	if limit <= 0 {
		return true
	}
	return s.inflightTargetShares()+size <= limit+1e-9
}

func (s *PairLockStrategy) isFilledDuplicate(orderID string, filledAt time.Time) bool {
	s.processedFilledOrdersMu.Lock()
	defer s.processedFilledOrdersMu.Unlock()

	if s.processedFilledOrders == nil {
		s.processedFilledOrders = make(map[string]time.Time)
	}
	if t, ok := s.processedFilledOrders[orderID]; ok {
		d := t.Sub(filledAt)
		if d < 0 {
			d = -d
		}
		if d < time.Second {
			return true
		}
	}
	s.processedFilledOrders[orderID] = filledAt
	// 清理旧记录
	now := time.Now()
	for k, v := range s.processedFilledOrders {
		if now.Sub(v) > time.Hour {
			delete(s.processedFilledOrders, k)
		}
	}
	return false
}

func (s *PairLockStrategy) calcUnifiedSize(yesAsk, noAsk domain.Price) float64 {
	size := s.config.OrderSize
	minOrder := s.config.MinOrderSize
	if minOrder <= 0 {
		minOrder = 1.1
	}
	yesDec := yesAsk.ToDecimal()
	noDec := noAsk.ToDecimal()
	if yesDec <= 0 || noDec <= 0 {
		return size
	}

	reqYes := minOrder / yesDec
	reqNo := minOrder / noDec
	if reqYes > size {
		size = reqYes
	}
	if reqNo > size {
		size = reqNo
	}

	// 避免极端浮点噪声：保留 4 位小数（shares 通常支持较小粒度）
	size = math.Ceil(size*10000) / 10000
	return size
}

func (s *PairLockStrategy) estimateLockedProfit() float64 {
	// 按“到期每套支付 1 USDC”估算：
	// 可锁定的套数 = min(upHoldings, downHoldings)
	sets := s.upHoldings
	if s.downHoldings < sets {
		sets = s.downHoldings
	}
	return sets*1.0 - (s.upTotalCost + s.downTotalCost)
}

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) <= 1e-6
}
