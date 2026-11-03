// Command sdf-orch runs the Go orchestration core (ADR 0035): API gateway,
// message bus, task queue and Attempt dispatcher, with /metrics for live
// goroutine count, queue depth, and Tool Proxy counters.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/paulpham157/sdf/go/internal/gateway"
	"github.com/paulpham157/sdf/go/internal/metrics"
	"github.com/paulpham157/sdf/go/internal/orchestrator"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	// Flag default is the env value, so -outcome-db wins when both are set.
	// Empty keeps MemoryStore (same as tests). A path opens OpenSQLiteStore.
	outcomeDB := flag.String("outcome-db", os.Getenv("SDF_TOOL_OUTCOME_DB"),
		"SQLite file for the Tool Proxy idempotency fence (env SDF_TOOL_OUTCOME_DB). Empty uses an in-process MemoryStore (no restart survival). Parent directory must exist. One process per file. See docs/sdf-orch.md")
	flag.Parse()

	reg := metrics.Default
	orch, err := orchestrator.New(orchestrator.Config{
		BusCapacity:    128,
		QueueCapacity:  1024,
		Workers:        8,
		DefaultTimeout: 60 * time.Second,
		OutcomeDBPath:  *outcomeDB,
		Metrics:        reg,
		Runner: func(ctx context.Context, attemptID string, _ []byte) error {
			// Placeholder sandbox: honour cancellation / timeout only.
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer orch.Close()

	srv := gateway.New(orch, reg)
	httpServer := &http.Server{Addr: *addr, Handler: srv.Handler()}

	go func() {
		fence := "in-memory"
		if *outcomeDB != "" {
			fence = *outcomeDB
		}
		log.Printf("sdf-orch listening on %s (metrics at /metrics, tool fence %s)", *addr, fence)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	orch.Close()
}
