package orchestrator_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/paulpham157/sdf/go/internal/orchestrator"
	"github.com/paulpham157/sdf/go/internal/toolproxy"
)

func TestOrchestratorWiresToolProxy(t *testing.T) {
	proxy := toolproxy.NewProxy(func(ctx context.Context, req toolproxy.ActionRequest) (any, error) {
		return map[string]any{"resource": req.Resource}, nil
	}, nil)
	started := make(chan struct{})
	o := orchestrator.New(orchestrator.Config{
		Workers:        1,
		DefaultTimeout: time.Second,
		ToolProxy:      proxy,
		Runner: func(ctx context.Context, attemptID string, _ []byte) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	defer o.Close()

	err := o.Start(context.Background(), orchestrator.Attempt{
		ID:                 "att-orch-1",
		IdempotencyKey:     "key-1",
		AgentID:            "codex",
		OwnerPrincipal:     "alice",
		SandboxID:          "sbx-1",
		AllowedCredentials: []string{"ANTHROPIC_API_KEY"},
		AllowedTools:       []string{"filesystem", "credentials"},
		Timeout:            500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runner not started")
	}

	res, err := o.CallTool(context.Background(), "att-orch-1", toolproxy.ActionRequest{
		Tool:     "filesystem",
		Action:   "read",
		Resource: "/workspace/a.go",
	}, "agent:codex", "alice")
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.Status != toolproxy.StatusExecuted || res.Actor != "agent:codex" {
		t.Fatalf("res=%+v", res)
	}

	_, err = o.CallTool(context.Background(), "att-orch-1", toolproxy.ActionRequest{
		Tool:     "credentials",
		Action:   "inject",
		Resource: "AWS_SECRET_ACCESS_KEY",
	}, "", "alice")
	if !errors.Is(err, toolproxy.ErrDenied) {
		t.Fatalf("want credential denial, got %v", err)
	}
}

func TestOrchestratorDefaultToolProxy(t *testing.T) {
	o := orchestrator.New(orchestrator.Config{Workers: 1})
	defer o.Close()
	if o.ToolProxy() == nil {
		t.Fatal("expected default ToolProxy")
	}
}
