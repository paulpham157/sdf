package metrics_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/paulpham157/sdf/go/internal/metrics"
)

func TestHandlerExposesGoroutinesAndQueueDepth(t *testing.T) {
	reg := &metrics.Registry{}
	reg.SetQueueDepth(7)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var snap metrics.Snapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.QueueDepth != 7 {
		t.Fatalf("queue_depth=%d", snap.QueueDepth)
	}
	if snap.Goroutines < 1 {
		t.Fatalf("goroutines=%d", snap.Goroutines)
	}
	if snap.ToolExecutedTotal != 0 || snap.ToolReplayTotal != 0 || snap.ToolClaimStuckTotal != 0 || snap.ToolPolicyDenyTotal != 0 {
		t.Fatalf("tool counters = %+v", snap)
	}
	var raw map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"goroutines", "queue_depth",
		"tool_executed_total", "tool_replay_total",
		"tool_claim_stuck_total", "tool_policy_deny_total",
	} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("missing %s in %s", key, rr.Body.String())
		}
	}
}

func TestToolProxyCountersMonotonic(t *testing.T) {
	reg := &metrics.Registry{}
	reg.IncToolExecuted()
	reg.IncToolReplay()
	reg.IncToolReplay()
	reg.IncToolClaimStuck()
	reg.IncToolPolicyDeny()
	reg.IncToolPolicyDeny()
	reg.IncToolPolicyDeny()
	snap := reg.Snapshot()
	if snap.ToolExecutedTotal != 1 || snap.ToolReplayTotal != 2 || snap.ToolClaimStuckTotal != 1 || snap.ToolPolicyDenyTotal != 3 {
		t.Fatalf("%+v", snap)
	}
}
