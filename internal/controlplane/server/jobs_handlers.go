package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) handleJobRunsList(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	runs, err := s.listJobRuns(ctx, limit)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("db list job runs: %v", err))
		return
	}
	writeJSON(w, 200, runs)
}

type jobTriggerRequest struct {
	Trigger string `json:"trigger,omitempty"`
}

func (s *Server) handleJobBalanceSyncNow(w http.ResponseWriter, r *http.Request) {
	var req jobTriggerRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	trigger := strings.TrimSpace(req.Trigger)
	if trigger == "" {
		trigger = "manual"
	}
	runID, err := s.startBalanceSyncBatch(trigger)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("start job failed: %v", err))
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "run_id": runID})
}

func (s *Server) handleJobRedeemNow(w http.ResponseWriter, r *http.Request) {
	var req jobTriggerRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	trigger := strings.TrimSpace(req.Trigger)
	if trigger == "" {
		trigger = "manual"
	}
	runID, err := s.startRedeemBatch(trigger)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("start job failed: %v", err))
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "run_id": runID})
}

func (s *Server) handleJobTradesSyncNow(w http.ResponseWriter, r *http.Request) {
	var req jobTriggerRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	trigger := strings.TrimSpace(req.Trigger)
	if trigger == "" {
		trigger = "manual"
	}
	runID, err := s.startTradesSyncBatch(trigger)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("start job failed: %v", err))
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "run_id": runID})
}

func (s *Server) handleJobPositionsSyncNow(w http.ResponseWriter, r *http.Request) {
	var req jobTriggerRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	trigger := strings.TrimSpace(req.Trigger)
	if trigger == "" {
		trigger = "manual"
	}
	runID, err := s.startPositionsSyncBatch(trigger)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("start job failed: %v", err))
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "run_id": runID})
}

func (s *Server) handleJobOpenOrdersSyncNow(w http.ResponseWriter, r *http.Request) {
	var req jobTriggerRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	trigger := strings.TrimSpace(req.Trigger)
	if trigger == "" {
		trigger = "manual"
	}
	runID, err := s.startOpenOrdersSyncBatch(trigger)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("start job failed: %v", err))
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "run_id": runID})
}

func (s *Server) handleJobEquitySnapshotNow(w http.ResponseWriter, r *http.Request) {
	var req jobTriggerRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	trigger := strings.TrimSpace(req.Trigger)
	if trigger == "" {
		trigger = "manual"
	}
	runID, err := s.startEquitySnapshotBatch(trigger)
	if err != nil {
		writeError(w, 500, fmt.Sprintf("start job failed: %v", err))
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "run_id": runID})
}
