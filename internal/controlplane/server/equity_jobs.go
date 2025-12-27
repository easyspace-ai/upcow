package server

import (
	"context"
	"encoding/json"
	"time"
)

func (s *Server) startEquitySnapshotBatch(trigger string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	meta, _ := json.Marshal(map[string]any{"trigger": trigger})
	metaStr := string(meta)
	runID, err := s.insertJobRunStart(ctx, "equity_snapshot", "batch", nil, &metaStr)
	if err != nil {
		return 0, err
	}
	go func() {
		jobCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		s.doEquitySnapshotBatch(jobCtx, runID, trigger)
	}()
	return runID, nil
}

func (s *Server) doEquitySnapshotBatch(ctx context.Context, runID int64, trigger string) {
	accounts, err := s.listAccounts(ctx)
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	okCount := 0
	errCount := 0
	for _, a := range accounts {
		if err := s.createEquitySnapshotForAccount(ctx, a.ID); err != nil {
			errCount++
			continue
		}
		okCount++
	}
	meta2, _ := json.Marshal(map[string]any{"trigger": trigger, "accounts": len(accounts), "ok": okCount, "err": errCount})
	metaStr2 := string(meta2)
	_ = s.finishJobRun(ctx, runID, errCount == 0, nilIfEmpty(errCount, "some accounts failed"), &metaStr2)
}

func (s *Server) createEquitySnapshotForAccount(ctx context.Context, accountID string) error {
	cash, ok, err := s.latestCashBalance(ctx, accountID)
	if err != nil {
		return err
	}
	if !ok {
		// 没有余额快照就不写净值
		cash = 0
	}
	posV, err := s.positionsValue(ctx, accountID)
	if err != nil {
		return err
	}
	snap := EquitySnapshot{
		AccountID:          accountID,
		CashUSDC:           cash,
		PositionsValueUSDC: posV,
		TotalEquityUSDC:    cash + posV,
		TS:                 time.Now(),
	}
	return s.insertEquitySnapshot(ctx, snap)
}
