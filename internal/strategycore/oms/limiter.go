package oms

import (
	"sync"
	"time"
)

// tokenBucket 是简单的按 marketSlug 维度的令牌桶限流器。
// 用途：限制“重下/撤单/FAK”等高成本写操作在极端行情里爆炸式触发。
type tokenBucket struct {
	capacity   float64
	refillRate float64 // tokens per second

	tokens     float64
	lastRefill time.Time
}

func newTokenBucket(capacity int, refillPerMinute int) *tokenBucket {
	if capacity <= 0 {
		capacity = 1
	}
	if refillPerMinute <= 0 {
		refillPerMinute = capacity
	}
	return &tokenBucket{
		capacity:   float64(capacity),
		refillRate: float64(refillPerMinute) / 60.0,
		tokens:     float64(capacity),
		lastRefill: time.Now(),
	}
}

func (b *tokenBucket) allow(cost float64) bool {
	if b == nil {
		return true
	}
	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.refillRate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.lastRefill = now
	}
	if b.tokens >= cost {
		b.tokens -= cost
		return true
	}
	return false
}

type perMarketLimiter struct {
	mu sync.Mutex
	m  map[string]*tokenBucket

	// bucket 参数
	capacity       int
	refillPerMinute int
}

func newPerMarketLimiter(capacity int, refillPerMinute int) *perMarketLimiter {
	return &perMarketLimiter{
		m:               make(map[string]*tokenBucket, 64),
		capacity:        capacity,
		refillPerMinute: refillPerMinute,
	}
}

func (l *perMarketLimiter) Allow(marketSlug string, cost int) bool {
	if l == nil || marketSlug == "" {
		return true
	}
	if cost <= 0 {
		cost = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.m[marketSlug]
	if b == nil {
		b = newTokenBucket(l.capacity, l.refillPerMinute)
		l.m[marketSlug] = b
	}
	return b.allow(float64(cost))
}

