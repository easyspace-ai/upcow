package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdkrelayer "github.com/betbot/gobet/pkg/sdk/relayer"
	sdktypes "github.com/betbot/gobet/pkg/sdk/types"
	"github.com/ethereum/go-ethereum/crypto"
	"math/big"
)

type createAccountRequest struct {
	AccountID string `json:"account_id"`
	Name      string `json:"name"`
}

func (s *Server) handleAccountsCreate(w http.ResponseWriter, r *http.Request) {
	var req createAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid json body")
		return
	}
	req.AccountID = strings.TrimSpace(req.AccountID)
	req.Name = strings.TrimSpace(req.Name)

	accountID, err := normalizeAccountID(req.AccountID)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if req.Name == "" {
		req.Name = accountID
	}

	mnemonic, err := s.loadMnemonic()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	derivationPath, err := derivationPathFromAccountID(accountID)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}

	derived, err := deriveWalletFromMnemonic(mnemonic, derivationPath)
	if err != nil {
		writeError(w, 400, fmt.Sprintf("derive failed: %v", err))
		return
	}

	// compute expected Safe as funder address (official relayer model)
	funderAddr, warn, err := computeAndMaybeDeploySafe(derived.PrivateKeyHex, derived.EOAAddress)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("register failed: %v", err))
		return
	}

	now := time.Now()
	row := accountRow{
		Account: Account{
			ID:             accountID,
			Name:           req.Name,
			DerivationPath: derivationPath,
			EOAAddress:     derived.EOAAddress,
			FunderAddress:  funderAddr,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		// mnemonic is stored in local encrypted file, not in sqlite
		MnemonicEnc: "",
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.insertAccount(ctx, row); err != nil {
		writeError(w, 500, fmt.Sprintf("db insert: %v", err))
		return
	}
	if warn != "" {
		writeJSON(w, 201, map[string]any{"account": row.Account, "warning": warn})
		return
	}
	writeJSON(w, 201, map[string]any{"account": row.Account})
}

func (s *Server) handleAccountsList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	accounts, err := s.listAccounts(ctx)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db list: %v", err))
		return
	}
	// Ensure JSON is [] not null when empty.
	if accounts == nil {
		accounts = []Account{}
	}
	writeJSON(w, 200, accounts)
}

func (s *Server) handleAccountGet(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(chiURLParam(r, "accountID"))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	a, err := s.getAccount(ctx, accountID)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db get: %v", err))
		return
	}
	if a == nil {
		writeError(w, 404, "account not found")
		return
	}
	bound, botID, _ := s.isAccountBound(ctx, accountID)
	writeJSON(w, 200, map[string]any{"account": a, "bound": bound, "bot_id": botID})
}

type updateAccountRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleAccountUpdate(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(chiURLParam(r, "accountID"))

	var req updateAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid json body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)

	if req.Name == "" {
		writeError(w, 400, "name is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := s.updateAccountName(ctx, accountID, req.Name); err != nil {
		writeError(w, 500, fmt.Sprintf("db update: %v", err))
		return
	}

	writeJSON(w, 200, map[string]any{"ok": true})
}

// reveal-mnemonic：按你要求提供，但需要 admin token（避免默认泄露）
func (s *Server) handleAccountRevealMnemonic(w http.ResponseWriter, r *http.Request) {
	// New security model: mnemonic never flows through HTTP and is never stored in sqlite.
	// Use cmd/mnemonic-init to provision local encrypted mnemonic file before starting the server.
	writeError(w, 410, "mnemonic is managed via local encrypted file; HTTP reveal is disabled")
}

func computeAndMaybeDeploySafe(privateKeyHex string, eoaAddress string) (funderAddress string, warning string, err error) {
	// Always compute expected safe address as funder.
	chainID := big.NewInt(137)
	relayerURL := strings.TrimSpace(os.Getenv("POLYMARKET_RELAYER_URL"))
	if relayerURL == "" {
		relayerURL = "https://relayer-v2.polymarket.com"
	}
	// Minimal client: GetExpectedSafe does not require headers/signing.
	rc := sdkrelayer.NewClient(relayerURL, chainID, nil, nil)
	safeAddr, err := rc.GetExpectedSafe(eoaAddress)
	if err != nil {
		return "", "", err
	}

	// Optional: attempt to deploy if configured.
	autoDeploy := strings.EqualFold(strings.TrimSpace(os.Getenv("GOBET_ACCOUNT_AUTO_DEPLOY_SAFE")), "true") ||
		strings.TrimSpace(os.Getenv("GOBET_ACCOUNT_AUTO_DEPLOY_SAFE")) == "1"
	if !autoDeploy {
		return safeAddr, "", nil
	}

	bc, err := loadBuilderCredsFromEnv()
	if err != nil {
		return safeAddr, "auto deploy skipped: missing builder creds", nil
	}

	pk, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(privateKeyHex), "0x"))
	if err != nil {
		return safeAddr, "", err
	}
	signFn := func(signer string, digest []byte) ([]byte, error) {
		_ = signer
		sig, err := crypto.Sign(digest, pk)
		if err != nil {
			return nil, err
		}
		if sig[64] < 27 {
			sig[64] += 27
		}
		return sig, nil
	}

	rc2 := sdkrelayer.NewClient(relayerURL, chainID, signFn, &sdktypes.BuilderApiKeyCreds{
		Key:        bc.Key,
		Secret:     bc.Secret,
		Passphrase: bc.Passphrase,
	})
	deployed, err := rc2.GetDeployed(safeAddr)
	if err != nil {
		return safeAddr, "auto deploy skipped: GetDeployed failed", nil
	}
	if deployed.Deployed {
		return safeAddr, "", nil
	}
	_, err = rc2.Deploy(&sdktypes.AuthOption{SingerAddress: eoaAddress, FunderAddress: safeAddr})
	if err != nil {
		// Non-fatal: safe can be deployed later; funder address is deterministic.
		return safeAddr, "auto deploy failed (safe not deployed yet)", nil
	}
	return safeAddr, "", nil
}

func (s *Server) handleAccountSyncBalance(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(chiURLParam(r, "accountID"))
	runID, err := s.startBalanceSyncAccount(accountID, "manual")
	if err != nil {
		writeError(w, 500, fmt.Sprintf("start job failed: %v", err))
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "run_id": runID})
}

func (s *Server) handleAccountRedeem(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(chiURLParam(r, "accountID"))
	runID, err := s.startRedeemAccount(accountID, "manual")
	if err != nil {
		writeError(w, 500, fmt.Sprintf("start job failed: %v", err))
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "run_id": runID})
}

func (s *Server) handleAccountSyncTrades(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(chiURLParam(r, "accountID"))
	runID, err := s.startTradesSyncAccount(accountID, "manual")
	if err != nil {
		writeError(w, 500, fmt.Sprintf("start job failed: %v", err))
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "run_id": runID})
}

func (s *Server) handleAccountSyncPositions(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(chiURLParam(r, "accountID"))
	runID, err := s.startPositionsSyncAccount(accountID, "manual")
	if err != nil {
		writeError(w, 500, fmt.Sprintf("start job failed: %v", err))
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "run_id": runID})
}

func (s *Server) handleAccountSyncOpenOrders(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(chiURLParam(r, "accountID"))
	runID, err := s.startOpenOrdersSyncAccount(accountID, "manual")
	if err != nil {
		writeError(w, 500, fmt.Sprintf("start job failed: %v", err))
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "run_id": runID})
}

func (s *Server) handleAccountEquity(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(chiURLParam(r, "accountID"))
	limit := 200
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 2000 {
			limit = n
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	snaps, err := s.listEquitySnapshots(ctx, accountID, limit)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db list equity: %v", err))
		return
	}
	writeJSON(w, 200, map[string]any{"account_id": accountID, "equity": snaps})
}

func (s *Server) handleAccountEquitySnapshotNow(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(chiURLParam(r, "accountID"))
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.createEquitySnapshotForAccount(ctx, accountID); err != nil {
		writeError(w, 500, fmt.Sprintf("create equity snapshot failed: %v", err))
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true})
}
