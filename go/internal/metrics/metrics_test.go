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
}
