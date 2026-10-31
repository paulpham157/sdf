// Package orchestrator dispatches Attempts to agents in sandboxes (ADR 0035).
// Each Attempt runs under a context with timeout; cancel stops its goroutine tree.
// Agent tool calls must go through the Tool Proxy (ADR 0026), never direct.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/paulpham157/sdf/go/internal/bus"
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
	o.queue = queue.New(cfg.QueueCapacity, cfg.Workers, o.handleTask, nil)
	return o, nil
}

// Bus returns the in-process message bus.
func (o *Orchestrator) Bus() *bus.Bus { return o.bus }

// Queue returns the task queue.
func (o *Orchestrator) Queue() *queue.Queue { return o.queue }

// ToolProxy returns the wired Tool Proxy.
func (o *Orchestrator) ToolProxy() *toolproxy.Proxy { return o.tools }

// Executions returns how many Attempts actually began running (post-idempotency).
func (o *Orchestrator) Executions() int64 { return o.execs.Load() }

// Start enqueues an Attempt. Duplicate IdempotencyKey does not run twice.
// Registers the Attempt's sandbox credential scope with the Tool Proxy.
func (o *Orchestrator) Start(ctx context.Context, a Attempt) error {
	if a.IdempotencyKey == "" {
		return errors.New("orchestrator: idempotency key required")
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

	if o.tools != nil {
		o.tools.RegisterScope(toolproxy.SandboxScope{
			SandboxID:          sandboxID,
			AttemptID:          a.ID,
			AgentID:            a.AgentID,
			OwnerPrincipal:     a.OwnerPrincipal,
			AllowedCredentials: append([]string(nil), a.AllowedCredentials...),
			AllowedTools:       append([]string(nil), a.AllowedTools...),
		})
	}

	t := queue.Task{
		ID:             a.ID,
		IdempotencyKey: a.IdempotencyKey,
		Payload:        append([]byte(nil), a.Payload...),
		MaxAttempts:    1,
	}
	err := o.queue.Enqueue(ctx, t)
	if err != nil {
		o.mu.Lock()
		delete(o.started, a.IdempotencyKey)
		delete(o.meta, a.IdempotencyKey)
		o.mu.Unlock()
		if errors.Is(err, queue.ErrDuplicateKey) {
			return ErrDuplicateAttempt
		}
		return err
	}
	return nil
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
