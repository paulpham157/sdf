package ui_test

// Fixed in app.js (safeDecode, route() inside render's try/catch), so this
// review test now runs untagged.

import (
	"strings"
	"testing"
)

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
