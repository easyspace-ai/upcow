package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) handleAccountTrades(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(chiURLParam(r, "accountID"))
	limit := 100
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	trades, err := s.listTradesByAccount(ctx, accountID, limit)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db list trades: %v", err))
		return
	}
	writeJSON(w, 200, map[string]any{"account_id": accountID, "trades": trades})
}

func (s *Server) handleAccountPositions(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(chiURLParam(r, "accountID"))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	positions, err := s.listPositionsByAccount(ctx, accountID)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db list positions: %v", err))
		return
	}
	writeJSON(w, 200, map[string]any{"account_id": accountID, "positions": positions})
}

func (s *Server) handleAccountOpenOrders(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(chiURLParam(r, "accountID"))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	orders, err := s.listOpenOrdersByAccount(ctx, accountID)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db list open orders: %v", err))
		return
	}
	writeJSON(w, 200, map[string]any{"account_id": accountID, "open_orders": orders})
}

func (s *Server) handleAccountStats(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(chiURLParam(r, "accountID"))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	st, err := s.stats24h(ctx, accountID)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("stats: %v", err))
		return
	}
	writeJSON(w, 200, st)
}
