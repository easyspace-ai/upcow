package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *Server) insertBot(ctx context.Context, b Bot) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO bots (id,name,config_path,config_yaml,log_path,persistence_dir,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?)
`, b.ID, b.Name, b.ConfigPath, b.ConfigYAML, b.LogPath, b.PersistenceDir, b.CreatedAt.Format(time.RFC3339Nano), b.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert bot: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO bot_process (bot_id) VALUES (?)`, b.ID)
	if err != nil {
		return fmt.Errorf("init bot_process: %w", err)
	}
	return nil
}

func (s *Server) updateBotConfig(ctx context.Context, botID string, newYAML string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE bots
SET config_yaml=?, updated_at=?
WHERE id=?
`, newYAML, time.Now().Format(time.RFC3339Nano), botID)
	if err != nil {
		return fmt.Errorf("update bot config: %w", err)
	}
	return nil
}

func (s *Server) getBot(ctx context.Context, botID string) (*Bot, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id,name,config_path,config_yaml,log_path,persistence_dir,created_at,updated_at
FROM bots WHERE id=?
`, botID)
	var b Bot
	var createdAt, updatedAt string
	if err := row.Scan(&b.ID, &b.Name, &b.ConfigPath, &b.ConfigYAML, &b.LogPath, &b.PersistenceDir, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	b.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	b.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &b, nil
}

func (s *Server) listBots(ctx context.Context) ([]Bot, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id,name,config_path,config_yaml,log_path,persistence_dir,created_at,updated_at
FROM bots ORDER BY created_at DESC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Bot
	for rows.Next() {
		var b Bot
		var createdAt, updatedAt string
		if err := rows.Scan(&b.ID, &b.Name, &b.ConfigPath, &b.ConfigYAML, &b.LogPath, &b.PersistenceDir, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		b.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		b.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Server) setBotPID(ctx context.Context, botID string, pid int) error {
	now := time.Now().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
UPDATE bot_process
SET pid=?, started_at=?, last_error=NULL
WHERE bot_id=?
`, pid, now, botID)
	return err
}

func (s *Server) clearBotPID(ctx context.Context, botID string, exitCode *int, lastErr *string) error {
	now := time.Now().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
UPDATE bot_process
SET pid=NULL, last_exit_at=?, last_exit_code=?, last_error=?
WHERE bot_id=?
`, now, exitCode, lastErr, botID)
	return err
}

func (s *Server) getBotProcess(ctx context.Context, botID string) (*BotProcess, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT bot_id,pid,started_at,last_exit_at,last_exit_code,last_error
FROM bot_process WHERE bot_id=?
`, botID)
	var p BotProcess
	var pid sql.NullInt64
	var startedAt, lastExitAt sql.NullString
	var lastExitCode sql.NullInt64
	var lastErr sql.NullString
	if err := row.Scan(&p.BotID, &pid, &startedAt, &lastExitAt, &lastExitCode, &lastErr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if pid.Valid {
		v := int(pid.Int64)
		p.PID = &v
	}
	if startedAt.Valid {
		if t, err := time.Parse(time.RFC3339Nano, startedAt.String); err == nil {
			p.StartedAt = &t
		}
	}
	if lastExitAt.Valid {
		if t, err := time.Parse(time.RFC3339Nano, lastExitAt.String); err == nil {
			p.LastExitAt = &t
		}
	}
	if lastExitCode.Valid {
		v := int(lastExitCode.Int64)
		p.LastExitCode = &v
	}
	if lastErr.Valid {
		v := lastErr.String
		p.LastError = &v
	}
	return &p, nil
}
