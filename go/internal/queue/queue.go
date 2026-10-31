// Package queue is the task queue with scheduling, retry, cancellation and
// idempotency keys (ADR 0035).
package queue

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/paulpham157/sdf/go/internal/metrics"
)

var (
	// ErrDuplicateKey means a task with this idempotency key already exists or ran.
	ErrDuplicateKey = errors.New("queue: duplicate idempotency key")
	// ErrClosed means the queue is shut down.
	ErrClosed = errors.New("queue: closed")
)

// Task is one unit of work on the queue.
type Task struct {
	ID             string
	IdempotencyKey string
	Payload        []byte
	MaxAttempts    int
	attempt        int
}

// Result is the outcome of running a Task.
type Result struct {
	TaskID string
	Err    error
}

// Handler runs a dequeued task. It must respect ctx cancellation.
type Handler func(ctx context.Context, t Task) error

// Queue is a bounded FIFO with idempotent enqueue and worker pool.
type Queue struct {
	ch       chan Task
	handler  Handler
	reg      *metrics.Registry
	mu       sync.Mutex
	seen     map[string]struct{} // idempotency keys that were accepted or completed
	inflight map[string]struct{}
	wg       sync.WaitGroup
	cancel   context.CancelFunc
	root     context.Context
	closed   bool
}

// New creates a queue with bounded capacity. Workers call handler for each task.
func New(capacity int, workers int, handler Handler, reg *metrics.Registry) *Queue {
	if capacity < 1 {
		capacity = 1
	}
	if workers < 1 {
		workers = 1
	}
	if reg == nil {
		reg = metrics.Default
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{
		ch:       make(chan Task, capacity),
		handler:  handler,
		reg:      reg,
		seen:     make(map[string]struct{}),
		inflight: make(map[string]struct{}),
		cancel:   cancel,
		root:     ctx,
	}
	for i := 0; i < workers; i++ {
		q.wg.Add(1)
		go q.worker()
	}
	return q
}

// Enqueue adds a task. Same IdempotencyKey twice returns ErrDuplicateKey and
// does not execute again. Blocks when the bounded channel is full until ctx
// cancels or space frees (backpressure).
func (q *Queue) Enqueue(ctx context.Context, t Task) error {
	if t.IdempotencyKey == "" {
		return errors.New("queue: idempotency key required")
	}
	if t.MaxAttempts < 1 {
		t.MaxAttempts = 1
	}

	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrClosed
	}
	if _, ok := q.seen[t.IdempotencyKey]; ok {
		q.mu.Unlock()
		return ErrDuplicateKey
	}
	q.seen[t.IdempotencyKey] = struct{}{}
	q.mu.Unlock()

	select {
	case <-ctx.Done():
		q.mu.Lock()
		delete(q.seen, t.IdempotencyKey)
		q.mu.Unlock()
		return ctx.Err()
	case <-q.root.Done():
		q.mu.Lock()
		delete(q.seen, t.IdempotencyKey)
		q.mu.Unlock()
		return ErrClosed
	case q.ch <- t:
		q.reg.AddQueueDepth(1)
		return nil
	}
}

// Depth is the number of tasks waiting in the channel.
func (q *Queue) Depth() int {
	return len(q.ch)
}

func (q *Queue) worker() {
	defer q.wg.Done()
	for {
		select {
		case <-q.root.Done():
			return
		case t, ok := <-q.ch:
			if !ok {
				return
			}
			q.reg.AddQueueDepth(-1)
			q.runOne(t)
		}
	}
}

func (q *Queue) runOne(t Task) {
	q.mu.Lock()
	q.inflight[t.IdempotencyKey] = struct{}{}
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		delete(q.inflight, t.IdempotencyKey)
		q.mu.Unlock()
	}()

	for t.attempt < t.MaxAttempts {
		t.attempt++
		err := q.handler(q.root, t)
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		// transient failure: retry remaining attempts with short backoff
		if t.attempt < t.MaxAttempts {
			select {
			case <-q.root.Done():
				return
			case <-time.After(time.Duration(t.attempt) * 5 * time.Millisecond):
			}
		}
	}
}

// Close cancels workers and waits for them to exit. Does not drain the channel.
func (q *Queue) Close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.mu.Unlock()
	q.cancel()
	q.wg.Wait()
}
