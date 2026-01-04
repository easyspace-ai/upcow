package updown

import (
	"context"
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

var log = logrus.WithField("strategy", "updown")

func init() {
	bbgo.RegisterStrategy(ID, &Strategy{})
}

// Strategy（新架构简化版）：
// - 不使用 Executor/in-flight/内部 loop
// - 每个周期最多执行一次（默认），避免信号风暴
// - 所有下单统一走 TradingService.ExecuteMultiLeg（即使单腿）
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	tradedThisCycle bool
	lastTradeAt     time.Time
	firstSeenAt     time.Time

	autoMerge common.AutoMergeController
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }

func (s *Strategy) Defaults() error   { return nil }
func (s *Strategy) Validate() error   { return s.Config.Validate() }
func (s *Strategy) Initialize() error { return nil }

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
	log.Infof("✅ [updown] 策略已订阅价格变化事件 (session=%s)", session.Name)
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	<-ctx.Done()
	return ctx.Err()
}

// OnCycle 框架层周期切换回调：重置 one-shot 状态（策略无需在 OnPriceChanged 中手工对比 slug）。
func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, _ *domain.Market) {
	s.tradedThisCycle = false
	s.lastTradeAt = time.Time{}
	s.firstSeenAt = time.Now()
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e != nil {
		log.Debugf("🔔 [updown] OnPriceChanged 被调用: market=%v, token=%s, price=%.4f", 
			e.Market != nil, e.TokenType, e.NewPrice.ToDecimal())
	} else {
		log.Debugf("🔔 [updown] OnPriceChanged 被调用: event=nil")
	}

	if e == nil || e.Market == nil {
		log.Debugf("⏭️ [updown] 跳过：事件或市场为空")
		return nil
	}
	if s.TradingService == nil {
		log.Debugf("⏭️ [updown] 跳过：TradingService 为空")
		return nil
	}
	s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)
	log.Debugf("✅ [updown] 通过基础检查: market=%s, token=%s, price=%.4f", 
		e.Market.Slug, e.TokenType, e.NewPrice.ToDecimal())

	if s.firstSeenAt.IsZero() {
		s.firstSeenAt = time.Now()
	}
	log.Debugf("📊 [updown] 状态检查: tradedThisCycle=%v, oncePerCycle=%v, lastTradeAt=%v",
		s.tradedThisCycle, s.Config.OncePerCycle, s.lastTradeAt)

	// 预热：避免刚连上 WS 的脏快照/假盘口
	if s.Config.WarmupMs > 0 && time.Since(s.firstSeenAt) < time.Duration(s.Config.WarmupMs)*time.Millisecond {
		log.Debugf("⏭️ [updown] 跳过：预热期未结束 (market=%s, elapsed=%v, warmup=%dms)", 
			e.Market.Slug, time.Since(s.firstSeenAt), s.Config.WarmupMs)
		return nil
	}

	if s.Config.OncePerCycle != nil && *s.Config.OncePerCycle && s.tradedThisCycle {
		log.Debugf("⏭️ [updown] 跳过：已在本周期交易过 (market=%s)", e.Market.Slug)
		return nil
	}
	if !s.lastTradeAt.IsZero() && time.Since(s.lastTradeAt) < 500*time.Millisecond {
		log.Debugf("⏭️ [updown] 跳过：距离上次交易不到500ms (market=%s, elapsed=%v)", 
			e.Market.Slug, time.Since(s.lastTradeAt))
		return nil
	}
	log.Debugf("✅ [updown] 通过所有检查，准备下单")

	token := domain.TokenTypeUp
	assetID := e.Market.YesAssetID
	if s.Config.TokenType == "down" || s.Config.TokenType == "no" {
		token = domain.TokenTypeDown
		assetID = e.Market.NoAssetID
	}
	log.Debugf("🎯 [updown] 交易目标: token=%s, assetID=%s", token, assetID)

	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	// 关键防线：用 bestBid/bestAsk 做盘口健康检查 + 价格上限
	bestBid, bestAsk, err := s.TradingService.GetBestPrice(orderCtx, assetID)
	log.Debugf("💰 [updown] 获取盘口价格: bestBid=%.2f, bestAsk=%.2f, err=%v", bestBid, bestAsk, err)

	if err != nil || bestAsk <= 0 || bestBid <= 0 {
		log.Debugf("⏭️ [updown] 跳过：无法获取盘口价格 (market=%s, err=%v)", e.Market.Slug, err)
		return nil
	}
	askCents := int(bestAsk*100 + 0.5)
	bidCents := int(bestBid*100 + 0.5)
	if askCents <= 0 || bidCents <= 0 {
		log.Debugf("⏭️ [updown] 跳过：无效盘口价格 (market=%s, ask=%d, bid=%d)", e.Market.Slug, askCents, bidCents)
		return nil
	}
	// 过滤极端 ask（例如 99c/100c 的假盘口或极差盘口）
	if s.Config.MaxBuyPriceCents > 0 && askCents > s.Config.MaxBuyPriceCents {
		log.Debugf("⏭️ [updown] 跳过：买入价超过上限 (market=%s, ask=%d, max=%d)", 
			e.Market.Slug, askCents, s.Config.MaxBuyPriceCents)
		return nil
	}
	spread := askCents - bidCents
	if spread < 0 {
		spread = -spread
	}
	if s.Config.MaxSpreadCents > 0 && spread > s.Config.MaxSpreadCents {
		log.Debugf("⏭️ [updown] 跳过：价差过大 (market=%s, spread=%d, max=%d)", 
			e.Market.Slug, spread, s.Config.MaxSpreadCents)
		return nil
	}

price := domain.Price{Pips: askCents * 100} // 1 cent = 100 pips

	log.Debugf("📝 [updown] 准备下单: assetID=%s, price=%.4f, size=%.4f", assetID, price.ToDecimal(), s.Config.OrderSize)

	req := execution.MultiLegRequest{
		Name:       "updown_once",
		MarketSlug: e.Market.Slug,
		Legs: []execution.LegIntent{
			{
				Name:      "buy",
				AssetID:   assetID,
				TokenType: token,
				Side:      types.SideBuy,
				Price:     price,
				Size:      s.Config.OrderSize,
				OrderType: types.OrderTypeFAK,
			},
		},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}

	_, err = s.TradingService.ExecuteMultiLeg(orderCtx, req)
	if err == nil {
		s.tradedThisCycle = true
		s.lastTradeAt = time.Now()
		log.Infof("✅ [updown] 已下单: token=%s price=%.4f size=%.4f market=%s", token, price.ToDecimal(), s.Config.OrderSize, e.Market.Slug)
	}

	return nil
}
