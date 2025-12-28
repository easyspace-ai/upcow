package threshold

import (
	"context"
	"time"

	"github.com/betbot/gobet/clob/types"
	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/events"
	"github.com/betbot/gobet/internal/execution"
	"github.com/betbot/gobet/internal/strategies/common"
	"github.com/betbot/gobet/internal/strategies/orderutil"
	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/bbgo"
	"github.com/sirupsen/logrus"
)

const ID = "threshold"

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy（新架构版）：
// - 单腿策略也走 ExecuteMultiLeg（统一队列/in-flight）
// - 使用 TradingService.GetBestPrice（已优先 WS bestbook）
type Strategy struct {
	TradingService *services.TradingService
	ThresholdStrategyConfig `yaml:",inline" json:",inline"`

	lastActionAt time.Time

	autoMerge common.AutoMergeController
}

func (s *Strategy) ID() string   { return ID }
func (s *Strategy) Name() string { return ID }
func (s *Strategy) Defaults() error { return nil }
func (s *Strategy) Validate() error { return s.ThresholdStrategyConfig.Validate() }
func (s *Strategy) Initialize() error { return nil }

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.OnPriceChanged(s)
}

func (s *Strategy) Run(ctx context.Context, _ bbgo.OrderExecutor, _ *bbgo.ExchangeSession) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *Strategy) OnCycle(_ context.Context, _ *domain.Market, _ *domain.Market) {
	s.lastActionAt = time.Time{}
}

func (s *Strategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}
	s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)

	if !s.lastActionAt.IsZero() && time.Since(s.lastActionAt) < 500*time.Millisecond {
		return nil
	}

	// 选择交易 token
	token := domain.TokenTypeUp
	assetID := e.Market.YesAssetID
	if s.TokenType == "NO" {
		token = domain.TokenTypeDown
		assetID = e.Market.NoAssetID
	}

	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	// BUY 条件
	ask, askErr := orderutil.QuoteBuyPrice(orderCtx, s.TradingService, assetID, s.MaxBuyPrice)
	if askErr == nil {
		okPrice := ask.ToCents() <= s.BuyThreshold
		if s.MaxBuyPrice > 0 && ask.ToCents() > s.MaxBuyPrice {
			okPrice = false
		}
		if okPrice {
			req := execution.MultiLegRequest{
				Name:      "threshold_buy",
				MarketSlug: e.Market.Slug,
				Legs: []execution.LegIntent{{
					Name:      "buy",
					AssetID:   assetID,
					TokenType: token,
					Side:      types.SideBuy,
					Price:     ask,
					Size:      s.OrderSize,
					OrderType: types.OrderTypeFAK,
				}},
				Hedge: execution.AutoHedgeConfig{Enabled: false},
			}
			_, err := s.TradingService.ExecuteMultiLeg(orderCtx, req)
			if err == nil {
				s.lastActionAt = time.Now()
			}
			return nil
		}
	}

	// SELL 条件（可选）
	if s.SellThreshold > 0 {
		bid, bidErr := orderutil.QuoteSellPrice(orderCtx, s.TradingService, assetID, 0)
		if bidErr == nil && bid.ToCents() >= s.SellThreshold {
			req := execution.MultiLegRequest{
				Name:      "threshold_sell",
				MarketSlug: e.Market.Slug,
				Legs: []execution.LegIntent{{
					Name:      "sell",
					AssetID:   assetID,
					TokenType: token,
					Side:      types.SideSell,
					Price:     bid,
					Size:      s.OrderSize,
					OrderType: types.OrderTypeFAK,
				}},
				Hedge: execution.AutoHedgeConfig{Enabled: false},
			}
			_, err := s.TradingService.ExecuteMultiLeg(orderCtx, req)
			if err == nil {
				s.lastActionAt = time.Now()
			}
		}
	}

	return nil
}

