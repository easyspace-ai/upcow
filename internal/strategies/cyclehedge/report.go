package cyclehedge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/betbot/gobet/internal/domain"
)

type cycleReport struct {
	Strategy string `json:"strategy"`
	MarketSlug string `json:"marketSlug"`
	CycleStartUnix int64 `json:"cycleStartUnix"`
	CycleEndUnix   int64 `json:"cycleEndUnix"`
	GeneratedAtUnix int64 `json:"generatedAtUnix"`

	TargetNotionalUSDC float64 `json:"targetNotionalUSDC"`
	TargetShares       float64 `json:"targetShares"`

	// positions snapshot
	UpShares   float64 `json:"upShares"`
	DownShares float64 `json:"downShares"`
	MinShares  float64 `json:"minShares"`
	UnhedgedShares float64 `json:"unhedgedShares"`
	UpAvgPrice   float64 `json:"upAvgPrice"`
	DownAvgPrice float64 `json:"downAvgPrice"`

	// implied locked profit (if both sides exist)
	LockedProfitCentsPerShare int     `json:"lockedProfitCentsPerShare"`
	LockedProfitUSDC          float64 `json:"lockedProfitUSDC"`

	// 目标口径：无论 UP/DOWN 胜出都盈利（worst-case PnL）
	UpTotalCostUSDC   float64 `json:"upTotalCostUSDC"`
	DownTotalCostUSDC float64 `json:"downTotalCostUSDC"`
	TotalCostUSDC     float64 `json:"totalCostUSDC"`
	PnLUpWinUSDC      float64 `json:"pnlUpWinUSDC"`
	PnLDownWinUSDC    float64 `json:"pnlDownWinUSDC"`
	WorstCasePnLUSDC  float64 `json:"worstCasePnlUSDC"`

	// counters
	Quotes int64 `json:"quotes"`
	OrdersPlacedYes int64 `json:"ordersPlacedYes"`
	OrdersPlacedNo  int64 `json:"ordersPlacedNo"`
	Cancels         int64 `json:"cancels"`
	TakerCompletes  int64 `json:"takerCompletes"`
	Flattens        int64 `json:"flattens"`
	CloseoutCancels int64 `json:"closeoutCancels"`
	MaxSingleSideStops int64 `json:"maxSingleSideStops"`

	ProfitChoice map[string]int64 `json:"profitChoice"`
	LastChosenProfit int `json:"lastChosenProfit"`

	// 成本计算监控统计
	CostCalculations int64 `json:"costCalculations"`
	CostCalculationErrors int64 `json:"costCalculationErrors"`
	CostBasisUsed int64 `json:"costBasisUsed"`
	CostAvgPriceUsed int64 `json:"costAvgPriceUsed"`
	CostEntryPriceUsed int64 `json:"costEntryPriceUsed"`
	CostSizeMismatches int64 `json:"costSizeMismatches"`
}

func (s *Strategy) finalizeAndReport(ctx context.Context, oldMarket *domain.Market) {
	if s == nil || oldMarket == nil || s.TradingService == nil {
		return
	}
	if s.EnableReport != nil && !*s.EnableReport {
		return
	}

	// snapshot stats (under lock)
	s.stateMu.Lock()
	st := s.stats
	s.stateMu.Unlock()

	// ensure we only report matching market (avoid duplicate on weird cycle calls)
	if st.MarketSlug != "" && oldMarket.Slug != "" && st.MarketSlug != oldMarket.Slug {
		// if oldMarket is not what we tracked, still allow report by querying positions, but keep slug consistent with oldMarket
	}

	upPos, downPos := s.positionSnapshot(oldMarket.Slug)
	minShares := upPos.shares
	if downPos.shares < minShares {
		minShares = downPos.shares
	}
	unhedged := upPos.shares - downPos.shares
	if unhedged < 0 {
		unhedged = -unhedged
	}

	lockedProfitCents := 0
	lockedProfitUSDC := 0.0
	if upPos.avg > 0 && downPos.avg > 0 && minShares > 0 {
		lockedProfitCents = 100 - int(upPos.avg*100+0.5) - int(downPos.avg*100+0.5)
		lockedProfitUSDC = minShares * float64(lockedProfitCents) / 100.0
	}

	// 新目标：按“总成本 & 两种胜出情景”计算 PnL（以 USDC 计）
	upShares, downShares, upCostUSDC, downCostUSDC := s.currentTotals(oldMarket.Slug)
	totalCostUSDC := upCostUSDC + downCostUSDC
	pnlUpWinUSDC := upShares - totalCostUSDC
	pnlDownWinUSDC := downShares - totalCostUSDC
	worst := pnlUpWinUSDC
	if pnlDownWinUSDC < worst {
		worst = pnlDownWinUSDC
	}

	pc := make(map[string]int64, len(st.ProfitChoice))
	for k, v := range st.ProfitChoice {
		pc[fmt.Sprintf("%dc", k)] = v
	}

	now := time.Now()
	rep := cycleReport{
		Strategy: ID,
		MarketSlug: oldMarket.Slug,
		CycleStartUnix: oldMarket.Timestamp,
		CycleEndUnix: oldMarket.Timestamp + int64(s.CycleDurationSeconds),
		GeneratedAtUnix: now.Unix(),
		TargetNotionalUSDC: st.TargetNotionalUSDC,
		TargetShares:       st.TargetShares,
		UpShares:   upPos.shares,
		DownShares: downPos.shares,
		MinShares:  minShares,
		UnhedgedShares: unhedged,
		UpAvgPrice:   upPos.avg,
		DownAvgPrice: downPos.avg,
		LockedProfitCentsPerShare: lockedProfitCents,
		LockedProfitUSDC:          lockedProfitUSDC,
		UpTotalCostUSDC:   upCostUSDC,
		DownTotalCostUSDC: downCostUSDC,
		TotalCostUSDC:     totalCostUSDC,
		PnLUpWinUSDC:      pnlUpWinUSDC,
		PnLDownWinUSDC:    pnlDownWinUSDC,
		WorstCasePnLUSDC:  worst,
		Quotes: st.Quotes,
		OrdersPlacedYes: st.OrdersPlacedYes,
		OrdersPlacedNo:  st.OrdersPlacedNo,
		Cancels:         st.Cancels,
		TakerCompletes:  st.TakerCompletes,
		Flattens:        st.Flattens,
		CloseoutCancels: st.CloseoutCancels,
		MaxSingleSideStops: st.MaxSingleSideStops,
		ProfitChoice: pc,
		LastChosenProfit: st.LastChosenProfit,
		CostCalculations: st.CostCalculations,
		CostCalculationErrors: st.CostCalculationErrors,
		CostBasisUsed: st.CostBasisUsed,
		CostAvgPriceUsed: st.CostAvgPriceUsed,
		CostEntryPriceUsed: st.CostEntryPriceUsed,
		CostSizeMismatches: st.CostSizeMismatches,
	}

	_ = s.writeReportFiles(ctx, rep)
}

type posSnap struct {
	shares float64
	avg    float64
}

func (s *Strategy) positionSnapshot(marketSlug string) (up posSnap, down posSnap) {
	positions := s.TradingService.GetOpenPositionsForMarket(marketSlug)
	for _, p := range positions {
		if p == nil || !p.IsOpen() || p.Size <= 0 {
			continue
		}
		avg := p.AvgPrice
		if avg <= 0 && p.EntryPrice.Pips > 0 {
			avg = p.EntryPrice.ToDecimal()
		}
		switch p.TokenType {
		case domain.TokenTypeUp:
			up.shares += p.Size
			if up.avg <= 0 && avg > 0 {
				up.avg = avg
			}
		case domain.TokenTypeDown:
			down.shares += p.Size
			if down.avg <= 0 && avg > 0 {
				down.avg = avg
			}
		}
	}
	return up, down
}

func (s *Strategy) writeReportFiles(ctx context.Context, rep cycleReport) error {
	_ = ctx // reserved (future: respect deadline)
	dir := s.ReportDir
	if dir == "" {
		dir = "data/reports/cyclehedge"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Warnf("⚠️ [%s] 创建报表目录失败: dir=%s err=%v", ID, dir, err)
		return err
	}

	blob, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}

	if s.ReportWritePerCycle == nil || *s.ReportWritePerCycle {
		name := fmt.Sprintf("%s_%d.json", rep.MarketSlug, rep.CycleStartUnix)
		path := filepath.Join(dir, name)
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, blob, 0o644); err == nil {
			_ = os.Rename(tmp, path)
		}
	}

	if s.ReportWriteJSONL == nil || *s.ReportWriteJSONL {
		jlPath := filepath.Join(dir, "report.jsonl")
		f, err := os.OpenFile(jlPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		line, _ := json.Marshal(rep)
		_, _ = f.Write(append(line, '\n'))
	}

	log.Infof("📊 [%s] 周期报表已写入: market=%s dir=%s lockedProfit=%dc/share est=%.2f worstPnL=%.4f",
		ID, rep.MarketSlug, dir, rep.LockedProfitCentsPerShare, rep.LockedProfitUSDC, rep.WorstCasePnLUSDC)
	return nil
}

