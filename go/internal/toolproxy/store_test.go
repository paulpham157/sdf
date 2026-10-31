package toolproxy_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/paulpham157/sdf/go/internal/toolproxy"
)

func TestDurableFenceRestartSurvival(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fence.db")

	var calls atomic.Int64
	exec := func(ctx context.Context, req toolproxy.ActionRequest) (any, error) {
		calls.Add(1)
		return map[string]any{"body": "hello", "n": 1}, nil
	}

	store1, err := toolproxy.OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	p1 := toolproxy.NewProxy(exec, store1)
	p1.RegisterScope(scope("att-dur-1"))
	req := toolproxy.ActionRequest{
		AttemptID: "att-dur-1",
		Tool:      "filesystem",
		Action:    "write",
		Resource:  "/out.txt",
		Context:   map[string]any{"body": "hello"},
	}
	r1, err := p1.Execute(context.Background(), req, "", "alice")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if r1.Status != toolproxy.StatusExecuted || r1.Replay {
		t.Fatalf("first=%+v", r1)
	}
	if err := store1.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate process restart: reopen the same DB file with a fresh Proxy.
	store2, err := toolproxy.OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	p2 := toolproxy.NewProxy(exec, store2)
	p2.RegisterScope(scope("att-dur-1"))
	r2, err := p2.Execute(context.Background(), req, "", "alice")
	if err != nil {
		t.Fatalf("after restart: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("executor called %d times; want 1 across restart", calls.Load())
	}
	if !r2.Replay || r2.Status != toolproxy.StatusExecuted {
		t.Fatalf("replay=%+v", r2)
	}
	if r1.ActionID != r2.ActionID {
		t.Fatalf("action id mismatch")
	}
	m, ok := r2.Value.(map[string]any)
	if !ok || m["body"] != "hello" {
		// JSON round-trip may use float64 for numbers; body must survive.
		t.Fatalf("value after restart = %#v", r2.Value)
	}
}

func TestDurableFenceConcurrentDuplicates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fence.db")
	store, err := toolproxy.OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var calls atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	p := toolproxy.NewProxy(func(ctx context.Context, req toolproxy.ActionRequest) (any, error) {
		calls.Add(1)
		close(started)
		<-release
		return map[string]any{"ok": true}, nil
	}, store)
	p.RegisterScope(scope("att-conc"))

	req := toolproxy.ActionRequest{
		AttemptID: "att-conc",
		Tool:      "filesystem",
		Action:    "read",
		Resource:  "/x",
	}

	const n = 8
	var wg sync.WaitGroup
	results := make([]toolproxy.Result, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			results[i], errs[i] = p.Execute(context.Background(), req, "", "alice")
		}()
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("executor never started")
	}
	// Give losers time to hit Claim while winner holds the slot.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("executor calls = %d; want 1", calls.Load())
	}
	var executed, replayed, claimedFail int
	for i := 0; i < n; i++ {
		if errs[i] != nil && !errors.Is(errs[i], toolproxy.ErrFailed) {
			t.Fatalf("unexpected err[%d]=%v", i, errs[i])
		}
		switch {
		case results[i].Status == toolproxy.StatusExecuted && !results[i].Replay:
			executed++
		case results[i].Status == toolproxy.StatusExecuted && results[i].Replay:
			replayed++
		case results[i].Status == toolproxy.StatusFailed:
			claimedFail++
		}
	}
	if executed+replayed+claimedFail != n {
		t.Fatalf("executed=%d replayed=%d claimedFail=%d", executed, replayed, claimedFail)
	}
	if executed != 1 {
		t.Fatalf("want exactly one non-replay executed, got %d", executed)
	}
}

func TestDurableFenceFailedSticks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fence.db")
	store, err := toolproxy.OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	p := toolproxy.NewProxy(func(ctx context.Context, req toolproxy.ActionRequest) (any, error) {
		calls.Add(1)
		return nil, errors.New("boom")
	}, store)
	p.RegisterScope(scope("att-fail"))
	req := toolproxy.ActionRequest{
		AttemptID: "att-fail",
		Tool:      "filesystem",
		Action:    "read",
		Resource:  "/boom",
	}
	_, err = p.Execute(context.Background(), req, "", "alice")
	if !errors.Is(err, toolproxy.ErrFailed) {
		t.Fatalf("err=%v", err)
	}
	_ = store.Close()

	store2, err := toolproxy.OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	p2 := toolproxy.NewProxy(func(ctx context.Context, req toolproxy.ActionRequest) (any, error) {
		calls.Add(1)
		return "should-not-run", nil
	}, store2)
	p2.RegisterScope(scope("att-fail"))
	r2, err := p2.Execute(context.Background(), req, "", "alice")
	if !errors.Is(err, toolproxy.ErrFailed) {
		t.Fatalf("after restart err=%v", err)
	}
	if !r2.Replay || r2.Status != toolproxy.StatusFailed {
		t.Fatalf("r2=%+v", r2)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestNoTTLEvictionDocumented(t *testing.T) {
	// Python SqlAlchemyAuditSink has no TTL / cleanup for tool_audits rows;
	// outcomes are append-only. This test documents that the Go SQLite fence
	// likewise has no eviction API — Lookup still returns after a long wait.
	dir := t.TempDir()
	path := filepath.Join(dir, "fence.db")
	store, err := toolproxy.OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	okClaim, err := store.Claim("a", "ACTION-x")
	if err != nil || !okClaim {
		t.Fatal(err)
	}
	ok, err := store.Claim("a", "ACTION-x")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("second claim should lose")
	}
	if err := store.Complete(toolproxy.StoredOutcome{
		AttemptID: "a", ActionID: "ACTION-x", Status: toolproxy.StatusExecuted, Reason: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	o, found, err := store.Lookup("a", "ACTION-x")
	if err != nil || !found || o.Status != toolproxy.StatusExecuted {
		t.Fatalf("found=%v o=%+v err=%v", found, o, err)
	}
}
