package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/betbot/gobet/internal/controlplane/server"
	"github.com/betbot/gobet/pkg/secretstore"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env (best-effort). If missing, fall back to real env vars.
	_ = godotenv.Load()

	// Optional: load config/secrets from encrypted badger (no plaintext .env required).
	secretDBPath := strings.TrimSpace(os.Getenv("GOBET_SECRET_DB"))
	if secretDBPath == "" {
		secretDBPath = "data/secrets.badger"
	}
	secretKey, _ := secretstore.ParseKey(os.Getenv("GOBET_SECRET_KEY"))
	var ss *secretstore.Store
	if secretKey != nil {
		if store, err := secretstore.Open(secretstore.OpenOptions{
			Path:          secretDBPath,
			EncryptionKey: secretKey,
			ReadOnly:      true,
		}); err == nil {
			ss = store
			defer ss.Close()
		}
	}

	getenv := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		// fallback to badger imported env (env/<KEY>)
		if ss != nil {
			if v, ok, _ := ss.GetString("env/" + key); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return def
	}

	var (
		listenAddr = flag.String("listen", getenv("GOBET_SERVER_LISTEN", ":8080"), "HTTP listen address")
		dbPath     = flag.String("db", getenv("GOBET_SERVER_DB", "data/controlplane.db"), "SQLite db file path")
		botBin     = flag.String("bot-bin", getenv("GOBET_BOT_BIN", "bot"), "bot executable (path or name in PATH)")
		dataDir    = flag.String("data-dir", getenv("GOBET_DATA_DIR", "data"), "base data directory")
		logsDir    = flag.String("logs-dir", getenv("GOBET_LOGS_DIR", "logs"), "base logs directory")
	)
	flag.Parse()

	srv, err := server.New(server.Config{
		DBPath:      *dbPath,
		BotBin:      *botBin,
		DataDir:     *dataDir,
		LogsDir:     *logsDir,
		Secrets:     ss,
		SecretsPath: secretDBPath,
		SecretsKey:  secretKey,
	})
	if err != nil {
		log.Fatalf("init server failed: %v", err)
	}
	defer srv.Close()

	httpSrv := &http.Server{
		Addr:              *listenAddr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("controlplane listening on %s", *listenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http server error: %v", err)
		}
	}()

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	<-stopCh

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)

	fmt.Println("server stopped")
}
