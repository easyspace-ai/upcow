package grid

import (
	"fmt"
	"time"

	"github.com/betbot/gobet/internal/strategies/common"
)

// logHealthTick emits a low-frequency health log for live trading.
// It must be called from the strategy loop (single goroutine).
func (s *GridStrategy) logHealthTick() {
	if s == nil || s.config == nil {
		return
	}

	interval := time.Duration(s.config.HealthLogIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if s.healthLogDebouncer == nil {
		s.healthLogDebouncer = common.NewDebouncer(interval)
	} else {
		s.healthLogDebouncer.SetInterval(interval)
	}

	if ready, _ := s.healthLogDebouncer.ReadyNow(); !ready {
		return
	}
	s.healthLogDebouncer.MarkNow()

	slug := ""
	if s.currentMarket != nil {
		slug = s.currentMarket.Slug
	}

	upWin, downWin := s.profitsUSDC()
	minProfit := upWin
	if downWin < minProfit {
		minProfit = downWin
	}

	openOrders := 0
	if s.tradingService != nil {
		openOrders = len(s.getActiveOrders())
	}

	planState := "none"
	planID := ""
	if s.plan != nil {
		planState = string(s.plan.State)
		planID = s.plan.ID
	}

	log.Infof("[HEALTH] market=%s prices(up=%dc down=%dc) openOrders=%d plan=%s(%s) minProfit=%.4f upWin=%.4f downWin=%.4f rounds=%d/%d adhocStrongHedge(inFlight=%v)",
		slug,
		s.currentPriceUp, s.currentPriceDown,
		openOrders,
		planState, planID,
		minProfit, upWin, downWin,
		s.roundsThisPeriod, s.config.MaxRoundsPerPeriod,
		s.strongHedgeInFlight,
	)

	// 额外诊断：如果进入周期末窗口但仍 minProfit<0，明确提示
	if s.currentMarket != nil && s.isInHedgeLockWindow(s.currentMarket) && minProfit < 0 {
		log.Warnf("[HEALTH] 周期末窗口仍未 break-even：minProfit=%.4f (market=%s) %s",
			minProfit, slug, fmt.Sprintf("upWin=%.4f downWin=%.4f", upWin, downWin))
	}
}
