// Package metrics exposes live goroutine count and queue depth for the Go orchestration core (ADR 0035).
package metrics

import (
	"encoding/json"
	"net/http"
	"runtime"
	"sync/atomic"
)

// Registry holds process-wide gauges updated by the queue and orchestrator.
type Registry struct {
	queueDepth atomic.Int64
}

// Default is the process-wide registry used by the core packages.
var Default = &Registry{}

// SetQueueDepth records the current task-queue depth.
func (r *Registry) SetQueueDepth(n int64) { r.queueDepth.Store(n) }

// AddQueueDepth adjusts depth by delta (enqueue +1, dequeue -1).
func (r *Registry) AddQueueDepth(delta int64) { r.queueDepth.Add(delta) }

// QueueDepth returns the current queue depth.
func (r *Registry) QueueDepth() int64 { return r.queueDepth.Load() }

// Snapshot is the JSON shape served on /metrics.
type Snapshot struct {
	Goroutines int   `json:"goroutines"`
	QueueDepth int64 `json:"queue_depth"`
}

// Snapshot returns live gauges.
func (r *Registry) Snapshot() Snapshot {
	return Snapshot{
		Goroutines: runtime.NumGoroutine(),
		QueueDepth: r.QueueDepth(),
	}
}

// Handler serves GET /metrics as JSON: goroutine count and queue depth.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(r.Snapshot())
	})
}
