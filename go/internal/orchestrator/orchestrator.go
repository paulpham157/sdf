// Package orchestrator dispatches Attempts to agents in sandboxes (ADR 0035).
// Each Attempt runs under a context with timeout; cancel stops its goroutine tree.
package orchestrator

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/paulpham157/sdf/go/internal/bus"
	"github.com/paulpham157/sdf/go/internal/queue"
)

// ErrDuplicateAttempt is returned when the same idempotency key is started twice.
var ErrDuplicateAttempt = errors.New("orchestrator: duplicate idempotency key")

// SandboxRunner is the sandbox side of an Attempt (injected for tests).
// It must return when ctx is cancelled.
type SandboxRunner func(ctx context.Context, attemptID string, payload []byte) error

// Attempt is one dispatch into a sandbox.
type Attempt struct {
	ID             string
	IdempotencyKey string
	AgentID        string
	Payload        []byte
	Timeout        time.Duration
}

type attemptMeta struct {
	agentID   string
	attemptID string
	timeout   time.Duration
}

// Orchestrator owns Attempt lifecycle: enqueue via the task queue, run under
// a timeout context, stream on the bus, and honour cancellation.
type Orchestrator struct {
	bus     *bus.Bus
	queue   *queue.Queue
	runner  SandboxRunner
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
}

// New builds an Orchestrator. Runner defaults to a no-op that waits on ctx.
func New(cfg Config) *Orchestrator {
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

	root, cancel := context.WithCancel(context.Background())
	o := &Orchestrator{
		bus:     bus.New(cfg.BusCapacity),
		runner:  cfg.Runner,
		timeout: cfg.DefaultTimeout,
		started: make(map[string]struct{}),
		meta:    make(map[string]attemptMeta),
		root:    root,
		cancel:  cancel,
	}
	o.queue = queue.New(cfg.QueueCapacity, cfg.Workers, o.handleTask, nil)
	return o
}

// Bus returns the in-process message bus.
func (o *Orchestrator) Bus() *bus.Bus { return o.bus }

// Queue returns the task queue.
func (o *Orchestrator) Queue() *queue.Queue { return o.queue }

// Executions returns how many Attempts actually began running (post-idempotency).
func (o *Orchestrator) Executions() int64 { return o.execs.Load() }

// Start enqueues an Attempt. Duplicate IdempotencyKey does not run twice.
func (o *Orchestrator) Start(ctx context.Context, a Attempt) error {
	if a.IdempotencyKey == "" {
		return errors.New("orchestrator: idempotency key required")
	}
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = o.timeout
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

// Close cancels the root context, closes the queue and bus, and waits for Attempts.
func (o *Orchestrator) Close() {
	o.cancel()
	o.queue.Close()
	o.Wait()
	o.bus.Close()
}
