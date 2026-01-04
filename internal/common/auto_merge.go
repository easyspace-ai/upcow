package common

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
)

// AutoMergeConfig is a per-strategy config for automatically merging complete sets (YES+NO -> USDC).
// It is disabled by default; strategies opt-in via config.
type AutoMergeConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`

	// MinCompleteSets: minimum complete sets (shares) required to trigger merge.
	MinCompleteSets float64 `yaml:"minCompleteSets" json:"minCompleteSets"`
	// MaxCompleteSetsPerRun: cap merge amount per run (0 means no cap).
	MaxCompleteSetsPerRun float64 `yaml:"maxCompleteSetsPerRun" json:"maxCompleteSetsPerRun"`
	// MergeRatio: merge amount = min(YES,NO) * MergeRatio. (0..1], default 1.
	MergeRatio float64 `yaml:"mergeRatio" json:"mergeRatio"`

	// IntervalSeconds: minimum time between auto-merge attempts.
	IntervalSeconds int `yaml:"intervalSeconds" json:"intervalSeconds"`

	// OnlyIfNoOpenOrders: require zero active open orders before merging (safer).
	OnlyIfNoOpenOrders bool `yaml:"onlyIfNoOpenOrders" json:"onlyIfNoOpenOrders"`

	// ReconcileAfterMerge: best-effort reconcile positions via Data API after submitting merge.
	ReconcileAfterMerge bool `yaml:"reconcileAfterMerge" json:"reconcileAfterMerge"`
	// ReconcileMaxWaitSeconds: how long to poll Data API to see inventory update (0 disables polling).
	ReconcileMaxWaitSeconds int `yaml:"reconcileMaxWaitSeconds" json:"reconcileMaxWaitSeconds"`

	// Metadata: optional relayer metadata (<=500 chars). If empty, a default is used.
	Metadata string `yaml:"metadata" json:"metadata"`
}

func (c *AutoMergeConfig) Normalize() {
	if c == nil {
		return
	}
	if c.MinCompleteSets < 0 {
		c.MinCompleteSets = 0
	}
	if c.MergeRatio <= 0 || c.MergeRatio > 1.0 {
		c.MergeRatio = 1.0
	}
	if c.IntervalSeconds <= 0 {
		c.IntervalSeconds = 60
	}
	if c.ReconcileMaxWaitSeconds < 0 {
		c.ReconcileMaxWaitSeconds = 0
	}
	// Safe default: require no open orders if user enables auto merge, unless explicitly set false.
	if c.OnlyIfNoOpenOrders == false {
		// keep user's value; no default override
	}
}

// AutoMergeController keeps runtime state (throttle/in-flight) per strategy instance.
type AutoMergeController struct {
	mu       sync.Mutex
	lastAt   time.Time
	inFlight bool
}

func (ctl *AutoMergeController) MaybeAutoMerge(
	ctx context.Context,
	ts *services.TradingService,
	market *domain.Market,
	cfg AutoMergeConfig,
	logf func(format string, args ...any),
) {
	cfg.Normalize()
	if !cfg.Enabled {
		return
	}
	if ts == nil || market == nil || !market.IsValid() || market.ConditionID == "" {
		return
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	// throttle + single-flight
	ctl.mu.Lock()
	if ctl.inFlight {
		ctl.mu.Unlock()
		return
	}
	if !ctl.lastAt.IsZero() && time.Since(ctl.lastAt) < time.Duration(cfg.IntervalSeconds)*time.Second {
		ctl.mu.Unlock()
		return
	}
	ctl.inFlight = true
	ctl.lastAt = time.Now()
	ctl.mu.Unlock()
	defer func() {
		ctl.mu.Lock()
		ctl.inFlight = false
		ctl.mu.Unlock()
	}()

	// safety: require no open orders (optional)
	if cfg.OnlyIfNoOpenOrders {
		if oo := ts.GetActiveOrders(); len(oo) > 0 {
			return
		}
	}

	// compute complete sets using local positions (fast path)
	var up, down float64
	for _, p := range ts.GetOpenPositionsForMarket(market.Slug) {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}
		if p.TokenType == domain.TokenTypeUp {
			up += p.Size
		} else if p.TokenType == domain.TokenTypeDown {
			down += p.Size
		}
	}
	complete := math.Min(up, down)
	if cfg.MinCompleteSets > 0 && complete < cfg.MinCompleteSets {
		return
	}
	if complete <= 0 {
		return
	}

	amount := complete * cfg.MergeRatio
	if amount > complete {
		amount = complete
	}
	if cfg.MaxCompleteSetsPerRun > 0 && amount > cfg.MaxCompleteSetsPerRun {
		amount = cfg.MaxCompleteSetsPerRun
	}
	if amount <= 0 {
		return
	}

	// 异步执行合并操作，避免阻塞价格事件处理
	go func() {
		// 使用独立的 context，避免使用已取消的 ctx
		mergeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		txHash, err := ts.MergeCompleteSetsViaRelayer(mergeCtx, market.ConditionID, amount, cfg.Metadata)
		if err != nil {
			logf("⚠️ autoMerge failed: market=%s amount=%.6f err=%v", market.Slug, amount, err)
			return
		}
		logf("✅ autoMerge submitted: market=%s amount=%.6f complete=%.6f tx=%s", market.Slug, amount, complete, txHash)

		// best-effort reconcile (Data API lags; optional polling)
		if cfg.ReconcileAfterMerge {
			_ = ts.ReconcileMarketPositionsFromDataAPI(mergeCtx, market)
			maxWait := time.Duration(cfg.ReconcileMaxWaitSeconds) * time.Second
			if maxWait <= 0 {
				return
			}
			deadline := time.Now().Add(maxWait)
			for time.Now().Before(deadline) {
				time.Sleep(3 * time.Second)
				_ = ts.ReconcileMarketPositionsFromDataAPI(mergeCtx, market)
			}
		}
	}()
}

