package velocitypairlock

import (
	"time"
)

type pricePoint struct {
	at    time.Time
	cents int
}

// VelocityTracker 用滑动窗口计算“价格变化速度”（分/秒）。
// 只保留窗口内的点，计算使用： (last-first)/dtSeconds
type VelocityTracker struct {
	window time.Duration
	points []pricePoint
}

func NewVelocityTracker(windowSeconds int) *VelocityTracker {
	if windowSeconds <= 0 {
		windowSeconds = 10
	}
	return &VelocityTracker{
		window: time.Duration(windowSeconds) * time.Second,
		points: make([]pricePoint, 0, 64),
	}
}

func (t *VelocityTracker) Reset() {
	if t == nil {
		return
	}
	// 热路径：复用底层数组
	t.points = t.points[:0]
}

func (t *VelocityTracker) Add(at time.Time, cents int) {
	if t == nil {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	t.points = append(t.points, pricePoint{at: at, cents: cents})
	t.prune(at)
}

func (t *VelocityTracker) prune(now time.Time) {
	if t == nil {
		return
	}
	if len(t.points) == 0 {
		return
	}
	cutoff := now.Add(-t.window)
	// 找到第一个 >= cutoff 的 index
	i := 0
	for i < len(t.points) && t.points[i].at.Before(cutoff) {
		i++
	}
	if i == 0 {
		return
	}
	if i >= len(t.points) {
		t.points = t.points[:0]
		return
	}
	// 复用切片（避免频繁分配）
	copy(t.points, t.points[i:])
	t.points = t.points[:len(t.points)-i]
}

// VelocityCentsPerSec 返回：
// - velocity: 分/秒（可为负）
// - move: window 内位移（分）
// - dt: window 内时间跨度（秒）
// - ok: 是否可计算（至少 2 个点且 dt>0）
func (t *VelocityTracker) VelocityCentsPerSec() (velocity float64, move int, dt float64, ok bool) {
	if t == nil || len(t.points) < 2 {
		return 0, 0, 0, false
	}
	first := t.points[0]
	last := t.points[len(t.points)-1]
	if last.at.Before(first.at) {
		return 0, 0, 0, false
	}
	sec := last.at.Sub(first.at).Seconds()
	if sec <= 0 {
		return 0, 0, 0, false
	}
	move = last.cents - first.cents
	velocity = float64(move) / sec
	return velocity, move, sec, true
}

