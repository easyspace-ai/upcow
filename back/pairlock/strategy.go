package pairlock

import (
	"context"
	"math"
	"strconv"
	"strings"
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

	rounds         int
	lastTradeAt    time.Time

	autoMerge common.AutoMergeController
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

func (s *PairLockStrategy) OnCycle(_ context.Context, _ *domain.Market, _ *domain.Market) {
	s.rounds = 0
	s.lastTradeAt = time.Time{}
}

func (s *PairLockStrategy) OnPriceChanged(ctx context.Context, e *events.PriceChangedEvent) error {
	if e == nil || e.Market == nil || s.TradingService == nil {
		return nil
	}
	s.autoMerge.MaybeAutoMerge(ctx, s.TradingService, e.Market, s.AutoMerge, log.Infof)
	m := e.Market

	// 临近结算不再开新轮：降低 WS/撮合延迟导致的单腿裸露风险。
	if s.EntryCutoffSeconds > 0 && isWithinEntryCutoff(m.Slug, m.Timestamp, s.EntryCutoffSeconds) {
		return nil
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

	total := yesAsk.ToCents() + noAsk.ToCents()
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
		s.rounds, s.MaxRoundsPerPeriod, yesAsk.ToCents(), noAsk.ToCents(), total, maxTotal, size, m.Slug)
	return nil
}

// isWithinEntryCutoff 判断是否进入“禁止开新仓”的截止窗口。
// 支持 slug 约定：{symbol}-{kind}-{timeframe}-{periodStartUnix}，例如 btc-updown-15m-1766322000。
// 若无法从 timeframe 推断周期时长，则退化为仅用 market.Timestamp + 15m 估算。
func isWithinEntryCutoff(slug string, periodStartUnix int64, cutoffSeconds int) bool {
	if cutoffSeconds <= 0 || periodStartUnix <= 0 {
		return false
	}

	dur := inferDurationFromSlug(slug)
	if dur <= 0 {
		dur = 15 * time.Minute
	}
	end := time.Unix(periodStartUnix, 0).Add(dur)
	return time.Until(end) <= time.Duration(cutoffSeconds)*time.Second
}

func inferDurationFromSlug(slug string) time.Duration {
	// 期望形式：a-b-15m-<ts> 或 a-b-1h-<ts>
	parts := strings.Split(slug, "-")
	if len(parts) < 2 {
		return 0
	}

	// 优先：倒数第2段一般是 timeframe（timestamp 风格）
	if len(parts) >= 2 {
		tf := parts[len(parts)-2]
		if d, ok := parseTimeframe(tf); ok {
			return d
		}
	}

	// 兜底：全段扫描（兼容 kind 中含 '-' 的情况）
	for _, p := range parts {
		if d, ok := parseTimeframe(p); ok {
			return d
		}
	}
	return 0
}

func parseTimeframe(tf string) (time.Duration, bool) {
	tf = strings.TrimSpace(tf)
	if tf == "" {
		return 0, false
	}
	switch tf {
	case "15m":
		return 15 * time.Minute, true
	case "30m":
		return 30 * time.Minute, true
	case "1h":
		return time.Hour, true
	case "4h":
		return 4 * time.Hour, true
	}

	// 宽松解析：形如 90m / 2h
	if strings.HasSuffix(tf, "m") {
		n, err := strconv.Atoi(strings.TrimSuffix(tf, "m"))
		if err == nil && n > 0 {
			return time.Duration(n) * time.Minute, true
		}
	}
	if strings.HasSuffix(tf, "h") {
		n, err := strconv.Atoi(strings.TrimSuffix(tf, "h"))
		if err == nil && n > 0 {
			return time.Duration(n) * time.Hour, true
		}
	}
	return 0, false
}

