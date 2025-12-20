package common

import (
	"sync"
	"time"
)

// Debouncer is a simple time-based gate:
// - Ready tells whether enough time has passed since last Mark.
// - Mark records a successful action time.
//
// NOTE: This is intentionally minimal and concurrency-safe.
type Debouncer struct {
	mu       sync.Mutex
	interval time.Duration
	last     time.Time
}

func NewDebouncer(interval time.Duration) *Debouncer {
	return &Debouncer{interval: interval}
}

func (d *Debouncer) SetInterval(interval time.Duration) {
	d.mu.Lock()
	d.interval = interval
	d.mu.Unlock()
}

func (d *Debouncer) Interval() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.interval
}

// Last returns the last marked time.
func (d *Debouncer) Last() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last
}

// Ready reports whether the action should run now, based on last successful Mark.
// It does NOT update internal state.
func (d *Debouncer) Ready(now time.Time) (ready bool, since time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.interval <= 0 {
		return true, 0
	}
	if d.last.IsZero() {
		return true, d.interval
	}
	since = now.Sub(d.last)
	return since >= d.interval, since
}

// ReadyNow is a convenience wrapper around Ready(time.Now()).
func (d *Debouncer) ReadyNow() (ready bool, since time.Duration) {
	return d.Ready(time.Now())
}

// Mark records a successful action time.
func (d *Debouncer) Mark(now time.Time) {
	d.mu.Lock()
	d.last = now
	d.mu.Unlock()
}

// MarkNow records time.Now() as successful action time.
func (d *Debouncer) MarkNow() { d.Mark(time.Now()) }

// Reset clears the last action time (next Ready will return true).
func (d *Debouncer) Reset() {
	d.mu.Lock()
	d.last = time.Time{}
	d.mu.Unlock()
}
