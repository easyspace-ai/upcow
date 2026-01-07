package oms

import (
	"fmt"
	"time"
)

type entryBudget struct {
	marketSlug string
	startedAt  time.Time

	reorders int
	cancels  int
	faks     int
}

type cooldownInfo struct {
	until  time.Time
	reason string
}

type entryBudgetConfig interface {
	GetPerEntryMaxHedgeReorders() int
	GetPerEntryMaxHedgeCancels() int
	GetPerEntryMaxHedgeFAK() int
	GetPerEntryMaxAgeSeconds() int
	GetPerEntryCooldownSeconds() int
}

func (o *OMS) entryGuardParams() (maxReorders, maxCancels, maxFAK int, maxAge time.Duration, cooldown time.Duration) {
	// defaults：偏“保守职业交易执行”
	maxReorders = 3
	maxCancels = 6
	maxFAK = 1
	maxAge = 120 * time.Second
	cooldown = 30 * time.Second

	if o == nil || o.config == nil {
		return
	}
	if c, ok := o.config.(entryBudgetConfig); ok {
		if v := c.GetPerEntryMaxHedgeReorders(); v > 0 {
			maxReorders = v
		}
		if v := c.GetPerEntryMaxHedgeCancels(); v > 0 {
			maxCancels = v
		}
		if v := c.GetPerEntryMaxHedgeFAK(); v > 0 {
			maxFAK = v
		}
		if v := c.GetPerEntryMaxAgeSeconds(); v > 0 {
			maxAge = time.Duration(v) * time.Second
		}
		if v := c.GetPerEntryCooldownSeconds(); v > 0 {
			cooldown = time.Duration(v) * time.Second
		}
	}
	return
}

func (o *OMS) ensureGuardMaps() {
	if o.entryBudgets == nil {
		o.entryBudgets = make(map[string]*entryBudget)
	}
	if o.cooldowns == nil {
		o.cooldowns = make(map[string]cooldownInfo)
	}
}

func (o *OMS) initEntryBudget(entryOrderID, marketSlug string, startedAt time.Time) {
	if o == nil || entryOrderID == "" || marketSlug == "" {
		return
	}
	o.ensureGuardMaps()
	if _, ok := o.entryBudgets[entryOrderID]; ok {
		return
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	o.entryBudgets[entryOrderID] = &entryBudget{
		marketSlug: marketSlug,
		startedAt:  startedAt,
	}
}

func (o *OMS) clearEntryBudget(entryOrderID string) {
	if o == nil || entryOrderID == "" || o.entryBudgets == nil {
		return
	}
	delete(o.entryBudgets, entryOrderID)
}

func (o *OMS) setCooldownLocked(marketSlug string, dur time.Duration, reason string) {
	if marketSlug == "" {
		return
	}
	o.ensureGuardMaps()
	if dur <= 0 {
		dur = 30 * time.Second
	}
	until := time.Now().Add(dur)
	cur, ok := o.cooldowns[marketSlug]
	// 延长，不缩短；原因以最新为准（更贴近排障）
	if ok && cur.until.After(until) {
		until = cur.until
	}
	o.cooldowns[marketSlug] = cooldownInfo{until: until, reason: reason}
}

// IsMarketInCooldown 冷静期总闸：仅用于禁止“新开仓”，不影响风险处理流程。
func (o *OMS) IsMarketInCooldown(marketSlug string) (bool, time.Duration, string) {
	if o == nil || marketSlug == "" {
		return false, 0, ""
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cooldowns == nil {
		return false, 0, ""
	}
	cd, ok := o.cooldowns[marketSlug]
	if !ok {
		return false, 0, ""
	}
	now := time.Now()
	if now.After(cd.until) {
		delete(o.cooldowns, marketSlug)
		return false, 0, ""
	}
	return true, cd.until.Sub(now), cd.reason
}

// ConsumeReorderAttempt 尝试消耗一次“对冲重下”预算；若超限则返回 false，并触发 market 冷静期。
func (o *OMS) ConsumeReorderAttempt(entryOrderID, marketSlug string, entryFilledTime time.Time) bool {
	if o == nil || entryOrderID == "" || marketSlug == "" {
		return true
	}
	maxReorders, _, _, maxAge, cooldown := o.entryGuardParams()

	o.mu.Lock()
	defer o.mu.Unlock()
	o.ensureGuardMaps()
	o.initEntryBudget(entryOrderID, marketSlug, entryFilledTime)
	b := o.entryBudgets[entryOrderID]

	age := time.Since(b.startedAt)
	if maxAge > 0 && age > maxAge {
		o.setCooldownLocked(marketSlug, cooldown, fmt.Sprintf("entry_age_exceeded %.0fs", age.Seconds()))
		return false
	}

	if maxReorders > 0 && b.reorders >= maxReorders {
		o.setCooldownLocked(marketSlug, cooldown, fmt.Sprintf("entry_reorder_exceeded %d", b.reorders))
		return false
	}
	b.reorders++
	return true
}

// RecordCancel 记录一次撤单动作；撤单不阻断，但超限会触发 market 冷静期（防风暴）。
func (o *OMS) RecordCancel(entryOrderID, marketSlug string, entryFilledTime time.Time) {
	if o == nil || entryOrderID == "" || marketSlug == "" {
		return
	}
	_, maxCancels, _, _, cooldown := o.entryGuardParams()

	o.mu.Lock()
	defer o.mu.Unlock()
	o.ensureGuardMaps()
	o.initEntryBudget(entryOrderID, marketSlug, entryFilledTime)
	b := o.entryBudgets[entryOrderID]
	b.cancels++
	if maxCancels > 0 && b.cancels > maxCancels {
		o.setCooldownLocked(marketSlug, cooldown, fmt.Sprintf("entry_cancel_exceeded %d", b.cancels))
	}
}

// RecordFAK 记录一次 FAK 动作；FAK 属于安全底线，不阻断，但超限会触发 market 冷静期。
func (o *OMS) RecordFAK(entryOrderID, marketSlug string, entryFilledTime time.Time) {
	if o == nil || entryOrderID == "" || marketSlug == "" {
		return
	}
	_, _, maxFAK, _, cooldown := o.entryGuardParams()

	o.mu.Lock()
	defer o.mu.Unlock()
	o.ensureGuardMaps()
	o.initEntryBudget(entryOrderID, marketSlug, entryFilledTime)
	b := o.entryBudgets[entryOrderID]
	b.faks++
	if maxFAK > 0 && b.faks > maxFAK {
		o.setCooldownLocked(marketSlug, cooldown, fmt.Sprintf("entry_fak_exceeded %d", b.faks))
	}
}

