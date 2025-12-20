package risk

import (
	"fmt"
	"sync/atomic"
	"time"
)

// ErrCircuitBreakerOpen 表示断路器已打开，禁止继续交易。
var ErrCircuitBreakerOpen = fmt.Errorf("circuit breaker open")

// CircuitBreakerConfig 断路器配置。
// 约定：阈值 <= 0 表示关闭对应限制。
type CircuitBreakerConfig struct {
	// MaxConsecutiveErrors 连续错误上限（下单失败/执行失败等）。
	MaxConsecutiveErrors int64

	// DailyLossLimitCents 当日最大亏损（分）。达到或超过时立即熔断。
	DailyLossLimitCents int64
}

// CircuitBreaker 高频快路径使用原子变量，低频配置更新使用原子值。
//
// 说明：
// - 本项目目前的 PnL 统计不是全链路闭环，因此 DailyLossLimitCents 只提供接口，
//   由上层在“确认成交/平仓”处调用 AddPnLCents() 更新。
type CircuitBreaker struct {
	halted atomic.Bool

	consecutiveErrors atomic.Int64
	dailyPnlCents     atomic.Int64
	dayKey            atomic.Int64 // YYYYMMDD

	// 配置（用 atomic.Value 也可以；这里用原子字段，保持简单）
	maxConsecutiveErrors atomic.Int64
	dailyLossLimitCents  atomic.Int64
}

func NewCircuitBreaker(cfg CircuitBreakerConfig) *CircuitBreaker {
	cb := &CircuitBreaker{}
	cb.SetConfig(cfg)
	return cb
}

func (cb *CircuitBreaker) SetConfig(cfg CircuitBreakerConfig) {
	if cb == nil {
		return
	}
	cb.maxConsecutiveErrors.Store(cfg.MaxConsecutiveErrors)
	cb.dailyLossLimitCents.Store(cfg.DailyLossLimitCents)
}

// Halt 手动熔断（如人工介入或检测到严重异常）。
func (cb *CircuitBreaker) Halt() {
	if cb == nil {
		return
	}
	cb.halted.Store(true)
}

// Resume 手动恢复（会同时清空连续错误计数）。
func (cb *CircuitBreaker) Resume() {
	if cb == nil {
		return
	}
	cb.halted.Store(false)
	cb.consecutiveErrors.Store(0)
}

// AllowTrading 快路径检查是否允许交易。
func (cb *CircuitBreaker) AllowTrading() error {
	if cb == nil {
		return nil
	}

	if cb.halted.Load() {
		return ErrCircuitBreakerOpen
	}

	// 连续错误熔断
	maxErr := cb.maxConsecutiveErrors.Load()
	if maxErr > 0 && cb.consecutiveErrors.Load() >= maxErr {
		cb.halted.Store(true)
		return ErrCircuitBreakerOpen
	}

	// 当日亏损熔断（若启用）
	limit := cb.dailyLossLimitCents.Load()
	if limit > 0 {
		cb.rollDayIfNeeded()
		pnl := cb.dailyPnlCents.Load()
		if pnl <= -limit {
			cb.halted.Store(true)
			return ErrCircuitBreakerOpen
		}
	}

	return nil
}

// OnSuccess 在一次关键执行成功后调用，用于清空连续错误计数。
func (cb *CircuitBreaker) OnSuccess() {
	if cb == nil {
		return
	}
	cb.consecutiveErrors.Store(0)
}

// OnError 在一次关键执行失败后调用，用于累计连续错误计数。
func (cb *CircuitBreaker) OnError() {
	if cb == nil {
		return
	}
	cb.consecutiveErrors.Add(1)
}

// AddPnLCents 增量更新当日 PnL（分）。
// 负数表示亏损，正数表示盈利。
func (cb *CircuitBreaker) AddPnLCents(delta int64) {
	if cb == nil {
		return
	}
	cb.rollDayIfNeeded()
	cb.dailyPnlCents.Add(delta)
}

func (cb *CircuitBreaker) rollDayIfNeeded() {
	// YYYYMMDD（本地时间即可；风控用途不要求跨时区精确）
	now := time.Now()
	key := int64(now.Year()*10000 + int(now.Month())*100 + now.Day())
	prev := cb.dayKey.Load()
	if prev == key {
		return
	}
	// 尝试切换 dayKey；成功者负责清零当日 PnL
	if cb.dayKey.CompareAndSwap(prev, key) {
		cb.dailyPnlCents.Store(0)
	}
}

