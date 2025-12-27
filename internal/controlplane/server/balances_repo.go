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
