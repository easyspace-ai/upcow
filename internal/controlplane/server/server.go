package server

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/go-chi/chi/v5"
	_ "modernc.org/sqlite"
)

type Config struct {
	DBPath  string
	BotBin  string
	DataDir string
	LogsDir string
}

type Server struct {
	cfg Config
	db  *sql.DB

	bgCancel func()
	bgWG     sync.WaitGroup
}

func New(cfg Config) (*Server, error) {
	if cfg.DBPath == "" {
		return nil, errors.New("db path is required")
	}
	if cfg.BotBin == "" {
		return nil, errors.New("bot-bin is required")
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}
	if cfg.LogsDir == "" {
		cfg.LogsDir = "logs"
	}

	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir db dir: %w", err)
	}

	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite：单连接更稳定
	db.SetMaxIdleConns(1)

	s := &Server{cfg: cfg, db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	s.startBackground()
	return s, nil
}

func (s *Server) Close() error {
	if s.bgCancel != nil {
		s.bgCancel()
		s.bgWG.Wait()
	}
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	r.Route("/api", func(r chi.Router) {
		r.Route("/accounts", func(r chi.Router) {
			r.Get("/", s.handleAccountsList)
			r.Post("/", s.handleAccountsCreate)
			r.Route("/{accountID}", func(r chi.Router) {
				r.Get("/", s.handleAccountGet)
				r.Put("/", s.handleAccountUpdate)
				r.Post("/reveal-mnemonic", s.handleAccountRevealMnemonic)
				r.Post("/sync_balance", s.handleAccountSyncBalance)
				r.Post("/redeem", s.handleAccountRedeem)
				r.Post("/sync_trades", s.handleAccountSyncTrades)
				r.Post("/sync_positions", s.handleAccountSyncPositions)
				r.Post("/sync_open_orders", s.handleAccountSyncOpenOrders)
				r.Get("/trades", s.handleAccountTrades)
				r.Get("/positions", s.handleAccountPositions)
				r.Get("/open_orders", s.handleAccountOpenOrders)
				r.Get("/stats", s.handleAccountStats)
			})
		})

		r.Route("/jobs", func(r chi.Router) {
			r.Get("/runs", s.handleJobRunsList)
			r.Post("/balance_sync", s.handleJobBalanceSyncNow)
			r.Post("/redeem", s.handleJobRedeemNow)
			r.Post("/trades_sync", s.handleJobTradesSyncNow)
			r.Post("/positions_sync", s.handleJobPositionsSyncNow)
			r.Post("/open_orders_sync", s.handleJobOpenOrdersSyncNow)
		})

		r.Route("/bots", func(r chi.Router) {
			r.Get("/", s.handleBotsList)
			r.Post("/", s.handleBotsCreate)
			r.Route("/{botID}", func(r chi.Router) {
				r.Get("/", s.handleBotGet)
				r.Put("/config", s.handleBotConfigUpdate) // 保存配置，不重启
				r.Get("/config/versions", s.handleBotConfigVersions)
				r.Post("/config/rollback", s.handleBotConfigRollback)
				r.Post("/bind_account", s.handleBotBindAccount)
				r.Post("/start", s.handleBotStart)
				r.Post("/stop", s.handleBotStop)
				r.Post("/restart", s.handleBotRestart)
				r.Get("/status", s.handleBotStatus)
				r.Get("/logs", s.handleBotLogsTail)
				r.Get("/logs/stream", s.handleBotLogsStream)
			})
		})
	})

	// UI：极简单页（阶段1）
	r.Get("/", s.handleUI)

	return r
}
