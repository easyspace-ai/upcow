package winbet

import (
	"sync"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

type priceSample struct {
	at    time.Time
	price float64 // decimal (0..1)
}

// velocitySampler 维护一个“固定窗口”的价格样本，用于计算 move/velocity（以 cents 为单位）。
// 设计目标：轻量、确定性、可在高频行情下稳定运行。
type velocitySampler struct {
	window time.Duration

	mu    sync.Mutex
	samps map[domain.TokenType][]priceSample
}

func newVelocitySampler(windowSeconds int) *velocitySampler {
	if windowSeconds <= 0 {
		windowSeconds = 10
	}
	return &velocitySampler{
		window: time.Duration(windowSeconds) * time.Second,
		samps:  make(map[domain.TokenType][]priceSample, 2),
	}
}

func (s *velocitySampler) Reset() {
	s.mu.Lock()
	s.samps = make(map[domain.TokenType][]priceSample, 2)
	s.mu.Unlock()
}

func (s *velocitySampler) Add(token domain.TokenType, price float64, at time.Time) {
	if at.IsZero() {
		at = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cut := at.Add(-s.window)
	arr := append(s.samps[token], priceSample{at: at, price: price})

	// 丢弃窗口外样本
	i := 0
	for i < len(arr) && arr[i].at.Before(cut) {
		i++
	}
	if i > 0 {
		arr = arr[i:]
	}
	// 防御：避免 slice 无界增长（极端情况下）
	if len(arr) > 1024 {
		arr = arr[len(arr)-1024:]
	}

	s.samps[token] = arr
}

// Stats 返回 (velocityCentsPerSec, deltaCents, absMoveCents, ok)。
// - deltaCents: 最近窗口内 (last - first) 的有符号变化（分）
// - absMoveCents: |deltaCents|
// - velocity: absMove / windowSeconds（用于门槛判断；方向由 deltaCents 决定）
func (s *velocitySampler) Stats(token domain.TokenType, now time.Time) (float64, int, int, bool) {
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	arr := s.samps[token]
	if len(arr) < 2 {
		return 0, 0, 0, false
	}

	// 确保窗口裁剪（防止很久没 Add 时窗口变长）
	cut := now.Add(-s.window)
	i := 0
	for i < len(arr) && arr[i].at.Before(cut) {
		i++
	}
	if i > 0 {
		arr = arr[i:]
		s.samps[token] = arr
	}
	if len(arr) < 2 {
		return 0, 0, 0, false
	}

	first := arr[0]
	last := arr[len(arr)-1]
	delta := last.price - first.price
	deltaCents := int(delta*100 + 0.5)
	absMove := deltaCents
	if absMove < 0 {
		absMove = -absMove
	}
	sec := s.window.Seconds()
	if sec <= 0 {
		sec = 1
	}
	vel := float64(absMove) / sec
	return vel, deltaCents, absMove, true
}

