package orchestrator_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
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
	o, err := orchestrator.New(orchestrator.Config{
		Workers:        1,
		DefaultTimeout: time.Second,
		ToolProxy:      proxy,
		Runner: func(ctx context.Context, attemptID string, _ []byte) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()

	err = o.Start(context.Background(), orchestrator.Attempt{
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
	o, err := orchestrator.New(orchestrator.Config{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if o.ToolProxy() == nil {
		t.Fatal("expected default ToolProxy")
	}
	if _, ok := o.ToolProxy().Store().(*toolproxy.MemoryStore); !ok {
		t.Fatalf("empty OutcomeDBPath: store type %T, want MemoryStore", o.ToolProxy().Store())
	}
}

func TestExplicitToolProxyIgnoresOutcomeDBPath(t *testing.T) {
	proxy := toolproxy.NewProxy(nil, nil)
	o, err := orchestrator.New(orchestrator.Config{
		Workers:       1,
		ToolProxy:     proxy,
		OutcomeDBPath: filepath.Join(t.TempDir(), "missing", "fence.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if o.ToolProxy() != proxy {
		t.Fatal("explicit ToolProxy should win over OutcomeDBPath")
	}
}

func TestOutcomeDBPathOpenError(t *testing.T) {
	_, err := orchestrator.New(orchestrator.Config{
		OutcomeDBPath: filepath.Join(t.TempDir(), "missing", "fence.db"),
	})
	if err == nil {
		t.Fatal("expected OpenSQLiteStore error for a missing parent directory")
	}
}

// TestOutcomeDBPathReplaysStoredResult proves the production knob: a set
// OutcomeDBPath uses SQLite, and the same attempt_id+action_id returns the
// stored result (Replay) instead of executing again, including after the
// process that wrote the row is closed and a new Orchestrator opens the file.
func TestOutcomeDBPathReplaysStoredResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fence.db")
	req := toolproxy.ActionRequest{
		Tool:     "filesystem",
		Action:   "read",
		Resource: "/workspace/a.go",
	}
	attempt := orchestrator.Attempt{
		ID:             "att-dur",
		IdempotencyKey: "key-dur",
		AgentID:        "codex",
		OwnerPrincipal: "alice",
		SandboxID:      "sbx-dur",
		AllowedTools:   []string{"filesystem"},
		Timeout:        time.Second,
	}

	o1, err := orchestrator.New(orchestrator.Config{
		Workers:       1,
		OutcomeDBPath: path,
		Runner: func(ctx context.Context, _ string, _ []byte) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := o1.ToolProxy().Store().(*toolproxy.SQLiteStore); !ok {
		t.Fatalf("store type %T, want SQLiteStore", o1.ToolProxy().Store())
	}
	if err := o1.Start(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	first, err := o1.CallTool(context.Background(), attempt.ID, req, "agent:codex", "alice")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.Replay || first.Status != toolproxy.StatusExecuted || first.ActionID == "" {
		t.Fatalf("first=%+v", first)
	}
	second, err := o1.CallTool(context.Background(), attempt.ID, req, "agent:codex", "alice")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !second.Replay || second.ActionID != first.ActionID || second.Status != toolproxy.StatusExecuted {
		t.Fatalf("second=%+v first=%+v", second, first)
	}
	if !reflect.DeepEqual(second.Value, first.Value) {
		t.Fatalf("stored value %#v, first %#v", second.Value, first.Value)
	}
	o1.Close()

	o2, err := orchestrator.New(orchestrator.Config{
		Workers:       1,
		OutcomeDBPath: path,
		Runner: func(ctx context.Context, _ string, _ []byte) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer o2.Close()
	if err := o2.Start(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	third, err := o2.CallTool(context.Background(), attempt.ID, req, "agent:codex", "alice")
	if err != nil {
		t.Fatalf("after restart: %v", err)
	}
	if !third.Replay || third.ActionID != first.ActionID || third.Status != toolproxy.StatusExecuted {
		t.Fatalf("after restart=%+v", third)
	}
	if !reflect.DeepEqual(third.Value, first.Value) {
		t.Fatalf("value after restart %#v, first %#v", third.Value, first.Value)
	}
}
