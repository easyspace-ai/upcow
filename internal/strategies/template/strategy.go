package template

import (
	"context"
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

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &Strategy{}) }

// Strategy: 新架构模板（最小示例）
// - 监听 price_change
// - 满足触发条件时，用 ExecuteMultiLeg 下单（单腿或多腿都一样）
type Strategy struct {
	TradingService *services.TradingService
	Config         `yaml:",inline" json:",inline"`

	lastMarket string
	fired      bool
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
	if e.Market.Slug != "" && e.Market.Slug != s.lastMarket {
		s.lastMarket = e.Market.Slug
		s.fired = false
	}
	if s.fired {
		return nil
	}

	// 示例：买 YES 一次（用于验证链路）
	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	price, err := orderutil.QuoteBuyPrice(orderCtx, s.TradingService, e.Market.YesAssetID, 0)
	if err != nil {
		return nil
	}

	req := execution.MultiLegRequest{
		Name:      "template_buy_yes",
		MarketSlug: e.Market.Slug,
		Legs: []execution.LegIntent{{
			Name:      "buy_yes",
			AssetID:   e.Market.YesAssetID,
			TokenType: domain.TokenTypeUp,
			Side:      types.SideBuy,
			Price:     price,
			Size:      s.OrderSize,
			OrderType: types.OrderTypeFAK,
		}},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}
	_, err = s.TradingService.ExecuteMultiLeg(orderCtx, req)
	if err == nil {
		s.fired = true
		log.Infof("✅ [template] 已下单: yes @ %dc size=%.4f market=%s", price.Cents, s.OrderSize, e.Market.Slug)
	}
	return nil
}

