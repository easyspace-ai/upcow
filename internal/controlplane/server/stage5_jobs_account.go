package server

import (
	"context"
	"encoding/json"
	"time"
)

func (s *Server) startTradesSyncAccount(accountID string, trigger string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	meta, _ := json.Marshal(map[string]any{"trigger": trigger})
	metaStr := string(meta)
	runID, err := s.insertJobRunStart(ctx, "trades_sync", "account", &accountID, &metaStr)
	if err != nil {
		return 0, err
	}
	go func() {
		jobCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		s.doTradesSyncAccount(jobCtx, runID, accountID, trigger)
	}()
	return runID, nil
}

func (s *Server) doTradesSyncAccount(ctx context.Context, runID int64, accountID string, trigger string) {
	mnemonic, err := s.loadMnemonic()
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	a, err := s.getAccount(ctx, accountID)
	if err != nil || a == nil {
		msg := "account not found"
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	path, err := derivationPathFromAccountID(accountID)
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	derived, err := deriveWalletFromMnemonic(mnemonic, path)
	if err != nil {
		msg := "derive failed"
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	inserted, lastTS, err := s.syncAccountTrades(ctx, *a, derived.PrivateKeyHex)
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	meta2, _ := json.Marshal(map[string]any{"trigger": trigger, "inserted": inserted, "last_ts": lastTS})
	metaStr2 := string(meta2)
	_ = s.finishJobRun(ctx, runID, true, nil, &metaStr2)
}

func (s *Server) startPositionsSyncAccount(accountID string, trigger string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	meta, _ := json.Marshal(map[string]any{"trigger": trigger})
	metaStr := string(meta)
	runID, err := s.insertJobRunStart(ctx, "positions_sync", "account", &accountID, &metaStr)
	if err != nil {
		return 0, err
	}
	go func() {
		jobCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		s.doPositionsSyncAccount(jobCtx, runID, accountID, trigger)
	}()
	return runID, nil
}

func (s *Server) doPositionsSyncAccount(ctx context.Context, runID int64, accountID string, trigger string) {
	a, err := s.getAccount(ctx, accountID)
	if err != nil || a == nil {
		msg := "account not found"
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	if err := s.syncAccountPositions(ctx, *a); err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	meta2, _ := json.Marshal(map[string]any{"trigger": trigger})
	metaStr2 := string(meta2)
	_ = s.finishJobRun(ctx, runID, true, nil, &metaStr2)
}

func (s *Server) startOpenOrdersSyncAccount(accountID string, trigger string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	meta, _ := json.Marshal(map[string]any{"trigger": trigger})
	metaStr := string(meta)
	runID, err := s.insertJobRunStart(ctx, "open_orders_sync", "account", &accountID, &metaStr)
	if err != nil {
		return 0, err
	}
	go func() {
		jobCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		s.doOpenOrdersSyncAccount(jobCtx, runID, accountID, trigger)
	}()
	return runID, nil
}

func (s *Server) doOpenOrdersSyncAccount(ctx context.Context, runID int64, accountID string, trigger string) {
	mnemonic, err := s.loadMnemonic()
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	a, err := s.getAccount(ctx, accountID)
	if err != nil || a == nil {
		msg := "account not found"
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	path, err := derivationPathFromAccountID(accountID)
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	derived, err := deriveWalletFromMnemonic(mnemonic, path)
	if err != nil {
		msg := "derive failed"
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	if err := s.syncAccountOpenOrders(ctx, *a, derived.PrivateKeyHex); err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	meta2, _ := json.Marshal(map[string]any{"trigger": trigger})
	metaStr2 := string(meta2)
	_ = s.finishJobRun(ctx, runID, true, nil, &metaStr2)
}
