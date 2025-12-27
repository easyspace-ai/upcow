package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *Server) getSyncState(ctx context.Context, accountID string, key string) (string, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT value FROM sync_state WHERE account_id=? AND key=?`, accountID, key)
	var v string
	if err := row.Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return v, true, nil
}

func (s *Server) setSyncState(ctx context.Context, accountID string, key string, value string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sync_state (account_id, key, value, updated_at)
VALUES (?,?,?,?)
ON CONFLICT(account_id,key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at
`, accountID, key, value, time.Now().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("set sync state: %w", err)
	}
	return nil
}
