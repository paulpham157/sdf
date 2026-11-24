package ui_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

func seedGraph(t *testing.T, db *store.Store) {
	t.Helper()
	if _, err := db.Insert([]record.Record{{
		Signal: "logs", SessionID: record.Str("ses-a"), Name: record.Str("sdf.git.commit"),
		Attributes: map[string]any{"vcs.ref.head.revision": "0123456789abcdef0123456789abcdef01234567", "sdf.git.branch": "feature-x"},
		Resource:   map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}
	adds := []decision.RecordInput{
		{ID: "capture", Statement: "Capture live spans.", Status: decision.StatusDecision, DecidedBy: "user", Alternatives: "Parse the log"},
		{ID: "status-ui", Statement: "Show agent status in the UI.", Status: decision.StatusDecision, DecidedBy: "user",
			Alternatives: "Warnings only; A check command", Reason: "Users cannot tell when an agent stopped."},
		{ID: "merge-first", Statement: "Merge main first.", Status: decision.StatusHypothesis, DecidedBy: "agent"},
		{ID: "silent", Statement: "Hooks fail silently.", Status: decision.StatusDecision, DecidedBy: "agent"},
	}
	for _, in := range adds {
		if _, err := decision.Add(db, in); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range [][3]string{
		{"implements", "commit:0123456789abcdef0123456789abcdef01234567", "decision:capture"},
		{"constrains", "decision:capture", "decision:status-ui"},
		{"blocks", "decision:merge-first", "decision:status-ui"},
		{"conflicts_with", "decision:status-ui", "decision:silent"},
	} {
		kind := func(s string) decision.Endpoint {
			k, ref, _ := strings.Cut(s, ":")
			return decision.Endpoint{Kind: k, Ref: ref}
		}
		if _, err := decision.Link(db, decision.EdgeInput{Relation: l[0], From: kind(l[1]), To: kind(l[2])}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDecisionGraphRoute(t *testing.T) {
	db := openDB(t)
	srv := startUI(t, db)
	status, header, body := get(t, srv.URL()+"api/decisions/graph")
	if status != 200 || !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
		t.Fatalf("status %d %s", status, body)
	}
	if !strings.Contains(body, "\"chapters\": []") {
		t.Fatalf("empty graph %s", body)
	}
	seedGraph(t, db)
	_, _, body = get(t, srv.URL()+"api/decisions/graph")
	var doc struct {
		Chapters []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			LabelSource string `json:"label_source"`
			Facts       struct {
				EndingsReached int `json:"endings_reached"`
				EndingsTotal   int `json:"endings_total"`
				Locked         int `json:"locked"`
				Conflicts      int `json:"conflicts"`
			} `json:"facts"`
			Explored struct {
				Decided int  `json:"decided"`
				Total   int  `json:"total"`
				Percent *int `json:"percent"`
			} `json:"explored"`
			Attention []struct {
				Kind string `json:"kind"`
			} `json:"needs_attention"`
			Nodes []struct {
				ID      string `json:"id"`
				Kind    string `json:"kind"`
				Role    string `json:"role"`
				Command string `json:"command"`
			} `json:"nodes"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Chapters) != 1 {
		t.Fatalf("%s", body)
	}
	c := doc.Chapters[0]
	if c.ID != "ch-capture" || c.Title != "feature-x" || c.LabelSource != "branch" {
		t.Fatalf("chapter %q %q %q", c.ID, c.Title, c.LabelSource)
	}
	// capture, status-ui (locked), merge-first, silent, 3 alternatives:
	// decided capture + silent = 2 of 2 + 1 + 1 + 3 = 7.
	if c.Facts.Locked != 1 || c.Facts.Conflicts != 1 || c.Facts.EndingsReached != 1 || c.Facts.EndingsTotal != 2 ||
		c.Explored.Decided != 2 || c.Explored.Total != 7 || c.Explored.Percent == nil || *c.Explored.Percent != 29 {
		t.Fatalf("facts %+v explored %+v", c.Facts, c.Explored)
	}
	if len(c.Attention) != 2 || c.Attention[0].Kind != "conflict" || c.Attention[1].Kind != "locked" {
		t.Fatalf("attention %+v", c.Attention)
	}
	var commands int
	for _, n := range c.Nodes {
		if n.Kind == "alternative" && strings.HasPrefix(n.Command, "sdf decision add --id ") {
			commands++
		}
	}
	if commands != 3 {
		t.Fatalf("%d what-if commands: %s", commands, body)
	}
	if post, _, posted := getPost(t, srv.URL()+"api/decisions/graph"); post != 405 && post != 404 {
		t.Fatalf("write route answered %d %s", post, posted)
	}
}

func TestPageRendersDecisionGraph(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)

	page := renderedPageAt(t, srv, "#/decisions")
	for _, want := range []string{
		"Decisions", "Chapters", "feature-x", "Chapter start", "capture", "status-ui",
		"1 of 2 endings reached", "Needs attention", "Conflict:", "status-ui waits on merge-first",
		"Explored", "2 of 7 choice points decided", "29%", "Show tensions", "Not taken",
		"Locked ending", "needs merge-first", "Parse the log",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("#/decisions is missing %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "Copy command") {
		t.Errorf("chapter view shows what-if commands before a decision is focused:\n%s", page)
	}

	focused := renderedPageAt(t, srv, "#/decisions/ch-capture/status-ui")
	for _, want := range []string{
		"← Chapter", "Show agent status in the UI.", "Users cannot tell when an agent stopped.",
		"Blocked by", "merge-first", "Conflicts with", "silent", "Constrained by",
		"Warnings only", "A check command", "Copy command",
		"sdf decision add --id status-ui-alt-1 --status hypothesis --statement 'Warnings only'",
	} {
		if !strings.Contains(focused, want) {
			t.Errorf("focused decision is missing %q:\n%s", want, focused)
		}
	}

	// A decision with no reason says so instead of leaving a gap.
	if p := renderedPageAt(t, srv, "#/decisions/ch-capture/silent"); !strings.Contains(p, "none recorded") {
		t.Errorf("no-reason decision:\n%s", p)
	}
	// An unknown chapter falls back to the newest one.
	if p := renderedPageAt(t, srv, "#/decisions/ch-missing"); !strings.Contains(p, "feature-x") {
		t.Errorf("unknown chapter:\n%s", p)
	}
}

func TestDecisionsTabAbsentWithoutDecisions(t *testing.T) {
	srv := startUI(t, openDB(t))
	if page := renderedPage(t, srv); strings.Contains(page, "Decisions") {
		t.Fatalf("empty store shows a Decisions tab:\n%s", page)
	}
	// Opening the route directly still explains the empty state.
	if page := renderedPageAt(t, srv, "#/decisions"); !strings.Contains(page, "sdf decision add") {
		t.Fatalf("empty Decisions tab:\n%s", page)
	}
}

// The replay scrubber: an ARIA slider over the chapter's events, and the
// graph and panel as of ?at=N with blocked, conflicted, and needs-attention
// recomputed from the events recorded so far.
func TestPageRendersDecisionReplay(t *testing.T) {
	// The label is in the browser's zone (the page runner's TZ), with the
	// zone's label, as the panel's times are; never a fixed zone.
	t.Setenv("TZ", "Asia/Ho_Chi_Minh")
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	zone := regexp.MustCompile(` GMT\+7\b`)

	// seedGraph records 4 decisions, then 4 edges: implements, constrains,
	// blocks, conflicts_with.
	now := renderPage(t, srv, "#/decisions", "")
	for _, want := range []string{`role="slider"`, `aria-valuemin="1"`, `aria-valuemax="8"`, `aria-valuenow="8"`, `aria-label="Replay position"`, "8 of 8", "▶ Replay"} {
		if !strings.Contains(now.Markup, want) {
			t.Errorf("replay at now is missing %q:\n%s", want, now.Markup)
		}
	}
	if !zone.MatchString(now.Text) {
		t.Errorf("replay label has no browser zone (GMT+7):\n%s", now.Text)
	}
	if regexp.MustCompile(` (EST|EDT)\b`).MatchString(now.Text) {
		t.Errorf("replay label is in New York time, not the browser's zone:\n%s", now.Text)
	}
	if strings.Contains(now.Text, "As of ") {
		t.Errorf("replay at now should show the live chapter:\n%s", now.Text)
	}

	// After the four decisions, before any edge: nothing is locked or in
	// conflict yet, and no ending exists.
	early := renderPage(t, srv, "#/decisions/ch-capture?at=4", "")
	for _, want := range []string{`aria-valuenow="4"`, "4 of 8", "As of ", "0 of 0 endings reached", "status-ui"} {
		if !strings.Contains(early.Markup, want) && !strings.Contains(early.Text, want) {
			t.Errorf("replay at 4 is missing %q:\n%s", want, early.Text)
		}
	}
	for _, unwanted := range []string{"waits on", "Conflict:", "Locked ending", "needs merge-first"} {
		if strings.Contains(early.Text, unwanted) {
			t.Errorf("replay at 4 shows %q before its edge is recorded:\n%s", unwanted, early.Text)
		}
	}
	if !zone.MatchString(early.Text) {
		t.Errorf("replay at 4 has no browser zone (GMT+7):\n%s", early.Text)
	}

	// After blocks (event 7), before conflicts_with: locked, not conflicted.
	locked := renderPage(t, srv, "#/decisions/ch-capture?at=7", "")
	for _, want := range []string{"7 of 8", "status-ui waits on merge-first", "Locked ending", "1 of 2 endings reached"} {
		if !strings.Contains(locked.Text, want) {
			t.Errorf("replay at 7 is missing %q:\n%s", want, locked.Text)
		}
	}
	if strings.Contains(locked.Text, "Conflict:") {
		t.Errorf("replay at 7 shows a conflict recorded at 8:\n%s", locked.Text)
	}

	// At 2, merge-first and silent are not recorded yet: outlines only,
	// hidden from assistive tech, with no text.
	two := renderPage(t, srv, "#/decisions/ch-capture?at=2", "")
	tag := regexp.MustCompile(`<g [^>]*data-node="silent"[^>]*>`).FindString(two.Markup)
	if !strings.Contains(tag, "dg-future") || !strings.Contains(tag, `aria-hidden="true"`) || strings.Contains(tag, "tabindex") {
		t.Errorf("future node at 2: %q", tag)
	}
	if strings.Contains(two.Text, "Hooks fail silently.") {
		t.Errorf("future node text drawn at 2:\n%s", two.Text)
	}

	// Out of range clamps: past the end is now, 0 is the first event.
	if p := renderPage(t, srv, "#/decisions/ch-capture?at=99", ""); strings.Contains(p.Text, "As of ") || !strings.Contains(p.Markup, `aria-valuenow="8"`) {
		t.Errorf("at=99 should be now:\n%s", p.Text)
	}
	if p := renderPage(t, srv, "#/decisions/ch-capture?at=0", ""); !strings.Contains(p.Markup, `aria-valuenow="1"`) {
		t.Errorf("at=0 should clamp to 1:\n%s", p.Markup)
	}
}

// The replay code keeps its contract in the asset: the browser's zone (no
// fixed timeZone), reduced motion, the step keys, and opacity-only
// animation.
func TestReplayAssetContract(t *testing.T) {
	script := readAsset(t, "app.js")
	if strings.Contains(script, "America/New_York") || strings.Contains(script, "timeZone:") {
		t.Error("app.js pins a time zone; the replay uses the browser's")
	}
	for _, want := range []string{`prefers-reduced-motion: reduce`, `case "[":`, `case "]":`, `"Home"`, `"End"`, `"PageUp"`, `"PageDown"`, `role: "slider"`, `"aria-valuetext"`} {
		if !strings.Contains(script, want) {
			t.Errorf("app.js is missing %q", want)
		}
	}
	css := readAsset(t, "app.css")
	for _, want := range []string{".dg-slider:focus-visible", "@keyframes dg-fade-in", ".dg-enter { animation: none; }"} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css is missing %q", want)
		}
	}
	if regexp.MustCompile(`@keyframes dg-fade-in \{[^}]*(transform|left|top)`).MatchString(css) {
		t.Error("replay animation must change opacity only")
	}
}

// The replay shares the tab's keys and focus rules: [ and ] step from a
// node and focus stays on it, / still reaches Jump from the slider, the
// keys are text inside Jump, and the List view omits nodes not recorded
// yet. Its status goes to the one live region outside the drawn tree.
func TestReplayKeepsFocusAndSharesKeys(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	stepped := renderPage(t, srv, "#/decisions/ch-capture/capture", `key=[@[data-node="capture"]`)
	if !strings.Contains(stepped.Markup, `aria-valuenow="7"`) {
		t.Errorf("[ did not step back:\n%s", stepped.Markup)
	}
	if !strings.Contains(stepped.Active, `data-node="capture"`) || !strings.Contains(stepped.Active, `tabindex="0"`) {
		t.Errorf("[ from a node lost focus: %q", stepped.Active)
	}
	if n := strings.Count(stepped.Outside, "aria-live"); n != 1 || strings.Contains(stepped.Markup, "aria-live") {
		t.Errorf("live regions: %d outside; inside: %v", n, strings.Contains(stepped.Markup, "aria-live"))
	}
	if res := renderPage(t, srv, "#/decisions", "key=/@.dg-slider"); !strings.Contains(res.Active, `class="dg-jump"`) {
		t.Errorf("/ from the slider: focus %q", res.Active)
	}
	if res := renderPage(t, srv, "#/decisions", "focus=.dg-jump,key=[,key=["); !strings.Contains(res.Markup, `aria-valuenow="8"`) {
		t.Error("[ typed into Jump stepped the replay")
	}
	list := renderPage(t, srv, "#/decisions/ch-capture?at=2", "click=~List")
	if strings.Contains(list.Text, "Hooks fail silently.") || strings.Contains(list.Text, "Merge main first.") || !strings.Contains(list.Text, "Capture live spans.") {
		t.Errorf("List view at 2 lists nodes not recorded yet:\n%s", list.Text)
	}
}
