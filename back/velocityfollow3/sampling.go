package velocityfollow

import (
	"math"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

func (s *Strategy) pruneLocked(now time.Time) {
	window := time.Duration(s.WindowSeconds) * time.Second
	if window <= 0 {
		window = 10 * time.Second
	}
	cut := now.Add(-window)
	for tok, arr := range s.samples {
		// 找到第一个 >= cut 的索引
		i := 0
		for i < len(arr) && arr[i].ts.Before(cut) {
			i++
		}
		if i > 0 {
			arr = arr[i:]
		}
		// 防止极端情况下 slice 无限增长（保守上限）
		if len(arr) > 512 {
			arr = arr[len(arr)-512:]
		}
		s.samples[tok] = arr
	}
}

func (s *Strategy) computeLocked(tok domain.TokenType) metrics {
	arr := s.samples[tok]
	if len(arr) < 2 {
		return metrics{}
	}
	first := arr[0]
	last := arr[len(arr)-1]
	dt := last.ts.Sub(first.ts).Seconds()
	if dt <= 0.001 {
		return metrics{}
	}
	delta := last.priceCents - first.priceCents
	// 只做“上行”触发（你的描述是追涨买上涨的一方）
	if delta <= 0 {
		return metrics{}
	}
	vel := float64(delta) / dt
	if math.IsNaN(vel) || math.IsInf(vel, 0) {
		return metrics{}
	}
	return metrics{ok: true, delta: delta, seconds: dt, velocity: vel}
}
