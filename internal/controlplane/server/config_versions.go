package server

import "time"

type BotConfigVersion struct {
	BotID      string    `json:"bot_id"`
	Version    int       `json:"version"`
	ConfigYAML string    `json:"config_yaml"`
	CreatedAt  time.Time `json:"created_at"`
	Comment    *string   `json:"comment,omitempty"`
}
