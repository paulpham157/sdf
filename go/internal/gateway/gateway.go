// Package gateway is the high-traffic HTTP API surface (ADR 0035).
package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/paulpham157/sdf/go/internal/metrics"
	"github.com/paulpham157/sdf/go/internal/orchestrator"
)

// Server exposes run-control endpoints and live metrics.
type Server struct {
	orch *orchestrator.Orchestrator
	reg  *metrics.Registry
	mux  *http.ServeMux
}

// New mounts routes on an internal mux.
func New(orch *orchestrator.Orchestrator, reg *metrics.Registry) *Server {
	if reg == nil {
		reg = metrics.Default
	}
	s := &Server{orch: orch, reg: reg, mux: http.NewServeMux()}
	s.mux.Handle("/metrics", reg.Handler())
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/v1/attempts", s.handleAttempts)
	return s
}

type startRequest struct {
	ID             string `json:"id"`
	IdempotencyKey string `json:"idempotency_key"`
	AgentID        string `json:"agent_id"`
	Payload        string `json:"payload"`
	TimeoutMS      int64  `json:"timeout_ms"`
}

func (s *Server) handleAttempts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req startRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	key := req.IdempotencyKey
	if key == "" {
		key = r.Header.Get("Idempotency-Key")
	}
	a := orchestrator.Attempt{
		ID:             req.ID,
		IdempotencyKey: key,
		AgentID:        req.AgentID,
		Payload:        []byte(req.Payload),
	}
	if req.TimeoutMS > 0 {
		a.Timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}
	err = s.orch.Start(r.Context(), a)
	if err != nil {
		if err == orchestrator.ErrDuplicateAttempt {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "duplicate idempotency key"})
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted", "id": req.ID})
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }
