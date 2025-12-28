package server

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"
)

func (s *Server) startTradesSyncBatch(trigger string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	meta, _ := json.Marshal(map[string]any{"trigger": trigger})
	metaStr := string(meta)
	runID, err := s.insertJobRunStart(ctx, "trades_sync", "batch", nil, &metaStr)
	if err != nil {
		return 0, err
	}
	go func() {
		jobCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		s.doTradesSyncBatch(jobCtx, runID, trigger)
	}()
	return runID, nil
}

func (s *Server) doTradesSyncBatch(ctx context.Context, runID int64, trigger string) {
	mnemonic, err := s.loadMnemonic()
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	accounts, err := s.listAccounts(ctx)
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	okCount := 0
	errCount := 0
	insertedTotal := 0
	for _, a := range accounts {
		path, err := derivationPathFromAccountID(a.ID)
		if err != nil {
			errCount++
			continue
		}
		derived, err := deriveWalletFromMnemonic(mnemonic, path)
		if err != nil {
			errCount++
			continue
		}
		n, _, err := s.syncAccountTrades(ctx, a, derived.PrivateKeyHex)
		if err != nil {
			errCount++
			continue
		}
		insertedTotal += n
		okCount++
	}
	meta2, _ := json.Marshal(map[string]any{"trigger": trigger, "accounts": len(accounts), "ok": okCount, "err": errCount, "inserted": insertedTotal})
	metaStr2 := string(meta2)
	_ = s.finishJobRun(ctx, runID, errCount == 0, nilIfEmpty(errCount, "some accounts failed"), &metaStr2)
}

func (s *Server) startPositionsSyncBatch(trigger string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	meta, _ := json.Marshal(map[string]any{"trigger": trigger})
	metaStr := string(meta)
	runID, err := s.insertJobRunStart(ctx, "positions_sync", "batch", nil, &metaStr)
	if err != nil {
		return 0, err
	}
	go func() {
		jobCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		s.doPositionsSyncBatch(jobCtx, runID, trigger)
	}()
	return runID, nil
}

func (s *Server) doPositionsSyncBatch(ctx context.Context, runID int64, trigger string) {
	accounts, err := s.listAccounts(ctx)
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	okCount := 0
	errCount := 0
	for _, a := range accounts {
		if err := s.syncAccountPositions(ctx, a); err != nil {
			errCount++
			continue
		}
		okCount++
	}
	meta2, _ := json.Marshal(map[string]any{"trigger": trigger, "accounts": len(accounts), "ok": okCount, "err": errCount})
	metaStr2 := string(meta2)
	_ = s.finishJobRun(ctx, runID, errCount == 0, nilIfEmpty(errCount, "some accounts failed"), &metaStr2)
}

func (s *Server) startOpenOrdersSyncBatch(trigger string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	meta, _ := json.Marshal(map[string]any{"trigger": trigger})
	metaStr := string(meta)
	runID, err := s.insertJobRunStart(ctx, "open_orders_sync", "batch", nil, &metaStr)
	if err != nil {
		return 0, err
	}
	go func() {
		jobCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		s.doOpenOrdersSyncBatch(jobCtx, runID, trigger)
	}()
	return runID, nil
}

func (s *Server) doOpenOrdersSyncBatch(ctx context.Context, runID int64, trigger string) {
	mnemonic, err := s.loadMnemonic()
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	host := strings.TrimSpace(os.Getenv("CLOB_API_URL"))
	_ = host
	accounts, err := s.listAccounts(ctx)
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	okCount := 0
	errCount := 0
	for _, a := range accounts {
		path, err := derivationPathFromAccountID(a.ID)
		if err != nil {
			errCount++
			continue
		}
		derived, err := deriveWalletFromMnemonic(mnemonic, path)
		if err != nil {
			errCount++
			continue
		}
		if err := s.syncAccountOpenOrders(ctx, a, derived.PrivateKeyHex); err != nil {
			errCount++
			continue
		}
		okCount++
	}
	meta2, _ := json.Marshal(map[string]any{"trigger": trigger, "accounts": len(accounts), "ok": okCount, "err": errCount})
	metaStr2 := string(meta2)
	_ = s.finishJobRun(ctx, runID, errCount == 0, nilIfEmpty(errCount, "some accounts failed"), &metaStr2)
}
