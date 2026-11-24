package ui_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
)

// Regression tests from the PR #301 review, now run as normal tests.
// pageRunner's append and replaceChildren turn null, undefined, and strings
// into text nodes as the DOM does (null draws as "null"), so a stray null
// kid reaches the page text here as it did in Chrome.
//
// Returning the replay to now (End, the Now button, ] on the last step, or
// a finished replay) redraws the panel. At now dgReplayNote returns null,
// and the panel was filled with panel.replaceChildren.apply(panel, kids),
// which drew the text "null" above "Chapter · connected decisions". The
// panel is now filled through fill(), which drops null kids as h() does.
func TestReviewBugReplayBackToNowDrawsNull(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	for _, flags := range []string{
		`key=End@.dg-slider`,
		`click=.dg-now`,
		`key=]@.dg-slider`,
	} {
		res := renderPage(t, srv, "#/decisions/ch-capture?at=7", flags)
		if !strings.Contains(res.Markup, `aria-valuenow="8"`) {
			t.Fatalf("%s: not back at now:\n%s", flags, res.Markup)
		}
		if strings.Contains(res.Text, "nullChapter") {
			t.Errorf("%s: the summary panel starts with the text \"null\":\n%s", flags, res.Text)
		}
	}
	// The focus panel has the same null (dgFocusPanel's second kid).
	res := renderPage(t, srv, "#/decisions/ch-capture/capture?at=7", `key=End@.dg-slider`)
	if strings.Contains(res.Text, "×null") {
		t.Errorf("focus panel shows \"null\" after End:\n%s", res.Text)
	}
}

// The replay's heads() used to walk every supersedes path with no memo, so
// a diamond chain (two decisions supersede one, a third supersedes both)
// cost 2^levels per open-blocker check, on every step. dgHeadResolver is
// the server's linear headResolver; with 24 levels a step must stay about
// as fast as the live page.
func TestReviewBugReplayDiamondHeadsExponential(t *testing.T) {
	db := openDB(t)
	add := func(id, status string) {
		if _, err := decision.Add(db, decision.RecordInput{ID: id, Statement: id, Status: status, DecidedBy: "agent"}); err != nil {
			t.Fatal(err)
		}
	}
	link := func(rel, from, to string) {
		if _, err := decision.Link(db, decision.EdgeInput{Relation: rel,
			From: decision.Endpoint{Kind: decision.KindDecision, Ref: from},
			To:   decision.Endpoint{Kind: decision.KindDecision, Ref: to}}); err != nil {
			t.Fatal(err)
		}
	}
	const levels = 24
	add("t0", decision.StatusHypothesis)
	add("target", decision.StatusDecision)
	link("blocks", "t0", "target")
	for i := 0; i < levels; i++ {
		for _, side := range []string{"l", "r"} {
			add(fmt.Sprintf("%s%d", side, i), decision.StatusHypothesis)
			link("supersedes", fmt.Sprintf("%s%d", side, i), fmt.Sprintf("t%d", i))
		}
		add(fmt.Sprintf("t%d", i+1), decision.StatusHypothesis)
		for _, side := range []string{"l", "r"} {
			link("supersedes", fmt.Sprintf("t%d", i+1), fmt.Sprintf("%s%d", side, i))
		}
	}
	srv := startUI(t, db)
	start := time.Now()
	renderPage(t, srv, "#/decisions", "")
	now := time.Since(start)
	m := 3 + levels*7
	start = time.Now()
	renderPage(t, srv, fmt.Sprintf("#/decisions/ch-t0?at=%d", m-1), "")
	step := time.Since(start)
	if step > now+2*time.Second {
		t.Errorf("one replay step on a %d-level supersedes diamond took %v (the live page %v)", levels, step, now)
	}
}
