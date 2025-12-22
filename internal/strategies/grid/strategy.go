package grid

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/internal/strategies/orderutil"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/bbgo"
)

const ID = "grid"

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy：BTC 15m 网格策略（重构版）
//
// 核心设计：
// - OnPriceChanged: 直接处理价格事件，不通过信号机制
// - OnOrderUpdate: 订单更新入队，由 processOrders 处理
// - processOrders: 单 goroutine 处理订单更新，挂止盈单
// - 简化状态管理，避免竞态条件
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	// 订单更新队列（来自 session.OnOrderUpdate）
	orderC chan *domain.Order

	// 状态锁（保护共享状态）
	mu sync.RWMutex

	// 当前价格（按 tokenType）
	currentPrice map[domain.TokenType]*events.PriceChangedEvent

	// 周期管理
	guard        common.MarketSlugGuard
	firstSeenAt  time.Time
	lastSubmitAt time.Time
	entriesThisCycle int
	roundsCompleted  int
	flattenedThisCycle bool

	// 轮次跟踪
	currentRound      int                              // 当前轮次编号（从1开始）
	roundsThisCycle   int                              // 本周期已完成的轮次数
	roundEntryOrders  map[int]map[string]*trackedOrder // round -> orderID -> trackedOrder
	roundStartTime    map[int]time.Time                // round -> 轮次开始时间

	// 追踪我们自己提交的订单：orderID -> meta
	tracked map[string]*trackedOrder
	// 已经使用过的 gridLevel（防止重复"同一层级反复入场"）
	usedLevel map[domain.TokenType]map[int]bool
}

type trackedOrderKind string

const (
	kindEntry trackedOrderKind = "entry"
	kindExit  trackedOrderKind = "exit"
)

type trackedOrder struct {
	Kind            trackedOrderKind
	TokenType       domain.TokenType
	AssetID         string
	MarketSlug      string
	GridLevel       int
	Side            types.Side
	EntryPriceCents int
	TargetExitCents int
	RequestedSize   float64

	// 已处理的成交量（用于从 OrderUpdate 计算 delta）
	SeenFilled float64

	// 出场单是否已挂（部分成交时也会挂）
	ExitPlaced bool

	// 出场下单重试（应对"刚成交立刻卖但平台还没同步持仓"的延迟）
	ExitAttempts      int
	NextExitAttemptAt time.Time
	LastExitError     string

	// 轮次ID
	RoundID int
}

func (s *Strategy) ID() string      { return ID }
func (s *Strategy) Name() string    { return ID }
func (s *Strategy) Defaults() error { return nil }
func (s *Strategy) Validate() error { return s.Config.Validate() }

func (s *Strategy) Initialize() error {
	if s.orderC == nil {
		s.orderC = make(chan *domain.Order, 2048)
	}
	if s.currentPrice == nil {
		s.currentPrice = make(map[domain.TokenType]*events.PriceChangedEvent)
	}
	if s.tracked == nil {
		s.tracked = make(map[string]*trackedOrder)
	}
	if s.usedLevel == nil {
		s.usedLevel = make(map[domain.TokenType]map[int]bool)
	}
	if s.roundEntryOrders == nil {
		s.roundEntryOrders = make(map[int]map[string]*trackedOrder)
	}
	if s.roundStartTime == nil {
		s.roundStartTime = make(map[int]time.Time)
	}
	return nil
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnOrderUpdate(s)
	session.OnPriceChanged(s)
	log.Infof("✅ [grid] 策略已订阅订单更新和价格更新 (session=%s)", session.Name)
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	// 启动订单处理循环
	go s.processOrders(ctx)
	<-ctx.Done()
	return ctx.Err()
}

// OnPriceChanged 直接处理价格事件，不通过信号机制
func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil {
		return nil
	}

	// 更新当前价格
	s.mu.Lock()
	s.currentPrice[e.TokenType] = e
	currentMarket := e.Market
	s.mu.Unlock()

	log.Infof("📥 [grid] OnPriceChanged: token=%s price=%dc market=%s", 
		e.TokenType, e.NewPrice.Cents, currentMarket.Slug)

	// 直接处理价格事件
	log.Debugf("🔍 [grid] OnPriceChanged: 准备调用 processPrice token=%s price=%dc", e.TokenType, e.NewPrice.Cents)
	s.processPrice(ctx, e, currentMarket)
	log.Debugf("🔍 [grid] OnPriceChanged: processPrice 返回 token=%s price=%dc", e.TokenType, e.NewPrice.Cents)

	return nil
}

// OnOrderUpdate 订单更新入队
func (s *Strategy) OnOrderUpdate(_ context.Context, order *domain.Order) error {
	if order == nil {
		return nil
	}
	select {
	case s.orderC <- order:
		log.Debugf("📥 [grid] 收到订单更新: orderID=%s status=%s filledSize=%.4f marketSlug=%s",
			order.OrderID, order.Status, order.FilledSize, order.MarketSlug)
	default:
		log.Warnf("⚠️ [grid] 订单更新队列已满，丢弃: orderID=%s status=%s", order.OrderID, order.Status)
	}
	return nil
}

// processPrice 处理价格事件，检查是否需要入场
func (s *Strategy) processPrice(ctx context.Context, e *events.PriceChangedEvent, m *domain.Market) {
	log.Infof("🔍 [grid] processPrice: 开始处理 token=%s price=%dc market=%s", e.TokenType, e.NewPrice.Cents, m.Slug)
	if s.TradingService == nil {
		log.Warnf("⚠️ [grid] processPrice: TradingService 为 nil")
		return
	}

	now := time.Now()
	if e.Timestamp.After(now) {
		now = e.Timestamp
	}

	// 周期切换：重置状态
	s.mu.Lock()
	if s.guard.Update(m.Slug) {
		s.firstSeenAt = now
		s.lastSubmitAt = time.Time{}
		s.entriesThisCycle = 0
		s.roundsCompleted = 0
		s.flattenedThisCycle = false
		s.tracked = make(map[string]*trackedOrder)
		s.usedLevel = make(map[domain.TokenType]map[int]bool)
		s.currentRound = 0
		s.roundsThisCycle = 0
		s.roundEntryOrders = make(map[int]map[string]*trackedOrder)
		s.roundStartTime = make(map[int]time.Time)
		log.Infof("🔄 [grid] 周期切换，重置状态: market=%s", m.Slug)
	}
	if s.firstSeenAt.IsZero() {
		s.firstSeenAt = now
	}
	s.mu.Unlock()

	// 预热
	if s.WarmupMs > 0 && now.Sub(s.firstSeenAt) < time.Duration(s.WarmupMs)*time.Millisecond {
		log.Debugf("🔍 [grid] processPrice: 预热中，跳过 token=%s price=%dc", e.TokenType, e.NewPrice.Cents)
		return
	}

	// 先处理订单更新（止盈/清理必须不受 cooldown/stopNewEntries 等限制）
	s.drainOrderUpdates(ctx, m)
	// 出场重试：即使没有新的订单更新，也要按计划重试挂止盈
	s.retryPendingExits(ctx, m)
	// 轮次推进：当上一轮所有订单都结束后，按配置决定是否开启下一轮
	s.maybeAdvanceRound(m.Slug)

	// 冷却 + 入场次数上限
	s.mu.RLock()
	lastSubmitAt := s.lastSubmitAt
	entriesThisCycle := s.entriesThisCycle
	currentRound := s.currentRound
	s.mu.RUnlock()

	if !lastSubmitAt.IsZero() && now.Sub(lastSubmitAt) < time.Duration(s.CooldownMs)*time.Millisecond {
		log.Debugf("🔍 [grid] processPrice: 冷却中，跳过 token=%s price=%dc", e.TokenType, e.NewPrice.Cents)
		return
	}
	if entriesThisCycle >= s.MaxEntriesPerPeriod {
		log.Infof("🔍 [grid] processPrice: 达到最大入场次数限制，跳过 token=%s price=%dc entriesThisCycle=%d", e.TokenType, e.NewPrice.Cents, entriesThisCycle)
		return
	}
	// 轮次上限：达到上限后不再新增入场（但仍会继续处理订单更新）
	if s.MaxRoundsPerPeriod > 0 && s.roundsCompleted >= s.MaxRoundsPerPeriod {
		return
	}

	// 轮次控制：检查是否可以开始新轮次
	waitForComplete := s.WaitForRoundCompleteEnabled() || currentRound == 0
	if !s.canStartNewRoundWithWait(m.Slug, waitForComplete, now) {
		if currentRound > 0 && waitForComplete && !s.isRoundComplete(currentRound, now) {
			log.Debugf("🔍 [grid] processPrice: 等待当前轮次完成 (round=%d)", currentRound)
		}
		return
	}

	// 如果需要开始新轮次
	s.mu.Lock()
	if s.currentRound == 0 {
		s.currentRound = 1
		s.roundEntryOrders[s.currentRound] = make(map[string]*trackedOrder)
		s.roundStartTime[s.currentRound] = now
		log.Infof("🔄 [grid] 开始第一轮: round=1 market=%s", m.Slug)
	} else if waitForComplete && s.isRoundComplete(s.currentRound, now) {
		s.completeRound(s.currentRound, m.Slug)
	}
	currentRound = s.currentRound
	s.mu.Unlock()

	// 周期后段控制：清仓/停止新增
	if m.Timestamp > 0 {
		elapsed := now.Unix() - m.Timestamp
		remain := int64(900) - elapsed

		// 6.1 清仓：不赌方向 —— 周期结束前把本周期持仓出清
		if !s.flattenedThisCycle {
			flattenSeconds := s.flattenSecondsBeforeEnd()
			if flattenSeconds > 0 && remain <= int64(flattenSeconds) {
				s.flattenPositions(ctx, m, remain)
				s.flattenedThisCycle = true
				return
			}
		}

		// 6.2 停止新增入场
		if s.StopNewEntriesSeconds > 0 && remain <= int64(s.StopNewEntriesSeconds) {
			return
		}
	}

	// 冻结检测：任一 side 进入极端共识区间则冻结（不再新增）
	if s.isFrozenPrice(e.NewPrice.Cents) {
		log.Infof("🔍 [grid] processPrice: 价格冻结，跳过 token=%s price=%dc", e.TokenType, e.NewPrice.Cents)
		if s.CancelEntryOrdersOnFreeze {
			s.cancelAllEntryOrders(ctx, m.Slug)
		}
		return
	}

	// 限制并发入场单数量
	if s.countOpenEntryOrders(m.Slug) >= s.MaxOpenEntryOrders {
		return
	}

	// 计算网格层级列表
	levels := s.gridLevels()
	if len(levels) < 2 {
		log.Infof("🔍 [grid] processPrice: 网格层级不足 (len=%d)", len(levels))
		return
	}

	// 选择要交易的 token
	tokenTargets := s.targetTokens()
	if len(tokenTargets) == 0 {
		log.Infof("🔍 [grid] processPrice: 无目标 token")
		return
	}
	// 10.1 库存中性 gating：净敞口过大时，只允许补“较少的一侧”
	tokenTargets = s.applyInventoryNeutrality(m.Slug, tokenTargets)
	if len(tokenTargets) == 0 {
		return
	}

	// 检查当前 token 是否在目标列表中
	tokenInTarget := false
	for _, tt := range tokenTargets {
		if tt == e.TokenType {
			tokenInTarget = true
			break
		}
	}
	if !tokenInTarget {
		return
	}

	// 获取资产 ID
	var assetID string
	if e.TokenType == domain.TokenTypeUp {
		assetID = m.YesAssetID
	} else {
		assetID = m.NoAssetID
	}
	if assetID == "" {
		return
	}

	priceCents := e.NewPrice.Cents
		level := nearestLowerOrEqual(levels, priceCents)
		if level == nil {
			log.Infof("🔍 [grid] processPrice: token=%s price=%dc 无匹配层级 (levels=%v)", e.TokenType, priceCents, levels)
			return
		}
		log.Infof("🔍 [grid] processPrice: token=%s price=%dc 匹配到层级=%dc", e.TokenType, priceCents, *level)

		// 已在该层级入场过：跳过（本周期内不重复）
		if s.isLevelUsed(e.TokenType, *level) {
			log.Debugf("🔍 [grid] processPrice: 层级已使用，跳过 token=%s price=%dc level=%dc", e.TokenType, e.NewPrice.Cents, *level)
			return
		}

		// 盘口 quote：要求 bestAsk <= level + slippage 才入场
		maxCents := *level + s.GridLevelSlippageCents
		if maxCents > 99 {
			maxCents = 99
		}
	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	bestAsk, size, skipped, _, _, _, _, err := common.QuoteAndAdjustBuy(
		orderCtx,
		s.TradingService,
		assetID,
		maxCents,
		s.OrderSize,
		s.MinOrderSize,
		s.AutoAdjustSize,
		s.MaxSizeAdjustRatio,
	)
	cancel()
	if err != nil || skipped || bestAsk.Cents <= 0 || size <= 0 {
		if err != nil {
			log.Infof("🔍 [grid] processPrice: token=%s level=%dc quote失败: %v", e.TokenType, *level, err)
		} else if skipped {
			log.Debugf("🔍 [grid] processPrice: token=%s level=%dc bestAsk=%dc 跳过 (skipped=true, bestAsk>%dc?)", e.TokenType, *level, bestAsk.Cents, maxCents)
		} else {
			log.Debugf("🔍 [grid] processPrice: token=%s level=%dc bestAsk=%dc size=%.4f 无效", e.TokenType, *level, bestAsk.Cents, size)
		}
		return
	}

	// 额外检查：bestAsk 应该在合理范围内
	if bestAsk.Cents > maxCents {
		log.Debugf("🔍 [grid] processPrice: token=%s level=%dc bestAsk=%dc 超出允许范围 (max=%dc)", e.TokenType, *level, bestAsk.Cents, maxCents)
		return
	}

	targetExit := bestAsk.Cents + s.ProfitTargetCents
	if targetExit > 99 {
		targetExit = 99
	}

	req := execution.MultiLegRequest{
		Name:      fmt.Sprintf("grid_entry_%s_%dc", strings.ToLower(string(e.TokenType)), *level),
		MarketSlug: m.Slug,
		Legs: []execution.LegIntent{{
			Name:      "buy",
			AssetID:   assetID,
			TokenType: e.TokenType,
			Side:      types.SideBuy,
			Price:     bestAsk,
			Size:      size,
			OrderType: types.OrderTypeFAK,
		}},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}

	orderCtx2, cancel2 := context.WithTimeout(ctx, 25*time.Second)
	created, err := s.TradingService.ExecuteMultiLeg(orderCtx2, req)
	cancel2()
	if err != nil {
		log.Errorf("❌ [grid] 入场失败: token=%s level=%dc bestAsk=%dc size=%.4f error=%v", 
			e.TokenType, *level, bestAsk.Cents, size, err)
		return
	}

	if len(created) == 0 || created[0] == nil || created[0].OrderID == "" {
		log.Warnf("⚠️ [grid] 入场返回空订单: token=%s level=%dc", e.TokenType, *level)
		return
	}

	oid := created[0].OrderID
	log.Infof("📌 [grid] 入场: token=%s level=%dc price=%dc size=%.4f orderID=%s market=%s round=%d",
		e.TokenType, *level, bestAsk.Cents, size, oid, m.Slug, currentRound)

	// 标记层级已使用
	s.mu.Lock()
	if s.usedLevel[e.TokenType] == nil {
		s.usedLevel[e.TokenType] = make(map[int]bool)
	}
	s.usedLevel[e.TokenType][*level] = true

	// 追踪订单
	s.tracked[oid] = &trackedOrder{
		Kind:            kindEntry,
		TokenType:       e.TokenType,
		AssetID:         assetID,
		MarketSlug:      m.Slug,
		GridLevel:       *level,
		Side:            types.SideBuy,
		EntryPriceCents: bestAsk.Cents,
		TargetExitCents: targetExit,
		RequestedSize:   size,
		SeenFilled:      0,
		ExitPlaced:      false,
		RoundID:         currentRound,
	}
	s.roundEntryOrders[currentRound][oid] = s.tracked[oid]
	s.lastSubmitAt = now
	s.entriesThisCycle++
	s.mu.Unlock()
}

// processOrders 处理订单更新，挂止盈单
func (s *Strategy) processOrders(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case order := <-s.orderC:
			if order == nil || order.OrderID == "" {
				continue
			}
			s.handleOrderUpdate(ctx, order)
		}
	}
}

// drainOrderUpdates 处理队列中的所有订单更新（非阻塞）
func (s *Strategy) drainOrderUpdates(ctx context.Context, m *domain.Market) {
	for {
		select {
		case order := <-s.orderC:
			if order == nil || order.OrderID == "" {
				continue
			}
			// 只处理当前市场的订单
			if m != nil && m.Slug != "" && order.MarketSlug != "" && order.MarketSlug != m.Slug {
				continue
			}
			s.handleOrderUpdate(ctx, order)
		default:
			return
		}
	}
}

// handleOrderUpdate 处理单个订单更新
func (s *Strategy) handleOrderUpdate(ctx context.Context, order *domain.Order) {
	s.mu.RLock()
	meta := s.tracked[order.OrderID]
	s.mu.RUnlock()

	if meta == nil {
		return
	}

	// 严格隔离：只处理本周期订单
	if meta.MarketSlug != "" && order.MarketSlug != "" && meta.MarketSlug != order.MarketSlug {
		return
	}

	// 更新 delta filled
	s.mu.Lock()
	if order.FilledSize > meta.SeenFilled {
		meta.SeenFilled = order.FilledSize
	}
	s.mu.Unlock()

	// 入场单：只要出现"有成交且尚未挂止盈"，就挂止盈
	if meta.Kind == kindEntry && !meta.ExitPlaced && order.FilledSize > 0 {
		exitSize := order.FilledSize
		if exitSize <= 0 {
			return
		}
		target := domain.Price{Cents: meta.TargetExitCents}
		req := execution.MultiLegRequest{
			Name:      fmt.Sprintf("grid_exit_%s_%dc", strings.ToLower(string(meta.TokenType)), meta.GridLevel),
			MarketSlug: order.MarketSlug,
			Legs: []execution.LegIntent{{
				Name:      "sell_tp",
				AssetID:   meta.AssetID,
				TokenType: meta.TokenType,
				Side:      types.SideSell,
				Price:     target,
				Size:      exitSize,
				OrderType: types.OrderTypeGTC,
			}},
			Hedge: execution.AutoHedgeConfig{Enabled: false},
		}
		orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		created, err := s.TradingService.ExecuteMultiLeg(orderCtx, req)
		cancel()
		if err == nil && len(created) > 0 && created[0] != nil && created[0].OrderID != "" {
			s.mu.Lock()
			meta.ExitPlaced = true
			// 追踪出场单
			s.tracked[created[0].OrderID] = &trackedOrder{
				Kind:            kindExit,
				TokenType:       meta.TokenType,
				AssetID:         meta.AssetID,
				MarketSlug:      order.MarketSlug,
				GridLevel:       meta.GridLevel,
				Side:            types.SideSell,
				EntryPriceCents: meta.EntryPriceCents,
				TargetExitCents: meta.TargetExitCents,
				RequestedSize:   exitSize,
			}
			s.mu.Unlock()
			log.Infof("🎯 [grid] 挂止盈: token=%s entry=%dc tp=%dc size=%.4f market=%s",
				meta.TokenType, meta.EntryPriceCents, meta.TargetExitCents, exitSize, order.MarketSlug)
		} else {
			log.Errorf("❌ [grid] 挂止盈失败: orderID=%s entryPrice=%dc targetPrice=%dc exitSize=%.4f error=%v",
				order.OrderID, meta.EntryPriceCents, meta.TargetExitCents, exitSize, err)
		}
	}

	// 清理：已结束的订单就不再追踪
	if order.Status == domain.OrderStatusFilled || order.Status == domain.OrderStatusCanceled || order.Status == domain.OrderStatusFailed {
		s.mu.Lock()
		meta := s.tracked[order.OrderID]
		// 如果是当前轮次的入场订单，从轮次跟踪中移除
		for roundID, roundOrders := range s.roundEntryOrders {
			if _, exists := roundOrders[order.OrderID]; exists {
				delete(roundOrders, order.OrderID)
				// 检查轮次是否完成
				if s.isRoundComplete(roundID, time.Now()) {
					s.completeRound(roundID, order.MarketSlug)
				}
				break
			}
		}
		// 关键：让网格"多轮次"跑起来 —— 当一个层级的订单生命周期结束后，释放该层级可再次入场。
		// - 入场单（FAK）如果没成交就结束：应释放 usedLevel（否则会永久跳过该层级）
		// - 止盈单（GTC/FAK）成交：代表该层级完成一轮获利，释放 usedLevel 以便再次在同层级循环
		// - 止盈单取消/失败：通常仍持仓未了结，避免加倍暴露，因此不自动释放
		if meta != nil {
			if meta.Kind == kindEntry {
				// entry 生命周期结束（常见：FAK 未成交 -> canceled/failed）
				if order.FilledSize <= 0 && (order.Status == domain.OrderStatusCanceled || order.Status == domain.OrderStatusFailed) {
					s.releaseLevel(meta.TokenType, meta.GridLevel)
				}
			} else if meta.Kind == kindExit {
				// exit 成交：完成一轮
				if order.Status == domain.OrderStatusFilled {
					s.releaseLevel(meta.TokenType, meta.GridLevel)
				}
			}
		}
		delete(s.tracked, order.OrderID)
		s.mu.Unlock()
	}
}

// isRoundComplete 检查轮次是否完成
func (s *Strategy) isRoundComplete(roundID int, now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	roundOrders := s.roundEntryOrders[roundID]
	if len(roundOrders) == 0 {
		// 空轮次：检查超时
		if s.EmptyRoundTimeoutSeconds > 0 && !s.roundStartTime[roundID].IsZero() {
			if now.Sub(s.roundStartTime[roundID]) >= time.Duration(s.EmptyRoundTimeoutSeconds)*time.Second {
				log.Infof("✅ [grid] 空轮次超时完成: round=%d", roundID)
				return true
			}
		}
		return false
	}

	// 检查所有入场订单是否都已挂止盈
	for orderID := range roundOrders {
		meta, exists := s.tracked[orderID]
		if !exists {
			continue
		}
		if !meta.ExitPlaced {
			return false
		}
	}
	return true
}

// completeRound 完成轮次并开始新轮次
func (s *Strategy) completeRound(roundID int, marketSlug string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if roundID != s.currentRound {
		return
	}

	s.roundsThisCycle++
	log.Infof("✅ [grid] 轮次完成: round=%d roundsThisCycle=%d market=%s", roundID, s.roundsThisCycle, marketSlug)

	// 开始新轮次
	s.currentRound++
	s.roundEntryOrders[s.currentRound] = make(map[string]*trackedOrder)
	s.roundStartTime[s.currentRound] = time.Now()
	// 清空已使用的层级
	s.usedLevel = make(map[domain.TokenType]map[int]bool)
	log.Infof("🔄 [grid] 开始新轮次: round=%d market=%s", s.currentRound, marketSlug)
}

func (s *Strategy) retryPendingExits(ctx context.Context, m *domain.Market) {
	if s == nil || s.TradingService == nil || m == nil {
		return
	}
	now := time.Now()
	for _, meta := range s.tracked {
		if meta == nil {
			continue
		}
		if meta.Kind != kindEntry || meta.ExitPlaced {
			continue
		}
		if meta.MarketSlug != "" && m.Slug != "" && meta.MarketSlug != m.Slug {
			continue
		}
		if meta.SeenFilled <= 0 {
			continue
		}
		if !meta.NextExitAttemptAt.IsZero() && now.Before(meta.NextExitAttemptAt) {
			continue
		}
		s.tryPlaceExit(ctx, m, meta)
	}
}

func (s *Strategy) shouldRetryExit(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// 覆盖常见“持仓/余额尚未同步”的报错关键词（交易所/网关差异较大，宁可宽松一点）
	for _, kw := range []string{
		"position",
		"balance",
		"insufficient",
		"not enough",
		"available",
		"allowance",
		"holdings",
		"shares",
		"amount",
	} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	// 默认也重试（但由退避控制频率），避免因为关键词不匹配而永远不挂止盈
	return true
}

func (s *Strategy) scheduleExitRetry(ctx context.Context, meta *trackedOrder) {
	if meta == nil {
		return
	}
	// 指数退避：200ms * 2^k，封顶 8s
	k := meta.ExitAttempts
	if k < 0 {
		k = 0
	}
	delay := 200 * time.Millisecond * time.Duration(1<<minInt(k, 6)) // 200ms..12.8s-ish，后面再 cap
	if delay > 8*time.Second {
		delay = 8 * time.Second
	}
	meta.NextExitAttemptAt = time.Now().Add(delay)

	// 重试会在下次 processPrice 调用时通过 retryPendingExits 自动触发
	// 不需要额外的信号机制
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s *Strategy) tryPlaceExit(ctx context.Context, m *domain.Market, meta *trackedOrder) {
	if s == nil || s.TradingService == nil || m == nil || meta == nil {
		return
	}

	// 以最新累计成交量为准（可能从 partial -> filled）
	exitSize := meta.SeenFilled
	if exitSize <= 0 {
		return
	}

	target := domain.Price{Cents: meta.TargetExitCents}
	exitOrderType := types.OrderTypeGTC
	// 保护：很小的 size 用 FAK 兜底（避免交易所最小 shares 约束导致挂单被拒）
	if exitSize < 5.0 {
		exitOrderType = types.OrderTypeFAK
	}

	req := execution.MultiLegRequest{
		Name:      fmt.Sprintf("grid_exit_%s_%dc", strings.ToLower(string(meta.TokenType)), meta.GridLevel),
		MarketSlug: m.Slug,
		Legs: []execution.LegIntent{{
			Name:      "sell_tp",
			AssetID:   meta.AssetID,
			TokenType: meta.TokenType,
			Side:      types.SideSell,
			Price:     target,
			Size:      exitSize,
			OrderType: exitOrderType,
		}},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}

	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	created, err := s.TradingService.ExecuteMultiLeg(orderCtx, req)
	cancel()
	if err != nil || len(created) == 0 || created[0] == nil || created[0].OrderID == "" {
		meta.ExitAttempts++
		if err != nil {
			meta.LastExitError = err.Error()
		} else {
			meta.LastExitError = "unknown exit order failure"
		}

		if s.shouldRetryExit(err) {
			log.Warnf("⏳ [grid] 挂止盈失败，准备重试: token=%s level=%dc tp=%dc size=%.4f attempts=%d err=%s",
				meta.TokenType, meta.GridLevel, meta.TargetExitCents, exitSize, meta.ExitAttempts, meta.LastExitError)
			s.scheduleExitRetry(ctx, meta)
		}
		return
	}

	// 成功：标记并追踪出场单
	meta.ExitPlaced = true
	meta.NextExitAttemptAt = time.Time{}
	oid := created[0].OrderID
	s.tracked[oid] = &trackedOrder{
		Kind:            kindExit,
		TokenType:       meta.TokenType,
		AssetID:         meta.AssetID,
		MarketSlug:      m.Slug,
		GridLevel:       meta.GridLevel,
		Side:            types.SideSell,
		EntryPriceCents: meta.EntryPriceCents,
		TargetExitCents: meta.TargetExitCents,
		RequestedSize:   exitSize,
	}
	log.Infof("🎯 [grid] 挂止盈成功: token=%s entry=%dc tp=%dc size=%.4f orderType=%s market=%s",
		meta.TokenType, meta.EntryPriceCents, meta.TargetExitCents, exitSize, exitOrderType, m.Slug)
}

func (s *Strategy) applyInventoryNeutrality(marketSlug string, targets []domain.TokenType) []domain.TokenType {
	if s == nil || s.TradingService == nil {
		return targets
	}
	if s.MaxNetExposureShares <= 0 {
		return targets
	}
	if !s.EnableDoubleSide {
		// 单向模式下不做净敞口限制（否则会把策略锁死）
		return targets
	}

	upSize, downSize := s.currentInventoryShares(marketSlug)
	net := upSize - downSize
	if math.Abs(net) < s.MaxNetExposureShares {
		return targets
	}

	need := domain.TokenTypeDown
	if net < 0 {
		need = domain.TokenTypeUp
	}

	out := make([]domain.TokenType, 0, 1)
	for _, tt := range targets {
		if tt == need {
			out = append(out, tt)
		}
	}
	return out
}

func (s *Strategy) currentInventoryShares(marketSlug string) (upSize float64, downSize float64) {
	if s == nil || s.TradingService == nil {
		return 0, 0
	}
	for _, p := range s.TradingService.GetOpenPositionsForMarket(marketSlug) {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}
		if p.TokenType == domain.TokenTypeUp {
			upSize += p.Size
		} else if p.TokenType == domain.TokenTypeDown {
			downSize += p.Size
		}
	}
	return upSize, downSize
}

func (s *Strategy) flattenSecondsBeforeEnd() int {
	if s == nil || s.FlattenSecondsBeforeEnd == nil {
		return 0
	}
	if *s.FlattenSecondsBeforeEnd <= 0 {
		return 0
	}
	return *s.FlattenSecondsBeforeEnd
}

func (s *Strategy) flattenPositions(ctx context.Context, m *domain.Market, remain int64) {
	if s == nil || s.TradingService == nil || m == nil {
		return
	}
	// 先撤掉所有入场单，避免清仓时又被入场单“补回去”
	cancelCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	s.cancelAllEntryOrders(cancelCtx, m.Slug)
	cancel()

	// 汇总本周期持仓（按 tokenType）
	upSize, downSize := s.currentInventoryShares(m.Slug)
	if upSize <= 0 && downSize <= 0 {
		log.Infof("🧹 [grid] 清仓窗口到达(remain=%ds)，但无持仓需要处理: market=%s", remain, m.Slug)
		return
	}

	log.Warnf("🧹 [grid] 清仓窗口到达(remain=%ds)：开始出清持仓 up=%.4f down=%.4f market=%s",
		remain, upSize, downSize, m.Slug)

	// 逐边用 FAK 快速卖出（不赌方向：宁可小滑点，也不要带仓进结算）
	sellOne := func(tt domain.TokenType, assetID string, size float64) {
		if size <= 0 || assetID == "" {
			return
		}
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		bestBid, err := orderutil.QuoteSellPrice(ctx, s.TradingService, assetID, 0)
		if err != nil || bestBid.Cents <= 0 {
			return
		}
		req := execution.MultiLegRequest{
			Name:      fmt.Sprintf("grid_flatten_%s", strings.ToLower(string(tt))),
			MarketSlug: m.Slug,
			Legs: []execution.LegIntent{{
				Name:      "sell_flatten",
				AssetID:   assetID,
				TokenType: tt,
				Side:      types.SideSell,
				Price:     bestBid,
				Size:      size,
				OrderType: types.OrderTypeFAK,
			}},
			Hedge: execution.AutoHedgeConfig{Enabled: false},
		}
		_, _ = s.TradingService.ExecuteMultiLeg(ctx, req)
	}

	sellOne(domain.TokenTypeUp, m.YesAssetID, upSize)
	sellOne(domain.TokenTypeDown, m.NoAssetID, downSize)
}

func (s *Strategy) maybeAdvanceRound(marketSlug string) {
	if s == nil || s.TradingService == nil {
		return
	}
	// 没有用过任何层级，说明还没开始一轮
	if !s.hasAnyUsedLevel() {
		return
	}
	// 等待本轮完全结束（默认 true）
	if s.WaitForRoundCompleteEnabled() && !s.isMarketRoundComplete(marketSlug) {
		return
	}
	// 本轮已结束：清空 usedLevel，让下一轮可以复用层级
	// 注意：roundsCompleted 表示“已完成轮次”计数；到达上限后，入场逻辑会被短路。
	s.roundsCompleted++
	s.usedLevel = make(map[domain.TokenType]map[int]bool)
	log.Infof("🔁 [grid] 本轮已完成，开始下一轮: completed=%d market=%s", s.roundsCompleted, marketSlug)
}

func (s *Strategy) hasAnyUsedLevel() bool {
	for _, m := range s.usedLevel {
		if len(m) > 0 {
			return true
		}
	}
	return false
}

func (s *Strategy) isMarketRoundComplete(marketSlug string) bool {
	// round complete 的定义：没有任何"我们追踪的"入场/止盈单仍处于 open/pending/partial
	orders := s.TradingService.GetActiveOrders()
	for _, o := range orders {
		if o == nil || o.OrderID == "" {
			continue
		}
		if marketSlug != "" && o.MarketSlug != "" && o.MarketSlug != marketSlug {
			continue
		}
		meta := s.tracked[o.OrderID]
		if meta == nil {
			continue
		}
		if meta.Kind != kindEntry && meta.Kind != kindExit {
			continue
		}
		if o.Status == domain.OrderStatusOpen || o.Status == domain.OrderStatusPartial || o.Status == domain.OrderStatusPending {
			return false
		}
	}
	return true
}

// canStartNewRoundWithWait 检查是否可以开始新轮次
func (s *Strategy) canStartNewRoundWithWait(marketSlug string, waitForComplete bool, now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.currentRound == 0 {
		return true
	}

	if s.MaxRoundsPerPeriod > 0 && s.roundsThisCycle >= s.MaxRoundsPerPeriod {
		log.Debugf("🔍 [grid] 达到最大轮次限制 (roundsThisCycle=%d, maxRoundsPerPeriod=%d)", s.roundsThisCycle, s.MaxRoundsPerPeriod)
		return false
	}

	if waitForComplete {
		return s.isRoundComplete(s.currentRound, now)
	}

	return true
}

// isLevelUsed 检查层级是否已使用
func (s *Strategy) isLevelUsed(tokenType domain.TokenType, level int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.usedLevel[tokenType] != nil && s.usedLevel[tokenType][level]
}

// countOpenEntryOrders 统计当前市场的开放入场单数量
func (s *Strategy) countOpenEntryOrders(marketSlug string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, meta := range s.tracked {
		if meta.Kind == kindEntry && meta.MarketSlug == marketSlug && !meta.ExitPlaced {
			count++
		}
	}
	return count
}

// cancelAllEntryOrders 取消所有入场单
func (s *Strategy) cancelAllEntryOrders(ctx context.Context, marketSlug string) {
	s.mu.RLock()
	orderIDs := make([]string, 0)
	for oid, meta := range s.tracked {
		if meta.Kind == kindEntry && meta.MarketSlug == marketSlug && !meta.ExitPlaced {
			orderIDs = append(orderIDs, oid)
		}
	}
	s.mu.RUnlock()

	for _, oid := range orderIDs {
		orderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := s.TradingService.CancelOrder(orderCtx, oid)
		cancel()
		if err != nil {
			log.Errorf("❌ [grid] 取消入场单失败: orderID=%s error=%v", oid, err)
		} else {
			log.Infof("✅ [grid] 已取消入场单: orderID=%s", oid)
		}
	}
}

// gridLevels 返回网格层级列表（排序后）
func (s *Strategy) gridLevels() []int {
	levels := make([]int, len(s.GridLevels))
	copy(levels, s.GridLevels)
	sort.Ints(levels)
	return levels
}

// targetTokens 返回要交易的 token 列表
func (s *Strategy) targetTokens() []domain.TokenType {
	if s.EnableDoubleSide {
		return []domain.TokenType{domain.TokenTypeUp, domain.TokenTypeDown}
	}
	return []domain.TokenType{domain.TokenTypeUp}
}

// isFrozenPrice 检查价格是否在冻结区间
func (s *Strategy) isFrozenPrice(cents int) bool {
	return cents <= 1 || cents >= 99
}

func (s *Strategy) releaseLevel(tt domain.TokenType, level int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.usedLevel[tt]
	if m == nil {
		return
	}
	delete(m, level)
}

// nearestLowerOrEqual 找到 <= priceCents 的最大层级
func nearestLowerOrEqual(levels []int, priceCents int) *int {
	var best *int
	for i := range levels {
		if levels[i] <= priceCents {
			if best == nil || levels[i] > *best {
				best = &levels[i]
			}
		}
	}
	return best
}
