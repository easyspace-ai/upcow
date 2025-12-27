package server

import "time"

type Bot struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	AccountID      *string   `json:"account_id,omitempty"`
	ConfigPath     string    `json:"config_path"`
	ConfigYAML     string    `json:"config_yaml"`
	LogPath        string    `json:"log_path"`
	PersistenceDir string    `json:"persistence_dir"`
	CurrentVersion int       `json:"current_version"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type BotProcess struct {
	BotID        string     `json:"bot_id"`
	PID          *int       `json:"pid,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	LastExitAt   *time.Time `json:"last_exit_at,omitempty"`
	LastExitCode *int       `json:"last_exit_code,omitempty"`
	LastError    *string    `json:"last_error,omitempty"`
}
