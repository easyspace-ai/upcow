package server

import (
	"context"
	"fmt"
	"time"
)

func (s *Server) insertBalanceSnapshot(ctx context.Context, accountID string, balanceUSDC float64, source string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO account_balances (account_id, balance_usdc, source, ts)
VALUES (?,?,?,?)
`, accountID, balanceUSDC, source, time.Now().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert balance: %w", err)
	}
	return nil
}

type BalanceSnapshot struct {
	AccountID   string    `json:"account_id"`
	BalanceUSDC float64   `json:"balance_usdc"`
	Source      string    `json:"source"`
	Timestamp   time.Time `json:"ts"`
}

func (s *Server) listBalanceSnapshots(ctx context.Context, accountID string, limit int) ([]BalanceSnapshot, error) {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT account_id, balance_usdc, source, ts
FROM account_balances
WHERE account_id=?
ORDER BY ts DESC
LIMIT ?
`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BalanceSnapshot
	for rows.Next() {
		var (
			b  BalanceSnapshot
			ts string
		)
		if err := rows.Scan(&b.AccountID, &b.BalanceUSDC, &b.Source, &ts); err != nil {
			return nil, err
		}
		b.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, b)
	}
	return out, rows.Err()
}
