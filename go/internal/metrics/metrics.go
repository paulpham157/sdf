// Package metrics exposes live goroutine count, queue depth, and Tool Proxy
// counters for the Go orchestration core (ADR 0035). Counters are monotonic
// for the life of the process and are served as JSON on /metrics.
package metrics

import (
	"encoding/json"
	"net/http"
	"runtime"
	"sync/atomic"
)

// Registry holds process-wide gauges and counters updated by the queue,
// orchestrator, and Tool Proxy.
type Registry struct {
	queueDepth atomic.Int64

	toolExecuted   atomic.Int64
	toolReplay     atomic.Int64
	toolClaimStuck atomic.Int64
	toolPolicyDeny atomic.Int64
}

// Default is the process-wide registry used by the core packages.
var Default = &Registry{}

// SetQueueDepth records the current task-queue depth.
func (r *Registry) SetQueueDepth(n int64) { r.queueDepth.Store(n) }

// AddQueueDepth adjusts depth by delta (enqueue +1, dequeue -1).
func (r *Registry) AddQueueDepth(delta int64) { r.queueDepth.Add(delta) }

// QueueDepth returns the current queue depth.
func (r *Registry) QueueDepth() int64 { return r.queueDepth.Load() }

// IncToolExecuted counts one successful first execution: the executor ran
// and the action_executed row was stored. Replays do not call this.
func (r *Registry) IncToolExecuted() { r.toolExecuted.Add(1) }

// IncToolReplay counts one delivery that returned a stored terminal outcome
// (executed or failed) without running the executor.
func (r *Registry) IncToolReplay() { r.toolReplay.Add(1) }

// IncToolClaimStuck counts one already-claimed failure: the action has a
// claim and no terminal outcome, so this delivery did not execute it.
func (r *Registry) IncToolClaimStuck() { r.toolClaimStuck.Add(1) }

// IncToolPolicyDeny counts one policy denial returned to the caller.
func (r *Registry) IncToolPolicyDeny() { r.toolPolicyDeny.Add(1) }

// Snapshot is the JSON shape served on /metrics.
// Tool Proxy fields are counters since process start. They reset when the
// process exits and are not stored in the fence file.
type Snapshot struct {
	Goroutines int   `json:"goroutines"`
	QueueDepth int64 `json:"queue_depth"`
	// ToolExecutedTotal is successful first executions.
	ToolExecutedTotal int64 `json:"tool_executed_total"`
	// ToolReplayTotal is terminal outcomes returned without re-execution.
	ToolReplayTotal int64 `json:"tool_replay_total"`
	// ToolClaimStuckTotal is already-claimed failures with no terminal outcome.
	ToolClaimStuckTotal int64 `json:"tool_claim_stuck_total"`
	// ToolPolicyDenyTotal is policy denials.
	ToolPolicyDenyTotal int64 `json:"tool_policy_deny_total"`
}

// Snapshot returns live gauges and Tool Proxy counters.
func (r *Registry) Snapshot() Snapshot {
	return Snapshot{
		Goroutines:          runtime.NumGoroutine(),
		QueueDepth:          r.QueueDepth(),
		ToolExecutedTotal:   r.toolExecuted.Load(),
		ToolReplayTotal:     r.toolReplay.Load(),
		ToolClaimStuckTotal: r.toolClaimStuck.Load(),
		ToolPolicyDenyTotal: r.toolPolicyDeny.Load(),
	}
}

// Handler serves GET /metrics as JSON: goroutine count, queue depth, and
// Tool Proxy counters.
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
