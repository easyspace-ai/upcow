package pairlock

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

const ID = "pairlock"

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &PairLockStrategy{}) }

// PairLockStrategy（新架构简化版）：
// - 触发条件：yesAsk + noAsk <= 100 - ProfitTargetCents
// - 直接使用 TradingService.ExecuteMultiLeg 并发提交两腿
// - 成交不匹配由 ExecutionEngine 自动对冲（SELL FAK）
type PairLockStrategy struct {
	TradingService *services.TradingService
	PairLockStrategyConfig `yaml:",inline" json:",inline"`

	lastMarketSlug string
	rounds         int
	lastTradeAt    time.Time
}

func (s *PairLockStrategy) ID() string   { return ID }
func (s *PairLockStrategy) Name() string { return ID }
func (s *PairLockStrategy) Defaults() error { return nil }
func (s *PairLockStrategy) Validate() error { return s.PairLockStrategyConfig.Validate() }
func (s *PairLockStrategy) Initialize() error { return nil }

func (s *PairLockStrategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
}

func (s *PairLockStrategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *PairLockStrategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}
	m := e.Market

	// 周期切换：重置轮数
	if m.Slug != "" && m.Slug != s.lastMarketSlug {
		s.lastMarketSlug = m.Slug
		s.rounds = 0
		s.lastTradeAt = time.Time{}
	}

	if s.rounds >= s.MaxRoundsPerPeriod {
		return nil
	}
	if !s.lastTradeAt.IsZero() && time.Since(s.lastTradeAt) < time.Duration(s.CooldownMs)*time.Millisecond {
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
	// 确保两腿都满足最小金额
	if yesAsk.ToDecimal() > 0 {
		minSharesYes := s.MinOrderSize / yesAsk.ToDecimal()
		size = math.Max(size, minSharesYes)
	}
	if noAsk.ToDecimal() > 0 {
		minSharesNo := s.MinOrderSize / noAsk.ToDecimal()
		size = math.Max(size, minSharesNo)
	}
	if size <= 0 || math.IsInf(size, 0) || math.IsNaN(size) {
		return nil
	}

	req := execution.MultiLegRequest{
		Name:      "pairlock",
		MarketSlug: m.Slug,
		Legs: []execution.LegIntent{
			{
				Name:      "buy_yes",
				AssetID:   m.YesAssetID,
				TokenType: domain.TokenTypeUp,
				Side:      types.SideBuy,
				Price:     yesAsk,
				Size:      size,
				OrderType: types.OrderTypeFAK,
			},
			{
				Name:      "buy_no",
				AssetID:   m.NoAssetID,
				TokenType: domain.TokenTypeDown,
				Side:      types.SideBuy,
				Price:     noAsk,
				Size:      size,
				OrderType: types.OrderTypeFAK,
			},
		},
		Hedge: execution.AutoHedgeConfig{
			Enabled:              true,
			Delay:                2 * time.Second,
			SellPriceOffsetCents: 2,
			MinExposureToHedge:   s.FailFlattenMinShares,
		},
	}
	if req.Hedge.MinExposureToHedge <= 0 {
		req.Hedge.MinExposureToHedge = 1.0
	}

	_, err = s.TradingService.ExecuteMultiLeg(orderCtx, req)
	if err != nil {
		return nil
	}

	s.rounds++
	s.lastTradeAt = time.Now()
	log.Infof("🎯 [pairlock] 开启一轮: rounds=%d/%d yesAsk=%dc noAsk=%dc total=%dc maxTotal=%dc size=%.4f market=%s",
		s.rounds, s.MaxRoundsPerPeriod, yesAsk.Cents, noAsk.Cents, total, maxTotal, size, m.Slug)
	return nil
}

