package rangeboth

import (
	"context"
	"fmt"
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

	// 价格样本
	samples map[domain.TokenType][]priceSample

	// 市场过滤（防误交易）
	marketSlugPrefix string

	// 全局约束
	minOrderSize float64
	minShareSize float64

	// 市场精度（系统级配置）
	currentPrecision *MarketPrecisionInfo
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

func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, _ *domain.Market) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.firstSeenAt = time.Now()
	s.lastTriggerAt = time.Time{}
	s.triggersCountThisCycle = 0
	s.samples = make(map[domain.TokenType][]priceSample)
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}
	s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)

	if !s.shouldHandleMarketEvent(e.Market) {
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
	requireBoth := true
	if s.RequireBothSides != nil {
		requireBoth = *s.RequireBothSides
	}

	upStable := upOK && (upMax-upMin) <= s.MaxRangeCents
	downStable := downOK && (downMax-downMin) <= s.MaxRangeCents

	stable := false
	if requireBoth {
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
	s.lastTriggerAt = now
	s.triggersCountThisCycle++
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

	upPrice := domain.Price{Pips: upLimitC * 100}
	downPrice := domain.Price{Pips: downLimitC * 100}

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

	log.Infof("📏 [%s] 触发：UP[%dc..%dc] DOWN[%dc..%dc] window=%ds range<=%dc | place: UP@%dc DOWN@%dc (src=%s) market=%s",
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
		_, execErr := s.TradingService.ExecuteMultiLeg(orderCtx, req)
		if execErr != nil {
			if isFailSafeRefusal(execErr) {
				return nil
			}
			return nil
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
	if _, err := s.TradingService.PlaceOrder(orderCtx, o1); err != nil {
		if isFailSafeRefusal(err) {
			return nil
		}
		return nil
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
	if _, err := s.TradingService.PlaceOrder(orderCtx, o2); err != nil {
		// 第二笔失败不回滚第一笔（符合“顺序”语义）；后续可在这里加撤单/重试策略
		_ = err
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

func (s *Strategy) chooseSequentialOrder(legs []execution.LegIntent, upLimitCents int, downLimitCents int) (first *execution.LegIntent, second *execution.LegIntent) {
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
		th := s.SequentialPriorityPriceCents
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
