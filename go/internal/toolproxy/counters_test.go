package toolproxy_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/paulpham157/sdf/go/internal/metrics"
	"github.com/paulpham157/sdf/go/internal/toolproxy"
)

func TestCountersMoveOnReplayDenyAndClaimStuck(t *testing.T) {
	reg := &metrics.Registry{}
	store := toolproxy.NewMemoryStore()
	var calls atomic.Int64
	p := toolproxy.NewProxy(func(_ context.Context, req toolproxy.ActionRequest) (any, error) {
		calls.Add(1)
		if req.Resource == "/boom" {
			return nil, errors.New("boom")
		}
		return map[string]any{"ok": true}, nil
	}, store)
	p.SetMetrics(reg)
	p.RegisterScope(scope("att-metrics"))

	okReq := toolproxy.ActionRequest{
		AttemptID: "att-metrics",
		Tool:      "filesystem",
		Action:    "read",
		Resource:  "/ok",
	}
	first, err := p.Execute(context.Background(), okReq, "", "alice")
	if err != nil || first.Replay || first.Status != toolproxy.StatusExecuted {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := p.Execute(context.Background(), okReq, "", "alice")
	if err != nil || !second.Replay || second.Status != toolproxy.StatusExecuted {
		t.Fatalf("replay=%+v err=%v", second, err)
	}

	boom := toolproxy.ActionRequest{
		AttemptID: "att-metrics",
		Tool:      "filesystem",
		Action:    "read",
		Resource:  "/boom",
	}
	failed, err := p.Execute(context.Background(), boom, "", "alice")
	if !errors.Is(err, toolproxy.ErrFailed) || failed.Replay {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	failedAgain, err := p.Execute(context.Background(), boom, "", "alice")
	if !errors.Is(err, toolproxy.ErrFailed) || !failedAgain.Replay {
		t.Fatalf("failed replay=%+v err=%v", failedAgain, err)
	}

	denied, err := p.Execute(context.Background(), toolproxy.ActionRequest{
		AttemptID: "att-metrics",
		Tool:      "credentials",
		Action:    "inject",
		Resource:  "OPENAI_API_KEY",
	}, "", "alice")
	if !errors.Is(err, toolproxy.ErrDenied) || denied.Status != toolproxy.StatusDenied {
		t.Fatalf("deny=%+v err=%v", denied, err)
	}
	if _, err := p.Execute(context.Background(), toolproxy.ActionRequest{
		AttemptID: "att-metrics",
		Tool:      "filesystem",
		Action:    "read",
		Resource:  "/nope",
	}, "agent:other", "alice"); !errors.Is(err, toolproxy.ErrDenied) {
		t.Fatalf("actor deny err=%v", err)
	}

	stuck := toolproxy.ActionRequest{
		AttemptID: "att-metrics",
		Actor:     "agent:codex",
		Tool:      "filesystem",
		Action:    "read",
		Resource:  "/stuck",
	}
	okClaim, err := store.Claim(toolproxy.Observation{AttemptID: stuck.AttemptID, ActionID: stuck.ActionID()})
	if err != nil || !okClaim {
		t.Fatalf("claim ok=%v err=%v", okClaim, err)
	}
	stuckRes, err := p.Execute(context.Background(), stuck, "", "alice")
	if !errors.Is(err, toolproxy.ErrFailed) || stuckRes.Replay || stuckRes.Status != toolproxy.StatusFailed {
		t.Fatalf("claim-stuck=%+v err=%v", stuckRes, err)
	}

	if calls.Load() != 2 {
		t.Fatalf("executor calls=%d; want 2 (success + failed, no replay or deny or claim-stuck)", calls.Load())
	}
	snap := reg.Snapshot()
	if snap.ToolExecutedTotal != 1 {
		t.Fatalf("executed=%d; failed, replay, deny, and claim-stuck must not count as a first success", snap.ToolExecutedTotal)
	}
	if snap.ToolReplayTotal != 2 {
		t.Fatalf("replay=%d; want the successful replay and the failed replay", snap.ToolReplayTotal)
	}
	if snap.ToolPolicyDenyTotal != 2 {
		t.Fatalf("deny=%d; want credential and actor denials", snap.ToolPolicyDenyTotal)
	}
	if snap.ToolClaimStuckTotal != 1 {
		t.Fatalf("claim_stuck=%d", snap.ToolClaimStuckTotal)
	}
}
