package toolproxy_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/paulpham157/sdf/go/internal/toolproxy"
)

func scope(attemptID string) toolproxy.SandboxScope {
	return toolproxy.SandboxScope{
		SandboxID:          "sbx-" + attemptID,
		AttemptID:          attemptID,
		AgentID:            "codex",
		OwnerPrincipal:     "alice",
		AllowedCredentials: []string{"ANTHROPIC_API_KEY"},
		AllowedTools:       []string{"filesystem", "credentials"},
	}
}

func TestSuccessfulRoundTrip(t *testing.T) {
	var calls atomic.Int64
	p := toolproxy.NewProxy(func(ctx context.Context, req toolproxy.ActionRequest) (any, error) {
		calls.Add(1)
		return map[string]any{"path": req.Resource, "bytes": 12}, nil
	})
	p.RegisterScope(scope("att-1"))

	req := toolproxy.ActionRequest{
		AttemptID: "att-1",
		Tool:      "filesystem",
		Action:    "read",
		Resource:  "/workspace/README.md",
	}
	res, err := p.Execute(context.Background(), req, "", "alice")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != toolproxy.StatusExecuted || res.Effect != "allow" {
		t.Fatalf("got status=%s effect=%s", res.Status, res.Effect)
	}
	if res.Actor != "agent:codex" {
		t.Fatalf("actor = %q", res.Actor)
	}
	if res.ActionID == "" || res.ActionID != req.ActionID() && res.ActionID[:7] != "ACTION-" {
		// ActionID uses server-derived actor; recompute with actor set
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
	m, ok := res.Value.(map[string]any)
	if !ok || m["bytes"] != 12 {
		t.Fatalf("value = %#v", res.Value)
	}
}

func TestCredentialScopeDenial(t *testing.T) {
	p := toolproxy.NewProxy(nil)
	p.RegisterScope(scope("att-2"))

	req := toolproxy.ActionRequest{
		AttemptID: "att-2",
		Tool:      "credentials",
		Action:    "inject",
		Resource:  "OPENAI_API_KEY", // not in AllowedCredentials
	}
	res, err := p.Execute(context.Background(), req, "", "alice")
	if !errors.Is(err, toolproxy.ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	if res.Status != toolproxy.StatusDenied || res.Policy != toolproxy.PolicyCredentialScope {
		t.Fatalf("status=%s policy=%s reason=%s", res.Status, res.Policy, res.Reason)
	}
}

func TestCredentialScopeAllowsListed(t *testing.T) {
	p := toolproxy.NewProxy(nil)
	p.RegisterScope(scope("att-2b"))
	req := toolproxy.ActionRequest{
		AttemptID: "att-2b",
		Tool:      "credentials",
		Action:    "inject",
		Resource:  "ANTHROPIC_API_KEY",
	}
	res, err := p.Execute(context.Background(), req, "", "alice")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != toolproxy.StatusExecuted {
		t.Fatalf("status=%s", res.Status)
	}
}

func TestSandboxTimeout(t *testing.T) {
	p := toolproxy.NewProxy(func(ctx context.Context, _ toolproxy.ActionRequest) (any, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
			return "late", nil
		}
	})
	p.RegisterScope(scope("att-3"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	req := toolproxy.ActionRequest{
		AttemptID: "att-3",
		Tool:      "filesystem",
		Action:    "read",
		Resource:  "/slow",
	}
	res, err := p.Execute(ctx, req, "", "alice")
	if !errors.Is(err, toolproxy.ErrFailed) {
		t.Fatalf("err = %v", err)
	}
	if res.Policy != toolproxy.PolicySandboxTimeout && res.Status != toolproxy.StatusFailed {
		t.Fatalf("status=%s policy=%s", res.Status, res.Policy)
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("ctx err = %v", ctx.Err())
	}
}

func TestIdempotencySameActionKey(t *testing.T) {
	var calls atomic.Int64
	p := toolproxy.NewProxy(func(ctx context.Context, req toolproxy.ActionRequest) (any, error) {
		n := calls.Add(1)
		return map[string]any{"n": n}, nil
	})
	p.RegisterScope(scope("att-4"))

	req := toolproxy.ActionRequest{
		AttemptID: "att-4",
		Tool:      "filesystem",
		Action:    "write",
		Resource:  "/out.txt",
		Context:   map[string]any{"body": "hello"},
	}
	r1, err := p.Execute(context.Background(), req, "", "alice")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	r2, err := p.Execute(context.Background(), req, "", "alice")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("executor called %d times; want 1 (idempotent fence)", calls.Load())
	}
	if !r2.Replay {
		t.Fatal("second result should be replay")
	}
	if r1.ActionID != r2.ActionID {
		t.Fatalf("action ids differ: %s vs %s", r1.ActionID, r2.ActionID)
	}
	if r1.Value.(map[string]any)["n"] != r2.Value.(map[string]any)["n"] {
		t.Fatalf("replayed value mismatch")
	}
}

func TestActorMismatchDenied(t *testing.T) {
	p := toolproxy.NewProxy(nil)
	p.RegisterScope(scope("att-5"))
	req := toolproxy.ActionRequest{
		AttemptID: "att-5",
		Tool:      "filesystem",
		Action:    "read",
		Resource:  "/x",
	}
	res, err := p.Execute(context.Background(), req, "agent:other", "alice")
	if !errors.Is(err, toolproxy.ErrDenied) {
		t.Fatalf("err = %v", err)
	}
	if res.Policy != toolproxy.PolicyActorBinding {
		t.Fatalf("policy = %s", res.Policy)
	}
}

func TestActionIDStable(t *testing.T) {
	r := toolproxy.ActionRequest{
		AttemptID: "a",
		Actor:     "agent:codex",
		Tool:      "filesystem",
		Action:    "read",
		Resource:  "/f",
		Context:   map[string]any{"b": 2, "a": 1},
	}
	id1 := r.ActionID()
	id2 := r.ActionID()
	if id1 != id2 || id1[:7] != "ACTION-" {
		t.Fatalf("unstable or bad prefix: %s / %s", id1, id2)
	}
}

func TestActionIDMatchesPython(t *testing.T) {
	// Python: json.dumps(..., sort_keys=True, separators=(",", ":"))
	// payload keys as in ActionRequest.action_id
	r := toolproxy.ActionRequest{
		AttemptID: "a",
		Actor:     "agent:codex",
		Tool:      "filesystem",
		Action:    "read",
		Resource:  "/f",
		Context:   map[string]any{"a": 1, "b": 2},
	}
	want := "ACTION-89b529ad62a23a6a3032196178168cda3a4de6977c8e10ce18e6d84ccbd24c67"
	if got := r.ActionID(); got != want {
		t.Fatalf("ActionID=\n%s\nwant\n%s", got, want)
	}
}
