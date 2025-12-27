package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *Server) insertJobRunStart(ctx context.Context, jobName string, scope string, accountID *string, metaJSON *string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT INTO job_runs (job_name, scope, account_id, started_at, meta_json)
VALUES (?,?,?,?,?)
`, jobName, scope, accountID, time.Now().Format(time.RFC3339Nano), metaJSON)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Server) finishJobRun(ctx context.Context, runID int64, ok bool, errMsg *string, metaJSON *string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE job_runs
SET finished_at=?, ok=?, error=?, meta_json=?
WHERE id=?
`, time.Now().Format(time.RFC3339Nano), boolToInt(ok), errMsg, metaJSON, runID)
	return err
}

func (s *Server) listJobRuns(ctx context.Context, limit int) ([]JobRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, job_name, scope, account_id, started_at, finished_at, ok, error, meta_json
FROM job_runs
ORDER BY started_at DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []JobRun
	for rows.Next() {
		var (
			j          JobRun
			accountID  sql.NullString
			startedAt  string
			finishedAt sql.NullString
			okVal      sql.NullInt64
			errStr     sql.NullString
			meta       sql.NullString
		)
		if err := rows.Scan(&j.ID, &j.JobName, &j.Scope, &accountID, &startedAt, &finishedAt, &okVal, &errStr, &meta); err != nil {
			return nil, err
		}
		if accountID.Valid {
			v := accountID.String
			j.AccountID = &v
		}
		j.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
		if finishedAt.Valid {
			if t, err := time.Parse(time.RFC3339Nano, finishedAt.String); err == nil {
				j.FinishedAt = &t
			}
		}
		if okVal.Valid {
			v := okVal.Int64 != 0
			j.OK = &v
		}
		if errStr.Valid {
			v := errStr.String
			j.Error = &v
		}
		if meta.Valid {
			v := meta.String
			j.MetaJSON = &v
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *Server) getJobRun(ctx context.Context, runID int64) (*JobRun, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, job_name, scope, account_id, started_at, finished_at, ok, error, meta_json
FROM job_runs
WHERE id=?
`, runID)
	var (
		j          JobRun
		accountID  sql.NullString
		startedAt  string
		finishedAt sql.NullString
		okVal      sql.NullInt64
		errStr     sql.NullString
		meta       sql.NullString
	)
	if err := row.Scan(&j.ID, &j.JobName, &j.Scope, &accountID, &startedAt, &finishedAt, &okVal, &errStr, &meta); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan job run: %w", err)
	}
	if accountID.Valid {
		v := accountID.String
		j.AccountID = &v
	}
	j.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
	if finishedAt.Valid {
		if t, err := time.Parse(time.RFC3339Nano, finishedAt.String); err == nil {
			j.FinishedAt = &t
		}
	}
	if okVal.Valid {
		v := okVal.Int64 != 0
		j.OK = &v
	}
	if errStr.Valid {
		v := errStr.String
		j.Error = &v
	}
	if meta.Valid {
		v := meta.String
		j.MetaJSON = &v
	}
	return &j, nil
}
