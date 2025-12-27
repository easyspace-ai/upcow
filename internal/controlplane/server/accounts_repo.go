package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type accountRow struct {
	Account
	MnemonicEnc string
}

func (s *Server) insertAccount(ctx context.Context, a accountRow) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO accounts (id,name,mnemonic_enc,derivation_path,eoa_address,funder_address,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?)
`, a.ID, a.Name, a.MnemonicEnc, a.DerivationPath, a.EOAAddress, a.FunderAddress, a.CreatedAt.Format(time.RFC3339Nano), a.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

func (s *Server) getAccount(ctx context.Context, accountID string) (*Account, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id,name,derivation_path,eoa_address,funder_address,created_at,updated_at
FROM accounts WHERE id=?
`, accountID)
	var a Account
	var created, updated string
	if err := row.Scan(&a.ID, &a.Name, &a.DerivationPath, &a.EOAAddress, &a.FunderAddress, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &a, nil
}

func (s *Server) getAccountRow(ctx context.Context, accountID string) (*accountRow, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id,name,mnemonic_enc,derivation_path,eoa_address,funder_address,created_at,updated_at
FROM accounts WHERE id=?
`, accountID)
	var a accountRow
	var created, updated string
	if err := row.Scan(&a.ID, &a.Name, &a.MnemonicEnc, &a.DerivationPath, &a.EOAAddress, &a.FunderAddress, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &a, nil
}

func (s *Server) listAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id,name,derivation_path,eoa_address,funder_address,created_at,updated_at
FROM accounts ORDER BY created_at DESC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Account
	for rows.Next() {
		var a Account
		var created, updated string
		if err := rows.Scan(&a.ID, &a.Name, &a.DerivationPath, &a.EOAAddress, &a.FunderAddress, &created, &updated); err != nil {
			return nil, err
		}
		a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Server) updateAccount(ctx context.Context, accountID string, name string, mnemonicEnc *string, derivationPath string, eoaAddress string, funderAddress string) error {
	if mnemonicEnc != nil {
		_, err := s.db.ExecContext(ctx, `
UPDATE accounts
SET name=?, mnemonic_enc=?, derivation_path=?, eoa_address=?, funder_address=?, updated_at=?
WHERE id=?
`, name, *mnemonicEnc, derivationPath, eoaAddress, funderAddress, time.Now().Format(time.RFC3339Nano), accountID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE accounts
SET name=?, derivation_path=?, eoa_address=?, funder_address=?, updated_at=?
WHERE id=?
`, name, derivationPath, eoaAddress, funderAddress, time.Now().Format(time.RFC3339Nano), accountID)
	return err
}

func (s *Server) isAccountBound(ctx context.Context, accountID string) (bool, string, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id FROM bots WHERE account_id=? LIMIT 1`, accountID)
	var botID string
	err := row.Scan(&botID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, "", nil
		}
		return false, "", err
	}
	return true, botID, nil
}

func (s *Server) botBoundAccount(ctx context.Context, botID string) (*string, error) {
	row := s.db.QueryRowContext(ctx, `SELECT account_id FROM bots WHERE id=?`, botID)
	var accountID sql.NullString
	if err := row.Scan(&accountID); err != nil {
		return nil, err
	}
	if accountID.Valid && accountID.String != "" {
		v := accountID.String
		return &v, nil
	}
	return nil, nil
}

func (s *Server) ensureAccountNotBoundToOtherBot(ctx context.Context, accountID string, targetBotID string) error {
	bound, botID, err := s.isAccountBound(ctx, accountID)
	if err != nil {
		return err
	}
	if bound && botID != targetBotID {
		return fmt.Errorf("account already bound to bot %s", botID)
	}
	return nil
}
