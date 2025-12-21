package pairedtrading

import (
	"context"
	"math"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/strategies/orderutil"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/sirupsen/logrus"
)

const ID = "pairedtrading"

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy（新架构简化版）：
// - 与 pairlock 类似，但保留独立配置入口
// - 触发条件：yesAsk + noAsk <= 100 - ProfitTargetCents
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	lastMarket string
	rounds     int
	lastAt     time.Time
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

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}
	m := e.Market
	if m.Slug != "" && m.Slug != s.lastMarket {
		s.lastMarket = m.Slug
		s.rounds = 0
		s.lastAt = time.Time{}
	}
	if s.rounds >= s.MaxRoundsPerPeriod {
		return nil
	}
	if !s.lastAt.IsZero() && time.Since(s.lastAt) < time.Duration(s.CooldownMs)*time.Millisecond {
		return nil
	}

	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	yesAsk, err := orderutil.QuoteBuyPrice(orderCtx, s.TradingService, m.YesAssetID, 0)
	if err != nil {
		return nil
	}
	noAsk, err := orderutil.QuoteBuyPrice(orderCtx, s.TradingService, m.NoAssetID, 0)
	if err != nil {
		return nil
	}
	total := yesAsk.Cents + noAsk.Cents
	maxTotal := 100 - s.ProfitTargetCents
	if total > maxTotal {
		return nil
	}

	size := s.OrderSize
	if yesAsk.ToDecimal() > 0 {
		size = math.Max(size, s.MinOrderSize/yesAsk.ToDecimal())
	}
	if noAsk.ToDecimal() > 0 {
		size = math.Max(size, s.MinOrderSize/noAsk.ToDecimal())
	}

	req := execution.MultiLegRequest{
		Name:      "pairedtrading_complete_set",
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
		log.Infof("🎯 [pairedtrading] complete-set: rounds=%d/%d total=%dc maxTotal=%dc size=%.4f market=%s",
			s.rounds, s.MaxRoundsPerPeriod, total, maxTotal, size, m.Slug)
	}
	return nil
}

