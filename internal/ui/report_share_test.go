package ui_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/paulpham157/sdf/internal/ui"
)

// Requests that arrive while a report is being built share it: one build
// (one round of git and GitHub lookups) for the page's parallel requests.
func TestConcurrentRequestsShareOneReportBuild(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	builds, sessions, err := ui.SharedReportForTest(db, 4)
	if err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Fatalf("%d builds for 4 overlapping requests, want 1", builds)
	}
	for _, n := range sessions {
		if n != sessions[0] || n == 0 {
			t.Fatalf("callers saw %v sessions, want the same non-zero count", sessions)
		}
	}
}

// The graph route reads the report without the budget, as sdf decision
// graph does: a budget file that does not parse breaks /api/summary, not
// the Decisions tab.
func TestDecisionGraphRouteIgnoresTheBudget(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	bad := filepath.Join(t.TempDir(), "budget.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := startUIWithBudget(t, db, bad)
	if status, _, _ := get(t, srv.URL()+"api/summary"); status != http.StatusInternalServerError {
		t.Fatalf("summary with a bad budget: %d, want 500", status)
	}
	status, header, body := get(t, srv.URL()+"api/decisions/graph")
	if status != http.StatusOK || !strings.Contains(body, `"chapters"`) || header.Get("Content-Length") == "" {
		t.Fatalf("graph: %d %v\n%s", status, header, body)
	}
}

// The page fetches the summary, the snapshot, the decisions, and the
// graph in parallel. Run that many times at once; -race checks that the
// shared report is only read.
func TestParallelPageRequestsAreRaceFree(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUIWithBudget(t, db, writeBudget(t, "2"))
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for round := 0; round < 8; round++ {
		for _, path := range []string{"api/summary", "api/snapshot", "api/decisions", "api/decisions/graph"} {
			wg.Add(1)
			go func(path string) {
				defer wg.Done()
				resp, err := http.Get(srv.URL() + path)
				if err != nil {
					errs <- err.Error()
					return
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					errs <- path + ": " + resp.Status
				}
			}(path)
		}
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}
