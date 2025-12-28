package server

import (
	"os"
	"strings"
)

// getenv reads from OS env first, then from secrets db under env/<KEY>.
func (s *Server) getenv(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	if s != nil && s.secrets != nil {
		if v, ok, _ := s.secrets.GetString("env/" + key); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
