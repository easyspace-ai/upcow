package server

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type EquitySnapshot struct {
	AccountID          string    `json:"account_id"`
	CashUSDC           float64   `json:"cash_usdc"`
	PositionsValueUSDC float64   `json:"positions_value_usdc"`
	TotalEquityUSDC    float64   `json:"total_equity_usdc"`
	TS                 time.Time `json:"ts"`
}

func (s *Server) latestCashBalance(ctx context.Context, accountID string) (float64, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT balance_usdc
FROM account_balances
WHERE account_id=?
ORDER BY ts DESC
LIMIT 1
`, accountID)
	var v sql.NullFloat64
	if err := row.Scan(&v); err != nil {
		return 0, false, err
	}
	if !v.Valid {
		return 0, false, nil
	}
	return v.Float64, true, nil
}

func (s *Server) positionsValue(ctx context.Context, accountID string) (float64, error) {
	// value ≈ sum(size * cur_price)
	row := s.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(size * cur_price), 0)
FROM positions_current
WHERE account_id=?
`, accountID)
	var v float64
	if err := row.Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

func (s *Server) insertEquitySnapshot(ctx context.Context, snap EquitySnapshot) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO account_equity_snapshots (account_id, cash_usdc, positions_value_usdc, total_equity_usdc, ts)
VALUES (?,?,?,?,?)
`, snap.AccountID, snap.CashUSDC, snap.PositionsValueUSDC, snap.TotalEquityUSDC, snap.TS.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert equity: %w", err)
	}
	return nil
}

func (s *Server) listEquitySnapshots(ctx context.Context, accountID string, limit int) ([]EquitySnapshot, error) {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT account_id, cash_usdc, positions_value_usdc, total_equity_usdc, ts
FROM account_equity_snapshots
WHERE account_id=?
ORDER BY ts DESC
LIMIT ?
`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EquitySnapshot
	for rows.Next() {
		var (
			snap EquitySnapshot
			ts   string
		)
		if err := rows.Scan(&snap.AccountID, &snap.CashUSDC, &snap.PositionsValueUSDC, &snap.TotalEquityUSDC, &ts); err != nil {
			return nil, err
		}
		snap.TS, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, snap)
	}
	return out, rows.Err()
}
