package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/betbot/gobet/internal/controlplane/server"
)

func main() {
	var (
		listenAddr = flag.String("listen", ":8080", "HTTP listen address")
		dbPath     = flag.String("db", "data/controlplane.db", "SQLite db file path")
		botBin     = flag.String("bot-bin", "bot", "bot executable (path or name in PATH)")
		dataDir    = flag.String("data-dir", "data", "base data directory")
		logsDir    = flag.String("logs-dir", "logs", "base logs directory")
	)
	flag.Parse()

	srv, err := server.New(server.Config{
		DBPath:  *dbPath,
		BotBin:  *botBin,
		DataDir: *dataDir,
		LogsDir: *logsDir,
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
