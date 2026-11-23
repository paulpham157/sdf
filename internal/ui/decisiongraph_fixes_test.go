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

// One live region for the page, outside the drawn tree, kept across draws,
// cleared on navigation; focus survives each draw and returns to the node
// when its panel closes.
func TestDecisionsLiveRegionAndFocusKeep(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	live := func(res pageResult) string {
		if n := strings.Count(res.Outside, "aria-live"); n != 1 {
			t.Errorf("%d live regions outside the app root: %s", n, res.Outside)
		}
		if strings.Contains(res.Markup, "aria-live") || strings.Contains(res.Markup, `role="status"`) {
			t.Errorf("a live region inside the redrawn tree:\n%s", res.Markup)
		}
		return res.Outside
	}

	copied := renderPage(t, srv, "#/decisions/ch-capture/status-ui", `focus=~Copy command,click=~Copy command`)
	if out := live(copied); !strings.Contains(out, "Copy failed") {
		t.Errorf("copy status not announced: %s", out)
	}
	if !strings.Contains(copied.Active, `data-focus-key="copy:`) {
		t.Errorf("focus left the Copy button: %q", copied.Active)
	}
	moved := renderPage(t, srv, "#/decisions/ch-capture/status-ui", `focus=~Copy command,click=~Copy command,hash=#/decisions/ch-capture`)
	if out := live(moved); strings.Contains(out, "Copy failed") {
		t.Errorf("navigation kept the old status: %s", out)
	}

	for _, flags := range []string{"key=Escape", `click=[aria-label="Close"]`, "click=~← Chapter"} {
		res := renderPage(t, srv, "#/decisions/ch-capture/status-ui", flags)
		if res.Hash != "#/decisions/ch-capture" || !strings.Contains(res.Active, `data-node="status-ui"`) {
			t.Errorf("%s: hash %q, focus %q; want the chapter with focus on status-ui", flags, res.Hash, res.Active)
		}
	}

	toggled := renderPage(t, srv, "#/decisions", "focus=~Show tensions,click=~Show tensions")
	if !strings.Contains(toggled.Active, `data-focus-key="tb:Show tensions"`) || !strings.Contains(toggled.Markup, `aria-pressed="true" data-focus-key="tb:Show tensions"`) {
		t.Errorf("toggle lost focus: %q", toggled.Active)
	}

	missed := renderPage(t, srv, "#/decisions", "focus=.dg-jump,input=zzz,key=Enter")
	if !strings.Contains(missed.Active, `class="dg-jump"`) || !strings.Contains(missed.Active, `value="zzz"`) {
		t.Errorf("Jump lost focus or its value: %q", missed.Active)
	}
	if out := live(missed); !strings.Contains(out, "No node matches zzz.") || !strings.Contains(missed.Text, "No node matches zzz.") {
		t.Errorf("no-match status: %s", out)
	}
}
