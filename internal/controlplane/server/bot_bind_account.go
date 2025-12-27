package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type bindAccountRequest struct {
	AccountID string `json:"account_id"`
}

func (s *Server) handleBotBindAccount(w http.ResponseWriter, r *http.Request) {
	botID := chiURLParam(r, "botID")

	var req bindAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid json body")
		return
	}
	req.AccountID = strings.TrimSpace(req.AccountID)
	accountID, err := normalizeAccountID(req.AccountID)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	bot, err := s.getBot(ctx, botID)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db get bot: %v", err))
		return
	}
	if bot == nil {
		writeError(w, 404, "bot not found")
		return
	}

	acct, err := s.getAccount(ctx, accountID)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db get account: %v", err))
		return
	}
	if acct == nil {
		writeError(w, 404, "account not found")
		return
	}

	// enforce one-to-one
	if err := s.ensureAccountNotBoundToOtherBot(ctx, accountID, botID); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if boundID, err := s.botBoundAccount(ctx, botID); err == nil && boundID != nil && *boundID != accountID {
		writeError(w, 409, fmt.Sprintf("bot already bound to account %s", *boundID))
		return
	}

	// New security model: do NOT inject private key into bot config or store it in sqlite.
	// We only bind account_id. Wallet will be derived on bot start from local encrypted mnemonic file + account_id.
	if err := s.bindBotAccount(ctx, botID, accountID); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, 409, "account already bound to another bot")
			return
		}
		writeError(w, 500, fmt.Sprintf("db bind bot: %v", err))
		return
	}

	writeJSON(w, 200, map[string]any{
		"ok":         true,
		"bot_id":     botID,
		"account_id": accountID,
		"note":       "account bound; restart bot to take effect (wallet is derived on start)",
	})
}
