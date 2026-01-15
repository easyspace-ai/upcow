package paircostarb

import (
	"strings"
	"time"

	"github.com/betbot/gobet/internal/domain"
	"github.com/betbot/gobet/internal/services"
)

// BinanceKlineSignal：使用 Binance Futures 的秒级/分钟 K 线做方向信号。
// - return = close_now / close_lookback - 1
// - return > threshold => UP active
// - return < -threshold => DOWN active
type BinanceKlineSignal struct {
	klines *services.BinanceFuturesKlines

	interval        string
	lookbackSeconds int
	thresholdBps    float64
	activeSeconds   int

	activeUntil time.Time
	dir         domain.TokenType
}

func NewBinanceKlineSignal(klines *services.BinanceFuturesKlines, cfg Config) *BinanceKlineSignal {
	interval := strings.ToLower(strings.TrimSpace(cfg.BinanceInterval))
	if interval == "" {
		interval = "1s"
	}
	return &BinanceKlineSignal{
		klines:          klines,
		interval:        interval,
		lookbackSeconds: cfg.BinanceLookbackSeconds,
		thresholdBps:    cfg.BinanceReturnThresholdBps,
		activeSeconds:   cfg.BinanceActiveSeconds,
	}
}

func (s *BinanceKlineSignal) Evaluate(now time.Time) (dir domain.TokenType, active bool) {
	if s == nil || s.klines == nil {
		return "", false
	}
	if s.lookbackSeconds <= 0 || s.thresholdBps <= 0 || s.activeSeconds <= 0 {
		// 配置无效则视作未启用
		return "", false
	}

	latest, ok := s.klines.Latest(s.interval)
	if ok && latest.Close > 0 {
		targetMs := latest.StartTimeMs - int64(s.lookbackSeconds)*1000
		prev, ok2 := s.klines.NearestAtOrBefore(s.interval, targetMs)
		if ok2 && prev.Close > 0 {
			ret := latest.Close/prev.Close - 1.0
			thr := s.thresholdBps / 10000.0
			if ret >= thr {
				s.dir = domain.TokenTypeUp
				s.activeUntil = now.Add(time.Duration(s.activeSeconds) * time.Second)
			} else if ret <= -thr {
				s.dir = domain.TokenTypeDown
				s.activeUntil = now.Add(time.Duration(s.activeSeconds) * time.Second)
			}
		}
	}

	if !s.activeUntil.IsZero() && now.Before(s.activeUntil) && (s.dir == domain.TokenTypeUp || s.dir == domain.TokenTypeDown) {
		return s.dir, true
	}
	return "", false
}
