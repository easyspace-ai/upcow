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
INSERT INTO bots (id,name,config_path,config_yaml,log_path,persistence_dir,current_version,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?)
`, b.ID, b.Name, b.ConfigPath, b.ConfigYAML, b.LogPath, b.PersistenceDir, b.CurrentVersion, b.CreatedAt.Format(time.RFC3339Nano), b.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert bot: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO bot_process (bot_id) VALUES (?)`, b.ID)
	if err != nil {
		return fmt.Errorf("init bot_process: %w", err)
	}
	return nil
}

func (s *Server) updateBotConfig(ctx context.Context, botID string, newYAML string, currentVersion int) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE bots
SET config_yaml=?, current_version=?, updated_at=?
WHERE id=?
`, newYAML, currentVersion, time.Now().Format(time.RFC3339Nano), botID)
	if err != nil {
		return fmt.Errorf("update bot config: %w", err)
	}
	return nil
}

func (s *Server) getBot(ctx context.Context, botID string) (*Bot, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id,name,config_path,config_yaml,log_path,persistence_dir,current_version,created_at,updated_at
FROM bots WHERE id=?
`, botID)
	var b Bot
	var createdAt, updatedAt string
	if err := row.Scan(&b.ID, &b.Name, &b.ConfigPath, &b.ConfigYAML, &b.LogPath, &b.PersistenceDir, &b.CurrentVersion, &createdAt, &updatedAt); err != nil {
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
SELECT id,name,config_path,config_yaml,log_path,persistence_dir,current_version,created_at,updated_at
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
		if err := rows.Scan(&b.ID, &b.Name, &b.ConfigPath, &b.ConfigYAML, &b.LogPath, &b.PersistenceDir, &b.CurrentVersion, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		b.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		b.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Server) nextBotConfigVersion(ctx context.Context, botID string) (int, error) {
	row := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM bot_config_versions WHERE bot_id=?`, botID)
	var max int
	if err := row.Scan(&max); err != nil {
		return 0, err
	}
	return max + 1, nil
}

func (s *Server) insertBotConfigVersion(ctx context.Context, botID string, version int, configYAML string, comment *string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO bot_config_versions (bot_id, version, config_yaml, created_at, comment)
VALUES (?,?,?,?,?)
`, botID, version, configYAML, time.Now().Format(time.RFC3339Nano), comment)
	return err
}

func (s *Server) listBotConfigVersions(ctx context.Context, botID string, limit int) ([]BotConfigVersion, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT bot_id, version, config_yaml, created_at, comment
FROM bot_config_versions
WHERE bot_id=?
ORDER BY version DESC
LIMIT ?
`, botID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BotConfigVersion
	for rows.Next() {
		var (
			v       BotConfigVersion
			created string
			comment sql.NullString
		)
		if err := rows.Scan(&v.BotID, &v.Version, &v.ConfigYAML, &created, &comment); err != nil {
			return nil, err
		}
		v.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		if comment.Valid {
			c := comment.String
			v.Comment = &c
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Server) getBotConfigVersion(ctx context.Context, botID string, version int) (*BotConfigVersion, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT bot_id, version, config_yaml, created_at, comment
FROM bot_config_versions
WHERE bot_id=? AND version=?
`, botID, version)
	var (
		v       BotConfigVersion
		created string
		comment sql.NullString
	)
	if err := row.Scan(&v.BotID, &v.Version, &v.ConfigYAML, &created, &comment); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	v.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if comment.Valid {
		c := comment.String
		v.Comment = &c
	}
	return &v, nil
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
