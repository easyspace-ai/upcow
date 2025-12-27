package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) handleAccountBalances(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(chiURLParam(r, "accountID"))
	limit := 200
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 2000 {
			limit = n
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	items, err := s.listBalanceSnapshots(ctx, accountID, limit)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db list balances: %v", err))
		return
	}
	writeJSON(w, 200, map[string]any{"account_id": accountID, "balances": items})
}
