package common

import (
	"context"
	"sync"
	"time"
)

// StartLoopOnce starts a single goroutine loop once.
//
// It standardizes the common boilerplate across strategies:
// - loopOnce.Do(...)
// - context.WithCancel
// - optional ticker lifecycle
//
// tick:
// - if tick > 0, a ticker is created and its channel is passed to run
// - if tick <= 0, tickC is nil (never fires)
func StartLoopOnce(
	parent context.Context,
	once *sync.Once,
	setCancel func(context.CancelFunc),
	tick time.Duration,
	run func(loopCtx context.Context, tickC <-chan time.Time),
) {
	if once == nil {
		// defensive: treat as "always start"
		loopCtx, cancel := context.WithCancel(parent)
		if setCancel != nil {
			setCancel(cancel)
		}
		go startLoop(loopCtx, tick, run)
		return
	}

	once.Do(func() {
		loopCtx, cancel := context.WithCancel(parent)
		if setCancel != nil {
			setCancel(cancel)
		}
		go startLoop(loopCtx, tick, run)
	})
}

func startLoop(loopCtx context.Context, tick time.Duration, run func(context.Context, <-chan time.Time)) {
	var tickC <-chan time.Time
	var ticker *time.Ticker
	if tick > 0 {
		ticker = time.NewTicker(tick)
		tickC = ticker.C
		defer ticker.Stop()
	}
	run(loopCtx, tickC)
}
