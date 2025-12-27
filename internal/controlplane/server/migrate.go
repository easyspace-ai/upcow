package server

import (
	"context"
	"fmt"
	"time"
)

func (s *Server) migrate() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stmts := []string{
		`PRAGMA journal_mode=WAL;`,
		`PRAGMA foreign_keys=ON;`,
		`
CREATE TABLE IF NOT EXISTS bots (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  config_path TEXT NOT NULL,
  config_yaml TEXT NOT NULL,
  log_path TEXT NOT NULL,
  persistence_dir TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);`,
		`
CREATE TABLE IF NOT EXISTS bot_process (
  bot_id TEXT PRIMARY KEY REFERENCES bots(id) ON DELETE CASCADE,
  pid INTEGER,
  started_at TEXT,
  last_exit_at TEXT,
  last_exit_code INTEGER,
  last_error TEXT
);`,
	}

	for _, q := range stmts {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("migrate exec failed: %w", err)
		}
	}
	return nil
}
