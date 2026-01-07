package gates

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("module", "winbet.gates")

// Config 是 winbet.Config 的最小子集（避免循环依赖）。
type Config interface {
	// market quality gate
	GetEnableMarketQualityGate() bool
	GetMarketQualityMinScore() float64
	GetMarketQualityMaxSpreadCents() int
	GetMarketQualityMaxBookAgeMs() int

	// stability gate
	GetPriceStabilityCheckEnabled() bool
	GetMaxPriceChangePercent() float64
	GetPriceChangeWindowSeconds() int
	GetMaxSpreadVolatilityPercent() float64
}

type Gates struct {
	cfg Config

	stabMu sync.Mutex
	stab   map[string]*stabilityWindow

	// 轻量限频：避免 gate 失败时刷屏
	logMu        sync.Mutex
	lastLogAt    map[string]time.Time // key=marketSlug
	lastLogMsg   map[string]string
	logMinPeriod time.Duration
}

func New(cfg Config) *Gates {
	return &Gates{
		cfg:          cfg,
		stab:         make(map[string]*stabilityWindow),
		lastLogAt:    make(map[string]time.Time),
		lastLogMsg:   make(map[string]string),
		logMinPeriod: 5 * time.Second,
	}
}

func (g *Gates) OnCycle(newMarket *domain.Market) {
	if g == nil || newMarket == nil {
		return
	}
	g.stabMu.Lock()
	delete(g.stab, newMarket.Slug)
	g.stabMu.Unlock()
}

// AllowTrade 在决策前调用：不通过时返回 false + 原因。
func (g *Gates) AllowTrade(ctx context.Context, ts *services.TradingService, market *domain.Market) (bool, string) {
	if g == nil || g.cfg == nil || ts == nil || market == nil {
		return true, ""
	}

	// 1) 市场质量 gate（基于 TradingService.GetMarketQuality）
	var mq *services.MarketQuality
	if g.cfg.GetEnableMarketQualityGate() {
		opt := services.MarketQualityOptions{
			MaxBookAge:     time.Duration(g.cfg.GetMarketQualityMaxBookAgeMs()) * time.Millisecond,
			MaxSpreadPips:  g.cfg.GetMarketQualityMaxSpreadCents() * 100, // 1c = 100 pips
			PreferWS:       true,
			FallbackToREST: true,
			AllowPartialWS: true,
		}
		got, err := ts.GetMarketQuality(ctx, market, &opt)
		if err != nil || got == nil {
			g.maybeLogGate(false, market.Slug, "market_quality_error")
			return false, "market_quality_error"
		}
		mq = got

		minScore := g.cfg.GetMarketQualityMinScore()
		if mq.Score < int(minScore) {
			reason := fmt.Sprintf("market_quality_low(score=%d<%.0f, source=%s, problems=%v)", mq.Score, minScore, mq.Source, mq.Problems)
			g.maybeLogGate(false, market.Slug, reason)
			return false, reason
		}
		// 保守：mq.Tradable() 还会检查 complete/fresh
		if !mq.Tradable() {
			reason := fmt.Sprintf("market_not_tradable(score=%d, complete=%v, fresh=%v, source=%s, problems=%v)",
				mq.Score, mq.Complete, mq.Fresh, mq.Source, mq.Problems)
			g.maybeLogGate(false, market.Slug, reason)
			return false, reason
		}
	}

	// 2) 价格稳定性 gate（用 top-of-book 衍生 mid 与 spread）
	if g.cfg.GetPriceStabilityCheckEnabled() {
		window := time.Duration(g.cfg.GetPriceChangeWindowSeconds()) * time.Second
		if window <= 0 {
			window = 5 * time.Second
		}

		// 如果上面没取到 mq，这里再取一次（尽量复用同口径数据源）
		if mq == nil {
			opt := services.MarketQualityOptions{
				MaxBookAge:     60 * time.Second,
				MaxSpreadPips:  1000,
				PreferWS:       true,
				FallbackToREST: true,
				AllowPartialWS: true,
			}
			got, err := ts.GetMarketQuality(ctx, market, &opt)
			if err != nil || got == nil {
				g.maybeLogGate(false, market.Slug, "stability_mq_error")
				return false, "stability_mq_error"
			}
			mq = got
		}

		now := time.Now()
		g.stabMu.Lock()
		sw := g.stab[market.Slug]
		if sw == nil {
			sw = newStabilityWindow(window)
			g.stab[market.Slug] = sw
		}
		sw.add(now, mq)
		maxPricePct, spreadVolPct := sw.stats(now)
		g.stabMu.Unlock()

		if maxAllowed := g.cfg.GetMaxPriceChangePercent(); maxAllowed > 0 && maxPricePct > maxAllowed {
			reason := fmt.Sprintf("price_unstable(maxChange=%.2f%%>%.2f%%)", maxPricePct, maxAllowed)
			g.maybeLogGate(false, market.Slug, reason)
			return false, reason
		}
		if maxAllowed := g.cfg.GetMaxSpreadVolatilityPercent(); maxAllowed > 0 && spreadVolPct > maxAllowed {
			reason := fmt.Sprintf("spread_volatile(vol=%.1f%%>%.1f%%)", spreadVolPct, maxAllowed)
			g.maybeLogGate(false, market.Slug, reason)
			return false, reason
		}
	}

	return true, ""
}

func (g *Gates) maybeLogGate(allowed bool, marketSlug, msg string) {
	// 只对失败做限频日志，避免刷屏
	if allowed {
		return
	}
	g.logMu.Lock()
	defer g.logMu.Unlock()
	lastAt := g.lastLogAt[marketSlug]
	lastMsg := g.lastLogMsg[marketSlug]
	now := time.Now()
	if msg == lastMsg && now.Sub(lastAt) < g.logMinPeriod {
		return
	}
	if now.Sub(lastAt) < g.logMinPeriod {
		return
	}
	g.lastLogAt[marketSlug] = now
	g.lastLogMsg[marketSlug] = msg
	log.Warnf("Gate blocked: market=%s reason=%s", marketSlug, msg)
}

