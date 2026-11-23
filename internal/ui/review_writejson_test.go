package ui_test

// Was behind the reviewbugs tag (failed at 5f0768e); the graph route now
// goes through writeJSON, so it runs in the default suite.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
)

// The other JSON routes go through writeJSON (issue #267): the body is
// encoded into memory first, so a response is never a 200 with a partial
// body, and it carries Content-Length. decisionGraph writes straight to the
// ResponseWriter, so a large document goes out chunked with no length.
func TestReviewBugDecisionGraphSkipsWriteJSON(t *testing.T) {
	db := openDB(t)
	for i := 0; i < 200; i++ {
		if _, err := decision.Add(db, decision.RecordInput{ID: fmt.Sprintf("d%03d", i), Statement: strings.Repeat("s", 80),
			Status: decision.StatusDecision}); err != nil {
			t.Fatal(err)
		}
	}
	srv := startUI(t, db)
	for _, path := range []string{"api/decisions", "api/decisions/graph"} {
		resp, err := http.Get(srv.URL() + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.Header.Get("Content-Length") == "" {
			t.Errorf("%s: no Content-Length (transfer %v); the route bypasses writeJSON", path, resp.TransferEncoding)
		}
	}
}
