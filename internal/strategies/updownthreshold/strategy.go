package updownthreshold

import (
	"context"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy：监控 up/down 两方向突破阈值买入，并在跌到 stopLoss 时止损卖出。
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	enteredThisCycle bool
	inPosition       bool
	positionToken    domain.TokenType

	lastUpCents   int
	lastDownCents int

	firstSeenAt   time.Time
	cycleStartAt  time.Time // 周期开始时间（用于延迟交易）
	lastActionAt  time.Time
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }

func (s *Strategy) Defaults() error   { return nil }
func (s *Strategy) Validate() error   { return s.Config.Validate() }
func (s *Strategy) Initialize() error { return nil }

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	log.Infof("✅ [%s] 策略已订阅价格变化事件 (session=%s)", ID, session.Name)
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, _ *domain.Market) {
	s.enteredThisCycle = false
	s.inPosition = false
	s.positionToken = ""
	s.lastUpCents = 0
	s.lastDownCents = 0
	s.firstSeenAt = time.Now()
	s.cycleStartAt = time.Now() // 记录周期开始时间
	s.lastActionAt = time.Time{}
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}
	if s.firstSeenAt.IsZero() {
		s.firstSeenAt = time.Now()
	}
	// 如果 cycleStartAt 未初始化（第一次启动时），也初始化为当前时间
	if s.cycleStartAt.IsZero() {
		s.cycleStartAt = time.Now()
	}
	// 预热：避免刚连上 WS 的脏快照/假盘口误触发
	if s.WarmupMs > 0 && time.Since(s.firstSeenAt) < time.Duration(s.WarmupMs)*time.Millisecond {
		return nil
	}
	// 轻量冷却：避免高频重复触发（止损/入场都适用）
	if !s.lastActionAt.IsZero() && time.Since(s.lastActionAt) < 250*time.Millisecond {
		return nil
	}

	token := e.TokenType
	if token != domain.TokenTypeUp && token != domain.TokenTypeDown {
		return nil
	}

	curCents := e.NewPrice.ToCents()
	prevCents := s.getLastCents(token)
	s.setLastCents(token, curCents)

	// 1) 已持仓：只对“持仓方向”的价格做止损判断
	if s.inPosition {
		if token != s.positionToken {
			return nil
		}
		return s.maybeStopLoss(ctx, e.Market, token)
	}

	// 2) 未持仓：判断是否允许入场
	if s.OncePerCycle != nil && *s.OncePerCycle && s.enteredThisCycle {
		return nil
	}
	if !s.tokenAllowed(token) {
		return nil
	}

	// 检查是否已过延迟交易时间
	delayedEntryDuration := time.Duration(s.DelayedEntryMinutes) * time.Minute
	canTradeAfterDelay := !s.cycleStartAt.IsZero() && time.Since(s.cycleStartAt) >= delayedEntryDuration

	if canTradeAfterDelay {
		// 延迟期后：只要价格 >= EntryCents 就买入（不需要"越过"逻辑）
		if curCents >= s.EntryCents {
			return s.enter(ctx, e.Market, token)
		}
		return nil
	}

	// 延迟期内：保持原来的"越过 entry"逻辑（必须从 <entry 跨到 >=entry）
	if prevCents <= 0 {
		return nil
	}
	if !(prevCents < s.EntryCents && curCents >= s.EntryCents) {
		return nil
	}
	return s.enter(ctx, e.Market, token)
}

func (s *Strategy) tokenAllowed(token domain.TokenType) bool {
	if s.TokenType == "" {
		return true
	}
	if s.TokenType == "up" || s.TokenType == "yes" {
		return token == domain.TokenTypeUp
	}
	if s.TokenType == "down" || s.TokenType == "no" {
		return token == domain.TokenTypeDown
	}
	return true
}

func (s *Strategy) getLastCents(token domain.TokenType) int {
	if token == domain.TokenTypeUp {
		return s.lastUpCents
	}
	return s.lastDownCents
}

func (s *Strategy) setLastCents(token domain.TokenType, cents int) {
	if token == domain.TokenTypeUp {
		s.lastUpCents = cents
		return
	}
	s.lastDownCents = cents
}

func (s *Strategy) enter(ctx context.Context, market *domain.Market, token domain.TokenType) error {
	assetID := market.GetAssetID(token)
	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	bestBid, bestAsk, err := s.TradingService.GetBestPrice(orderCtx, assetID)
	if err != nil || bestAsk <= 0 || bestBid <= 0 {
		return nil
	}

	askCents := int(bestAsk*100 + 0.5)
	bidCents := int(bestBid*100 + 0.5)
	if askCents <= 0 || bidCents <= 0 {
		return nil
	}
	if askCents < s.EntryCents {
		// 防御：即使事件价格已跨越，真实盘口 ask 可能尚未跨越
		return nil
	}
	if s.MaxBuyPriceCents > 0 && askCents > s.MaxBuyPriceCents {
		return nil
	}
	spread := askCents - bidCents
	if spread < 0 {
		spread = -spread
	}
	if s.MaxSpreadCents > 0 && spread > s.MaxSpreadCents {
		return nil
	}

	price := domain.Price{Pips: askCents * 100} // 1 cent = 100 pips
	req := execution.MultiLegRequest{
		Name:       "updownthreshold_entry",
		MarketSlug: market.Slug,
		Legs: []execution.LegIntent{{
			Name:      "buy",
			AssetID:   assetID,
			TokenType: token,
			Side:      types.SideBuy,
			Price:     price,
			Size:      s.OrderSize,
			OrderType: types.OrderTypeFAK,
		}},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}

	_, err = s.TradingService.ExecuteMultiLeg(orderCtx, req)
	if err != nil {
		return nil
	}

	s.inPosition = true
	s.positionToken = token
	s.enteredThisCycle = true
	s.lastActionAt = time.Now()
	log.Infof("✅ [%s] 入场买入: token=%s ask=%dc size=%.4f market=%s", ID, token, askCents, s.OrderSize, market.Slug)
	return nil
}

func (s *Strategy) maybeStopLoss(ctx context.Context, market *domain.Market, token domain.TokenType) error {
	assetID := market.GetAssetID(token)
	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	bestBid, bestAsk, err := s.TradingService.GetBestPrice(orderCtx, assetID)
	if err != nil || bestAsk <= 0 || bestBid <= 0 {
		return nil
	}

	bidCents := int(bestBid*100 + 0.5)
	askCents := int(bestAsk*100 + 0.5)
	if bidCents <= 0 || askCents <= 0 {
		return nil
	}

	// 止损：跌到 <= stopLossCents
	if bidCents > s.StopLossCents {
		return nil
	}

	spread := askCents - bidCents
	if spread < 0 {
		spread = -spread
	}
	if s.MaxSpreadCents > 0 && spread > s.MaxSpreadCents {
		// 盘口异常时不急着止损，避免用假 bid 卖出
		return nil
	}

	price := domain.Price{Pips: bidCents * 100}
	req := execution.MultiLegRequest{
		Name:       "updownthreshold_stoploss",
		MarketSlug: market.Slug,
		Legs: []execution.LegIntent{{
			Name:      "sell_stoploss",
			AssetID:   assetID,
			TokenType: token,
			Side:      types.SideSell,
			Price:     price,
			Size:      s.OrderSize,
			OrderType: types.OrderTypeFAK,
		}},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}

	_, err = s.TradingService.ExecuteMultiLeg(orderCtx, req)
	if err != nil {
		return nil
	}

	s.inPosition = false
	s.positionToken = ""
	s.lastActionAt = time.Now()
	log.Warnf("🛑 [%s] 触发止损卖出: token=%s bid=%dc size=%.4f market=%s", ID, token, bidCents, s.OrderSize, market.Slug)
	return nil
}
