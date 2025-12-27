package server

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	sdkapi "github.com/betbot/gobet/pkg/sdk/api"
	sdkredeem "github.com/betbot/gobet/pkg/sdk/redeem"
	sdktypes "github.com/betbot/gobet/pkg/sdk/types"
)

func (s *Server) startBalanceSyncAccount(accountID string, trigger string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	metaJSON, _ := json.Marshal(map[string]any{"trigger": trigger})
	metaStr := string(metaJSON)
	runID, err := s.insertJobRunStart(ctx, "balance_sync", "account", &accountID, &metaStr)
	if err != nil {
		return 0, err
	}
	go func() {
		jobCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		s.doBalanceSyncAccount(jobCtx, runID, accountID, trigger)
	}()
	return runID, nil
}

func (s *Server) doBalanceSyncAccount(ctx context.Context, runID int64, accountID string, trigger string) {
	a, err := s.getAccount(ctx, accountID)
	if err != nil || a == nil {
		msg := "account not found"
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	bal, err := sdkapi.GetOnChainUSDCBalance(ctx, a.FunderAddress)
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}
	_ = s.insertBalanceSnapshot(ctx, a.ID, bal, "polygon_rpc")
	meta2, _ := json.Marshal(map[string]any{"trigger": trigger, "balance_usdc": bal})
	metaStr2 := string(meta2)
	_ = s.finishJobRun(ctx, runID, true, nil, &metaStr2)
}

func (s *Server) startRedeemAccount(accountID string, trigger string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	metaJSON, _ := json.Marshal(map[string]any{"trigger": trigger})
	metaStr := string(metaJSON)
	runID, err := s.insertJobRunStart(ctx, "redeem", "account", &accountID, &metaStr)
	if err != nil {
		return 0, err
	}
	go func() {
		jobCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		s.doRedeemAccount(jobCtx, runID, accountID, trigger)
	}()
	return runID, nil
}

func (s *Server) doRedeemAccount(ctx context.Context, runID int64, accountID string, trigger string) {
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
	mnemonic, err := loadMnemonicFromFile(masterKey)
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}

	baseURL := strings.TrimSpace(os.Getenv("POLYMARKET_API_URL"))
	if baseURL == "" {
		baseURL = "https://clob.polymarket.com"
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
	res, err := sdkredeem.RunOnce(ctx, client, opts)
	if err != nil {
		msg := err.Error()
		_ = s.finishJobRun(ctx, runID, false, &msg, nil)
		return
	}

	meta2, _ := json.Marshal(map[string]any{"trigger": trigger, "redeemed": res.Redeemed, "usdc": res.TotalUSDC})
	metaStr2 := string(meta2)
	_ = s.finishJobRun(ctx, runID, true, nil, &metaStr2)
}
