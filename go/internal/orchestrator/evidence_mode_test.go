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

func TestEvidenceMode_PromotionAndAuditStamp(t *testing.T) {
	orch, err := orchestrator.New(orchestrator.Config{
		Workers:        1,
		DefaultTimeout: time.Second,
		Runner: func(context.Context, string, []byte) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer orch.Close()

	const id = "att-mode"
	if err := orch.Start(context.Background(), orchestrator.Attempt{
		ID:             id,
		IdempotencyKey: "key-mode",
		AgentID:        "codex",
		OwnerPrincipal: "alice",
		SandboxID:      "sbx-scope",
		AllowedTools:   []string{"filesystem"},
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := orch.EvidenceMode(id); err != nil || got != toolproxy.EvidenceModeSynthetic {
		t.Fatalf("scope id promoted mode: %q err=%v", got, err)
	}
	promoted, err := orch.ConfirmLive(id, "  ")
	if err != nil || promoted {
		t.Fatalf("blank sandbox promoted=%v err=%v", promoted, err)
	}
	promoted, err = orch.ConfirmLive("no-such", "sbx-real")
	if err != nil || promoted {
		t.Fatalf("unknown attempt promoted=%v err=%v", promoted, err)
	}
	if _, ok, err := orch.ToolProxy().Store().AttemptEvidenceMode("no-such"); err != nil || ok {
		t.Fatalf("unknown attempt was recorded ok=%v err=%v", ok, err)
	}

	first := toolproxy.ActionRequest{Tool: "filesystem", Action: "read", Resource: "/a"}
	res, err := orch.CallTool(context.Background(), id, first, "agent:codex", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if res.EvidenceMode != toolproxy.EvidenceModeSynthetic {
		t.Fatalf("first result mode = %q", res.EvidenceMode)
	}

	promoted, err = orch.ConfirmLive(id, "sbx-real")
	if err != nil || !promoted {
		t.Fatalf("promote promoted=%v err=%v", promoted, err)
	}
	again, err := orch.ConfirmLive(id, "sbx-real")
	if err != nil || again {
		t.Fatalf("second promote promoted=%v err=%v", again, err)
	}
	if got, err := orch.EvidenceMode(id); err != nil || got != toolproxy.EvidenceModeLive {
		t.Fatalf("mode after promote = %q err=%v", got, err)
	}

	replay, err := orch.CallTool(context.Background(), id, first, "agent:codex", "alice")
	if err != nil || !replay.Replay {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if replay.EvidenceMode != toolproxy.EvidenceModeLive {
		t.Fatalf("replay result mode = %q; want the new policy_decided row", replay.EvidenceMode)
	}
	if !reflect.DeepEqual(modeTrace(t, orch, id, res.ActionID), []string{
		toolproxy.EventPolicyDecided + "=synthetic",
		toolproxy.EventActionClaimed + "=synthetic",
		toolproxy.EventActionExecuted + "=synthetic",
		toolproxy.EventPolicyDecided + "=live",
	}) {
		t.Fatalf("trace = %v", modeTrace(t, orch, id, res.ActionID))
	}

	second := toolproxy.ActionRequest{Tool: "filesystem", Action: "write", Resource: "/b"}
	wrote, err := orch.CallTool(context.Background(), id, second, "agent:codex", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if wrote.EvidenceMode != toolproxy.EvidenceModeLive {
		t.Fatalf("post-promotion result mode = %q", wrote.EvidenceMode)
	}
	if !reflect.DeepEqual(modeTrace(t, orch, id, wrote.ActionID), []string{
		toolproxy.EventPolicyDecided + "=live",
		toolproxy.EventActionClaimed + "=live",
		toolproxy.EventActionExecuted + "=live",
	}) {
		t.Fatalf("later action trace = %v", modeTrace(t, orch, id, wrote.ActionID))
	}

	err = orch.Start(context.Background(), orchestrator.Attempt{
		ID:             id,
		IdempotencyKey: "key-mode",
		AgentID:        "codex",
	})
	if err != orchestrator.ErrDuplicateAttempt {
		t.Fatalf("duplicate start: %v", err)
	}
	if got, err := orch.EvidenceMode(id); err != nil || got != toolproxy.EvidenceModeLive {
		t.Fatalf("duplicate start demoted mode to %q err=%v", got, err)
	}
}

func TestEvidenceMode_StampAtWriteDuringExecute(t *testing.T) {
	var orch *orchestrator.Orchestrator
	proxy := toolproxy.NewProxy(func(context.Context, toolproxy.ActionRequest) (any, error) {
		promoted, err := orch.ConfirmLive("att-stamp", "sbx-real")
		if err != nil {
			return nil, err
		}
		if !promoted {
			return nil, errors.New("sandbox report did not promote")
		}
		return "ok", nil
	}, nil)
	var err error
	orch, err = orchestrator.New(orchestrator.Config{
		Workers:        1,
		DefaultTimeout: time.Second,
		ToolProxy:      proxy,
		Runner: func(context.Context, string, []byte) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer orch.Close()

	if err := orch.Start(context.Background(), orchestrator.Attempt{
		ID:             "att-stamp",
		IdempotencyKey: "key-stamp",
		AgentID:        "codex",
		OwnerPrincipal: "alice",
		SandboxID:      "sbx-scope",
		AllowedTools:   []string{"filesystem"},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := orch.CallTool(context.Background(), "att-stamp", toolproxy.ActionRequest{
		Tool: "filesystem", Action: "read", Resource: "/a",
	}, "agent:codex", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if res.EvidenceMode != toolproxy.EvidenceModeSynthetic {
		t.Fatalf("result mode = %q; want the policy_decided row from before promotion", res.EvidenceMode)
	}
	if !reflect.DeepEqual(modeTrace(t, orch, "att-stamp", res.ActionID), []string{
		toolproxy.EventPolicyDecided + "=synthetic",
		toolproxy.EventActionClaimed + "=synthetic",
		toolproxy.EventActionExecuted + "=live",
	}) {
		t.Fatalf("trace = %v", modeTrace(t, orch, "att-stamp", res.ActionID))
	}
}

func TestEvidenceMode_FailureBeforeAndAfterSandbox(t *testing.T) {
	t.Run("before sandbox", func(t *testing.T) {
		done := make(chan struct{})
		orch, err := orchestrator.New(orchestrator.Config{
			Workers:        1,
			DefaultTimeout: 5 * time.Second,
			Runner: func(context.Context, string, []byte) error {
				close(done)
				return errors.New("crashed before sandbox")
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer orch.Close()
		if err := orch.Start(context.Background(), orchestrator.Attempt{
			ID: "att-before", IdempotencyKey: "k-before", AgentID: "codex",
		}); err != nil {
			t.Fatal(err)
		}
		waitDone(t, done)
		if got, err := orch.EvidenceMode("att-before"); err != nil || got != toolproxy.EvidenceModeSynthetic {
			t.Fatalf("mode = %q err=%v", got, err)
		}
	})

	t.Run("after sandbox", func(t *testing.T) {
		done := make(chan struct{})
		var orch *orchestrator.Orchestrator
		var err error
		orch, err = orchestrator.New(orchestrator.Config{
			Workers:        1,
			DefaultTimeout: 5 * time.Second,
			Runner: func(_ context.Context, id string, _ []byte) error {
				defer close(done)
				promoted, err := orch.ConfirmLive(id, "sbx-live")
				if err != nil {
					return err
				}
				if !promoted {
					return errors.New("expected promotion")
				}
				return errors.New("crashed after sandbox")
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer orch.Close()
		if err := orch.Start(context.Background(), orchestrator.Attempt{
			ID: "att-after", IdempotencyKey: "k-after", AgentID: "codex",
		}); err != nil {
			t.Fatal(err)
		}
		waitDone(t, done)
		if got, err := orch.EvidenceMode("att-after"); err != nil || got != toolproxy.EvidenceModeLive {
			t.Fatalf("mode = %q err=%v", got, err)
		}
	})
}

func TestEvidenceMode_SQLiteSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fence.db")
	o1, err := orchestrator.New(orchestrator.Config{
		Workers:        1,
		OutcomeDBPath:  path,
		DefaultTimeout: time.Second,
		Runner: func(context.Context, string, []byte) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	const id = "att-disk"
	if err := o1.Start(context.Background(), orchestrator.Attempt{
		ID: id, IdempotencyKey: "k-disk", AgentID: "codex", OwnerPrincipal: "alice",
		AllowedTools: []string{"filesystem"},
	}); err != nil {
		t.Fatal(err)
	}
	first, err := o1.CallTool(context.Background(), id, toolproxy.ActionRequest{
		Tool: "filesystem", Action: "read", Resource: "/a",
	}, "agent:codex", "alice")
	if err != nil {
		t.Fatal(err)
	}
	promoted, err := o1.ConfirmLive(id, "sbx-real")
	if err != nil || !promoted {
		t.Fatalf("promote promoted=%v err=%v", promoted, err)
	}
	if _, err := o1.CallTool(context.Background(), id, toolproxy.ActionRequest{
		Tool: "filesystem", Action: "write", Resource: "/b",
	}, "agent:codex", "alice"); err != nil {
		t.Fatal(err)
	}
	o1.Close()

	o2, err := orchestrator.New(orchestrator.Config{
		Workers:        1,
		OutcomeDBPath:  path,
		DefaultTimeout: time.Second,
		Runner: func(context.Context, string, []byte) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer o2.Close()
	if got, err := o2.EvidenceMode(id); err != nil || got != toolproxy.EvidenceModeLive {
		t.Fatalf("reopened mode = %q err=%v", got, err)
	}
	if !reflect.DeepEqual(modeTrace(t, o2, id, first.ActionID), []string{
		toolproxy.EventPolicyDecided + "=synthetic",
		toolproxy.EventActionClaimed + "=synthetic",
		toolproxy.EventActionExecuted + "=synthetic",
	}) {
		t.Fatalf("reopened trace = %v", modeTrace(t, o2, id, first.ActionID))
	}
	// A new dispatch registers the in-memory credential scope. It must not
	// demote the mode restored from the fence file.
	if err := o2.Start(context.Background(), orchestrator.Attempt{
		ID: id, IdempotencyKey: "k-disk-again", AgentID: "codex", OwnerPrincipal: "alice",
		AllowedTools: []string{"filesystem"},
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := o2.EvidenceMode(id); err != nil || got != toolproxy.EvidenceModeLive {
		t.Fatalf("restarted start demoted mode to %q err=%v", got, err)
	}
	third, err := o2.CallTool(context.Background(), id, toolproxy.ActionRequest{
		Tool: "filesystem", Action: "stat", Resource: "/c",
	}, "agent:codex", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if third.EvidenceMode != toolproxy.EvidenceModeLive {
		t.Fatalf("post-restart result mode = %q", third.EvidenceMode)
	}
}

func TestStartEnqueueFailureDropsSyntheticAttempt(t *testing.T) {
	entered := make(chan struct{}, 1)
	orch, err := orchestrator.New(orchestrator.Config{
		Workers:        1,
		QueueCapacity:  1,
		DefaultTimeout: 30 * time.Second,
		Runner: func(ctx context.Context, id string, _ []byte) error {
			if id == "rolled-back" {
				t.Errorf("rolled-back attempt ran")
			}
			select {
			case entered <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer orch.Close()

	if err := orch.Start(context.Background(), orchestrator.Attempt{
		ID: "hold-worker", IdempotencyKey: "k-hold", AgentID: "codex",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not enter the runner")
	}
	if err := orch.Start(context.Background(), orchestrator.Attempt{
		ID: "hold-queue", IdempotencyKey: "k-queue", AgentID: "codex",
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err = orch.Start(ctx, orchestrator.Attempt{
		ID: "rolled-back", IdempotencyKey: "k-roll", AgentID: "codex",
	})
	if err == nil {
		t.Fatal("expected enqueue to fail while the queue is full")
	}
	if _, ok, err := orch.ToolProxy().Store().AttemptEvidenceMode("rolled-back"); err != nil || ok {
		t.Fatalf("failed start left an Attempt record ok=%v err=%v", ok, err)
	}
	promoted, err := orch.ConfirmLive("rolled-back", "sbx-real")
	if err != nil || promoted {
		t.Fatalf("failed start was promotable promoted=%v err=%v", promoted, err)
	}
}

func modeTrace(t *testing.T, orch *orchestrator.Orchestrator, attemptID, actionID string) []string {
	t.Helper()
	obs, err := orch.ToolProxy().Store().Observations(attemptID, actionID)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(obs))
	for i, ev := range obs {
		out[i] = ev.Event + "=" + ev.EvidenceMode
	}
	return out
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not finish")
	}
}
