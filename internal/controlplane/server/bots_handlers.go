package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	pkgconfig "github.com/betbot/gobet/pkg/config"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

type createBotRequest struct {
	Name       string `json:"name"`
	ConfigYAML string `json:"config_yaml"`
}

func (s *Server) handleBotsCreate(w http.ResponseWriter, r *http.Request) {
	var req createBotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.ConfigYAML = strings.TrimSpace(req.ConfigYAML)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.ConfigYAML == "" {
		writeError(w, http.StatusBadRequest, "config_yaml is required")
		return
	}

	if err := validateBotConfigYAMLStrict(req.ConfigYAML); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("config invalid: %v", err))
		return
	}

	id := uuid.NewString()
	configPath := filepath.Join(s.cfg.DataDir, "bots", id, "config.yaml")
	logPath := filepath.Join(s.cfg.LogsDir, "bots", id, "bot.log")
	persistenceDir := filepath.Join(s.cfg.DataDir, "persistence", id)

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("mkdir config dir: %v", err))
		return
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("mkdir logs dir: %v", err))
		return
	}
	if err := os.MkdirAll(persistenceDir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("mkdir persistence dir: %v", err))
		return
	}

	// 强制注入：log_file + persistence_dir（保证目录隔离），覆盖用户配置。
	configYAML, err := upsertYAMLKey(req.ConfigYAML, "log_file", logPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("set log_file failed: %v", err))
		return
	}
	configYAML, err = upsertYAMLKey(configYAML, "persistence_dir", persistenceDir)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("set persistence_dir failed: %v", err))
		return
	}

	if err := validateBotConfigYAMLStrict(configYAML); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("config invalid: %v", err))
		return
	}

	if err := os.WriteFile(configPath, []byte(configYAML+"\n"), 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("write config: %v", err))
		return
	}

	now := time.Now()
	b := Bot{
		ID:             id,
		Name:           req.Name,
		ConfigPath:     configPath,
		ConfigYAML:     configYAML,
		LogPath:        logPath,
		PersistenceDir: persistenceDir,
		CurrentVersion: 1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.insertBot(ctx, b); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("db insert: %v", err))
		return
	}
	// version 1
	if err := s.insertBotConfigVersion(ctx, b.ID, 1, b.ConfigYAML, ptrString("init")); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("db insert version: %v", err))
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) handleBotsList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	bots, err := s.listBots(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("db list: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, bots)
}

func (s *Server) handleBotGet(w http.ResponseWriter, r *http.Request) {
	botID := chiURLParam(r, "botID")
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	b, err := s.getBot(ctx, botID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("db get: %v", err))
		return
	}
	if b == nil {
		writeError(w, http.StatusNotFound, "bot not found")
		return
	}
	p, _ := s.getBotProcess(ctx, botID)
	writeJSON(w, http.StatusOK, map[string]any{"bot": b, "process": p})
}

type updateConfigRequest struct {
	ConfigYAML string `json:"config_yaml"`
}

func (s *Server) handleBotConfigUpdate(w http.ResponseWriter, r *http.Request) {
	botID := chiURLParam(r, "botID")
	var req updateConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	req.ConfigYAML = strings.TrimSpace(req.ConfigYAML)
	if req.ConfigYAML == "" {
		writeError(w, http.StatusBadRequest, "config_yaml is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	b, err := s.getBot(ctx, botID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("db get: %v", err))
		return
	}
	if b == nil {
		writeError(w, http.StatusNotFound, "bot not found")
		return
	}

	// 强制注入：log_file + persistence_dir（避免用户误改导致互相踩数据/日志混乱）
	configYAML, err := upsertYAMLKey(req.ConfigYAML, "log_file", b.LogPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("set log_file failed: %v", err))
		return
	}
	configYAML, err = upsertYAMLKey(configYAML, "persistence_dir", b.PersistenceDir)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("set persistence_dir failed: %v", err))
		return
	}

	if err := validateBotConfigYAMLStrict(configYAML); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("config invalid: %v", err))
		return
	}

	if err := os.MkdirAll(filepath.Dir(b.ConfigPath), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("mkdir config dir: %v", err))
		return
	}
	if err := os.WriteFile(b.ConfigPath, []byte(configYAML+"\n"), 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("write config: %v", err))
		return
	}

	nextVer, err := s.nextBotConfigVersion(ctx, botID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("db next version: %v", err))
		return
	}
	if err := s.insertBotConfigVersion(ctx, botID, nextVer, configYAML, nil); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("db insert version: %v", err))
		return
	}
	if err := s.updateBotConfig(ctx, botID, configYAML, nextVer); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("db update: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "current_version": nextVer})
}

// validateBotConfigYAMLStrict：更接近 bot 的真实校验逻辑，但不触网、不设置全局环境变量。
func validateBotConfigYAMLStrict(yamlText string) error {
	var cf pkgconfig.ConfigFile
	if err := yaml.Unmarshal([]byte(yamlText), &cf); err != nil {
		return fmt.Errorf("yaml parse failed: %w", err)
	}

	kind := strings.TrimSpace(cf.Market.Kind)
	if kind == "" {
		kind = "updown"
	}

	cfg := &pkgconfig.Config{
		Wallet: pkgconfig.WalletConfig{
			PrivateKey:    strings.TrimSpace(cf.Wallet.PrivateKey),
			FunderAddress: strings.TrimSpace(cf.Wallet.FunderAddress),
		},
		Proxy:              nil, // server 校验不在这里处理 proxy
		ExchangeStrategies: cf.ExchangeStrategies,
		Market: pkgconfig.MarketConfig{
			Symbol:        strings.TrimSpace(cf.Market.Symbol),
			Timeframe:     strings.TrimSpace(cf.Market.Timeframe),
			Kind:          kind,
			SlugPrefix:    strings.TrimSpace(cf.Market.SlugPrefix),
			SlugTemplates: cf.Market.SlugTemplates,
			Precision:     cf.Market.Precision,
		},
		LogLevel:       strings.TrimSpace(cf.LogLevel),
		LogFile:        strings.TrimSpace(cf.LogFile),
		LogByCycle:     cf.LogByCycle,
		PersistenceDir: strings.TrimSpace(cf.PersistenceDir),
		MinOrderSize:   cf.MinOrderSize,
		MinShareSize:   cf.MinShareSize,
		DryRun:         cf.DryRun,
	}
	return cfg.Validate()
}

func upsertYAMLKey(yamlText string, key string, value any) (string, error) {
	var m map[string]any
	if err := yaml.Unmarshal([]byte(yamlText), &m); err != nil {
		return "", err
	}
	m[key] = value
	out, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
