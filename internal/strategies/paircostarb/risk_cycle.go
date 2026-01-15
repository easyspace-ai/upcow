package paircostarb

import (
	"time"

	"github.com/betbot/gobet/internal/domain"
	gcfg "github.com/betbot/gobet/pkg/config"
)

func isInCycleEndProtection(cfg Config, now time.Time, market *domain.Market) bool {
	if market == nil || market.Timestamp <= 0 {
		return false
	}
	// 优先用 StopTimeBufferSeconds
	if cfg.StopTimeBufferSeconds > 0 {
		cycleDur := 15 * time.Minute
		if gc := gcfg.Get(); gc != nil {
			if sp, err := gc.Market.Spec(); err == nil {
				if d := sp.Duration(); d > 0 {
					cycleDur = d
				}
			}
		}
		start := time.Unix(market.Timestamp, 0)
		end := start.Add(cycleDur)
		return end.Sub(now) <= time.Duration(cfg.StopTimeBufferSeconds)*time.Second
	}
	if cfg.CycleEndProtectionMinutes <= 0 {
		return false
	}
	cycleDur := 15 * time.Minute
	if gc := gcfg.Get(); gc != nil {
		if sp, err := gc.Market.Spec(); err == nil {
			if d := sp.Duration(); d > 0 {
				cycleDur = d
			}
		}
	}
	start := time.Unix(market.Timestamp, 0)
	end := start.Add(cycleDur)
	protect := time.Duration(cfg.CycleEndProtectionMinutes) * time.Minute
	return end.Sub(now) <= protect
}
