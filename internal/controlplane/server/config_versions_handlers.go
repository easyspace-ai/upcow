package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleBotConfigVersions(w http.ResponseWriter, r *http.Request) {
	botID := chi.URLParam(r, "botID")
	limit := 50
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	b, err := s.getBot(ctx, botID)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db get: %v", err))
		return
	}
	if b == nil {
		writeError(w, 404, "bot not found")
		return
	}

	versions, err := s.listBotConfigVersions(ctx, botID, limit)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db list versions: %v", err))
		return
	}
	writeJSON(w, 200, map[string]any{
		"bot_id":          botID,
		"current_version": b.CurrentVersion,
		"versions":        versions,
	})
}

type rollbackRequest struct {
	Version int `json:"version"`
}

func (s *Server) handleBotConfigRollback(w http.ResponseWriter, r *http.Request) {
	botID := chi.URLParam(r, "botID")
	var req rollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid json body")
		return
	}
	if req.Version <= 0 {
		writeError(w, 400, "version must be > 0")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	b, err := s.getBot(ctx, botID)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db get: %v", err))
		return
	}
	if b == nil {
		writeError(w, 404, "bot not found")
		return
	}

	old, err := s.getBotConfigVersion(ctx, botID, req.Version)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db get version: %v", err))
		return
	}
	if old == nil {
		writeError(w, 404, "version not found")
		return
	}

	// 回滚策略：写入一个“新版本”（保持版本单调递增，便于审计）
	nextVer, err := s.nextBotConfigVersion(ctx, botID)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db next version: %v", err))
		return
	}
	comment := fmt.Sprintf("rollback to v%d", req.Version)
	if err := s.insertBotConfigVersion(ctx, botID, nextVer, old.ConfigYAML, &comment); err != nil {
		writeError(w, 500, fmt.Sprintf("db insert version: %v", err))
		return
	}

	if err := validateBotConfigYAMLStrict(old.ConfigYAML); err != nil {
		writeError(w, 400, fmt.Sprintf("config invalid: %v", err))
		return
	}

	if err := os.MkdirAll(filepath.Dir(b.ConfigPath), 0o755); err != nil {
		writeError(w, 500, fmt.Sprintf("mkdir config dir: %v", err))
		return
	}
	if err := os.WriteFile(b.ConfigPath, []byte(old.ConfigYAML+"\n"), 0o644); err != nil {
		writeError(w, 500, fmt.Sprintf("write config: %v", err))
		return
	}

	if err := s.updateBotConfig(ctx, botID, old.ConfigYAML, nextVer); err != nil {
		writeError(w, 500, fmt.Sprintf("db update bot: %v", err))
		return
	}

	writeJSON(w, 200, map[string]any{"ok": true, "current_version": nextVer})
}
