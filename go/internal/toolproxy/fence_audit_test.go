package toolproxy_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/paulpham157/sdf/go/internal/toolproxy"

	_ "modernc.org/sqlite"
)

func TestPolicyDecidedAppendedAndReplaySkipsExecutor(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(t *testing.T) toolproxy.OutcomeStore
	}{
		{name: "memory", open: func(t *testing.T) toolproxy.OutcomeStore { return toolproxy.NewMemoryStore() }},
		{name: "sqlite", open: func(t *testing.T) toolproxy.OutcomeStore {
			t.Helper()
			s, err := toolproxy.OpenSQLiteStore(filepath.Join(t.TempDir(), "fence.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.open(t)
			var calls atomic.Int64
			p := toolproxy.NewProxy(func(ctx context.Context, req toolproxy.ActionRequest) (any, error) {
				calls.Add(1)
				return map[string]any{"body": "hello"}, nil
			}, store)
			p.RegisterScope(scope("att-audit"))
			req := toolproxy.ActionRequest{
				AttemptID: "att-audit",
				Tool:      "filesystem",
				Action:    "read",
				Resource:  "/out.txt",
			}

			first, err := p.Execute(context.Background(), req, "", "alice")
			if err != nil {
				t.Fatal(err)
			}
			if first.Replay || first.Status != toolproxy.StatusExecuted {
				t.Fatalf("first=%+v", first)
			}
			events := eventNames(t, store, req.AttemptID, first.ActionID)
			if !reflect.DeepEqual(events, []string{
				toolproxy.EventPolicyDecided,
				toolproxy.EventActionClaimed,
				toolproxy.EventActionExecuted,
			}) {
				t.Fatalf("events after execute = %v", events)
			}
			obs, err := store.Observations(req.AttemptID, first.ActionID)
			if err != nil {
				t.Fatal(err)
			}
			if obs[0].Principal != "alice" || obs[0].Decision != "allow" || obs[0].Policy != toolproxy.PolicyAllowlist {
				t.Fatalf("policy row = %+v", obs[0])
			}
			if obs[2].Value == nil {
				t.Fatal("terminal event dropped the executor value needed for replay")
			}

			second, err := p.Execute(context.Background(), req, "", "alice")
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatalf("executor calls = %d; want 1", calls.Load())
			}
			if !second.Replay || second.Status != toolproxy.StatusExecuted || second.ActionID != first.ActionID {
				t.Fatalf("second=%+v", second)
			}
			if !reflect.DeepEqual(second.Value, first.Value) {
				t.Fatalf("replay value %#v, first %#v", second.Value, first.Value)
			}
			events = eventNames(t, store, req.AttemptID, first.ActionID)
			if !reflect.DeepEqual(events, []string{
				toolproxy.EventPolicyDecided,
				toolproxy.EventActionClaimed,
				toolproxy.EventActionExecuted,
				toolproxy.EventPolicyDecided,
			}) {
				t.Fatalf("events after replay = %v", events)
			}
			obs, err = store.Observations(req.AttemptID, first.ActionID)
			if err != nil {
				t.Fatal(err)
			}
			if obs[3].Principal != "alice" || obs[3].Decision != "allow" || obs[3].AuditID == obs[0].AuditID {
				t.Fatalf("replay decision = %+v first = %+v", obs[3], obs[0])
			}

			// A non-owner does not receive the replay. Their denial is its own row.
			_, err = p.Execute(context.Background(), req, "", "mallory")
			if !errors.Is(err, toolproxy.ErrDenied) {
				t.Fatalf("mallory err = %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("executor calls after deny = %d", calls.Load())
			}
			obs, err = store.Observations(req.AttemptID, first.ActionID)
			if err != nil {
				t.Fatal(err)
			}
			last := obs[len(obs)-1]
			if last.Event != toolproxy.EventPolicyDecided || last.Decision != "deny" ||
				last.Principal != "mallory" || last.Policy != toolproxy.PolicyAttemptBinding {
				t.Fatalf("deny row = %+v", last)
			}
			executed := 0
			for _, ev := range obs {
				if ev.Event == toolproxy.EventActionExecuted {
					executed++
				}
			}
			if executed != 1 {
				t.Fatalf("action_executed rows = %d", executed)
			}
		})
	}
}

func TestFailedReplayRecordsPolicyDecidedAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fence.db")
	var calls atomic.Int64
	exec := func(ctx context.Context, req toolproxy.ActionRequest) (any, error) {
		calls.Add(1)
		return nil, errors.New("boom")
	}
	store, err := toolproxy.OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	p := toolproxy.NewProxy(exec, store)
	p.RegisterScope(scope("att-fail-audit"))
	req := toolproxy.ActionRequest{
		AttemptID: "att-fail-audit",
		Tool:      "filesystem",
		Action:    "read",
		Resource:  "/boom",
	}
	first, err := p.Execute(context.Background(), req, "", "alice")
	if !errors.Is(err, toolproxy.ErrFailed) {
		t.Fatalf("first err = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = toolproxy.OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p = toolproxy.NewProxy(func(ctx context.Context, req toolproxy.ActionRequest) (any, error) {
		calls.Add(1)
		return "should-not-run", nil
	}, store)
	p.RegisterScope(scope("att-fail-audit"))
	second, err := p.Execute(context.Background(), req, "", "alice")
	if !errors.Is(err, toolproxy.ErrFailed) {
		t.Fatalf("after restart err = %v", err)
	}
	if !second.Replay || second.Status != toolproxy.StatusFailed || calls.Load() != 1 {
		t.Fatalf("second=%+v calls=%d", second, calls.Load())
	}
	events := eventNames(t, store, req.AttemptID, first.ActionID)
	if !reflect.DeepEqual(events, []string{
		toolproxy.EventPolicyDecided,
		toolproxy.EventActionClaimed,
		toolproxy.EventActionFailed,
		toolproxy.EventPolicyDecided,
	}) {
		t.Fatalf("events = %v", events)
	}
}

func TestDenialRecordsPolicyDecidedWithoutExecution(t *testing.T) {
	store := toolproxy.NewMemoryStore()
	var calls atomic.Int64
	p := toolproxy.NewProxy(func(ctx context.Context, req toolproxy.ActionRequest) (any, error) {
		calls.Add(1)
		return "nope", nil
	}, store)
	p.RegisterScope(scope("att-deny"))
	req := toolproxy.ActionRequest{
		AttemptID: "att-deny",
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
	events := eventNames(t, store, req.AttemptID, res.ActionID)
	if !reflect.DeepEqual(events, []string{toolproxy.EventPolicyDecided}) {
		t.Fatalf("events = %v", events)
	}
	obs, err := store.Observations(req.AttemptID, res.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	if obs[0].Decision != "deny" || obs[0].Principal != "alice" || obs[0].Actor != "agent:codex" {
		t.Fatalf("row = %+v", obs[0])
	}
	_, err = p.Execute(context.Background(), req, "agent:other", "alice")
	if !errors.Is(err, toolproxy.ErrDenied) {
		t.Fatal(err)
	}
	events = eventNames(t, store, req.AttemptID, res.ActionID)
	if !reflect.DeepEqual(events, []string{toolproxy.EventPolicyDecided, toolproxy.EventPolicyDecided}) {
		t.Fatalf("events after second denial = %v", events)
	}
	if calls.Load() != 0 {
		t.Fatalf("executor calls = %d", calls.Load())
	}
}

func TestExecutionEventsAreUniqueAndPolicyDecidedIsNot(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(t *testing.T) toolproxy.OutcomeStore
	}{
		{name: "memory", open: func(t *testing.T) toolproxy.OutcomeStore { return toolproxy.NewMemoryStore() }},
		{name: "sqlite", open: func(t *testing.T) toolproxy.OutcomeStore {
			t.Helper()
			s, err := toolproxy.OpenSQLiteStore(filepath.Join(t.TempDir(), "fence.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.open(t)
			for i := 0; i < 3; i++ {
				err := store.AppendPolicy(toolproxy.Observation{
					AuditID:   "AUDIT-fixed",
					AttemptID: "a",
					ActionID:  "ACTION-1",
					Decision:  "deny",
					Reason:    "first",
					Policy:    "allowlist",
					Principal: "alice",
				})
				if i == 0 && err != nil {
					t.Fatal(err)
				}
				if i > 0 && err != nil {
					t.Fatalf("redelivery of the same audit id: %v", err)
				}
			}
			err := store.AppendPolicy(toolproxy.Observation{
				AuditID:   "AUDIT-second",
				AttemptID: "a",
				ActionID:  "ACTION-1",
				Decision:  "allow",
				Reason:    "again",
				Policy:    "allowlist",
				Principal: "alice",
			})
			if err != nil {
				t.Fatal(err)
			}
			obs, err := store.Observations("a", "ACTION-1")
			if err != nil {
				t.Fatal(err)
			}
			if len(obs) != 2 {
				t.Fatalf("policy rows = %d, want 2 (same audit id is a no-op)", len(obs))
			}
			if obs[0].Reason != "first" {
				t.Fatalf("redelivery overwrote the first decision: %+v", obs[0])
			}

			ok, err := store.Claim(toolproxy.Observation{AttemptID: "a", ActionID: "ACTION-1", Principal: "alice"})
			if err != nil || !ok {
				t.Fatalf("claim ok=%v err=%v", ok, err)
			}
			ok, err = store.Claim(toolproxy.Observation{AttemptID: "a", ActionID: "ACTION-1"})
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				t.Fatal("second claim reserved the same action")
			}
			if err := store.Complete(toolproxy.StoredOutcome{
				AttemptID: "a", ActionID: "ACTION-1", Status: toolproxy.StatusExecuted,
				Reason: "executor completed", Value: "first",
			}); err != nil {
				t.Fatal(err)
			}
			if err := store.Complete(toolproxy.StoredOutcome{
				AttemptID: "a", ActionID: "ACTION-1", Status: toolproxy.StatusExecuted,
				Reason: "later", Value: "second",
			}); err != nil {
				t.Fatalf("same terminal status should be a no-op: %v", err)
			}
			if err := store.Complete(toolproxy.StoredOutcome{
				AttemptID: "a", ActionID: "ACTION-1", Status: toolproxy.StatusFailed,
				Error: "nope",
			}); err == nil || !strings.Contains(err.Error(), "terminal outcome is executed") {
				t.Fatalf("err = %v", err)
			}
			got, found, err := store.Lookup("a", "ACTION-1")
			if err != nil || !found || got.Status != toolproxy.StatusExecuted || got.Value != "first" {
				t.Fatalf("found=%v outcome=%+v err=%v", found, got, err)
			}
		})
	}
}

func TestLegacyToolOutcomesMigrateToEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE tool_outcomes (
  attempt_id TEXT NOT NULL,
  action_id  TEXT NOT NULL,
  status     TEXT NOT NULL,
  actor      TEXT NOT NULL DEFAULT '',
  reason     TEXT NOT NULL DEFAULT '',
  policy     TEXT NOT NULL DEFAULT '',
  value_json TEXT,
  error      TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  PRIMARY KEY (attempt_id, action_id)
)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
INSERT INTO tool_outcomes (attempt_id, action_id, status, actor, reason, policy, value_json, error, created_at)
VALUES ('att-old', 'ACTION-old', 'executed', 'agent:codex', 'executor completed', 'allowlist', '{"body":"hello"}', '', '2026-10-31T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
INSERT INTO tool_outcomes (attempt_id, action_id, status, actor, reason, policy, value_json, error, created_at)
VALUES ('att-old', 'ACTION-claimed', 'claimed', '', '', '', NULL, '', '2026-10-31T00:00:01Z')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := toolproxy.OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	got, found, err := store.Lookup("att-old", "ACTION-old")
	if err != nil || !found || got.Status != toolproxy.StatusExecuted {
		t.Fatalf("found=%v outcome=%+v err=%v", found, got, err)
	}
	m, ok := got.Value.(map[string]any)
	if !ok || m["body"] != "hello" {
		t.Fatalf("migrated value = %#v", got.Value)
	}
	okClaim, err := store.Claim(toolproxy.Observation{AttemptID: "att-old", ActionID: "ACTION-old"})
	if err != nil {
		t.Fatal(err)
	}
	if okClaim {
		t.Fatal("migrated executed action accepted another claim")
	}
	claimed, found, err := store.Lookup("att-old", "ACTION-claimed")
	if err != nil || found {
		t.Fatalf("claimed-only lookup found=%v outcome=%+v err=%v", found, claimed, err)
	}
	okClaim, err = store.Claim(toolproxy.Observation{AttemptID: "att-old", ActionID: "ACTION-claimed"})
	if err != nil {
		t.Fatal(err)
	}
	if okClaim {
		t.Fatal("migrated claim accepted another claim")
	}
	events := eventNames(t, store, "att-old", "ACTION-old")
	if !reflect.DeepEqual(events, []string{toolproxy.EventActionClaimed, toolproxy.EventActionExecuted}) {
		t.Fatalf("migrated events = %v", events)
	}
	migrated, err := store.Observations("att-old", "ACTION-old")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range migrated {
		if ev.EvidenceMode != toolproxy.EvidenceModeSynthetic {
			t.Fatalf("legacy row %s mode = %q; history must stay synthetic", ev.Event, ev.EvidenceMode)
		}
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'tool_outcomes'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("legacy tool_outcomes table still present")
	}
	var indexSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'uq_tool_outcome_execution_event'`).Scan(&indexSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(indexSQL, "policy_decided") {
		t.Fatalf("execution unique index = %s", indexSQL)
	}
}

func eventNames(t *testing.T, store toolproxy.OutcomeStore, attemptID, actionID string) []string {
	t.Helper()
	obs, err := store.Observations(attemptID, actionID)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(obs))
	for i, o := range obs {
		names[i] = o.Event
	}
	return names
}
