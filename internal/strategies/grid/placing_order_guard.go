package grid

import (
	"time"

	"github.com/betbot/gobet/internal/domain"
)

const placingOrderTimeout = 60 * time.Second

// diagnoseAndResetPlacingOrder checks whether isPlacingOrder is stuck.
// If the flag is set for too long, it resets it to avoid strategy deadlock.
//
// It is safe to call from anywhere (handles its own locking).
func (s *GridStrategy) diagnoseAndResetPlacingOrder(tokenType domain.TokenType, priceCents int, marketSlug string) {
	s.placeOrderMu.Lock()
	defer s.placeOrderMu.Unlock()

	if !s.isPlacingOrder {
		return
	}
	if s.isPlacingOrderSetTime.IsZero() {
		log.Warnf("⚠️ [价格更新诊断] onPriceChangedInternal开始处理但 isPlacingOrder=true (SetTime未设置): %s @ %dc, market=%s",
			tokenType, priceCents, marketSlug)
		return
	}

	timeSinceSet := time.Since(s.isPlacingOrderSetTime)
	if timeSinceSet > placingOrderTimeout {
		log.Warnf("⚠️ [价格更新诊断] isPlacingOrder标志已持续%v（超过%v），强制重置: %s @ %dc",
			timeSinceSet, placingOrderTimeout, tokenType, priceCents)
		s.isPlacingOrder = false
		s.isPlacingOrderSetTime = time.Time{}
		return
	}

	log.Warnf("⚠️ [价格更新诊断] onPriceChangedInternal开始处理但 isPlacingOrder=true (已持续%v): %s @ %dc, market=%s",
		timeSinceSet, tokenType, priceCents, marketSlug)
}
