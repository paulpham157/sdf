package ui_test

import (
	"regexp"
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

// Every node, whatever its state, and every ghost carries the pre-drawn
// keyboard ring, and only the :focus-visible rule strokes it.
func TestDecisionNodesCarryKeyboardRing(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	for _, hash := range []string{"#/decisions", "#/decisions/ch-capture/status-ui"} {
		res := renderPage(t, srv, hash, "")
		nodes := regexp.MustCompile(`<g [^>]*class="dg-node[ "]`).FindAllString(res.Markup, -1)
		rings := strings.Count(res.Markup, `class="dg-kb-ring"`)
		if len(nodes) == 0 || len(nodes) != rings {
			t.Errorf("%s: %d nodes, %d keyboard rings", hash, len(nodes), rings)
		}
		for _, state := range []string{"dg-on-path", "dg-role-locked_ending", "dg-role-hypothesis", "dg-kind-ending", "dg-kind-start"} {
			if !strings.Contains(res.Markup, state) {
				t.Errorf("%s: no node in state %s to check", hash, state)
			}
		}
		if hash != "#/decisions" && !strings.Contains(res.Markup, "Not taken: Warnings only") {
			t.Errorf("%s: no ghost drawn", hash)
		}
	}
	css := readAsset(t, "app.css")
	if !strings.Contains(css, ".dg-node:focus-visible .dg-kb-ring { stroke: var(--green); stroke-width: 2.5; }") {
		t.Error("app.css has no keyboard ring rule")
	}
	for _, m := range regexp.MustCompile(`(?m)^[^{}\n]*dg-kb-ring[^{}\n]*\{`).FindAllString(css, -1) {
		if m != ".dg-kb-ring {" && m != ".dg-node:focus-visible .dg-kb-ring {" {
			t.Errorf("another rule styles the keyboard ring: %s", m)
		}
	}
}
