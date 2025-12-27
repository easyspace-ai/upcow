package server

import (
	"context"
	"database/sql"
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
CREATE TABLE IF NOT EXISTS accounts (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  mnemonic_enc TEXT NOT NULL,
  derivation_path TEXT NOT NULL,
  eoa_address TEXT NOT NULL,
  funder_address TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);`,
		`
CREATE TABLE IF NOT EXISTS bots (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  account_id TEXT,
  config_path TEXT NOT NULL,
  config_yaml TEXT NOT NULL,
  log_path TEXT NOT NULL,
  persistence_dir TEXT NOT NULL,
  current_version INTEGER NOT NULL DEFAULT 0,
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
		`
CREATE TABLE IF NOT EXISTS bot_config_versions (
  bot_id TEXT NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  version INTEGER NOT NULL,
  config_yaml TEXT NOT NULL,
  created_at TEXT NOT NULL,
  comment TEXT,
  PRIMARY KEY (bot_id, version)
);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_bots_account_id_unique ON bots(account_id) WHERE account_id IS NOT NULL;`,
		`
CREATE TABLE IF NOT EXISTS account_balances (
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  balance_usdc REAL NOT NULL,
  source TEXT NOT NULL,
  ts TEXT NOT NULL
);`,
		`CREATE INDEX IF NOT EXISTS idx_account_balances_account_ts ON account_balances(account_id, ts DESC);`,
		`
CREATE TABLE IF NOT EXISTS job_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job_name TEXT NOT NULL,
  scope TEXT NOT NULL, -- "batch" | "account"
  account_id TEXT,     -- nullable when batch
  started_at TEXT NOT NULL,
  finished_at TEXT,
  ok INTEGER,
  error TEXT,
  meta_json TEXT
);`,
		`CREATE INDEX IF NOT EXISTS idx_job_runs_started_at ON job_runs(started_at DESC);`,
	}

	for _, q := range stmts {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("migrate exec failed: %w", err)
		}
	}

	// 兼容：旧库没有 current_version 列时，补齐（SQLite 不支持 ADD COLUMN IF NOT EXISTS）
	hasCol, err := hasColumn(ctx, s.db, "bots", "current_version")
	if err != nil {
		return err
	}
	if !hasCol {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE bots ADD COLUMN current_version INTEGER NOT NULL DEFAULT 0;`); err != nil {
			return fmt.Errorf("alter bots add current_version: %w", err)
		}
	}

	// 兼容：旧库没有 account_id 列时补齐
	hasAccountIDCol, err := hasColumn(ctx, s.db, "bots", "account_id")
	if err != nil {
		return err
	}
	if !hasAccountIDCol {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE bots ADD COLUMN account_id TEXT;`); err != nil {
			return fmt.Errorf("alter bots add account_id: %w", err)
		}
		// unique index already in stmts
	}

	return nil
}

func hasColumn(ctx context.Context, db *sql.DB, table string, col string) (bool, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s);`, table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	// PRAGMA table_info 返回：cid,name,type,notnull,dflt_value,pk
	for rows.Next() {
		var (
			cid       int
			name      string
			typ       string
			notnull   int
			dfltValue any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}
