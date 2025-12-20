package common

import "sync"

// InFlightLimiter limits concurrent "in-flight" operations.
//
// max:
// - max <= 0 means unlimited.
//
// It is safe for concurrent use.
type InFlightLimiter struct {
	mu       sync.Mutex
	inFlight int
	max      int
}

func NewInFlightLimiter(max int) *InFlightLimiter {
	return &InFlightLimiter{max: max}
}

func (l *InFlightLimiter) SetMax(max int) {
	l.mu.Lock()
	l.max = max
	l.mu.Unlock()
}

func (l *InFlightLimiter) Max() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.max
}

func (l *InFlightLimiter) InFlight() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inFlight
}

func (l *InFlightLimiter) AtLimit() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.max > 0 && l.inFlight >= l.max
}

// TryAcquire increments in-flight counter if under the limit.
// Returns true if acquired.
func (l *InFlightLimiter) TryAcquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.max > 0 && l.inFlight >= l.max {
		return false
	}
	l.inFlight++
	return true
}

// Release decrements in-flight counter if possible.
// It is safe to call Release more times than Acquire; it will clamp at 0.
func (l *InFlightLimiter) Release() {
	l.mu.Lock()
	if l.inFlight > 0 {
		l.inFlight--
	}
	l.mu.Unlock()
}

func (l *InFlightLimiter) Reset() {
	l.mu.Lock()
	l.inFlight = 0
	l.mu.Unlock()
}
