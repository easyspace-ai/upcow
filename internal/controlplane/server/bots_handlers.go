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

	"github.com/go-chi/chi/v5"
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

	if err := validateBotConfigYAML(req.ConfigYAML); err != nil {
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
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.insertBot(ctx, b); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("db insert: %v", err))
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
	botID := chi.URLParam(r, "botID")
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
	botID := chi.URLParam(r, "botID")
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

	if err := validateBotConfigYAML(configYAML); err != nil {
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
	if err := s.updateBotConfig(ctx, botID, configYAML); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("db update: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// validateBotConfigYAML：只做“静态校验”，避免 server 引入交易逻辑/网络依赖。
// 注意：这里验证的是你们 pkg/config 期望的字段形状（不是全量业务校验）。
func validateBotConfigYAML(yamlText string) error {
	var cf map[string]any
	if err := yaml.Unmarshal([]byte(yamlText), &cf); err != nil {
		return fmt.Errorf("yaml parse failed: %w", err)
	}
	// wallet
	walletRaw, ok := cf["wallet"]
	if !ok {
		return fmt.Errorf("wallet is required")
	}
	wallet, ok := walletRaw.(map[string]any)
	if !ok {
		return fmt.Errorf("wallet must be an object")
	}
	privateKey, _ := wallet["private_key"].(string)
	funderAddress, _ := wallet["funder_address"].(string)
	if strings.TrimSpace(privateKey) == "" {
		return fmt.Errorf("wallet.private_key is required")
	}
	if strings.TrimSpace(funderAddress) == "" {
		return fmt.Errorf("wallet.funder_address is required")
	}

	// market
	marketRaw, ok := cf["market"]
	if !ok {
		return fmt.Errorf("market is required")
	}
	market, ok := marketRaw.(map[string]any)
	if !ok {
		return fmt.Errorf("market must be an object")
	}
	symbol, _ := market["symbol"].(string)
	timeframe, _ := market["timeframe"].(string)
	if strings.TrimSpace(symbol) == "" || strings.TrimSpace(timeframe) == "" {
		return fmt.Errorf("market.symbol and market.timeframe are required")
	}

	// exchangeStrategies
	es, ok := cf["exchangeStrategies"]
	if !ok {
		return fmt.Errorf("exchangeStrategies is required")
	}
	if _, ok := es.([]any); !ok {
		// yaml.v3 解出来是 []interface{}
		return fmt.Errorf("exchangeStrategies must be a list")
	}

	return nil
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
