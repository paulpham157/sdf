// Package orchestrator dispatches Attempts to agents in sandboxes (ADR 0035).
// Each Attempt runs under a context with timeout; cancel stops its goroutine tree.
// Agent tool calls must go through the Tool Proxy (ADR 0026), never direct.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/paulpham157/sdf/go/internal/bus"
	"github.com/paulpham157/sdf/go/internal/metrics"
	"github.com/paulpham157/sdf/go/internal/queue"
	"github.com/paulpham157/sdf/go/internal/toolproxy"
)

// ErrDuplicateAttempt is returned when the same idempotency key is started twice.
var ErrDuplicateAttempt = errors.New("orchestrator: duplicate idempotency key")

// ErrNoToolProxy is returned when CallTool is used without a configured Proxy.
var ErrNoToolProxy = errors.New("orchestrator: tool proxy not configured")

// SandboxRunner is the sandbox side of an Attempt (injected for tests).
// It must return when ctx is cancelled.
type SandboxRunner func(ctx context.Context, attemptID string, payload []byte) error

// Attempt is one dispatch into a sandbox.
type Attempt struct {
	ID                 string
	IdempotencyKey     string
	AgentID            string
	Payload            []byte
	Timeout            time.Duration
	OwnerPrincipal     string
	SandboxID          string
	AllowedCredentials []string
	AllowedTools       []string
}

type attemptMeta struct {
	agentID   string
	attemptID string
	timeout   time.Duration
}

// Orchestrator owns Attempt lifecycle: enqueue via the task queue, run under
// a timeout context, stream on the bus, and honour cancellation.
// Tool calls from agents flow through tools (*toolproxy.Proxy).
type Orchestrator struct {
	bus     *bus.Bus
	queue   *queue.Queue
	runner  SandboxRunner
	tools   *toolproxy.Proxy
	owned   toolproxy.OutcomeStore // non-nil when New opened OutcomeDBPath
	timeout time.Duration

	mu      sync.Mutex
	started map[string]struct{}
	meta    map[string]attemptMeta
	execs   atomic.Int64
	active  sync.WaitGroup
	root    context.Context
	cancel  context.CancelFunc
}

// Config wires the four-layer core for one process.
type Config struct {
	BusCapacity    int
	QueueCapacity  int
	Workers        int
	DefaultTimeout time.Duration
	Runner         SandboxRunner
	ToolProxy      *toolproxy.Proxy
	// OutcomeDBPath is the SQLite file for the Tool Proxy idempotency fence.
	// Used only when ToolProxy is nil. Empty keeps the in-process MemoryStore
	// (tests and processes that do not need restart survival). A non-empty
	// path opens toolproxy.OpenSQLiteStore and the Orchestrator closes it.
	OutcomeDBPath string
	// Metrics receives queue depth and, when ToolProxy is nil, Tool Proxy
	// counters. Nil uses metrics.Default. An explicit ToolProxy keeps the
	// registry it was built with.
	Metrics *metrics.Registry
}

// New builds an Orchestrator. Runner defaults to a no-op that waits on ctx.
// ToolProxy defaults to an in-process proxy with a no-op executor and a
// MemoryStore. When ToolProxy is nil and OutcomeDBPath is set, New opens
// that path with toolproxy.OpenSQLiteStore and owns the store.
func New(cfg Config) (*Orchestrator, error) {
	if cfg.BusCapacity < 1 {
		cfg.BusCapacity = 64
	}
	if cfg.QueueCapacity < 1 {
		cfg.QueueCapacity = 256
	}
	if cfg.Workers < 1 {
		cfg.Workers = 4
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = 30 * time.Second
	}
	if cfg.Runner == nil {
		cfg.Runner = func(ctx context.Context, _ string, _ []byte) error {
			<-ctx.Done()
			return ctx.Err()
		}
	}
	reg := cfg.Metrics
	if reg == nil {
		reg = metrics.Default
	}
	var owned toolproxy.OutcomeStore
	if cfg.ToolProxy == nil {
		if cfg.OutcomeDBPath != "" {
			store, err := toolproxy.OpenSQLiteStore(cfg.OutcomeDBPath)
			if err != nil {
				return nil, fmt.Errorf("orchestrator: outcome db: %w", err)
			}
			owned = store
			cfg.ToolProxy = toolproxy.NewProxy(nil, store)
		} else {
			cfg.ToolProxy = toolproxy.NewProxy(nil, nil)
		}
		cfg.ToolProxy.SetMetrics(reg)
	}

	root, cancel := context.WithCancel(context.Background())
	o := &Orchestrator{
		bus:     bus.New(cfg.BusCapacity),
		runner:  cfg.Runner,
		tools:   cfg.ToolProxy,
		owned:   owned,
		timeout: cfg.DefaultTimeout,
		started: make(map[string]struct{}),
		meta:    make(map[string]attemptMeta),
		root:    root,
		cancel:  cancel,
	}
	// The orchestrator is the Attempt record the fence reads at insert
	// (ADR 0038). Installed before workers run.
	o.tools.SetEvidenceModeSource(o.readEvidenceMode)
	o.queue = queue.New(cfg.QueueCapacity, cfg.Workers, o.handleTask, reg)
	return o, nil
}

func (o *Orchestrator) readEvidenceMode(attemptID string) (string, bool, error) {
	return o.tools.Store().AttemptEvidenceMode(attemptID)
}

// Bus returns the in-process message bus.
func (o *Orchestrator) Bus() *bus.Bus { return o.bus }

// Queue returns the task queue.
func (o *Orchestrator) Queue() *queue.Queue { return o.queue }

// ToolProxy returns the wired Tool Proxy.
func (o *Orchestrator) ToolProxy() *toolproxy.Proxy { return o.tools }

// Executions returns how many Attempts actually began running (post-idempotency).
func (o *Orchestrator) Executions() int64 { return o.execs.Load() }

// Start enqueues an Attempt. Duplicate IdempotencyKey does not run twice
// and does not change a stored evidence mode. Registers the Attempt's
// sandbox credential scope with the Tool Proxy. The Attempt is recorded
// synthetic until ConfirmLive (ADR 0031, ADR 0038). SandboxID is only the
// credential-scope id.
func (o *Orchestrator) Start(ctx context.Context, a Attempt) error {
	if a.IdempotencyKey == "" {
		return errors.New("orchestrator: idempotency key required")
	}
	if strings.TrimSpace(a.ID) == "" {
		return errors.New("orchestrator: attempt id required")
	}
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = o.timeout
	}
	sandboxID := a.SandboxID
	if sandboxID == "" {
		sandboxID = a.ID
	}

	o.mu.Lock()
	if _, ok := o.started[a.IdempotencyKey]; ok {
		o.mu.Unlock()
		return ErrDuplicateAttempt
	}
	o.started[a.IdempotencyKey] = struct{}{}
	o.meta[a.IdempotencyKey] = attemptMeta{
		agentID:   a.AgentID,
		attemptID: a.ID,
		timeout:   timeout,
	}
	o.mu.Unlock()

	created, err := o.tools.Store().EnsureAttempt(a.ID)
	if err != nil {
		o.abandonStart(a.IdempotencyKey, a.ID, false)
		return err
	}

	o.tools.RegisterScope(toolproxy.SandboxScope{
		SandboxID:          sandboxID,
		AttemptID:          a.ID,
		AgentID:            a.AgentID,
		OwnerPrincipal:     a.OwnerPrincipal,
		AllowedCredentials: append([]string(nil), a.AllowedCredentials...),
		AllowedTools:       append([]string(nil), a.AllowedTools...),
	})

	t := queue.Task{
		ID:             a.ID,
		IdempotencyKey: a.IdempotencyKey,
		Payload:        append([]byte(nil), a.Payload...),
		MaxAttempts:    1,
	}
	err = o.queue.Enqueue(ctx, t)
	if err != nil {
		o.abandonStart(a.IdempotencyKey, a.ID, created)
		if errors.Is(err, queue.ErrDuplicateKey) {
			return ErrDuplicateAttempt
		}
		return err
	}
	return nil
}

// abandonStart removes the idempotency reservation. When forgetMode is set
// and no other in-flight start still names attemptID, the synthetic Attempt
// record and its scope are removed so a start that never queued cannot be
// promoted to live.
func (o *Orchestrator) abandonStart(key, attemptID string, forgetMode bool) {
	o.mu.Lock()
	delete(o.started, key)
	delete(o.meta, key)
	still := false
	for _, m := range o.meta {
		if m.attemptID == attemptID {
			still = true
			break
		}
	}
	o.mu.Unlock()
	if !forgetMode || still {
		return
	}
	if err := o.tools.Store().ForgetSyntheticAttempt(attemptID); err == nil {
		o.tools.DropScope(attemptID)
	}
}

// ConfirmLive promotes an Attempt from synthetic to live once a real sandbox
// exists (ADR 0031, ADR 0038). sandboxID must be non-empty after trimming.
// An unknown Attempt is not recorded. The promotion is one-way. On SQLite
// the update commits before return. The Attempt's credential-scope SandboxID
// does not promote.
func (o *Orchestrator) ConfirmLive(attemptID, sandboxID string) (bool, error) {
	if strings.TrimSpace(sandboxID) == "" || strings.TrimSpace(attemptID) == "" {
		return false, nil
	}
	return o.tools.Store().PromoteAttempt(attemptID)
}

// EvidenceMode reports the stored mode for an Attempt this process started.
// An unknown Attempt is synthetic and is not recorded (ADR 0031).
func (o *Orchestrator) EvidenceMode(attemptID string) (string, error) {
	mode, ok, err := o.tools.Store().AttemptEvidenceMode(attemptID)
	if err != nil {
		return "", err
	}
	if !ok {
		return toolproxy.EvidenceModeSynthetic, nil
	}
	return toolproxy.StampEvidenceMode(mode), nil
}

// CallTool sends one structured tool call through the Tool Proxy for an Attempt.
// Agents must use this instead of invoking tools or reading credentials directly.
// claimedActor empty skips the claim check; principal empty skips owner check.
func (o *Orchestrator) CallTool(ctx context.Context, attemptID string, req toolproxy.ActionRequest, claimedActor, principal string) (toolproxy.Result, error) {
	if o.tools == nil {
		return toolproxy.Result{}, ErrNoToolProxy
	}
	req.AttemptID = attemptID
	return o.tools.Execute(ctx, req, claimedActor, principal)
}

func (o *Orchestrator) loadMeta(key string) (attemptMeta, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	m, ok := o.meta[key]
	return m, ok
}

func (o *Orchestrator) deleteMeta(key string) {
	o.mu.Lock()
	delete(o.meta, key)
	o.mu.Unlock()
}

func (o *Orchestrator) handleTask(ctx context.Context, t queue.Task) error {
	meta, ok := o.loadMeta(t.IdempotencyKey)
	if !ok {
		meta = attemptMeta{attemptID: t.ID, timeout: o.timeout}
	}
	defer o.deleteMeta(t.IdempotencyKey)

	runCtx, cancel := context.WithTimeout(ctx, meta.timeout)
	defer cancel()

	o.active.Add(1)
	defer o.active.Done()
	o.execs.Add(1)

	_ = o.bus.Publish(runCtx, bus.Message{
		From:    "orchestrator",
		To:      meta.agentID,
		Payload: []byte("attempt_started:" + meta.attemptID),
	})

	err := o.runner(runCtx, meta.attemptID, t.Payload)

	pubCtx, pubCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_ = o.bus.Publish(pubCtx, bus.Message{
		From:    "orchestrator",
		To:      meta.agentID,
		Payload: []byte("attempt_finished:" + meta.attemptID),
	})
	pubCancel()
	return err
}

// Wait blocks until all in-flight Attempts finish (after cancel or natural end).
func (o *Orchestrator) Wait() { o.active.Wait() }

// Close cancels the root context, closes the queue and bus, waits for Attempts,
// and closes an OutcomeDBPath store this Orchestrator opened.
func (o *Orchestrator) Close() {
	o.cancel()
	o.queue.Close()
	o.Wait()
	o.bus.Close()
	if o.owned != nil {
		_ = o.owned.Close()
		o.owned = nil
	}
}
