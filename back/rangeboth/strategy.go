package rangeboth

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
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/betbot/gobet/pkg/config"
	"github.com/betbot/gobet/pkg/marketspec"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy：检测 UP/DOWN 在短窗口内“窄幅波动”，然后双边挂 BUY GTC 限价单。
//
// 适用场景：你描述的 “5 秒内波动不超过 5 个点 -> 两边挂单”。
// - 触发更像“波动收敛/横盘”，属于做市/捕捉偏离的一种变体。
// - 本策略默认只挂单，不做自动对冲/平仓逻辑（后续可以叠加退出规则）。
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	mu sync.Mutex

	autoMerge common.AutoMergeController

	// 周期状态
	firstSeenAt            time.Time
	lastTriggerAt          time.Time
	triggersCountThisCycle int

	// 追踪最近一次挂单的两笔订单ID（用于判断是否两边都成交）
	pendingUpOrderID   string
	pendingDownOrderID string
	// 标志位：记录是否两边都挂单了（用于判断是否两边都成交）
	pendingPairComplete bool

	// autoMerge 状态追踪（用于检测合并完成并重置计数）
	lastMergeCheckUpShares   float64
	lastMergeCheckDownShares float64
	lastMergeCheckTime       time.Time

	// 价格样本
	samples map[domain.TokenType][]priceSample

	// 市场过滤（防误交易）
	marketSlugPrefix string
	marketSpec       *marketspec.MarketSpec // 保存marketSpec用于计算周期剩余时间

	// 全局约束
	minOrderSize float64
	minShareSize float64

	// 市场精度（系统级配置）
	currentPrecision *MarketPrecisionInfo

	// 实时终端显示
	dashboard *Dashboard

	// 智能对冲定时检查
	rebalanceTicker   *time.Ticker
	rebalanceStopChan chan struct{}
	rebalanceWg       sync.WaitGroup
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }

func (s *Strategy) Defaults() error { return nil }
func (s *Strategy) Validate() error { return s.Config.Validate() }

func (s *Strategy) Initialize() error {
	if s.samples == nil {
		s.samples = make(map[domain.TokenType][]priceSample)
	}

	gc := config.Get()
	if gc == nil {
		return fmt.Errorf("[%s] 全局配置未加载：拒绝启动（避免误交易）", ID)
	}
	sp, err := gc.Market.Spec()
	if err != nil {
		return fmt.Errorf("[%s] 读取 market 配置失败：%w（拒绝启动，避免误交易）", ID, err)
	}
	// 本策略专门针对 15m up/down（防误用）
	if sp.Timeframe != "15m" {
		return fmt.Errorf("[%s] 当前仅支持 timeframe=15m（收到 %q）", ID, sp.Timeframe)
	}

	// 保存marketSpec用于后续计算周期剩余时间
	s.marketSpec = &sp

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
		s.minOrderSize = 1.0
	}
	if s.minShareSize <= 0 {
		s.minShareSize = 5.0
	}

	if gc.Market.Precision != nil {
		s.currentPrecision = &MarketPrecisionInfo{
			TickSize:     gc.Market.Precision.TickSize,
			MinOrderSize: gc.Market.Precision.MinOrderSize,
			NegRisk:      gc.Market.Precision.NegRisk,
		}
		log.Infof("✅ [%s] 已加载市场精度: tick_size=%s min_order_size=%s neg_risk=%v",
			ID, s.currentPrecision.TickSize, s.currentPrecision.MinOrderSize, s.currentPrecision.NegRisk)
	}

	// 初始化Dashboard（默认使用ANSI版本，更适合后台运行）
	// 注意：bubbletea版本需要在主线程中运行才能正确显示，当前架构下使用ANSI版本更合适
	if s.TradingService != nil {
		s.dashboard = NewDashboard(s.TradingService, &sp)
		s.dashboard.SetStrategy(s) // 设置策略引用，用于获取波动幅度数据
		log.Infof("✅ [%s] Dashboard已初始化（ANSI版本）", ID)
	}

	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	session.OnOrderUpdate(s)
	log.Infof("✅ [%s] 策略已订阅价格变化事件和订单更新事件 (session=%s)", ID, session.Name)
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	// 启动Dashboard（ANSI版本，适合后台运行）
	if s.dashboard != nil {
		s.dashboard.Start()
		defer s.dashboard.Stop()
		log.Infof("✅ [%s] Dashboard已启动（ANSI版本）", ID)
	}

	// 启动智能对冲定时检查（如果启用）
	if s.RebalanceEnabled {
		s.startRebalanceChecker(ctx)
		defer s.stopRebalanceChecker()
	}

	<-ctx.Done()
	return ctx.Err()
}

// startRebalanceChecker 启动智能对冲定时检查器
func (s *Strategy) startRebalanceChecker(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.rebalanceTicker != nil {
		return // 已经启动
	}

	interval := time.Duration(s.RebalanceCheckIntervalSeconds) * time.Second
	s.rebalanceTicker = time.NewTicker(interval)
	s.rebalanceStopChan = make(chan struct{})

	s.rebalanceWg.Add(1)
	go func() {
		defer s.rebalanceWg.Done()
		defer s.rebalanceTicker.Stop()

		for {
			select {
			case <-s.rebalanceTicker.C:
				// 获取当前市场
				if s.TradingService == nil {
					continue
				}
				currentMarketSlug := s.TradingService.GetCurrentMarket()
				if currentMarketSlug == "" {
					continue
				}

				// 获取市场信息（需要从某个地方获取，暂时跳过）
				// 这里我们依赖OnPriceChanged事件来触发对冲检查
				// 定时检查主要用于确保不会遗漏
				log.Debugf("🔄 [%s] 定时检查对冲状态（每%d秒）", ID, s.RebalanceCheckIntervalSeconds)

			case <-s.rebalanceStopChan:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	log.Infof("✅ [%s] 智能对冲定时检查已启动（间隔: %d秒）", ID, s.RebalanceCheckIntervalSeconds)
}

// stopRebalanceChecker 停止智能对冲定时检查器
func (s *Strategy) stopRebalanceChecker() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.rebalanceTicker == nil {
		return
	}

	close(s.rebalanceStopChan)
	s.rebalanceTicker.Stop()
	s.rebalanceTicker = nil

	s.rebalanceWg.Wait()
	log.Infof("✅ [%s] 智能对冲定时检查已停止", ID)
}

func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, newMarket *domain.Market) {
	s.mu.Lock()
	s.firstSeenAt = time.Now()
	s.lastTriggerAt = time.Time{}
	s.triggersCountThisCycle = 0
	s.pendingUpOrderID = ""
	s.pendingDownOrderID = ""
	s.pendingPairComplete = false
	s.samples = make(map[domain.TokenType][]priceSample)

	// 重置合并追踪状态
	s.lastMergeCheckUpShares = 0
	s.lastMergeCheckDownShares = 0
	s.lastMergeCheckTime = time.Time{}

	// 重置智能对冲定时检查器（新周期重新开始）
	if s.rebalanceTicker != nil {
		s.stopRebalanceChecker()
	}
	
	// 更新Dashboard的市场规格（如果marketSpec已更新）
	if s.marketSpec != nil && s.dashboard != nil {
		s.dashboard.UpdateMarketSpec(s.marketSpec)
		// 如果Dashboard正在运行，重新启动以确保周期切换后正常显示
		if s.dashboard.IsRunning() {
			log.Infof("🔄 [%s] 周期切换，重启Dashboard", ID)
			s.dashboard.Start() // Start方法会先停止旧的再启动新的
		}
	}
	s.mu.Unlock()
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}

	now := e.Timestamp
	if now.IsZero() {
		now = time.Now()
	}
	priceCents := e.NewPrice.ToCents()

	if priceCents <= 0 || priceCents >= 100 {
		return nil
	}

	// 周期结束保护：周期结束后N秒内不交易
	if s.marketSpec != nil && e.Market.Timestamp > 0 {
		cycleDuration := s.marketSpec.Duration()
		cycleEndTime := time.Unix(e.Market.Timestamp, 0).Add(cycleDuration)
		remainingSeconds := int(cycleEndTime.Sub(now).Seconds())

		// 如果周期已结束，或者剩余时间小于保护时间，不交易
		if remainingSeconds <= 0 || remainingSeconds <= s.CycleEndProtectionSeconds {
			log.Debugf("⏸️ [%s] 跳过：周期结束保护（剩余时间: %d秒 <= %d秒）", ID, remainingSeconds, s.CycleEndProtectionSeconds)
			return nil
		}
	}

	// 检测autoMerge完成并重置计数
	s.checkAndResetAfterMerge(e.Market)

	// 执行autoMerge（在检查之后，避免影响重置逻辑）
	s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)

	if !s.shouldHandleMarketEvent(e.Market) {
		return nil
	}

	s.mu.Lock()
	if s.firstSeenAt.IsZero() {
		s.firstSeenAt = now
	}
	// 预热期
	if s.WarmupMs > 0 && now.Sub(s.firstSeenAt) < time.Duration(s.WarmupMs)*time.Millisecond {
		s.mu.Unlock()

		return nil
	}
	// 冷却
	if !s.lastTriggerAt.IsZero() && now.Sub(s.lastTriggerAt) < time.Duration(s.CooldownMs)*time.Millisecond {
		s.mu.Unlock()
		return nil
	}
	// 每周期触发次数限制
	if s.MaxTriggersPerCycle > 0 && s.triggersCountThisCycle >= s.MaxTriggersPerCycle {
		s.mu.Unlock()

		return nil
	}
	// 更新样本并裁剪窗口
	lookback := time.Duration(s.LookbackSeconds) * time.Second

	cutoff := now.Add(-lookback)

	s.samples[e.TokenType] = append(s.samples[e.TokenType], priceSample{ts: now, priceCents: priceCents})
	s.samples[domain.TokenTypeUp] = pruneSamples(s.samples[domain.TokenTypeUp], cutoff)
	s.samples[domain.TokenTypeDown] = pruneSamples(s.samples[domain.TokenTypeDown], cutoff)

	upMin, upMax, upOK := rangeCents(s.samples[domain.TokenTypeUp])

	downMin, downMax, downOK := rangeCents(s.samples[domain.TokenTypeDown])
	// 由于订单簿是镜像的（UP+DOWN=100分），只需要UP或DOWN其中一个满足窄幅条件即可

	upStable := upOK && (upMax-upMin) <= s.MaxRangeCents
	downStable := downOK && (downMax-downMin) <= s.MaxRangeCents
	stable := upStable || downStable

	if false {
		stable = upStable && downStable
	} else {
		// 注意：即使只要求一边满足，本策略仍会“双边挂单”，因此该模式更适合调试/放宽触发。
		stable = upStable || downStable
	}

	if !stable {
		s.mu.Unlock()
		return nil
	}
	// 锁内先更新 trigger 相关状态，避免并发重复触发
	// 注意：triggersCountThisCycle 不在挂单时增加，而是在两边订单都成交后通过 OnOrderUpdate 增加
	s.lastTriggerAt = now
	s.mu.Unlock()

	// 若当前市场已有同侧活跃买单，则跳过（避免堆叠挂单）
	active := s.TradingService.GetActiveOrders()
	if hasActiveBuyOrder(active, e.Market.Slug, e.Market.YesAssetID) || hasActiveBuyOrder(active, e.Market.Slug, e.Market.NoAssetID) {
		return nil
	}

	orderCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	yesBid, yesAsk, noBid, noAsk, source, err := s.TradingService.GetTopOfBook(orderCtx, e.Market)
	if err != nil {
		return nil
	}
	yesBidC := yesBid.ToCents()
	yesAskC := yesAsk.ToCents()
	noBidC := noBid.ToCents()
	noAskC := noAsk.ToCents()

	// 判断当前阶段（基于价格触发）
	phase := s.getCyclePhase(yesBidC, noBidC)

	// Phase2 (rebalance阶段)：智能对冲（当UP或DOWN价格达到阈值时触发）
	if phase == "rebalance" && s.RebalanceEnabled {
		s.mu.Unlock()
		triggerReason := ""
		if yesBidC >= s.RebalanceTriggerPriceCents {
			triggerReason = fmt.Sprintf("UP价格 %dc >= %dc", yesBidC, s.RebalanceTriggerPriceCents)
		} else if noBidC >= s.RebalanceTriggerPriceCents {
			triggerReason = fmt.Sprintf("DOWN价格 %dc >= %dc", noBidC, s.RebalanceTriggerPriceCents)
		}
		log.Infof("🔄 [%s] 进入补仓阶段：%s", ID, triggerReason)
		return s.handleRebalancePhase(ctx, e.Market, now)
	}

	// Phase1 (build阶段)：正常建仓逻辑（继续原有逻辑）

	// 价差检查（两边都需要检查）
	if s.MaxSpreadCents > 0 {
		ys := yesAskC - yesBidC
		if ys < 0 {
			ys = -ys
		}
		ns := noAskC - noBidC
		if ns < 0 {
			ns = -ns
		}
		if ys > s.MaxSpreadCents || ns > s.MaxSpreadCents {
			return nil
		}
	}

	upLimitC, okUp := chooseLimitBuyPrice(yesBidC, yesAskC, s.LimitPriceOffsetCents)
	downLimitC, okDown := chooseLimitBuyPrice(noBidC, noAskC, s.LimitPriceOffsetCents)
	if !okUp || !okDown {
		return nil
	}

	// 价格区间检查逻辑：
	// 由于订单簿是镜像的（UP+DOWN=100分），只需要UP或DOWN其中一个在价格区间内即可
	// 例如：如果UP在60-90分区间，则DOWN在10-40分区间（镜像）
	upInRange := yesBidC >= s.MinPriceCents && yesBidC <= s.MaxPriceCents
	downInRange := noBidC >= s.MinPriceCents && noBidC <= s.MaxPriceCents

	if !upInRange && !downInRange {
		log.Debugf("⏸️ [%s] 跳过：UP价格 %dc 和 DOWN价格 %dc 都不在区间 [%d-%d] 内", ID, yesBidC, noBidC, s.MinPriceCents, s.MaxPriceCents)
		return nil
	}

	if upInRange {
		log.Debugf("✅ [%s] UP价格 %dc 在区间 [%d-%d] 内，继续执行", ID, yesBidC, s.MinPriceCents, s.MaxPriceCents)
	} else {
		log.Debugf("✅ [%s] DOWN价格 %dc 在区间 [%d-%d] 内，继续执行（UP=%dc，镜像关系）", ID, noBidC, s.MinPriceCents, s.MaxPriceCents, yesBidC)
	}

	// 将美分转换为 Price（Pips = 美分 * 100，四舍五入）
	// 例如：27.1 美分 = 2710 pips
	upPrice := domain.Price{Pips: int(math.Round(upLimitC * 100))}
	downPrice := domain.Price{Pips: int(math.Round(downLimitC * 100))}

	// size：允许分别配置
	upSize := s.OrderSizeUp
	downSize := s.OrderSizeDown
	if upSize <= 0 {
		upSize = s.OrderSize
	}
	if downSize <= 0 {
		downSize = s.OrderSize
	}

	upPriceDec := upPrice.ToDecimal()
	downPriceDec := downPrice.ToDecimal()
	upSize = ensureMinOrderSize(upSize, upPriceDec, s.minOrderSize)
	downSize = ensureMinOrderSize(downSize, downPriceDec, s.minOrderSize)
	if upSize < s.minShareSize {
		upSize = s.minShareSize
	}
	if downSize < s.minShareSize {
		downSize = s.minShareSize
	}
	upSize = adjustSizeForMakerAmountPrecision(upSize, upPriceDec)
	downSize = adjustSizeForMakerAmountPrecision(downSize, downPriceDec)

	// tick/neg_risk（可选）
	var tickSize types.TickSize
	var negRisk *bool
	if s.currentPrecision != nil {
		if parsed, err := ParseTickSize(s.currentPrecision.TickSize); err == nil {
			tickSize = parsed
		}
		negRisk = boolPtr(s.currentPrecision.NegRisk)
	}

	log.Infof("📏 [%s] 触发：UP[%dc..%dc] DOWN[%dc..%dc] window=%ds range<=%dc | place: UP@%.1fc DOWN@%.1fc (src=%s) market=%s",
		ID, upMin, upMax, downMin, downMax, s.LookbackSeconds, s.MaxRangeCents, upLimitC, downLimitC, source, e.Market.Slug)

	legs := []execution.LegIntent{
		{
			Name:      "maker_buy_up",
			AssetID:   e.Market.YesAssetID,
			TokenType: domain.TokenTypeUp,
			Side:      types.SideBuy,
			Price:     upPrice,
			Size:      upSize,
			OrderType: types.OrderTypeGTC,
			TickSize:  tickSize,
			NegRisk:   negRisk,
		},
		{
			Name:      "maker_buy_down",
			AssetID:   e.Market.NoAssetID,
			TokenType: domain.TokenTypeDown,
			Side:      types.SideBuy,
			Price:     downPrice,
			Size:      downSize,
			OrderType: types.OrderTypeGTC,
			TickSize:  tickSize,
			NegRisk:   negRisk,
		},
	}

	if s.OrderExecutionMode == "parallel" {
		req := execution.MultiLegRequest{
			Name:       "rangeboth",
			MarketSlug: e.Market.Slug,
			Legs:       legs,
			Hedge:      execution.AutoHedgeConfig{Enabled: false},
		}
		result, execErr := s.TradingService.ExecuteMultiLeg(orderCtx, req)
		if execErr != nil {
			if isFailSafeRefusal(execErr) {
				return nil
			}
			return nil
		}
		// 记录并行模式下的订单ID
		if result != nil && len(result) >= 2 {
			s.mu.Lock()
			hasUp := false
			hasDown := false
			for _, order := range result {
				if order != nil && order.OrderID != "" {
					if order.TokenType == domain.TokenTypeUp {
						s.pendingUpOrderID = order.OrderID
						hasUp = true
					} else if order.TokenType == domain.TokenTypeDown {
						s.pendingDownOrderID = order.OrderID
						hasDown = true
					}
				}
			}
			// 如果两边都有订单，设置标志位
			s.pendingPairComplete = hasUp && hasDown
			s.mu.Unlock()
		}
		return nil
	}

	// sequential：按优先规则决定先后顺序，仅保证“先下第一笔成功返回，再下第二笔”
	first, second := s.chooseSequentialOrder(legs, upLimitC, downLimitC)
	if first == nil || second == nil {
		return nil
	}

	o1 := &domain.Order{
		MarketSlug:   e.Market.Slug,
		AssetID:      first.AssetID,
		TokenType:    first.TokenType,
		Side:         first.Side,
		Price:        first.Price,
		Size:         first.Size,
		OrderType:    first.OrderType,
		TickSize:     first.TickSize,
		NegRisk:      first.NegRisk,
		IsEntryOrder: true,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
	}
	placedOrder1, err := s.TradingService.PlaceOrder(orderCtx, o1)
	if err != nil {
		if isFailSafeRefusal(err) {
			return nil
		}
		return nil
	}
	// 记录第一笔订单ID
	var hasUp, hasDown bool
	if placedOrder1 != nil && placedOrder1.OrderID != "" {
		s.mu.Lock()
		if placedOrder1.TokenType == domain.TokenTypeUp {
			s.pendingUpOrderID = placedOrder1.OrderID
			hasUp = true
		} else if placedOrder1.TokenType == domain.TokenTypeDown {
			s.pendingDownOrderID = placedOrder1.OrderID
			hasDown = true
		}
		s.mu.Unlock()
	}

	o2 := &domain.Order{
		MarketSlug:   e.Market.Slug,
		AssetID:      second.AssetID,
		TokenType:    second.TokenType,
		Side:         second.Side,
		Price:        second.Price,
		Size:         second.Size,
		OrderType:    second.OrderType,
		TickSize:     second.TickSize,
		NegRisk:      second.NegRisk,
		IsEntryOrder: true,
		Status:       domain.OrderStatusPending,
		CreatedAt:    time.Now(),
	}
	placedOrder2, err := s.TradingService.PlaceOrder(orderCtx, o2)
	if err != nil {
		// 第二笔失败不回滚第一笔（符合“顺序”语义）；后续可在这里加撤单/重试策略
		_ = err
	} else {
		// 记录第二笔订单ID
		if placedOrder2 != nil && placedOrder2.OrderID != "" {
			s.mu.Lock()
			if placedOrder2.TokenType == domain.TokenTypeUp {
				s.pendingUpOrderID = placedOrder2.OrderID
				hasUp = true
			} else if placedOrder2.TokenType == domain.TokenTypeDown {
				s.pendingDownOrderID = placedOrder2.OrderID
				hasDown = true
			}
			// 如果两边都有订单，设置标志位
			s.pendingPairComplete = hasUp && hasDown
			s.mu.Unlock()
		}
	}

	return nil
}

func (s *Strategy) shouldHandleMarketEvent(m *domain.Market) bool {
	if s == nil || m == nil {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(m.Slug), s.marketSlugPrefix) {
		return false
	}
	if s.TradingService != nil {
		cur := s.TradingService.GetCurrentMarket()
		if cur != "" && cur != m.Slug {
			return false
		}
	}
	return true
}

// handleRebalancePhase 处理Phase2阶段的智能对冲逻辑
func (s *Strategy) handleRebalancePhase(ctx context.Context, market *domain.Market, now time.Time) error {
	if s.TradingService == nil || market == nil {
		return nil
	}

	// 获取当前持仓状态
	positions := s.TradingService.GetOpenPositionsForMarket(market.Slug)
	var upShares, downShares float64
	var upCost, downCost float64

	for _, pos := range positions {
		if pos == nil || !pos.IsOpen() {
			continue
		}
		currentSize := pos.Size
		if currentSize <= 0 {
			continue
		}

		if pos.TokenType == domain.TokenTypeUp {
			upShares += currentSize
			if pos.AvgPrice > 0 && currentSize > 0 {
				upCost += pos.AvgPrice * currentSize
			} else if pos.CostBasis > 0 && pos.TotalFilledSize > 0 {
				avgPrice := pos.CostBasis / pos.TotalFilledSize
				upCost += avgPrice * currentSize
			} else if pos.EntryPrice.Pips > 0 && currentSize > 0 {
				upCost += pos.EntryPrice.ToDecimal() * currentSize
			}
		} else if pos.TokenType == domain.TokenTypeDown {
			downShares += currentSize
			if pos.AvgPrice > 0 && currentSize > 0 {
				downCost += pos.AvgPrice * currentSize
			} else if pos.CostBasis > 0 && pos.TotalFilledSize > 0 {
				avgPrice := pos.CostBasis / pos.TotalFilledSize
				downCost += avgPrice * currentSize
			} else if pos.EntryPrice.Pips > 0 && currentSize > 0 {
				downCost += pos.EntryPrice.ToDecimal() * currentSize
			}
		}
	}

	// 计算当前对冲状态
	hedgeState := CalculateHedgeState(upShares, downShares, upCost, downCost)

	// 检查是否已对冲
	if IsHedged(hedgeState, s.RebalanceMinProfit) {
		log.Debugf("✅ [%s] 已对冲，最小收益: $%.4f (目标: $%.4f)", ID, hedgeState.MinProfit, s.RebalanceMinProfit)
		return nil
	}

	//log.Infof("🔄 [%s] 未对冲，开始智能补单。当前状态: UP=%.4f($%.2f) DOWN=%.4f($%.2f) 最小收益=$%.4f",
	//	ID, upShares, upCost, downShares, downCost, hedgeState.MinProfit)

	// 获取订单簿价格
	orderCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	yesBid, _, noBid, _, _, err := s.TradingService.GetTopOfBook(orderCtx, market)
	if err != nil {
		return fmt.Errorf("获取订单簿失败: %w", err)
	}

	upPrice := yesBid.ToDecimal()
	downPrice := noBid.ToDecimal()

	// 计算需要补的数量
	upNeeded, downNeeded := CalculateHedgeNeeds(hedgeState, upPrice, downPrice, s.RebalanceMinProfit, s.RebalanceMaxOrderSize)

	if upNeeded <= 0 && downNeeded <= 0 {
		log.Debugf("✅ [%s] 计算后无需补单", ID)
		return nil
	}

	//log.Infof("📊 [%s] 需要补单: UP=%.4f DOWN=%.4f", ID, upNeeded, downNeeded)

	// 取消未成交的挂单
	if err := s.cancelPendingOrders(ctx, market.Slug); err != nil {
		log.Warnf("⚠️ [%s] 取消挂单失败: %v", ID, err)
		// 继续执行补单逻辑
	}

	// 智能补单
	if err := s.placeRebalanceOrders(ctx, market, upNeeded, downNeeded); err != nil {
		return fmt.Errorf("补单失败: %w", err)
	}

	return nil
}

// getCyclePhase 根据价格返回当前阶段
// Phase1 (build): 正常建仓阶段
// Phase2 (rebalance): 智能对冲阶段（当UP或DOWN价格达到阈值时触发）
// 触发条件：如果UP或DOWN价格 >= RebalanceTriggerPriceCents（默认90分），则进入补仓阶段
func (s *Strategy) getCyclePhase(yesBidCents, noBidCents int) string {
	// 如果UP或DOWN价格达到阈值，进入补仓阶段
	if yesBidCents >= s.RebalanceTriggerPriceCents || noBidCents >= s.RebalanceTriggerPriceCents {
		return "rebalance"
	}
	// 否则保持建仓阶段
	return "build"
}

// getRemainingSeconds 计算周期剩余秒数
func (s *Strategy) getRemainingSeconds(market *domain.Market, now time.Time) int {
	if s.marketSpec == nil || market == nil || market.Timestamp <= 0 {
		return 0
	}
	cycleStart := time.Unix(market.Timestamp, 0)
	cycleEndTime := cycleStart.Add(s.marketSpec.Duration())
	remaining := cycleEndTime.Sub(now)
	if remaining < 0 {
		return 0
	}
	return int(remaining.Seconds())
}

// cancelPendingOrders 取消未成交的挂单（仅取消Pending/Open/Partial状态的订单）
func (s *Strategy) cancelPendingOrders(ctx context.Context, marketSlug string) error {
	if s.TradingService == nil {
		return fmt.Errorf("trading service not initialized")
	}

	activeOrders := s.TradingService.GetActiveOrders()
	cancelledCount := 0

	for _, order := range activeOrders {
		if order == nil {
			continue
		}
		// 只取消当前市场的订单
		if order.MarketSlug != marketSlug {
			continue
		}
		// 只取消未成交的订单（Pending/Open/Partial）
		if order.Status != domain.OrderStatusPending &&
			order.Status != domain.OrderStatusOpen &&
			order.Status != domain.OrderStatusPartial {
			continue
		}
		// 不取消已成交或已取消的订单
		if order.IsFinalStatus() {
			continue
		}

		// 取消订单
		err := s.TradingService.CancelOrder(ctx, order.OrderID)
		if err != nil {
			log.Warnf("⚠️ [%s] 取消挂单失败: orderID=%s status=%s err=%v", ID, order.OrderID, order.Status, err)
			continue
		}
		cancelledCount++
		log.Infof("✅ [%s] 已取消挂单: orderID=%s status=%s", ID, order.OrderID, order.Status)
	}

	if cancelledCount > 0 {
		log.Infof("🔄 [%s] 共取消 %d 个未成交挂单", ID, cancelledCount)
	}

	return nil
}

// placeRebalanceOrders 根据计算结果智能补单
func (s *Strategy) placeRebalanceOrders(ctx context.Context, market *domain.Market, upNeeded, downNeeded float64) error {
	if s.TradingService == nil || market == nil {
		return fmt.Errorf("trading service not initialized")
	}

	if upNeeded <= 0 && downNeeded <= 0 {
		return nil // 不需要补单
	}

	orderCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// 获取订单簿价格
	yesBid, yesAsk, noBid, noAsk, _, err := s.TradingService.GetTopOfBook(orderCtx, market)
	if err != nil {
		return fmt.Errorf("获取订单簿失败: %w", err)
	}

	yesBidC := yesBid.ToCents()
	yesAskC := yesAsk.ToCents()
	noBidC := noBid.ToCents()
	noAskC := noAsk.ToCents()

	// 计算限价
	upLimitC, okUp := chooseLimitBuyPrice(yesBidC, yesAskC, s.LimitPriceOffsetCents)
	downLimitC, okDown := chooseLimitBuyPrice(noBidC, noAskC, s.LimitPriceOffsetCents)

	// tick/neg_risk（可选）
	var tickSize types.TickSize
	var negRisk *bool
	if s.currentPrecision != nil {
		if parsed, err := ParseTickSize(s.currentPrecision.TickSize); err == nil {
			tickSize = parsed
		}
		negRisk = boolPtr(s.currentPrecision.NegRisk)
	}

	ordersPlaced := 0

	// 补UP单
	if upNeeded > 0 && okUp {
		upPrice := domain.Price{Pips: int(math.Round(upLimitC * 100))}
		upPriceDec := upPrice.ToDecimal()

		// 确保满足最小订单要求
		upSize := ensureMinOrderSize(upNeeded, upPriceDec, s.minOrderSize)
		if upSize < s.minShareSize {
			upSize = s.minShareSize
		}
		upSize = adjustSizeForMakerAmountPrecision(upSize, upPriceDec)

		// 检查价格区间（仅主leg需要检查）
		if yesBidC >= s.MinPriceCents && yesBidC <= s.MaxPriceCents {
			order := &domain.Order{
				MarketSlug:   market.Slug,
				AssetID:      market.YesAssetID,
				TokenType:    domain.TokenTypeUp,
				Side:         types.SideBuy,
				Price:        upPrice,
				Size:         upSize,
				OrderType:    types.OrderTypeGTC,
				TickSize:     tickSize,
				NegRisk:      negRisk,
				IsEntryOrder: true,
				Status:       domain.OrderStatusPending,
				CreatedAt:    time.Now(),
			}

			placedOrder, err := s.TradingService.PlaceOrder(orderCtx, order)
			if err != nil {
				log.Warnf("⚠️ [%s] 补UP单失败: size=%.4f price=%.4f err=%v", ID, upSize, upPriceDec, err)
			} else if placedOrder != nil && placedOrder.OrderID != "" {
				ordersPlaced++
				log.Infof("✅ [%s] 已补UP单: orderID=%s size=%.4f price=%.4f", ID, placedOrder.OrderID, upSize, upPriceDec)
			}
		} else {
			log.Debugf("⏸️ [%s] 跳过补UP单：价格 %dc 不在区间 [%d-%d] 内", ID, yesBidC, s.MinPriceCents, s.MaxPriceCents)
		}
	}

	// 补DOWN单
	if downNeeded > 0 && okDown {
		downPrice := domain.Price{Pips: int(math.Round(downLimitC * 100))}
		downPriceDec := downPrice.ToDecimal()

		// 确保满足最小订单要求
		downSize := ensureMinOrderSize(downNeeded, downPriceDec, s.minOrderSize)
		if downSize < s.minShareSize {
			downSize = s.minShareSize
		}
		downSize = adjustSizeForMakerAmountPrecision(downSize, downPriceDec)

		// DOWN单不需要检查价格区间（对冲单）
		order := &domain.Order{
			MarketSlug:   market.Slug,
			AssetID:      market.NoAssetID,
			TokenType:    domain.TokenTypeDown,
			Side:         types.SideBuy,
			Price:        downPrice,
			Size:         downSize,
			OrderType:    types.OrderTypeGTC,
			TickSize:     tickSize,
			NegRisk:      negRisk,
			IsEntryOrder: true,
			Status:       domain.OrderStatusPending,
			CreatedAt:    time.Now(),
		}

		placedOrder, err := s.TradingService.PlaceOrder(orderCtx, order)
		if err != nil {
			log.Warnf("⚠️ [%s] 补DOWN单失败: size=%.4f price=%.4f err=%v", ID, downSize, downPriceDec, err)
		} else if placedOrder != nil && placedOrder.OrderID != "" {
			ordersPlaced++
			log.Infof("✅ [%s] 已补DOWN单: orderID=%s size=%.4f price=%.4f", ID, placedOrder.OrderID, downSize, downPriceDec)
		}
	}

	if ordersPlaced > 0 {
		log.Infof("🔄 [%s] 共补单 %d 笔（UP: %.4f, DOWN: %.4f）", ID, ordersPlaced, upNeeded, downNeeded)
	}

	return nil
}

func (s *Strategy) chooseSequentialOrder(legs []execution.LegIntent, upLimitCents float64, downLimitCents float64) (first *execution.LegIntent, second *execution.LegIntent) {
	if len(legs) != 2 {
		return nil, nil
	}
	// 默认顺序：UP -> DOWN
	a := &legs[0]
	b := &legs[1]

	mode := strings.ToLower(strings.TrimSpace(s.SequentialPriorityMode))
	switch mode {
	case "up_first":
		return a, b
	case "down_first":
		return b, a
	case "higher_price":
		if downLimitCents > upLimitCents {
			return b, a
		}
		return a, b
	case "price_above":
		th := float64(s.SequentialPriorityPriceCents)
		if upLimitCents >= th && downLimitCents < th {
			return a, b
		}
		if downLimitCents >= th && upLimitCents < th {
			return b, a
		}
		// 两边都 >= th 或都 < th：回退到 higher_price
		if downLimitCents > upLimitCents {
			return b, a
		}
		return a, b
	default:
		return a, b
	}
}

// OnOrderUpdate 处理订单更新事件，当两边订单都成交时增加 triggersCountThisCycle
func (s *Strategy) OnOrderUpdate(ctx context.Context, order *domain.Order) error {
	if order == nil || order.OrderID == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 检查是否是我们在追踪的订单
	isUpOrder := order.OrderID == s.pendingUpOrderID
	isDownOrder := order.OrderID == s.pendingDownOrderID

	if !isUpOrder && !isDownOrder {
		// 不是我们追踪的订单，忽略
		return nil
	}

	// 检查订单是否已成交
	isFilled := order.Status == domain.OrderStatusFilled

	if isFilled {
		// 订单已成交，清除对应的追踪ID
		if isUpOrder {
			s.pendingUpOrderID = ""
		}
		if isDownOrder {
			s.pendingDownOrderID = ""
		}

		// 只有当两边都挂单了（pendingPairComplete=true），且两边都成交了（两个ID都为空），才增加计数
		if s.pendingPairComplete && s.pendingUpOrderID == "" && s.pendingDownOrderID == "" {
			// 两边都成交了，增加计数
			s.triggersCountThisCycle++
			s.pendingPairComplete = false // 重置标志位
			log.Infof("✅ [%s] 两边订单都成交，增加触发计数: triggersCountThisCycle=%d", ID, s.triggersCountThisCycle)
		}
	}

	return nil
}

// VolatilitySnapshot 波动幅度快照（用于Dashboard显示）
type VolatilitySnapshot struct {
	UpMinCents      int  // UP最小价格（分）
	UpMaxCents      int  // UP最大价格（分）
	UpRangeCents    int  // UP波动幅度（分）
	UpStable        bool // UP是否稳定
	DownMinCents    int  // DOWN最小价格（分）
	DownMaxCents    int  // DOWN最大价格（分）
	DownRangeCents  int  // DOWN波动幅度（分）
	DownStable      bool // DOWN是否稳定
	SampleCountUp   int  // UP样本数量
	SampleCountDown int  // DOWN样本数量
	LookbackSeconds int  // 观察窗口（秒）
	MaxRangeCents   int  // 最大允许波动（分）
}

// GetVolatilitySnapshot 获取当前波动幅度快照（线程安全）
func (s *Strategy) GetVolatilitySnapshot() VolatilitySnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot := VolatilitySnapshot{
		LookbackSeconds: s.LookbackSeconds,
		MaxRangeCents:   s.MaxRangeCents,
	}

	// 计算UP的波动
	upMin, upMax, upOK := rangeCents(s.samples[domain.TokenTypeUp])
	if upOK {
		snapshot.UpMinCents = upMin
		snapshot.UpMaxCents = upMax
		snapshot.UpRangeCents = upMax - upMin
		snapshot.UpStable = snapshot.UpRangeCents <= s.MaxRangeCents
		snapshot.SampleCountUp = len(s.samples[domain.TokenTypeUp])
	}

	// 计算DOWN的波动
	downMin, downMax, downOK := rangeCents(s.samples[domain.TokenTypeDown])
	if downOK {
		snapshot.DownMinCents = downMin
		snapshot.DownMaxCents = downMax
		snapshot.DownRangeCents = downMax - downMin
		snapshot.DownStable = snapshot.DownRangeCents <= s.MaxRangeCents
		snapshot.SampleCountDown = len(s.samples[domain.TokenTypeDown])
	}

	return snapshot
}

// checkAndResetAfterMerge 检测autoMerge完成并重置计数
func (s *Strategy) checkAndResetAfterMerge(market *domain.Market) {
	if market == nil || s.TradingService == nil {
		return
	}

	// 获取当前持仓
	positions := s.TradingService.GetOpenPositionsForMarket(market.Slug)
	var currentUpShares, currentDownShares float64
	for _, pos := range positions {
		if pos == nil || !pos.IsOpen() {
			continue
		}
		if pos.TokenType == domain.TokenTypeUp {
			currentUpShares += pos.TotalFilledSize
		} else if pos.TokenType == domain.TokenTypeDown {
			currentDownShares += pos.TotalFilledSize
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 如果上次检查时间超过5秒，更新基准值（避免频繁重置）
	if s.lastMergeCheckTime.IsZero() || time.Since(s.lastMergeCheckTime) > 5*time.Second {
		s.lastMergeCheckUpShares = currentUpShares
		s.lastMergeCheckDownShares = currentDownShares
		s.lastMergeCheckTime = time.Now()
		return
	}

	// 检测合并完成：持仓明显减少（至少减少0.1 shares，避免浮点误差）
	upDecreased := s.lastMergeCheckUpShares > 0 && currentUpShares < s.lastMergeCheckUpShares-0.1
	downDecreased := s.lastMergeCheckDownShares > 0 && currentDownShares < s.lastMergeCheckDownShares-0.1

	if upDecreased || downDecreased {
		// 合并完成，重置计数
		oldCount := s.triggersCountThisCycle
		s.triggersCountThisCycle = 0
		log.Infof("🔄 [%s] autoMerge完成，重置触发计数: %d -> 0 (UP: %.4f->%.4f, DOWN: %.4f->%.4f)",
			ID, oldCount,
			s.lastMergeCheckUpShares, currentUpShares,
			s.lastMergeCheckDownShares, currentDownShares)

		// 更新基准值
		s.lastMergeCheckUpShares = currentUpShares
		s.lastMergeCheckDownShares = currentDownShares
		s.lastMergeCheckTime = time.Now()
	}
}
