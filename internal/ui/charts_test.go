package ui_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/ui"
)

// Realistic ids: a Codex thread, a Grok CLI session, a Claude Code
// session.id, and full 40-character commit SHAs.
const (
	chartsCodexSession  = "019a8f3e-7c2b-7d41-9a55-3be1c0d2e4f6"
	chartsGrokSession   = "grok-7f3c2a9e-51d0-4b8e-a6c1-0d9e8f7a6b5c"
	chartsClaudeSession = "5d1c9b7e-2a4f-4e3b-8c6d-9f0a1b2c3d4e"
	chartsSHACodex      = "3f2a9c1e5b7d4a6c8e0f1a2b3c4d5e6f7a8b9c0d"
	chartsSHAGrok       = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	chartsSHANoSession  = "9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a291807"
	chartsSHAClaude     = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"
	chartsSHACodex2     = "5b6c7d8e9f0a1b2c3d4e5f60718293a4b5c6d7e8"
)

var (
	chartsDay1 = time.Date(2026, 11, 20, 14, 30, 0, 0, time.UTC)
	chartsDay2 = time.Date(2026, 11, 21, 9, 15, 0, 0, time.UTC)
	chartsDay3 = time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)
)

// stamp is the UTC form decision records and hook events use.
func stamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000-07:00")
}

func mustInsert(t *testing.T, db *store.Store, recs ...record.Record) {
	t.Helper()
	if _, err := db.Insert(recs); err != nil {
		t.Fatal(err)
	}
}

// hookCommit is the log the post-commit hook posts: sdf.git.commit with a
// full SHA and, when set, a session.id.
func hookCommit(service, agent, session, sha string, at time.Time) record.Record {
	attrs := map[string]any{
		"event.name":            "sdf.git.commit",
		"sdf.git.agent":         agent,
		"vcs.ref.head.revision": sha,
		"sdf.git.repo":          "/home/dev/sdf",
		"sdf.git.branch":        "feat/store",
		"sdf.git.hook":          "1",
	}
	rec := record.Record{
		Signal:       "logs",
		ServiceName:  record.Str(service),
		Name:         record.Str("sdf.git.commit"),
		TimeUnixNano: record.Int64(at.UnixNano()),
		Attributes:   attrs,
		Resource:     map[string]any{"service.name": service, "sdf.git.hook": "1"},
	}
	if session != "" {
		rec.SessionID = record.Str(session)
		attrs["session.id"] = session
	}
	return rec
}

func claudeCommit(session, sha string, at time.Time) record.Record {
	attrs := map[string]any{
		"event.name":        "tool_result",
		"tool_name":         "Bash",
		"success":           "true",
		"duration_ms":       "412",
		"sdf.git_commit_id": sha,
	}
	rec := record.Record{
		Signal:       "logs",
		ServiceName:  record.Str("claude-code"),
		Name:         record.Str("claude_code.tool_result"),
		TimeUnixNano: record.Int64(at.UnixNano()),
		Attributes:   attrs,
		Resource:     map[string]any{"service.name": "claude-code"},
	}
	if session != "" {
		rec.SessionID = record.Str(session)
		attrs["session.id"] = session
	}
	return rec
}

func addDecisionAt(t *testing.T, db *store.Store, id, status, statement string, at time.Time) {
	t.Helper()
	if err := db.InsertDecision(store.Decision{
		ID: id, Statement: statement, Status: status, RecordedAt: stamp(at),
	}); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, db *store.Store, relation, fromKind, fromRef, toKind, toRef string) {
	t.Helper()
	if _, err := decision.Link(db, decision.EdgeInput{
		Relation: relation,
		From:     decision.Endpoint{Kind: fromKind, Ref: fromRef},
		To:       decision.Endpoint{Kind: toKind, Ref: toRef},
	}); err != nil {
		t.Fatal(err)
	}
}

// seedChartStore is one store with audit-shaped commits and decisions
// spread across three UTC days (with a quiet day in between):
//
//   - 2026-11-20: a Codex hook commit whose session later implements
//     use-sqlite, and a Grok hook commit whose session has no edge
//     (unlinked commit). store-columns is recorded this day and stays
//     active with no implements edge (unlinked decision).
//   - 2026-11-21: a second Codex hook commit with its own implements
//     edge (linked by the audit rule), and a Claude Code tool_result
//     commit that implements pure-go.
//   - 2026-11-22: no commits (a quiet day in the series).
//   - 2026-11-23: a Claude Code tool_result commit with no session.
//
// use-sqlite and pure-go are implemented; store-json is superseded.
func seedChartStore(t *testing.T, db *store.Store) {
	t.Helper()
	mustInsert(t, db,
		hookCommit("codex_exec", "codex", chartsCodexSession, chartsSHACodex, chartsDay1),
		hookCommit("grok-cli", "grok-cli", chartsGrokSession, chartsSHAGrok, chartsDay1.Add(10*time.Minute)),
		hookCommit("codex_exec", "codex", chartsCodexSession, chartsSHACodex2, chartsDay2),
		claudeCommit(chartsClaudeSession, chartsSHAClaude, chartsDay2.Add(40*time.Minute)),
		claudeCommit("", chartsSHANoSession, chartsDay3.Add(20*time.Minute)),
	)
	addDecisionAt(t, db, "use-sqlite", "decision", "Keep the collector store in SQLite.", chartsDay1.Add(time.Hour))
	addDecisionAt(t, db, "pure-go", "decision", "The driver stays pure Go.", chartsDay1.Add(2*time.Hour))
	addDecisionAt(t, db, "store-json", "decision", "Store attributes as one JSON column.", chartsDay1.Add(3*time.Hour))
	addDecisionAt(t, db, "store-columns", "decision", "Store hot attributes in their own columns.", chartsDay1.Add(4*time.Hour))
	link(t, db, decision.RelImplements, decision.KindSession, chartsCodexSession, decision.KindDecision, "use-sqlite")
	link(t, db, decision.RelImplements, decision.KindCommit, chartsSHAClaude, decision.KindDecision, "pure-go")
	link(t, db, decision.RelImplements, decision.KindCommit, chartsSHACodex2, decision.KindDecision, "use-sqlite")
	link(t, db, decision.RelSupersedes, decision.KindDecision, "store-columns", decision.KindDecision, "store-json")
}

type chartsDoc struct {
	Schema string `json:"schema"`
	Totals struct {
		CommitsTotal                  int    `json:"commits_total"`
		CommitsWithImplements         int    `json:"commits_with_implements"`
		CommitsLinked                 int    `json:"commits_linked"`
		UnlinkedCommits               int    `json:"unlinked_commits"`
		UnlinkedDecisions             int    `json:"unlinked_decisions"`
		DecisionsActive               int    `json:"decisions_active"`
		DecisionsActiveWithImplements int    `json:"decisions_active_with_implements"`
		RecallStatus                  string `json:"recall_status"`
		Recall                        string `json:"recall"`
		RecallReason                  string `json:"recall_reason"`
	} `json:"totals"`
	RecallByDay []struct {
		Day                    string `json:"day"`
		CommitsTotal           int    `json:"commits_total"`
		CommitsWithImplements  int    `json:"commits_with_implements"`
		CommitsLinked          int    `json:"commits_linked"`
		RecallStatus           string `json:"recall_status"`
		Recall                 string `json:"recall"`
		CumulativeTotal        int    `json:"cumulative_total"`
		CumulativeImplements   int    `json:"cumulative_implements"`
		CumulativeRecallStatus string `json:"cumulative_recall_status"`
		CumulativeRecall       string `json:"cumulative_recall"`
	} `json:"recall_by_day"`
	UnlinkedByDay []struct {
		Day               string `json:"day"`
		UnlinkedCommits   int    `json:"unlinked_commits"`
		UnlinkedDecisions int    `json:"unlinked_decisions"`
	} `json:"unlinked_by_day"`
}

func getCharts(t *testing.T, srv *ui.Server) (int, http.Header, chartsDoc, string) {
	t.Helper()
	status, hdr, body := get(t, srv.URL()+"api/overview-charts")
	var doc chartsDoc
	if status == 200 {
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatalf("charts JSON: %v\n%s", err, body)
		}
	}
	return status, hdr, doc, body
}

func TestOverviewChartsEmptyStore(t *testing.T) {
	db := openDB(t)
	srv := startUI(t, db)
	status, hdr, doc, body := getCharts(t, srv)
	if status != 200 {
		t.Fatalf("GET /api/overview-charts: %d %s", status, body)
	}
	if !strings.HasPrefix(hdr.Get("Content-Type"), "application/json") {
		t.Fatalf("content-type %q", hdr.Get("Content-Type"))
	}
	if doc.Schema != "sdf.overview.charts/v1" {
		t.Fatalf("schema %q in %s", doc.Schema, body)
	}
	if doc.Totals.CommitsTotal != 0 || doc.Totals.CommitsWithImplements != 0 || doc.Totals.CommitsLinked != 0 ||
		doc.Totals.UnlinkedCommits != 0 || doc.Totals.UnlinkedDecisions != 0 || doc.Totals.DecisionsActive != 0 {
		t.Fatalf("empty totals %+v", doc.Totals)
	}
	if doc.Totals.RecallStatus != "unknown" || doc.Totals.Recall != "" || !strings.Contains(doc.Totals.RecallReason, "no commits") {
		t.Fatalf("empty recall %+v", doc.Totals)
	}
	if doc.RecallByDay == nil || len(doc.RecallByDay) != 0 {
		t.Fatalf("recall_by_day should be an empty list, got %#v", doc.RecallByDay)
	}
	if doc.UnlinkedByDay == nil || len(doc.UnlinkedByDay) != 0 {
		t.Fatalf("unlinked_by_day should be an empty list, got %#v", doc.UnlinkedByDay)
	}
	head, err := http.Head(srv.URL() + "api/overview-charts")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, head.Body)
	head.Body.Close()
	if head.StatusCode != 200 {
		t.Fatalf("HEAD %d", head.StatusCode)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req, err := http.NewRequest(method, srv.URL()+"api/overview-charts", strings.NewReader("no"))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s %d", method, resp.StatusCode)
		}
	}
}

func TestOverviewChartsRecallAndUnlinkedCounts(t *testing.T) {
	db := openDB(t)
	seedChartStore(t, db)
	srv := startUI(t, db)
	status, _, doc, body := getCharts(t, srv)
	if status != 200 {
		t.Fatalf("status %d %s", status, body)
	}
	if doc.Schema != "sdf.overview.charts/v1" {
		t.Fatalf("schema %q", doc.Schema)
	}

	// Five recorded commits: Codex (implements only via its session, not
	// from the commit), Grok (unlinked), Codex2 (implements from the
	// commit), no-session Claude, Claude with implements from the commit.
	// Commits with an implements edge from commit:SHA: Codex2 and Claude (2).
	// Audit-linked (session has a decision AND commit implements): Codex2
	// only — Claude has no session, Codex has a session decision but no
	// commit implements edge.
	// Unlinked session commits: Grok only.
	// Active decisions: use-sqlite (implemented), pure-go (implemented),
	// store-columns (no implements). store-json is superseded.
	if doc.Totals.CommitsTotal != 5 {
		t.Fatalf("commits_total %d, want 5\n%s", doc.Totals.CommitsTotal, body)
	}
	if doc.Totals.CommitsWithImplements != 2 {
		t.Fatalf("commits_with_implements %d, want 2\n%s", doc.Totals.CommitsWithImplements, body)
	}
	if doc.Totals.CommitsLinked != 1 {
		t.Fatalf("commits_linked %d, want 1 (audit rule)\n%s", doc.Totals.CommitsLinked, body)
	}
	if doc.Totals.UnlinkedCommits != 1 {
		t.Fatalf("unlinked_commits %d, want 1\n%s", doc.Totals.UnlinkedCommits, body)
	}
	if doc.Totals.UnlinkedDecisions != 1 {
		t.Fatalf("unlinked_decisions %d, want 1\n%s", doc.Totals.UnlinkedDecisions, body)
	}
	if doc.Totals.DecisionsActive != 3 || doc.Totals.DecisionsActiveWithImplements != 2 {
		t.Fatalf("active decisions %+v\n%s", doc.Totals, body)
	}
	if doc.Totals.RecallStatus != "known" || doc.Totals.Recall != "0.4" {
		t.Fatalf("recall %+v (2/5), want known 0.4", doc.Totals)
	}

	days := map[string]struct {
		total, implements, linked int
		cumTotal, cumImpl         int
		recall, cumRecall         string
	}{}
	for _, d := range doc.RecallByDay {
		days[d.Day] = struct {
			total, implements, linked int
			cumTotal, cumImpl         int
			recall, cumRecall         string
		}{d.CommitsTotal, d.CommitsWithImplements, d.CommitsLinked, d.CumulativeTotal, d.CumulativeImplements, d.Recall, d.CumulativeRecall}
	}
	// 20–23, with a quiet 22 between the last implements commit and the
	// no-session commit.
	d20 := days["2026-11-20"]
	// Two commits, neither has an implements edge from the commit.
	if d20.total != 2 || d20.implements != 0 || d20.linked != 0 || d20.cumTotal != 2 || d20.cumImpl != 0 || d20.recall != "0" || d20.cumRecall != "0" {
		t.Fatalf("2026-11-20 %+v", d20)
	}
	d21 := days["2026-11-21"]
	// Two commits, both with implements; Codex2 is audit-linked.
	// Cumulative so far is 2/4 = 0.5.
	if d21.total != 2 || d21.implements != 2 || d21.linked != 1 || d21.cumTotal != 4 || d21.cumImpl != 2 {
		t.Fatalf("2026-11-21 %+v", d21)
	}
	if d21.recall != "1" || d21.cumRecall != "0.5" {
		t.Fatalf("2026-11-21 recall daily %q cum %q, want 1 and 0.5", d21.recall, d21.cumRecall)
	}
	d22 := days["2026-11-22"]
	if d22.total != 0 || d22.implements != 0 || d22.cumTotal != 4 || d22.cumImpl != 2 || d22.cumRecall != "0.5" || d22.recall != "" {
		t.Fatalf("quiet 2026-11-22 %+v (daily recall should be omitted when there are no commits)", d22)
	}
	d23 := days["2026-11-23"]
	if d23.total != 1 || d23.implements != 0 || d23.linked != 0 || d23.cumTotal != 5 || d23.cumImpl != 2 || d23.cumRecall != "0.4" {
		t.Fatalf("2026-11-23 %+v", d23)
	}
	if len(doc.RecallByDay) != 4 {
		t.Fatalf("recall_by_day days %d, want 4 (20–23): %+v", len(doc.RecallByDay), doc.RecallByDay)
	}

	unlinked := map[string][2]int{}
	for _, d := range doc.UnlinkedByDay {
		unlinked[d.Day] = [2]int{d.UnlinkedCommits, d.UnlinkedDecisions}
	}
	if got := unlinked["2026-11-20"]; got != [2]int{1, 1} {
		t.Fatalf("unlinked 2026-11-20 %v, want [1 1] (Grok commit, store-columns)", got)
	}
	if got := unlinked["2026-11-21"]; got != [2]int{0, 0} {
		t.Fatalf("unlinked 2026-11-21 %v, want [0 0]", got)
	}
	if got := unlinked["2026-11-22"]; got != [2]int{0, 0} {
		t.Fatalf("unlinked 2026-11-22 %v, want [0 0]", got)
	}
	if got := unlinked["2026-11-23"]; got != [2]int{0, 0} {
		t.Fatalf("unlinked 2026-11-23 %v, want [0 0]", got)
	}
}

func TestSnapshotActivityByDay(t *testing.T) {
	db := openDB(t)
	// Two priced sessions on 20 Nov, one free session on 23 Nov, and one
	// unpriced session the same day as the free one so that day's cost is
	// unknown. A fifth session has no timestamp.
	mustInsert(t, db,
		record.Record{
			Signal: "logs", ServiceName: record.Str("claude-code"), SessionID: record.Str("priced-a"),
			Name: record.Str("claude_code.api_request"), TimeUnixNano: record.Int64(chartsDay1.UnixNano()),
			Attributes: map[string]any{
				"model": "anthropic/claude-haiku-4.5", "input_tokens": 1000000, "output_tokens": 0,
			},
			Resource: map[string]any{},
		},
		record.Record{
			Signal: "logs", ServiceName: record.Str("claude-code"), SessionID: record.Str("priced-b"),
			Name: record.Str("claude_code.api_request"), TimeUnixNano: record.Int64(chartsDay1.Add(2 * time.Hour).UnixNano()),
			Attributes: map[string]any{
				"model": "anthropic/claude-haiku-4.5", "input_tokens": 1000000, "output_tokens": 0,
			},
			Resource: map[string]any{},
		},
		record.Record{
			Signal: "traces", ServiceName: record.Str("pi"), SessionID: record.Str("free-pi"),
			Name: record.Str("chat flint"), StartUnixNano: record.Int64(chartsDay3.UnixNano()), EndUnixNano: record.Int64(chartsDay3.Add(time.Second).UnixNano()),
			Attributes: map[string]any{
				"gen_ai.request.model":       "recursive/flint-v1.0:free",
				"gen_ai.usage.input_tokens":  10,
				"gen_ai.usage.output_tokens": 4,
				"pi.cost.usd":                0,
			},
			Resource: map[string]any{},
		},
		record.Record{
			Signal: "logs", ServiceName: record.Str("claude-code"), SessionID: record.Str("unpriced"),
			Name: record.Str("claude_code.api_request"), TimeUnixNano: record.Int64(chartsDay3.Add(time.Hour).UnixNano()),
			Attributes: map[string]any{
				"model": "not-a-real-model", "input_tokens": 5, "output_tokens": 2,
			},
			Resource: map[string]any{},
		},
		record.Record{
			Signal: "logs", ServiceName: record.Str("claude-code"), SessionID: record.Str("no-time"),
			Name:       record.Str("claude_code.user_prompt"),
			Attributes: map[string]any{"prompt": "hello"},
			Resource:   map[string]any{},
		},
	)
	srv := startUI(t, db)
	raw := apiSnapshot(t, srv)
	var snap struct {
		ActivityByDay []struct {
			Day             string    `json:"day"`
			Sessions        int       `json:"sessions"`
			ComputedCostUSD ui.Amount `json:"computed_cost_usd"`
		} `json:"activity_by_day"`
		UndatedSessions int `json:"undated_sessions"`
	}
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.UndatedSessions != 1 {
		t.Fatalf("undated_sessions %d, want 1\n%s", snap.UndatedSessions, raw)
	}
	by := map[string]struct {
		n      int
		status string
		value  string
	}{}
	for _, d := range snap.ActivityByDay {
		by[d.Day] = struct {
			n      int
			status string
			value  string
		}{d.Sessions, d.ComputedCostUSD.Status, d.ComputedCostUSD.Value}
	}
	d20 := by["2026-11-20"]
	if d20.n != 2 || d20.status != "known" || d20.value == "" || d20.value == "0" {
		t.Fatalf("2026-11-20 activity %+v (two priced haiku sessions, cost should be known and non-zero)\n%s", d20, raw)
	}
	d21 := by["2026-11-21"]
	if d21.n != 0 || d21.status != "known" || d21.value != "0" {
		t.Fatalf("quiet 2026-11-21 %+v, want 0 sessions and known 0 cost", d21)
	}
	d22 := by["2026-11-22"]
	if d22.n != 0 || d22.status != "known" || d22.value != "0" {
		t.Fatalf("quiet 2026-11-22 %+v", d22)
	}
	d23 := by["2026-11-23"]
	if d23.n != 2 || d23.status != "known" || d23.value != "" {
		// one free (0) + one unknown => the day's cost is unknown, value omitted
		if d23.n != 2 || d23.status != "unknown" || d23.value != "" {
			t.Fatalf("2026-11-23 %+v, want 2 sessions and unknown cost\n%s", d23, raw)
		}
	}
	if len(snap.ActivityByDay) != 4 {
		t.Fatalf("activity_by_day len %d, want 4 (20–23): %+v", len(snap.ActivityByDay), snap.ActivityByDay)
	}
}

func TestOverviewChartsPageRendersTablesAndSvg(t *testing.T) {
	db := openDB(t)
	seedChartStore(t, db)
	mustInsert(t, db, record.Record{
		Signal: "logs", ServiceName: record.Str("claude-code"), SessionID: record.Str("priced-a"),
		Name: record.Str("claude_code.api_request"), TimeUnixNano: record.Int64(chartsDay1.UnixNano()),
		Attributes: map[string]any{
			"model": "anthropic/claude-haiku-4.5", "input_tokens": 1000000, "output_tokens": 0,
		},
		Resource: map[string]any{},
	})
	srv := startUI(t, db)
	page := renderPage(t, srv, "#/overview", "")
	for _, want := range []string{
		"Decision recall",
		"Unlinked commits",
		"Unlinked decisions",
		"Sessions and cost",
		"2026-11-20",
		"2026-11-21",
	} {
		if !strings.Contains(page.Text, want) && !strings.Contains(page.Markup, want) {
			t.Errorf("overview missing %q\ntext:\n%s\nmarkup:\n%s", want, page.Text, page.Markup)
		}
	}
	if !strings.Contains(page.Markup, "<svg") && !strings.Contains(page.Markup, "<svg ") {
		t.Fatalf("overview has no inline SVG chart\n%s", page.Markup)
	}
	if !strings.Contains(page.Markup, "<table") {
		t.Fatalf("overview charts have no data table fallback\n%s", page.Markup)
	}
	// 2 of 5 commits have an implements edge.
	if !strings.Contains(page.Text, "2 of 5") && !strings.Contains(page.Text, "40%") && !strings.Contains(page.Text, "0.4") {
		t.Fatalf("recall 2/5 not shown:\n%s", page.Text)
	}
}

func TestOverviewChartsEmptyPageOmitsChartSection(t *testing.T) {
	db := openDB(t)
	srv := startUI(t, db)
	page := renderPage(t, srv, "#/overview", "")
	if !strings.Contains(page.Text, "No telemetry sessions") {
		t.Fatalf("empty overview missing the init empty state:\n%s", page.Text)
	}
	if strings.Contains(page.Text, "Overview charts") || strings.Contains(page.Text, "Decision recall") {
		t.Fatalf("empty overview should omit the charts section:\n%s", page.Text)
	}
}

func TestOverviewChartsStaysAvailableDuringInsert(t *testing.T) {
	db := openDB(t)
	seedChartStore(t, db)
	srv := startUI(t, db)
	errc := make(chan error, 1)
	go func() {
		var recs []record.Record
		for i := 0; i < 200; i++ {
			recs = append(recs, record.Record{
				Signal: "logs", ServiceName: record.Str("claude-code"),
				SessionID:    record.Str(fmt.Sprintf("bulk-%d", i)),
				Name:         record.Str("claude_code.user_prompt"),
				TimeUnixNano: record.Int64(chartsDay3.UnixNano() + int64(i)),
				Attributes:   map[string]any{"prompt": "bulk"},
				Resource:     map[string]any{},
			})
		}
		_, err := db.Insert(recs)
		errc <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	got := 0
	for time.Now().Before(deadline) {
		status, _, _, body := getCharts(t, srv)
		if status != 200 {
			t.Fatalf("charts during insert: %d %s", status, body)
		}
		got++
		select {
		case err := <-errc:
			if err != nil {
				t.Fatal(err)
			}
			if got == 0 {
				t.Fatal("insert finished before a charts request completed")
			}
			return
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	t.Fatal("timed out waiting for insert")
}
