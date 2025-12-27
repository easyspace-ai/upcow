package server

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	sdkapi "github.com/betbot/gobet/pkg/sdk/api"
	sdkredeem "github.com/betbot/gobet/pkg/sdk/redeem"
	sdktypes "github.com/betbot/gobet/pkg/sdk/types"
)

func (s *Server) startBackground() {
	ctx, cancel := context.WithCancel(context.Background())
	s.bgCancel = cancel

	balanceInterval := parseDurationEnv("GOBET_BALANCE_SYNC_INTERVAL", 60*time.Second)
	redeemInterval := parseDurationEnv("GOBET_REDEEM_INTERVAL", 3*time.Minute)
	tradesInterval := parseDurationEnv("GOBET_TRADES_SYNC_INTERVAL", 60*time.Second)
	positionsInterval := parseDurationEnv("GOBET_POSITIONS_SYNC_INTERVAL", 60*time.Second)
	openOrdersInterval := parseDurationEnv("GOBET_OPEN_ORDERS_SYNC_INTERVAL", 60*time.Second)

	s.bgWG.Add(5)
	go func() {
		defer s.bgWG.Done()
		s.balanceSyncLoop(ctx, balanceInterval)
	}()
	go func() {
		defer s.bgWG.Done()
		s.redeemLoop(ctx, redeemInterval)
	}()
	go func() {
		defer s.bgWG.Done()
		s.tradesSyncLoop(ctx, tradesInterval)
	}()
	go func() {
		defer s.bgWG.Done()
		s.positionsSyncLoop(ctx, positionsInterval)
	}()
	go func() {
		defer s.bgWG.Done()
		s.openOrdersSyncLoop(ctx, openOrdersInterval)
	}()
}

func (s *Server) balanceSyncLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = s.startBalanceSyncBatch("scheduled")
		}
	}
}

func (s *Server) redeemLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	// 没有 builder creds 时直接不跑定时 redeem（避免刷失败记录）
	if _, err := loadBuilderCredsFromEnv(); err != nil {
		return
	}
	if strings.TrimSpace(os.Getenv("GOBET_MASTER_KEY")) == "" {
		return
	}

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = s.startRedeemBatch("scheduled")
		}
	}
}

func (s *Server) tradesSyncLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	if strings.TrimSpace(os.Getenv("GOBET_MASTER_KEY")) == "" {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = s.startTradesSyncBatch("scheduled")
		}
	}
}

func (s *Server) positionsSyncLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = s.startPositionsSyncBatch("scheduled")
		}
	}
}

func (s *Server) openOrdersSyncLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	if strings.TrimSpace(os.Getenv("GOBET_MASTER_KEY")) == "" {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = s.startOpenOrdersSyncBatch("scheduled")
		}
	}
}

func parseDurationEnv(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		// 兼容纯数字秒
		if n, err2 := strconv.Atoi(v); err2 == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
		return def
	}
	return d
}

type builderCreds struct {
	Key        string
	Secret     string
	Passphrase string
}

func loadBuilderCredsFromEnv() (*builderCreds, error) {
	key := strings.TrimSpace(os.Getenv("BUILDER_API_KEY"))
	secret := strings.TrimSpace(os.Getenv("BUILDER_SECRET"))
	pass := strings.TrimSpace(os.Getenv("BUILDER_PASS_PHRASE"))
	if key == "" || secret == "" || pass == "" {
		return nil, ErrMissingRedeemCreds
	}
	return &builderCreds{Key: key, Secret: secret, Passphrase: pass}, nil
}

var ErrMissingRedeemCreds = &simpleError{"missing BUILDER_API_KEY/BUILDER_SECRET/BUILDER_PASS_PHRASE"}

type simpleError struct{ s string }

func (e *simpleError) Error() string { return e.s }

// startBalanceSyncBatch creates job_run and runs asynchronously.
func (s *Server) startBalanceSyncBatch(trigger string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	meta, _ := json.Marshal(map[string]any{"trigger": trigger})
	metaStr := string(meta)
	runID, err := s.insertJobRunStart(ctx, "balance_sync", "batch", nil, &metaStr)
	if err != nil {
		return 0, err
	}

	go func() {
		jobCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		s.doBalanceSyncBatch(jobCtx, runID, trigger)
	}()
	return runID, nil
}

func (s *Server) doBalanceSyncBatch(ctx context.Context, runID int64, trigger string) {
	accounts, err := s.listAccounts(ctx)
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}

	workers := parseIntEnv("GOBET_JOB_WORKERS", 4, 1, 32)
	sem := make(chan struct{}, workers)

	type res struct {
		accountID string
		ok        bool
		err       string
		balance   float64
	}
	outCh := make(chan res, len(accounts))

	for _, a := range accounts {
		a := a
		select {
		case <-ctx.Done():
			break
		default:
		}
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			acctCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			bal, err := sdkapi.GetOnChainUSDCBalance(acctCtx, a.FunderAddress)
			if err != nil {
				outCh <- res{accountID: a.ID, ok: false, err: err.Error()}
				return
			}
			_ = s.insertBalanceSnapshot(acctCtx, a.ID, bal, "polygon_rpc")
			outCh <- res{accountID: a.ID, ok: true, balance: bal}
		}()
	}

	// wait for all workers to drain
	for i := 0; i < cap(sem); i++ {
		sem <- struct{}{}
	}

	close(outCh)
	var (
		okCount  int
		errCount int
	)
	for r := range outCh {
		if r.ok {
			okCount++
		} else {
			errCount++
		}
	}

	meta2, _ := json.Marshal(map[string]any{"trigger": trigger, "accounts": len(accounts), "ok": okCount, "err": errCount})
	metaStr2 := string(meta2)
	_ = s.finishJobRun(ctx, runID, errCount == 0, nilIfEmpty(errCount, "some accounts failed"), &metaStr2)
}

func nilIfEmpty(errCount int, msg string) *string {
	if errCount == 0 {
		return nil
	}
	return &msg
}

func parseIntEnv(key string, def int, min int, max int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func (s *Server) startRedeemBatch(trigger string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	meta, _ := json.Marshal(map[string]any{"trigger": trigger})
	metaStr := string(meta)
	runID, err := s.insertJobRunStart(ctx, "redeem", "batch", nil, &metaStr)
	if err != nil {
		return 0, err
	}
	go func() {
		jobCtx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		s.doRedeemBatch(jobCtx, runID, trigger)
	}()
	return runID, nil
}

func (s *Server) doRedeemBatch(ctx context.Context, runID int64, trigger string) {
	bc, err := loadBuilderCredsFromEnv()
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	masterKey, err := loadMasterKey()
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}

	baseURL := strings.TrimSpace(os.Getenv("POLYMARKET_API_URL"))
	if baseURL == "" {
		baseURL = "https://clob.polymarket.com"
	}

	accounts, err := s.listAccounts(ctx)
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}

	workers := parseIntEnv("GOBET_JOB_WORKERS", 2, 1, 8) // redeem 慢且限流，默认更低
	sem := make(chan struct{}, workers)

	type rr struct {
		ok      bool
		err     string
		redeems int
		usdc    float64
	}
	outCh := make(chan rr, len(accounts))

	for _, a := range accounts {
		a := a
		select {
		case <-ctx.Done():
			break
		default:
		}
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			acctCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
			defer cancel()

			mn, err := s.getAccountRow(acctCtx, a.ID)
			if err != nil || mn == nil {
				outCh <- rr{ok: false, err: "load account row failed"}
				return
			}
			mnemonic, err := decryptFromString(masterKey, mn.MnemonicEnc)
			if err != nil {
				outCh <- rr{ok: false, err: "decrypt mnemonic failed"}
				return
			}
			derived, err := deriveWalletFromMnemonic(mnemonic, a.DerivationPath)
			if err != nil {
				outCh <- rr{ok: false, err: "derive failed"}
				return
			}

			client := sdkapi.NewClient(baseURL)
			opts := sdkredeem.RunOnceOptions{
				PrivateKeyHex: derived.PrivateKeyHex,
				FunderAddress: a.FunderAddress,
				BuilderCreds: &sdktypes.BuilderApiKeyCreds{
					Key:        bc.Key,
					Secret:     bc.Secret,
					Passphrase: bc.Passphrase,
				},
				RelayerURL: strings.TrimSpace(os.Getenv("POLYMARKET_RELAYER_URL")),
			}
			res, err := sdkredeem.RunOnce(acctCtx, client, opts)
			if err != nil {
				outCh <- rr{ok: false, err: err.Error()}
				return
			}
			outCh <- rr{ok: true, redeems: res.Redeemed, usdc: res.TotalUSDC}
		}()
	}

	for i := 0; i < cap(sem); i++ {
		sem <- struct{}{}
	}
	close(outCh)

	var (
		okCount      int
		errCount     int
		totalRedeems int
		totalUSDC    float64
	)
	for r := range outCh {
		if r.ok {
			okCount++
			totalRedeems += r.redeems
			totalUSDC += r.usdc
		} else {
			errCount++
		}
	}

	meta2, _ := json.Marshal(map[string]any{
		"trigger":       trigger,
		"accounts":      len(accounts),
		"ok":            okCount,
		"err":           errCount,
		"redeems_total": totalRedeems,
		"usdc_total":    totalUSDC,
	})
	metaStr2 := string(meta2)
	_ = s.finishJobRun(ctx, runID, errCount == 0, nilIfEmpty(errCount, "some accounts failed"), &metaStr2)
}
