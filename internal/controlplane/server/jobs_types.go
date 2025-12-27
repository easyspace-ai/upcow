package server

import "time"

type JobRun struct {
	ID         int64      `json:"id"`
	JobName    string     `json:"job_name"`
	Scope      string     `json:"scope"`
	AccountID  *string    `json:"account_id,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	OK         *bool      `json:"ok,omitempty"`
	Error      *string    `json:"error,omitempty"`
	MetaJSON   *string    `json:"meta_json,omitempty"`
}
