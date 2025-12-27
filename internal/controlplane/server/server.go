package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/gin-gonic/gin"
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
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", s.wrap(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	api := r.Group("/api")

	accounts := api.Group("/accounts")
	accounts.GET("/", s.wrap(s.handleAccountsList))
	accounts.POST("/", s.wrap(s.handleAccountsCreate))
	accountsID := accounts.Group("/:accountID")
	accountsID.GET("/", s.wrap(s.handleAccountGet))
	accountsID.PUT("/", s.wrap(s.handleAccountUpdate))
	accountsID.POST("/reveal-mnemonic", s.wrap(s.handleAccountRevealMnemonic))
	accountsID.POST("/sync_balance", s.wrap(s.handleAccountSyncBalance))
	accountsID.POST("/redeem", s.wrap(s.handleAccountRedeem))
	accountsID.POST("/sync_trades", s.wrap(s.handleAccountSyncTrades))
	accountsID.POST("/sync_positions", s.wrap(s.handleAccountSyncPositions))
	accountsID.POST("/sync_open_orders", s.wrap(s.handleAccountSyncOpenOrders))
	accountsID.GET("/balances", s.wrap(s.handleAccountBalances))
	accountsID.GET("/trades", s.wrap(s.handleAccountTrades))
	accountsID.GET("/positions", s.wrap(s.handleAccountPositions))
	accountsID.GET("/open_orders", s.wrap(s.handleAccountOpenOrders))
	accountsID.GET("/stats", s.wrap(s.handleAccountStats))
	accountsID.GET("/equity", s.wrap(s.handleAccountEquity))
	accountsID.POST("/equity_snapshot", s.wrap(s.handleAccountEquitySnapshotNow))

	jobs := api.Group("/jobs")
	jobs.GET("/runs", s.wrap(s.handleJobRunsList))
	jobs.POST("/balance_sync", s.wrap(s.handleJobBalanceSyncNow))
	jobs.POST("/redeem", s.wrap(s.handleJobRedeemNow))
	jobs.POST("/trades_sync", s.wrap(s.handleJobTradesSyncNow))
	jobs.POST("/positions_sync", s.wrap(s.handleJobPositionsSyncNow))
	jobs.POST("/open_orders_sync", s.wrap(s.handleJobOpenOrdersSyncNow))
	jobs.POST("/equity_snapshot", s.wrap(s.handleJobEquitySnapshotNow))

	bots := api.Group("/bots")
	bots.GET("/", s.wrap(s.handleBotsList))
	bots.POST("/", s.wrap(s.handleBotsCreate))
	botID := bots.Group("/:botID")
	botID.GET("/", s.wrap(s.handleBotGet))
	botID.PUT("/config", s.wrap(s.handleBotConfigUpdate))
	botID.GET("/config/versions", s.wrap(s.handleBotConfigVersions))
	botID.POST("/config/rollback", s.wrap(s.handleBotConfigRollback))
	botID.POST("/bind_account", s.wrap(s.handleBotBindAccount))
	botID.POST("/start", s.wrap(s.handleBotStart))
	botID.POST("/stop", s.wrap(s.handleBotStop))
	botID.POST("/restart", s.wrap(s.handleBotRestart))
	botID.GET("/status", s.wrap(s.handleBotStatus))
	botID.GET("/logs", s.wrap(s.handleBotLogsTail))
	botID.GET("/logs/stream", s.wrap(s.handleBotLogsStream))

	// UI
	r.GET("/", s.wrap(s.handleUI))

	return r
}

type paramsKeyType string

const paramsKey paramsKeyType = "gobet_path_params"

// wrap adapts existing net/http handlers to gin, injecting path params into request context.
func (s *Server) wrap(h func(http.ResponseWriter, *http.Request)) gin.HandlerFunc {
	return func(c *gin.Context) {
		m := map[string]string{}
		for _, p := range c.Params {
			m[p.Key] = p.Value
		}
		ctx := context.WithValue(c.Request.Context(), paramsKey, m)
		c.Request = c.Request.WithContext(ctx)
		h(c.Writer, c.Request)
	}
}
