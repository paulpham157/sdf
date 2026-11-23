package ui_test

import (
	"strings"
	"testing"
)

// A malformed percent escape in the hash (decodeURIComponent throws on it)
// must not leave the page undrawn: a bad chapter or node falls back to the
// newest chapter's view, and a bad session id reads as an unknown session.
func TestPageSurvivesMalformedHash(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	for _, hash := range []string{"#/decisions/ch-capture/%E0%A4%A", "#/decisions/%zz"} {
		page := renderedPageAt(t, srv, hash)
		if !strings.Contains(page, "Chapters") || !strings.Contains(page, "feature-x") || !strings.Contains(page, "Explored") {
			t.Errorf("%s did not fall back to the chapter view:\n%s", hash, page)
		}
	}
	// A bad chapter with a good node focuses that node in the newest chapter.
	if page := renderedPageAt(t, srv, "#/decisions/%/capture"); !strings.Contains(page, "Capture live spans.") || !strings.Contains(page, "← Chapter") {
		t.Errorf("bad chapter with a good node:\n%s", page)
	}
	for _, hash := range []string{"#/sessions/%E0%A4%A", "#/sessions/%"} {
		page := renderedPageAt(t, srv, hash)
		if !strings.Contains(page, "No session with that id.") {
			t.Errorf("%s did not draw the unknown-session page:\n%s", hash, page)
		}
	}
	// A well-formed escape still decodes.
	if page := renderedPageAt(t, srv, "#/sessions/ses%2Da"); strings.Contains(page, "No session with that id.") {
		t.Errorf("escaped session id did not decode:\n%s", page)
	}
}
