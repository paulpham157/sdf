package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/paulpham157/sdf/go/internal/gateway"
	"github.com/paulpham157/sdf/go/internal/metrics"
	"github.com/paulpham157/sdf/go/internal/orchestrator"
)

func TestAttemptsIdempotencyConflict(t *testing.T) {
	block := make(chan struct{})
	orch := orchestrator.New(orchestrator.Config{
		Workers: 1,
		Runner: func(ctx context.Context, _ string, _ []byte) error {
			select {
			case <-block:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		DefaultTimeout: 5 * time.Second,
	})
	defer func() { close(block); orch.Close() }()

	s := gateway.New(orch, metrics.Default)
	body, _ := json.Marshal(map[string]any{
		"id": "a1", "idempotency_key": "k1", "agent_id": "ag", "payload": "p",
	})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/attempts", bytes.NewReader(body)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", rr.Code, rr.Body.String())
	}
	rr2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr2, httptest.NewRequest(http.MethodPost, "/v1/attempts", bytes.NewReader(body)))
	if rr2.Code != http.StatusConflict {
		t.Fatalf("second: want 409, got %d %s", rr2.Code, rr2.Body.String())
	}
}
