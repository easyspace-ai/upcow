package risk

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
)

var log = logrus.WithField("component", "circuit_breaker")

// ErrCircuitBreakerOpen 表示断路器已打开，禁止继续交易。
var ErrCircuitBreakerOpen = fmt.Errorf("circuit breaker open")

// CircuitBreakerConfig 断路器配置。
// 约定：阈值 <= 0 表示关闭对应限制。
type CircuitBreakerConfig struct {
	// MaxConsecutiveErrors 连续错误上限（下单失败/执行失败等）。
	MaxConsecutiveErrors int64

	// DailyLossLimitCents 当日最大亏损（分）。达到或超过时立即熔断。
	DailyLossLimitCents int64

	// CooldownSeconds 熔断后的冷却时间（秒）。冷却时间后自动尝试恢复。
	// 0 表示不自动恢复，需要手动调用 Resume()。
	CooldownSeconds int64
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

	// 自动恢复相关
	lastHaltedAt    atomic.Int64 // Unix timestamp (秒)
	cooldownSeconds atomic.Int64

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
	if cfg.CooldownSeconds > 0 {
		cb.cooldownSeconds.Store(cfg.CooldownSeconds)
	}
}

// Halt 手动熔断（如人工介入或检测到严重异常）。
func (cb *CircuitBreaker) Halt() {
	if cb == nil {
		return
	}
	if cb.halted.CompareAndSwap(false, true) {
		cb.lastHaltedAt.Store(time.Now().Unix())
		log.Warn("🚨 Circuit Breaker 手动熔断")
	}
}

// Resume 手动恢复（会同时清空连续错误计数）。
func (cb *CircuitBreaker) Resume() {
	if cb == nil {
		return
	}
	if cb.halted.CompareAndSwap(true, false) {
		cb.consecutiveErrors.Store(0)
		cb.lastHaltedAt.Store(0)
		log.Info("✅ Circuit Breaker 手动恢复")
	}
}

// AllowTrading 快路径检查是否允许交易。
func (cb *CircuitBreaker) AllowTrading() error {
	if cb == nil {
		return nil
	}

	// 检查是否处于熔断状态
	if cb.halted.Load() {
		// 检查是否有自动恢复机制
		cooldown := cb.cooldownSeconds.Load()
		if cooldown > 0 {
			lastHalted := cb.lastHaltedAt.Load()
			if lastHalted > 0 {
				now := time.Now().Unix()
				elapsed := now - lastHalted
				if elapsed >= cooldown {
					// 冷却时间已过，尝试自动恢复
					if cb.halted.CompareAndSwap(true, false) {
						cb.consecutiveErrors.Store(0)
						log.Infof("🔄 Circuit Breaker 自动恢复：冷却时间已过 (cooldown=%ds, elapsed=%ds)", cooldown, elapsed)
					}
				} else {
					// 仍在冷却期内
					return ErrCircuitBreakerOpen
				}
			} else {
				// 没有记录熔断时间，直接返回错误
				return ErrCircuitBreakerOpen
			}
		} else {
			// 没有自动恢复机制
			return ErrCircuitBreakerOpen
		}
	}

	// 连续错误熔断
	maxErr := cb.maxConsecutiveErrors.Load()
	if maxErr > 0 {
		errors := cb.consecutiveErrors.Load()
		if errors >= maxErr {
			// 达到错误阈值，触发熔断
			if cb.halted.CompareAndSwap(false, true) {
				cb.lastHaltedAt.Store(time.Now().Unix())
				log.Warnf("🚨 Circuit Breaker 打开：连续错误达到阈值 (errors=%d/%d)", errors, maxErr)
			}
			return ErrCircuitBreakerOpen
		}
	}

	// 当日亏损熔断（若启用）
	limit := cb.dailyLossLimitCents.Load()
	if limit > 0 {
		cb.rollDayIfNeeded()
		pnl := cb.dailyPnlCents.Load()
		if pnl <= -limit {
			// 达到亏损阈值，触发熔断
			if cb.halted.CompareAndSwap(false, true) {
				cb.lastHaltedAt.Store(time.Now().Unix())
				log.Warnf("🚨 Circuit Breaker 打开：当日亏损达到阈值 (pnl=%dc, limit=%dc)", pnl, limit)
			}
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
	prevErrors := cb.consecutiveErrors.Load()
	if prevErrors > 0 {
		cb.consecutiveErrors.Store(0)
		log.Debugf("✅ Circuit Breaker: 成功执行，重置错误计数 (prev=%d)", prevErrors)
	}
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

