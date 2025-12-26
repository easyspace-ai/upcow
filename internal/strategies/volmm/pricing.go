package volmm

import (
	"math"
	"sort"
	"time"

	"github.com/betbot/gobet/internal/strategies/common"
)

type underlyingSample struct {
	ts    time.Time
	price float64
}

// underlyingTracker 维护 Chainlink 秒级价格样本并计算动能/波动特征。
// 注意：这里不依赖完整 K 线，只依赖实时价样本。
type underlyingTracker struct {
	velWindow time.Duration
	accWindow time.Duration
	volWindow time.Duration

	samples []underlyingSample
}

func newUnderlyingTracker(cfg Config) *underlyingTracker {
	return &underlyingTracker{
		velWindow: time.Duration(cfg.VelWindowSeconds) * time.Second,
		accWindow: time.Duration(cfg.AccWindowSeconds) * time.Second,
		volWindow: time.Duration(cfg.VolLookbackSeconds) * time.Second,
		samples:   make([]underlyingSample, 0, 256),
	}
}

func (t *underlyingTracker) Update(ts time.Time, price float64) {
	if price <= 0 || ts.IsZero() {
		return
	}
	t.samples = append(t.samples, underlyingSample{ts: ts, price: price})
	t.prune(ts)
}

func (t *underlyingTracker) prune(now time.Time) {
	keep := t.volWindow
	if t.accWindow > keep {
		keep = t.accWindow
	}
	if t.velWindow > keep {
		keep = t.velWindow
	}
	keep += 5 * time.Second // buffer

	cut := now.Add(-keep)
	// samples are append-only and timestamps should be monotonic-ish; use linear trim
	i := 0
	for i < len(t.samples) && t.samples[i].ts.Before(cut) {
		i++
	}
	if i > 0 {
		t.samples = append([]underlyingSample{}, t.samples[i:]...)
	}
	// hard cap
	if len(t.samples) > 4096 {
		t.samples = t.samples[len(t.samples)-4096:]
	}
}

func (t *underlyingTracker) latest() (time.Time, float64, bool) {
	if len(t.samples) == 0 {
		return time.Time{}, 0, false
	}
	s := t.samples[len(t.samples)-1]
	return s.ts, s.price, true
}

func (t *underlyingTracker) priceAtOrBefore(target time.Time) (float64, bool) {
	if len(t.samples) == 0 {
		return 0, false
	}
	// binary search by ts
	idx := sort.Search(len(t.samples), func(i int) bool {
		return !t.samples[i].ts.Before(target)
	})
	if idx < len(t.samples) && t.samples[idx].ts.Equal(target) {
		return t.samples[idx].price, true
	}
	// use previous
	idx--
	if idx >= 0 && idx < len(t.samples) {
		return t.samples[idx].price, true
	}
	return 0, false
}

type momentumFeatures struct {
	VelNorm float64
	AccNorm float64
	Sigma   float64 // 1s log-return std (approx)
}

// Features 计算动能与波动率（近似）。
func (t *underlyingTracker) Features(now time.Time) momentumFeatures {
	if len(t.samples) < 3 {
		return momentumFeatures{}
	}

	// realized vol: stddev of 1s log returns in volWindow
	cut := now.Add(-t.volWindow)
	var rets []float64
	var prev *underlyingSample
	for i := range t.samples {
		s := t.samples[i]
		if s.ts.Before(cut) {
			continue
		}
		if prev != nil && s.price > 0 && prev.price > 0 {
			// allow irregular dt; treat as 1-step return sample
			rets = append(rets, math.Log(s.price/prev.price))
		}
		prev = &s
	}
	sigma := stddev(rets)
	if sigma <= 0 {
		sigma = 1e-6
	}

	latestTs, latestPrice, ok := t.latest()
	if !ok || latestPrice <= 0 {
		return momentumFeatures{Sigma: sigma}
	}
	_ = latestTs

	// v10 = log(S/S(t-vel))
	velBase, okV := t.priceAtOrBefore(now.Add(-t.velWindow))
	accBase, okA := t.priceAtOrBefore(now.Add(-t.accWindow))
	if !okV || !okA || velBase <= 0 || accBase <= 0 {
		return momentumFeatures{Sigma: sigma}
	}
	v := math.Log(latestPrice / velBase)
	vLong := math.Log(latestPrice / accBase)
	a := v - vLong

	// normalize by sigma * sqrt(window)
	velNorm := v / (sigma * math.Sqrt(math.Max(t.velWindow.Seconds(), 1)))
	accNorm := a / (sigma * math.Sqrt(math.Max(t.accWindow.Seconds(), 1)))
	if math.IsNaN(velNorm) || math.IsInf(velNorm, 0) {
		velNorm = 0
	}
	if math.IsNaN(accNorm) || math.IsInf(accNorm, 0) {
		accNorm = 0
	}
	return momentumFeatures{VelNorm: velNorm, AccNorm: accNorm, Sigma: sigma}
}

type pricingResult struct {
	FairUp   float64
	FairDown float64
	Z        float64
	RawX     float64
	Feat     momentumFeatures
}

func computePricing(cfg Config, strikePrice float64, underlyingPrice float64, remainingSeconds float64, feat momentumFeatures) pricingResult {
	if remainingSeconds < 1 {
		remainingSeconds = 1
	}
	if strikePrice <= 0 || underlyingPrice <= 0 {
		return pricingResult{}
	}

	delta := underlyingPrice - strikePrice
	rawX := delta / math.Sqrt(remainingSeconds)
	z := cfg.K*rawX + cfg.C + cfg.Kv*feat.VelNorm + cfg.Ka*feat.AccNorm
	p := common.NormCdf(z)

	// clamp
	minp := cfg.PMin
	if minp < 0 {
		minp = 0
	}
	if minp > 0.49 {
		minp = 0.49
	}
	if p < minp {
		p = minp
	}
	if p > 1-minp {
		p = 1 - minp
	}

	return pricingResult{
		FairUp:   p,
		FairDown: 1 - p,
		Z:        z,
		RawX:     rawX,
		Feat:     feat,
	}
}

func stddev(xs []float64) float64 {
	n := len(xs)
	if n <= 1 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(n)
	var v float64
	for _, x := range xs {
		d := x - mean
		v += d * d
	}
	return math.Sqrt(v / float64(n-1))
}

