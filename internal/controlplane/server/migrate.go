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
  desired_running INTEGER NOT NULL DEFAULT 0,
  restart_attempts INTEGER NOT NULL DEFAULT 0,
  last_restart_at TEXT,
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
		`
CREATE TABLE IF NOT EXISTS sync_state (
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (account_id, key)
);`,
		`
CREATE TABLE IF NOT EXISTS positions_current (
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  asset TEXT NOT NULL,
  condition_id TEXT NOT NULL,
  outcome TEXT NOT NULL,
  size REAL NOT NULL,
  avg_price REAL NOT NULL,
  cur_price REAL NOT NULL,
  realized_pnl REAL NOT NULL,
  title TEXT,
  slug TEXT,
  outcome_index INTEGER,
  event_slug TEXT,
  ts TEXT NOT NULL,
  PRIMARY KEY (account_id, asset)
);`,
		`CREATE INDEX IF NOT EXISTS idx_positions_current_account ON positions_current(account_id);`,
		`
CREATE TABLE IF NOT EXISTS open_orders_current (
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  order_id TEXT NOT NULL,
  status TEXT,
  owner TEXT,
  maker_address TEXT,
  market TEXT,
  asset_id TEXT,
  side TEXT,
  original_size REAL,
  size_matched REAL,
  price REAL,
  outcome TEXT,
  created_at_ts INTEGER,
  expiration TEXT,
  order_type TEXT,
  ts TEXT NOT NULL,
  PRIMARY KEY (account_id, order_id)
);`,
		`CREATE INDEX IF NOT EXISTS idx_open_orders_current_account ON open_orders_current(account_id);`,
		`
CREATE TABLE IF NOT EXISTS clob_trades (
  trade_id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  maker_address TEXT,
  owner TEXT,
  market TEXT,
  asset_id TEXT,
  side TEXT,
  size REAL,
  price REAL,
  outcome TEXT,
  status TEXT,
  match_time_ts INTEGER,
  transaction_hash TEXT,
  volume_usdc REAL,
  raw_json TEXT,
  created_at TEXT NOT NULL
);`,
		`CREATE INDEX IF NOT EXISTS idx_clob_trades_account_time ON clob_trades(account_id, match_time_ts DESC);`,
		`
CREATE TABLE IF NOT EXISTS account_equity_snapshots (
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  cash_usdc REAL NOT NULL,
  positions_value_usdc REAL NOT NULL,
  total_equity_usdc REAL NOT NULL,
  ts TEXT NOT NULL
);`,
		`CREATE INDEX IF NOT EXISTS idx_account_equity_account_ts ON account_equity_snapshots(account_id, ts DESC);`,
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

	// 兼容：旧库没有 bot_process desired_running/restart_attempts/last_restart_at 时补齐
	for _, col := range []struct {
		name string
		ddl  string
	}{
		{"desired_running", `ALTER TABLE bot_process ADD COLUMN desired_running INTEGER NOT NULL DEFAULT 0;`},
		{"restart_attempts", `ALTER TABLE bot_process ADD COLUMN restart_attempts INTEGER NOT NULL DEFAULT 0;`},
		{"last_restart_at", `ALTER TABLE bot_process ADD COLUMN last_restart_at TEXT;`},
	} {
		ok, err := hasColumn(ctx, s.db, "bot_process", col.name)
		if err != nil {
			return err
		}
		if !ok {
			if _, err := s.db.ExecContext(ctx, col.ddl); err != nil {
				return fmt.Errorf("alter bot_process add %s: %w", col.name, err)
			}
		}
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
