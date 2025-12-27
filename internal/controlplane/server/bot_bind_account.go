package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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
	if req.AccountID == "" {
		writeError(w, 400, "account_id is required")
		return
	}

	masterKey, err := loadMasterKey()
	if err != nil {
		writeError(w, 500, err.Error())
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

	acctRow, err := s.getAccountRow(ctx, req.AccountID)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db get account: %v", err))
		return
	}
	if acctRow == nil {
		writeError(w, 404, "account not found")
		return
	}

	// enforce one-to-one
	if err := s.ensureAccountNotBoundToOtherBot(ctx, req.AccountID, botID); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if boundID, err := s.botBoundAccount(ctx, botID); err == nil && boundID != nil && *boundID != req.AccountID {
		writeError(w, 409, fmt.Sprintf("bot already bound to account %s", *boundID))
		return
	}

	mnemonic, err := decryptFromString(masterKey, acctRow.MnemonicEnc)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("decrypt mnemonic failed: %v", err))
		return
	}
	derived, err := deriveWalletFromMnemonic(mnemonic, acctRow.DerivationPath)
	if err != nil {
		writeError(w, 400, fmt.Sprintf("derive failed: %v", err))
		return
	}

	// Update bot config YAML: wallet.private_key + wallet.funder_address
	newYAML, err := upsertYAMLWalletKeys(bot.ConfigYAML, derived.PrivateKeyHex, acctRow.FunderAddress)
	if err != nil {
		writeError(w, 400, fmt.Sprintf("update config failed: %v", err))
		return
	}
	// Keep isolation constraints
	newYAML, err = upsertYAMLKey(newYAML, "log_file", bot.LogPath)
	if err != nil {
		writeError(w, 400, fmt.Sprintf("set log_file failed: %v", err))
		return
	}
	newYAML, err = upsertYAMLKey(newYAML, "persistence_dir", bot.PersistenceDir)
	if err != nil {
		writeError(w, 400, fmt.Sprintf("set persistence_dir failed: %v", err))
		return
	}

	if err := validateBotConfigYAMLStrict(newYAML); err != nil {
		writeError(w, 400, fmt.Sprintf("config invalid: %v", err))
		return
	}

	// persist config file + new config version + bind account_id (transaction-ish)
	if err := os.MkdirAll(filepath.Dir(bot.ConfigPath), 0o755); err != nil {
		writeError(w, 500, fmt.Sprintf("mkdir config dir: %v", err))
		return
	}
	if err := os.WriteFile(bot.ConfigPath, []byte(newYAML+"\n"), 0o644); err != nil {
		writeError(w, 500, fmt.Sprintf("write config: %v", err))
		return
	}

	nextVer, err := s.nextBotConfigVersion(ctx, botID)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db next version: %v", err))
		return
	}
	comment := fmt.Sprintf("bind account %s (eoa=%s)", req.AccountID, acctRow.EOAAddress)

	// do small transaction to keep things consistent
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db begin: %v", err))
		return
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO bot_config_versions (bot_id, version, config_yaml, created_at, comment)
VALUES (?,?,?,?,?)
`, botID, nextVer, newYAML, time.Now().Format(time.RFC3339Nano), comment); err != nil {
		writeError(w, 500, fmt.Sprintf("db insert version: %v", err))
		return
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE bots
SET account_id=?, config_yaml=?, current_version=?, updated_at=?
WHERE id=?
`, req.AccountID, newYAML, nextVer, time.Now().Format(time.RFC3339Nano), botID); err != nil {
		// 可能触发 UNIQUE index（一个账号绑定多个 bot）
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, 409, "account already bound to another bot")
			return
		}
		writeError(w, 500, fmt.Sprintf("db update bot: %v", err))
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, 500, fmt.Sprintf("db commit: %v", err))
		return
	}

	writeJSON(w, 200, map[string]any{
		"ok":              true,
		"bot_id":          botID,
		"account_id":      req.AccountID,
		"current_version": nextVer,
		"note":            "config updated; restart bot to take effect",
	})
}

func upsertYAMLWalletKeys(yamlText string, privateKeyHex string, funderAddress string) (string, error) {
	var m map[string]any
	if err := yaml.Unmarshal([]byte(yamlText), &m); err != nil {
		return "", err
	}
	w, ok := m["wallet"].(map[string]any)
	if !ok || w == nil {
		w = map[string]any{}
	}
	w["private_key"] = strings.TrimSpace(privateKeyHex)
	w["funder_address"] = strings.TrimSpace(funderAddress)
	m["wallet"] = w
	out, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
