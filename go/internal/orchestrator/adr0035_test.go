package orchestrator_test

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/paulpham157/sdf/go/internal/bus"
	"github.com/paulpham157/sdf/go/internal/orchestrator"
	"github.com/paulpham157/sdf/go/internal/queue"
)


func maxRSSBytes() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return -1
	}
	// Linux: RuMaxrss is kilobytes
	return ru.Maxrss * 1024
}

// settleGoroutines waits until NumGoroutine is stable (GC / runtime noise).
func settleGoroutines() int {
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	n := runtime.NumGoroutine()
	time.Sleep(20 * time.Millisecond)
	n2 := runtime.NumGoroutine()
	if n2 < n {
		return n2
	}
	return n
}

// TestCancellationChaos_GoroutineCountMatches verifies ADR 0035: after
// cancelling in-flight Attempts, goroutine count returns to baseline
// (no leaked Attempt goroutines).
func TestCancellationChaos_GoroutineCountMatches(t *testing.T) {
	baseline := settleGoroutines()

	var entered atomic.Int64
	release := make(chan struct{})
	const N = 64
	orch := orchestrator.New(orchestrator.Config{
		BusCapacity:    8,
		QueueCapacity:  256,
		Workers:        N, // one worker per attempt so all enter the runner
		DefaultTimeout: 30 * time.Second,
		Runner: func(ctx context.Context, _ string, _ []byte) error {
			entered.Add(1)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		},
	})

	ctx := context.Background()
	for i := 0; i < N; i++ {
		err := orch.Start(ctx, orchestrator.Attempt{
			ID:             fmt.Sprintf("a-%d", i),
			IdempotencyKey: fmt.Sprintf("key-%d", i),
			AgentID:        "agent",
			Payload:        []byte("x"),
		})
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for entered.Load() < int64(N) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if entered.Load() < int64(N) {
		t.Fatalf("only %d/%d attempts entered runner", entered.Load(), N)
	}

	orch.Close() // cancels root → workers + attempt contexts
	close(release)

	after := settleGoroutines()
	// Allow a small runtime slack (netpoller / testing harness), not N leaks.
	const slack = 8
	if after > baseline+slack {
		t.Fatalf("goroutine leak: before=%d after=%d (slack=%d)", baseline, after, slack)
	}
}

// TestIdempotency_SameKeySingleExecution verifies ADR 0035: the same
// idempotency key sent twice results in a single execution.
func TestIdempotency_SameKeySingleExecution(t *testing.T) {
	var runs atomic.Int64
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	orch := orchestrator.New(orchestrator.Config{
		Workers:        2,
		QueueCapacity:  8,
		DefaultTimeout: 5 * time.Second,
		Runner: func(ctx context.Context, _ string, _ []byte) error {
			runs.Add(1)
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-block:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	defer orch.Close()

	ctx := context.Background()
	a := orchestrator.Attempt{
		ID:             "attempt-1",
		IdempotencyKey: "idem-42",
		AgentID:        "agent",
		Payload:        []byte("once"),
	}
	if err := orch.Start(ctx, a); err != nil {
		t.Fatalf("first start: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first attempt did not start")
	}

	err := orch.Start(ctx, orchestrator.Attempt{
		ID:             "attempt-2",
		IdempotencyKey: "idem-42",
		AgentID:        "agent",
		Payload:        []byte("again"),
	})
	if err != orchestrator.ErrDuplicateAttempt {
		t.Fatalf("second start: want ErrDuplicateAttempt, got %v", err)
	}

	close(block)
	deadline := time.Now().Add(2 * time.Second)
	for orch.Executions() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("executions: want 1, got %d", got)
	}
	if got := orch.Executions(); got != 1 {
		t.Fatalf("orchestrator.Executions: want 1, got %d", got)
	}
}

// TestBackpressure_MillionsStableGoroutinesAndRAM verifies ADR 0035:
// millions of bus messages against bounded channels keep goroutine count
// and heap within bounds (producers block rather than grow memory).
func TestBackpressure_MillionsStableGoroutinesAndRAM(t *testing.T) {
	if testing.Short() {
		t.Skip("millions of messages")
	}

	const (
		capacity = 64
		total    = 2_000_000
	)

	b := bus.New(capacity)
	defer b.Close()

	out := b.Subscribe("consumer")
	var consumed atomic.Int64
	var peakDepth atomic.Int64
	var peakG atomic.Int64
	stopMon := make(chan struct{})
	var monWG sync.WaitGroup
	monWG.Add(1)
	go func() {
		defer monWG.Done()
		ticker := time.NewTicker(200 * time.Microsecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopMon:
				return
			case <-ticker.C:
				g := int64(runtime.NumGoroutine())
				for {
					old := peakG.Load()
					if g <= old || peakG.CompareAndSwap(old, g) {
						break
					}
				}
				d := int64(len(out))
				for {
					old := peakDepth.Load()
					if d <= old || peakDepth.CompareAndSwap(old, d) {
						break
					}
				}
			}
		}
	}()

	var consumerWG sync.WaitGroup
	consumerWG.Add(1)
	go func() {
		defer consumerWG.Done()
		for range out {
			consumed.Add(1)
		}
	}()

	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	gBefore := runtime.NumGoroutine()
	peakG.Store(int64(gBefore))
	rssBefore := maxRSSBytes()

	ctx := context.Background()
	var produced atomic.Int64
	var prodWG sync.WaitGroup
	const producers = 4
	per := total / producers
	t0 := time.Now()
	prodWG.Add(producers)
	for p := 0; p < producers; p++ {
		go func() {
			defer prodWG.Done()
			for i := 0; i < per; i++ {
				if err := b.Publish(ctx, bus.Message{From: "p", To: "consumer", Payload: []byte("x")}); err != nil {
					t.Errorf("publish: %v", err)
					return
				}
				produced.Add(1)
			}
		}()
	}
	prodWG.Wait()
	elapsed := time.Since(t0)
	b.Unsubscribe("consumer")
	consumerWG.Wait()
	close(stopMon)
	monWG.Wait()

	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)
	gAfter := runtime.NumGoroutine()

	if produced.Load() != int64(total) {
		t.Fatalf("produced %d want %d", produced.Load(), total)
	}
	if consumed.Load() != int64(total) {
		t.Fatalf("consumed %d want %d", consumed.Load(), total)
	}

	throughput := float64(total) / elapsed.Seconds()
	t.Logf("BENCH go_bus_backpressure")
	t.Logf("messages=%d", total)
	t.Logf("capacity=%d", capacity)
	t.Logf("producers=%d", producers)
	t.Logf("elapsed_sec=%.6f", elapsed.Seconds())
	t.Logf("throughput_msgs_per_sec=%.2f", throughput)
	t.Logf("goroutines_before=%d", gBefore)
	t.Logf("peak_goroutines=%d", peakG.Load())
	t.Logf("goroutines_after=%d", gAfter)
	t.Logf("peak_queue_depth=%d", peakDepth.Load())
	t.Logf("heap_alloc_before_bytes=%d", memBefore.HeapAlloc)
	t.Logf("heap_alloc_after_bytes=%d", memAfter.HeapAlloc)
	t.Logf("heap_sys_before_bytes=%d", memBefore.HeapSys)
	t.Logf("heap_sys_after_bytes=%d", memAfter.HeapSys)
	rss := maxRSSBytes()
	t.Logf("rss_before_bytes=%d", rssBefore)
	t.Logf("rss_max_bytes=%d", rss)

	const goroutineSlack = 8
	if gAfter > gBefore+goroutineSlack {
		t.Fatalf("goroutines grew: before=%d after=%d", gBefore, gAfter)
	}
	const maxHeapGrowth = 64 << 20 // 64 MiB
	var growth uint64
	if memAfter.HeapAlloc > memBefore.HeapAlloc {
		growth = memAfter.HeapAlloc - memBefore.HeapAlloc
	}
	if growth > maxHeapGrowth {
		t.Fatalf("heap grew too much: +%d bytes (before=%d after=%d)", growth, memBefore.HeapAlloc, memAfter.HeapAlloc)
	}
}


// TestQueueIdempotency_Direct mirrors queue-level single execution.
func TestQueueIdempotency_Direct(t *testing.T) {
	var n atomic.Int64
	done := make(chan struct{})
	q := queue.New(4, 1, func(ctx context.Context, task queue.Task) error {
		n.Add(1)
		close(done)
		return nil
	}, nil)
	defer q.Close()

	ctx := context.Background()
	if err := q.Enqueue(ctx, queue.Task{ID: "1", IdempotencyKey: "k", Payload: nil}); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, queue.Task{ID: "2", IdempotencyKey: "k", Payload: nil}); err != queue.ErrDuplicateKey {
		t.Fatalf("want ErrDuplicateKey, got %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
	time.Sleep(20 * time.Millisecond)
	if n.Load() != 1 {
		t.Fatalf("want 1 run, got %d", n.Load())
	}
}
