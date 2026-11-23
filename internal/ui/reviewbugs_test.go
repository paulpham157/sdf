//go:build reviewbugs

package ui_test

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

// A malformed percent escape in the hash makes decodeURIComponent throw in
// route(), which render() calls outside its try/catch, so the Decisions
// route never draws. It should fall back to the chapter view or show an
// error like any other render failure.
func TestReviewBugMalformedDecisionsHash(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	page := renderedPageAt(t, srv, "#/decisions/ch-capture/%E0%A4%A")
	if !strings.Contains(page, "Chapters") && !strings.Contains(page, "could not") {
		t.Fatalf("malformed hash left the page undrawn:\n%s", page)
	}
}
