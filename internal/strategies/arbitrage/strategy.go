package arbitrage

import (
	"context"
	"math"
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

const ID = "arbitrage"

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy（新架构简化版，方向无关 complete-set）：
// - 当 yesAsk + noAsk <= 100 - ProfitTargetCents 时，买入等量 YES+NO（FAK）
// - 自动对冲由执行引擎处理
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	rounds     int
	lastAt     time.Time

	autoMerge common.AutoMergeController
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }
func (s *Strategy) Defaults() error { return nil }
func (s *Strategy) Validate() error { return s.Config.Validate() }
func (s *Strategy) Initialize() error { return nil }
func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) { session.OnPriceChanged(s) }
func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, _ *domain.Market) {
	s.rounds = 0
	s.lastAt = time.Time{}
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}
	// 可选：自动 merge（每策略独立配置；默认关闭）
	s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)
	m := e.Market
	if s.rounds >= s.MaxRoundsPerPeriod {
		return nil
	}
	if !s.lastAt.IsZero() && time.Since(s.lastAt) < time.Duration(s.CooldownMs)*time.Millisecond {
		return nil
	}

	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	mq, err := s.TradingService.GetMarketQuality(orderCtx, m, nil)
	if err != nil || mq == nil {
		return nil
	}
	// 统一盘口 gate：由服务层给出质量分/原因码
	if !mq.Tradable() {
		return nil
	}
	arb := mq.Arbitrage
	if arb == nil || arb.Type != "long" {
		return nil
	}
	// ProfitTargetCents：旧口径（0.01），换算成 pips（0.0001）
	targetProfitPips := s.ProfitTargetCents * 100
	if arb.ProfitPips < targetProfitPips {
		return nil
	}

	// 使用“有效买入价”（可能来自镜像侧的 bid）
	yesAsk := domain.Price{Pips: arb.BuyYesPips}
	noAsk := domain.Price{Pips: arb.BuyNoPips}

	size := s.OrderSize
	if yesAsk.ToDecimal() > 0 {
		size = math.Max(size, s.MinOrderSize/yesAsk.ToDecimal())
	}
	if noAsk.ToDecimal() > 0 {
		size = math.Max(size, s.MinOrderSize/noAsk.ToDecimal())
	}

	req := execution.MultiLegRequest{
		Name:      "arbitrage_complete_set",
		MarketSlug: m.Slug,
		Legs: []execution.LegIntent{
			{Name: "buy_yes", AssetID: m.YesAssetID, TokenType: domain.TokenTypeUp, Side: types.SideBuy, Price: yesAsk, Size: size, OrderType: types.OrderTypeFAK},
			{Name: "buy_no", AssetID: m.NoAssetID, TokenType: domain.TokenTypeDown, Side: types.SideBuy, Price: noAsk, Size: size, OrderType: types.OrderTypeFAK},
		},
		Hedge: execution.AutoHedgeConfig{Enabled: true, Delay: 2 * time.Second, SellPriceOffsetCents: 2, MinExposureToHedge: 1.0},
	}
	_, err = s.TradingService.ExecuteMultiLeg(orderCtx, req)
	if err == nil {
		s.rounds++
		s.lastAt = time.Now()
		log.Infof("🎯 [arbitrage] complete-set(effective): rounds=%d/%d profit=%dct cost=%.4f src=%s score=%d size=%.4f market=%s",
			s.rounds, s.MaxRoundsPerPeriod, arb.ProfitPips/100, float64(arb.LongCostPips)/10000.0, mq.Source, mq.Score, size, m.Slug)
	}
	return nil
}

