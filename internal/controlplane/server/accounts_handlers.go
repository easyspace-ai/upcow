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

	"github.com/google/uuid"
)

type createAccountRequest struct {
	Name           string `json:"name"`
	Mnemonic       string `json:"mnemonic"`
	DerivationPath string `json:"derivation_path"`
	FunderAddress  string `json:"funder_address"`
}

func (s *Server) handleAccountsCreate(w http.ResponseWriter, r *http.Request) {
	masterKey, err := loadMasterKey()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	var req createAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid json body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Mnemonic = strings.TrimSpace(req.Mnemonic)
	req.DerivationPath = strings.TrimSpace(req.DerivationPath)
	req.FunderAddress = strings.TrimSpace(req.FunderAddress)

	if req.Name == "" || req.Mnemonic == "" || req.DerivationPath == "" || req.FunderAddress == "" {
		writeError(w, 400, "name, mnemonic, derivation_path, funder_address are required")
		return
	}

	derived, err := deriveWalletFromMnemonic(req.Mnemonic, req.DerivationPath)
	if err != nil {
		writeError(w, 400, fmt.Sprintf("derive failed: %v", err))
		return
	}

	enc, err := encryptToString(masterKey, req.Mnemonic)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("encrypt failed: %v", err))
		return
	}

	now := time.Now()
	id := uuid.NewString()
	row := accountRow{
		Account: Account{
			ID:             id,
			Name:           req.Name,
			DerivationPath: req.DerivationPath,
			EOAAddress:     derived.EOAAddress,
			FunderAddress:  req.FunderAddress,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		MnemonicEnc: enc,
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.insertAccount(ctx, row); err != nil {
		writeError(w, 500, fmt.Sprintf("db insert: %v", err))
		return
	}
	writeJSON(w, 201, row.Account)
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
	Name           string  `json:"name"`
	Mnemonic       *string `json:"mnemonic,omitempty"`
	DerivationPath string  `json:"derivation_path"`
	FunderAddress  string  `json:"funder_address"`
}

func (s *Server) handleAccountUpdate(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(chiURLParam(r, "accountID"))

	var req updateAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid json body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.DerivationPath = strings.TrimSpace(req.DerivationPath)
	req.FunderAddress = strings.TrimSpace(req.FunderAddress)

	if req.Name == "" || req.DerivationPath == "" || req.FunderAddress == "" {
		writeError(w, 400, "name, derivation_path, funder_address are required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	row, err := s.getAccountRow(ctx, accountID)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db get: %v", err))
		return
	}
	if row == nil {
		writeError(w, 404, "account not found")
		return
	}

	masterKey, err := loadMasterKey()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	mnemonic := ""
	var mnemonicEnc *string
	if req.Mnemonic != nil {
		mnemonic = strings.TrimSpace(*req.Mnemonic)
	} else {
		mnemonic, err = decryptFromString(masterKey, row.MnemonicEnc)
		if err != nil {
			writeError(w, 500, fmt.Sprintf("decrypt failed: %v", err))
			return
		}
	}

	derived, err := deriveWalletFromMnemonic(mnemonic, req.DerivationPath)
	if err != nil {
		writeError(w, 400, fmt.Sprintf("derive failed: %v", err))
		return
	}

	if req.Mnemonic != nil {
		enc, err := encryptToString(masterKey, mnemonic)
		if err != nil {
			writeError(w, 500, fmt.Sprintf("encrypt failed: %v", err))
			return
		}
		mnemonicEnc = &enc
	}

	if err := s.updateAccount(ctx, accountID, req.Name, mnemonicEnc, req.DerivationPath, derived.EOAAddress, req.FunderAddress); err != nil {
		writeError(w, 500, fmt.Sprintf("db update: %v", err))
		return
	}

	writeJSON(w, 200, map[string]any{"ok": true})
}

// reveal-mnemonic：按你要求提供，但需要 admin token（避免默认泄露）
func (s *Server) handleAccountRevealMnemonic(w http.ResponseWriter, r *http.Request) {
	adminToken := strings.TrimSpace(os.Getenv("GOBET_ADMIN_TOKEN"))
	if adminToken == "" {
		writeError(w, 500, "GOBET_ADMIN_TOKEN not set")
		return
	}
	if strings.TrimSpace(r.Header.Get("X-Admin-Token")) != adminToken {
		writeError(w, 403, "forbidden")
		return
	}

	accountID := strings.TrimSpace(chiURLParam(r, "accountID"))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	row, err := s.getAccountRow(ctx, accountID)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db get: %v", err))
		return
	}
	if row == nil {
		writeError(w, 404, "account not found")
		return
	}

	masterKey, err := loadMasterKey()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	mnemonic, err := decryptFromString(masterKey, row.MnemonicEnc)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("decrypt failed: %v", err))
		return
	}
	writeJSON(w, 200, map[string]any{"mnemonic": mnemonic})
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
