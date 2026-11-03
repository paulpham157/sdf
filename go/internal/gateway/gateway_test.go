package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/paulpham157/sdf/go/internal/gateway"
	"github.com/paulpham157/sdf/go/internal/metrics"
	"github.com/paulpham157/sdf/go/internal/orchestrator"
	"github.com/paulpham157/sdf/go/internal/toolproxy"
)

func TestAttemptsIdempotencyConflict(t *testing.T) {
	block := make(chan struct{})
	orch, err := orchestrator.New(orchestrator.Config{
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
	if err != nil {
		t.Fatal(err)
	}
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

func TestAttemptStartReportsSyntheticAndRejectsCallerMode(t *testing.T) {
	var runs atomic.Int64
	orch, err := orchestrator.New(orchestrator.Config{
		Workers:        1,
		DefaultTimeout: time.Second,
		Runner: func(context.Context, string, []byte) error {
			runs.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer orch.Close()
	s := gateway.New(orch, metrics.Default)

	for _, body := range []string{
		`{"id":"a-live","idempotency_key":"k-live","agent_id":"ag","payload":"p","evidence_mode":"live"}`,
		`{"id":"a-syn","idempotency_key":"k-syn","agent_id":"ag","payload":"p","evidence_mode":"synthetic"}`,
		`{"id":"a-null","idempotency_key":"k-null","agent_id":"ag","payload":"p","evidence_mode":null}`,
	} {
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/attempts", strings.NewReader(body)))
		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("body %s: status %d %s", body, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "ADR 0031") {
			t.Fatalf("body %s: diagnostic %s", body, rr.Body.String())
		}
	}
	if runs.Load() != 0 || orch.Executions() != 0 {
		t.Fatalf("caller mode started an attempt runs=%d execs=%d", runs.Load(), orch.Executions())
	}

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/attempts", strings.NewReader(
		`{"id":"a-ok","idempotency_key":"k-ok","agent_id":"ag","payload":"p"}`)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("accept: %d %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["evidence_mode"] != "synthetic" || resp["id"] != "a-ok" {
		t.Fatalf("accept body = %#v", resp)
	}
}

func TestMetricsEndpointCountsExecuteReplayAndDeny(t *testing.T) {
	reg := &metrics.Registry{}
	orch, err := orchestrator.New(orchestrator.Config{
		Workers:        1,
		Metrics:        reg,
		DefaultTimeout: time.Second,
		Runner: func(ctx context.Context, _ string, _ []byte) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer orch.Close()

	if err := orch.Start(context.Background(), orchestrator.Attempt{
		ID:                 "att-m",
		IdempotencyKey:     "k-m",
		AgentID:            "codex",
		OwnerPrincipal:     "alice",
		AllowedTools:       []string{"filesystem", "credentials"},
		AllowedCredentials: []string{"ANTHROPIC_API_KEY"},
		Timeout:            time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	req := toolproxy.ActionRequest{Tool: "filesystem", Action: "read", Resource: "/a"}
	if _, err := orch.CallTool(context.Background(), "att-m", req, "agent:codex", "alice"); err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := orch.CallTool(context.Background(), "att-m", req, "agent:codex", "alice")
	if err != nil || !second.Replay {
		t.Fatalf("replay=%+v err=%v", second, err)
	}
	_, err = orch.CallTool(context.Background(), "att-m", toolproxy.ActionRequest{
		Tool: "credentials", Action: "inject", Resource: "OPENAI_API_KEY",
	}, "agent:codex", "alice")
	if !errors.Is(err, toolproxy.ErrDenied) {
		t.Fatalf("deny err=%v", err)
	}

	s := gateway.New(orch, reg)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d %s", rr.Code, rr.Body.String())
	}
	var snap metrics.Snapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.ToolExecutedTotal != 1 || snap.ToolReplayTotal != 1 || snap.ToolPolicyDenyTotal != 1 || snap.ToolClaimStuckTotal != 0 {
		t.Fatalf("metrics %+v body %s", snap, rr.Body.String())
	}
}
