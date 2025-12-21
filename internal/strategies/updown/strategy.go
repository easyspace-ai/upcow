package updown

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

	lastMarketSlug  string
	tradedThisCycle bool
	lastTradeAt     time.Time
	firstSeenAt     time.Time
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }

func (s *Strategy) Defaults() error { return nil }
func (s *Strategy) Validate() error { return s.Config.Validate() }
func (s *Strategy) Initialize() error { return nil }

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil {
		return nil
	}
	if s.TradingService == nil {
		return nil
	}

	// 周期切换：重置 one-shot 状态
	if e.Market.Slug != "" && e.Market.Slug != s.lastMarketSlug {
		s.lastMarketSlug = e.Market.Slug
		s.tradedThisCycle = false
		s.firstSeenAt = time.Now()
	}
	if s.firstSeenAt.IsZero() {
		s.firstSeenAt = time.Now()
	}

	// 预热：避免刚连上 WS 的脏快照/假盘口
	if s.Config.WarmupMs > 0 && time.Since(s.firstSeenAt) < time.Duration(s.Config.WarmupMs)*time.Millisecond {
		return nil
	}

	if s.Config.OncePerCycle != nil && *s.Config.OncePerCycle && s.tradedThisCycle {
		return nil
	}
	if !s.lastTradeAt.IsZero() && time.Since(s.lastTradeAt) < 500*time.Millisecond {
		return nil
	}

	token := domain.TokenTypeUp
	assetID := e.Market.YesAssetID
	if s.Config.TokenType == "down" || s.Config.TokenType == "no" {
		token = domain.TokenTypeDown
		assetID = e.Market.NoAssetID
	}

	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	// 关键防线：用 bestBid/bestAsk 做盘口健康检查 + 价格上限
	bestBid, bestAsk, err := s.TradingService.GetBestPrice(orderCtx, assetID)
	if err != nil || bestAsk <= 0 || bestBid <= 0 {
		return nil
	}
	askCents := int(bestAsk*100 + 0.5)
	bidCents := int(bestBid*100 + 0.5)
	if askCents <= 0 || bidCents <= 0 {
		return nil
	}
	// 过滤极端 ask（例如 99c/100c 的假盘口或极差盘口）
	if s.Config.MaxBuyPriceCents > 0 && askCents > s.Config.MaxBuyPriceCents {
		return nil
	}
	spread := askCents - bidCents
	if spread < 0 {
		spread = -spread
	}
	if s.Config.MaxSpreadCents > 0 && spread > s.Config.MaxSpreadCents {
		return nil
	}

	price := domain.Price{Cents: askCents}

	req := execution.MultiLegRequest{
		Name:      "updown_once",
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
		log.Infof("✅ [updown] 已下单: token=%s price=%dc size=%.4f market=%s", token, price.Cents, s.Config.OrderSize, e.Market.Slug)
	}

	return nil
}

