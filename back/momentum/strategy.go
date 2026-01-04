package momentum

import (
	"context"
	"fmt"
	"math"
	"sync"
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

const ID = "momentum"

var log = logrus.WithField("strategy", ID)

func init() { bbgo.RegisterStrategy(ID, &MomentumStrategy{}) }

// MomentumStrategy（新架构版）：
// - 维持原有 Polygon feed 信号逻辑
// - 下单统一走 TradingService.ExecuteMultiLeg（单腿）
type MomentumStrategy struct {
	TradingService *services.TradingService

	MomentumStrategyConfig `yaml:",inline" json:",inline"`
	config                *MomentumStrategyConfig `json:"-" yaml:"-"`

	autoMerge common.AutoMergeController

	loopOnce   sync.Once
	loopCancel context.CancelFunc
	signalC    chan MomentumSignal

	mu            sync.RWMutex
	currentMarket *domain.Market
}

func (s *MomentumStrategy) ID() string   { return ID }
func (s *MomentumStrategy) Name() string { return ID }
func (s *MomentumStrategy) Defaults() error { return nil }

func (s *MomentumStrategy) Validate() error {
	s.config = &s.MomentumStrategyConfig
	return s.MomentumStrategyConfig.Validate()
}

func (s *MomentumStrategy) Initialize() error {
	s.config = &s.MomentumStrategyConfig
	return s.MomentumStrategyConfig.Validate()
}

func (s *MomentumStrategy) Subscribe(session *bbgo.ExchangeSession) {
	// 用价格事件同步 market（周期切换）
	session.OnPriceChanged(s)
}

// OnPriceChanged 只用于保存当前 market
func (s *MomentumStrategy) OnPriceChanged(ctx context.Context, event *events.PriceChangedEvent) error {
	if event == nil || event.Market == nil {
		return nil
	}
	if s.TradingService != nil {
		s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, event.Market, s.AutoMerge, log.Infof)
	}
	s.mu.Lock()
	s.currentMarket = event.Market
	s.mu.Unlock()
	return nil
}

func (s *MomentumStrategy) Run(ctx context.Context, _ bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	if session != nil && session.Market() != nil {
		s.mu.Lock()
		s.currentMarket = session.Market()
		s.mu.Unlock()
	}
	s.startLoop(ctx)
	<-ctx.Done()
	return ctx.Err()
}

func (s *MomentumStrategy) Shutdown(ctx context.Context, wg *sync.WaitGroup) {
	_ = ctx
	_ = wg
	if s.loopCancel != nil {
		s.loopCancel()
	}
}

func (s *MomentumStrategy) startLoop(parent context.Context) {
	s.loopOnce.Do(func() {
		loopCtx, cancel := context.WithCancel(parent)
		s.loopCancel = cancel
		s.signalC = make(chan MomentumSignal, 1024)

		// 外部行情源：Polygon
		if s.config != nil && s.config.UsePolygonFeed {
			go runPolygonFeed(loopCtx, s.config.Asset, s.config.ThresholdBps, s.config.WindowSecs, s.signalC, log)
		}

		go s.loop(loopCtx)
	})
}

func (s *MomentumStrategy) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case sig := <-s.signalC:
			_ = s.handleSignal(ctx, sig)
		}
	}
}

func (s *MomentumStrategy) handleSignal(ctx context.Context, sig MomentumSignal) error {
	ts := s.TradingService
	cfg := s.config
	s.mu.RLock()
	market := s.currentMarket
	s.mu.RUnlock()
	if ts == nil || cfg == nil || market == nil {
		return nil
	}

	// 决策：Up -> 买 YES，Down -> 买 NO
	tokenType := domain.TokenTypeUp
	if sig.Dir == DirectionDown {
		tokenType = domain.TokenTypeDown
	}
	assetID := market.GetAssetID(tokenType)

	absMove := int(math.Abs(float64(sig.MoveBps)))
	estimatedFair := 50 + absMove/10
	maxPay := estimatedFair - cfg.MinEdgeCents
	if maxPay < 1 {
		maxPay = 1
	}

	orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	price, err := orderutil.QuoteBuyPrice(orderCtx, ts, assetID, maxPay)
	if err != nil {
		return nil
	}
	if price.ToDecimal() <= 0 {
		return fmt.Errorf("无效价格: %v", price)
	}
	shares := cfg.SizeUSDC / price.ToDecimal()
	if shares <= 0 {
		return nil
	}

	req := execution.MultiLegRequest{
		Name:      fmt.Sprintf("momentum_%s_%d", sig.Asset, absMove),
		MarketSlug: market.Slug,
		Legs: []execution.LegIntent{{
			Name:      "buy",
			AssetID:   assetID,
			TokenType: tokenType,
			Side:      types.SideBuy,
			Price:     price,
			Size:      shares,
			OrderType: types.OrderTypeFAK,
		}},
		Hedge: execution.AutoHedgeConfig{Enabled: false},
	}

	_, _ = ts.ExecuteMultiLeg(orderCtx, req)
	return nil
}

