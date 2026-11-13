package ui_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/outcome"
	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/summary"
	"github.com/paulpham157/sdf/internal/ui"
)

func TestHookAndTrailerCountAsLinked(t *testing.T) {
	report := summary.Report{Sessions: []summary.SessionSummary{
		{Tool: "codex", SessionID: "hooked", Outcome: &summary.SessionOutcome{Status: "linked", Match: "hook", Reason: "git hook"}},
		{Tool: "codex", SessionID: "trailed", Outcome: &summary.SessionOutcome{Status: "linked", Match: "trailer", Reason: "SDF-Session"}},
		{Tool: "pi", SessionID: "exact", Outcome: &summary.SessionOutcome{Status: "linked", Match: "exact", Reason: "exported sha"}},
		{Tool: "pi", SessionID: "guessed", Outcome: &summary.SessionOutcome{Status: "linked", Match: "heuristic", Reason: "one commit in the window"}},
		{Tool: "opencode", SessionID: "open", Outcome: &summary.SessionOutcome{Status: "unlinked", Reason: "no commit"}},
		{Tool: "opencode", SessionID: "odd", Outcome: &summary.SessionOutcome{Status: "linked", Match: "guess", Reason: "not a link method"}},
	}}
	snap := ui.BuildSnapshot(report, ui.SnapshotMeta{})
	byID := map[string]string{}
	for _, view := range snap.Sessions {
		byID[view.Summary.SessionID] = view.LinkMethod
		if view.LinkMethod == "none" && view.LinkReason == "" {
			t.Fatalf("missing reason for %s", view.Summary.SessionID)
		}
	}
	if byID["hooked"] != "hook" || byID["trailed"] != "trailer" || byID["exact"] != "exact" || byID["guessed"] != "heuristic" || byID["open"] != "none" || byID["odd"] != "none" {
		t.Fatalf("%v", byID)
	}
	if snap.Totals.Linked != 4 || snap.Totals.Unlinked != 2 || snap.Totals.LinkedExact != 1 || snap.Totals.LinkedHook != 1 || snap.Totals.LinkedTrailer != 1 || snap.Totals.LinkedHeuristic != 1 {
		t.Fatalf("%+v", snap.Totals)
	}
	if len(snap.Agents) != 3 {
		t.Fatalf("%d agents", len(snap.Agents))
	}
	for _, agent := range snap.Agents {
		switch agent.Tool {
		case "codex":
			if agent.Linked != 2 || agent.LinkedHook != 1 || agent.LinkedTrailer != 1 || agent.LinkedExact != 0 || agent.LinkedHeuristic != 0 {
				t.Fatalf("codex %+v", agent)
			}
		case "pi":
			if agent.Linked != 2 || agent.LinkedExact != 1 || agent.LinkedHeuristic != 1 || agent.LinkedHook != 0 || agent.LinkedTrailer != 0 {
				t.Fatalf("pi %+v", agent)
			}
		case "opencode":
			if agent.Linked != 0 || agent.Unlinked != 2 {
				t.Fatalf("opencode %+v", agent)
			}
		default:
			t.Fatalf("tool %s", agent.Tool)
		}
	}
	script := readAsset(t, "app.js")
	if !strings.Contains(script, `const LINK_METHODS = ["exact", "hook", "trailer", "heuristic"];`) {
		t.Fatal("app.js link order")
	}
	if strings.Count(script, "linkName(view.link_method)") != 2 {
		t.Fatal("link cell and detail should both use linkName")
	}
	if strings.Contains(script, `view.link_method === "exact"`) || !strings.Contains(script, `totals.linked_hook`) || !strings.Contains(script, `totals.linked_trailer`) {
		t.Fatal("app.js still drops hook or trailer")
	}
	hookAt := strings.Index(script, `totals.linked_hook`)
	trailerAt := strings.Index(script, `totals.linked_trailer`)
	heuristicAt := strings.Index(script, `totals.linked_heuristic`)
	if hookAt < 0 || trailerAt < hookAt || heuristicAt < trailerAt {
		t.Fatal("link phrase order")
	}
	if !strings.Contains(script, `function pullState`) || !strings.Contains(script, "closed without merging") || !strings.Contains(script, "outcome.accepted_reason") {
		t.Fatal("app.js should show a closed unmerged pull request")
	}
}

func TestEmptyDatabaseMatchesSummaryAndPointsAtInit(t *testing.T) {
	db := openDB(t)
	srv := startUI(t, db)
	status, hdr, body := get(t, srv.URL())
	if status != 200 || !strings.Contains(body, "/app.js") || strings.Contains(body, "http://") || strings.Contains(body, "https://") {
		t.Fatalf("page %d %q", status, body)
	}
	if hdr.Get("Content-Security-Policy") == "" || hdr.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("headers %#v", hdr)
	}
	summaryBody := apiSummary(t, srv)
	want := summaryJSON(t, db)
	if summaryBody != want {
		t.Fatalf("api\n%s\nsummary\n%s", summaryBody, want)
	}
	snap := decodeSnapshot(t, apiSnapshot(t, srv))
	if len(snap.Sessions) != 0 || snap.CostPerAccepted.Status != "unknown" || snap.CostPerAccepted.USD != "" || snap.CostPerAccepted.Reason != "no sessions" {
		t.Fatalf("%+v", snap.CostPerAccepted)
	}
	if snap.Totals.ComputedCostUSD.Status != "unknown" || snap.Totals.ComputedCostUSD.Value != "" {
		t.Fatalf("%+v", snap.Totals.ComputedCostUSD)
	}
	if snap.Totals.AgentReportedCostUSD.Value != "" || snap.Totals.InputTokens.Value != "" {
		t.Fatalf("%+v", snap.Totals)
	}
	script := readAsset(t, "app.js")
	if !strings.Contains(script, "sdf init") || strings.Contains(script, "http://") || strings.Contains(script, "https://") {
		t.Fatal("page script should point at sdf init and stay offline")
	}
}

func TestAPINumbersMatchSummaryAndUnknownIsNotZero(t *testing.T) {
	db := openDB(t)
	if _, err := db.Insert([]record.Record{
		{
			Signal: "logs", ServiceName: record.Str("claude-code"), SessionID: record.Str("missing-price"),
			Name: record.Str("claude_code.api_request"), TimeUnixNano: record.Int64(1_000_000_000),
			Attributes: map[string]any{
				"model": "not-a-real-model", "input_tokens": 5, "output_tokens": 2, "cost_usd": 1.5,
			},
			Resource: map[string]any{},
		},
		{
			Signal: "logs", ServiceName: record.Str("claude-code"), SessionID: record.Str("two-models"),
			Name: record.Str("claude_code.api_request"), TimeUnixNano: record.Int64(3_000_000_000),
			Attributes: map[string]any{"model": "vendor/a", "input_tokens": 1, "output_tokens": 1},
			Resource:   map[string]any{},
		},
		{
			Signal: "logs", ServiceName: record.Str("claude-code"), SessionID: record.Str("two-models"),
			Name: record.Str("claude_code.user_prompt"), TimeUnixNano: record.Int64(3_100_000_000),
			Attributes: map[string]any{"model": "vendor/b"},
			Resource:   map[string]any{},
		},
		{
			Signal: "traces", ServiceName: record.Str("pi"), SessionID: record.Str("free-pi"),
			Name: record.Str("chat flint"), StartUnixNano: record.Int64(5_000_000_000), EndUnixNano: record.Int64(6_000_000_000),
			Attributes: map[string]any{
				"gen_ai.request.model":       "recursive/flint-v1.0:free",
				"gen_ai.usage.input_tokens":  10,
				"gen_ai.usage.output_tokens": 4,
				"pi.cost.usd":                0,
			},
			Resource: map[string]any{},
		},
		{
			Signal: "traces", ServiceName: record.Str("pi"), SessionID: record.Str("free-pi"),
			Name: record.Str("execute_tool bash"), StartUnixNano: record.Int64(5_100_000_000), EndUnixNano: record.Int64(5_200_000_000),
			Attributes: map[string]any{"gen_ai.tool.name": "bash", "gen_ai.tool.call.id": "call_1", "pi.tool.is_error": false},
			Resource:   map[string]any{},
		},
	}); err != nil {
		t.Fatal(err)
	}
	srv := startUI(t, db)
	if got, want := apiSummary(t, srv), summaryJSON(t, db); got != want {
		t.Fatalf("api\n%s\nsummary\n%s", got, want)
	}
	snap := decodeSnapshot(t, apiSnapshot(t, srv))
	if len(snap.Sessions) != 3 {
		t.Fatalf("sessions %d", len(snap.Sessions))
	}
	if snap.Sessions[0].Summary.SessionID != "free-pi" || snap.Sessions[1].Summary.SessionID != "two-models" || snap.Sessions[2].Summary.SessionID != "missing-price" {
		t.Fatalf("order %s %s %s", snap.Sessions[0].Summary.SessionID, snap.Sessions[1].Summary.SessionID, snap.Sessions[2].Summary.SessionID)
	}
	byID := map[string]sessionView{}
	for _, view := range snap.Sessions {
		byID[view.Summary.SessionID] = view
		raw, err := json.Marshal(view.ComputedCostUSD)
		if err != nil {
			t.Fatal(err)
		}
		if view.ComputedCostUSD.Status != "known" && (view.ComputedCostUSD.Value != "" || bytes.Contains(raw, []byte(`"value"`))) {
			t.Fatalf("%s unknown cost rendered as a number: %s", view.Summary.SessionID, raw)
		}
	}
	free := byID["free-pi"]
	if free.ComputedCostUSD.Status != "known" || free.ComputedCostUSD.Value != "0" {
		t.Fatalf("free computed %+v raw %+v", free.ComputedCostUSD, free.Summary.ComputedCost)
	}
	if free.AgentReportedUSD.Status != "known" || free.AgentReportedUSD.Value != "0" {
		t.Fatalf("free agent %+v", free.AgentReportedUSD)
	}
	if free.InputTokens.Value != "10" || free.OutputTokens.Value != "4" || free.InputTokens.Detail != "input_tokens" {
		t.Fatalf("tokens %+v %+v", free.InputTokens, free.OutputTokens)
	}
	if free.ModelBreakdown.Status != "known" || free.ToolCallsStatus != "reported" || len(free.ToolCounts) != 1 || free.ToolCounts[0].Count != "1" {
		t.Fatalf("model %+v tools %+v %s", free.ModelBreakdown, free.ToolCounts, free.ToolCallsStatus)
	}
	if free.LinkMethod != "none" || !strings.Contains(free.LinkReason, "no working directory") {
		t.Fatalf("link %s %s", free.LinkMethod, free.LinkReason)
	}
	missing := byID["missing-price"]
	if missing.ComputedCostUSD.Status != "unknown" || missing.ComputedCostUSD.Value != "" || missing.Summary.ComputedCost == nil || missing.Summary.ComputedCost.USD != "" {
		t.Fatalf("missing price %+v summary %+v", missing.ComputedCostUSD, missing.Summary.ComputedCost)
	}
	if missing.AgentReportedUSD.Status != "known" || missing.AgentReportedUSD.Value != summary.FormatNumber(1.5) {
		t.Fatalf("agent %s", missing.AgentReportedUSD.Value)
	}
	if missing.Duration.Status != "unknown" || missing.Duration.Value != "" {
		t.Fatalf("duration %+v", missing.Duration)
	}
	two := byID["two-models"]
	if two.ModelBreakdown.Status != "unknown" || two.ModelBreakdown.Tokens != nil || !strings.Contains(two.ModelBreakdown.Reason, "does not split") {
		t.Fatalf("%+v", two.ModelBreakdown)
	}
	if snap.Totals.ComputedCostUSD.Status != "unknown" || snap.Totals.ComputedCostUSD.Value != "" {
		t.Fatalf("total computed %+v", snap.Totals.ComputedCostUSD)
	}
	var piRollup *agentRollup
	for i := range snap.Agents {
		if snap.Agents[i].Tool == "pi" {
			piRollup = &snap.Agents[i]
		}
		got := snap.Agents[i].CostPerAccepted
		want := outcome.CostPerAccepted(sessionsFor(snap, snap.Agents[i].Tool))
		if got != want {
			t.Fatalf("%s cost %+v want %+v", snap.Agents[i].Tool, got, want)
		}
	}
	if piRollup == nil || piRollup.ComputedCostUSD.Status != "known" || piRollup.ComputedCostUSD.Value != "0" {
		t.Fatalf("pi rollup %+v", piRollup)
	}
	if snap.CostPerAccepted != outcome.CostPerAccepted(allSessions(snap)) {
		t.Fatalf("headline %+v", snap.CostPerAccepted)
	}
}

func TestPerAgentAcceptedCostUsesTheOutcomeRule(t *testing.T) {
	yes, no := true, false
	report := summary.Report{Sessions: []summary.SessionSummary{
		{
			SessionID: "merged", Tool: "pi",
			ComputedCost: &pricing.Cost{Status: "known", USD: "2"},
			Outcome:      &summary.SessionOutcome{Status: "linked", Match: "exact", Accepted: &yes, Commits: []summary.LinkedCommit{{SHA: strings.Repeat("ab", 20)}}},
		},
		{
			SessionID: "open", Tool: "pi",
			ComputedCost: &pricing.Cost{Status: "known", USD: "4"},
			Outcome:      &summary.SessionOutcome{Status: "linked", Match: "heuristic", Accepted: &no},
		},
		{
			SessionID: "codex", Tool: "codex",
			ComputedCost: &pricing.Cost{Status: "unknown", Reason: "no price"},
			Outcome:      &summary.SessionOutcome{Status: "unlinked", Reason: "no working directory attribute and no --repo"},
		},
	}}
	rolled := outcome.CostPerAccepted(report.Sessions)
	report.CostPerAccepted = &rolled
	snap := ui.BuildSnapshot(report, ui.SnapshotMeta{})
	if snap.CostPerAccepted != rolled || snap.CostPerAccepted.Status != "unknown" || snap.CostPerAccepted.USD != "" {
		t.Fatalf("%+v", snap.CostPerAccepted)
	}
	if snap.Totals.ComputedCostUSD.Status != "unknown" || snap.Totals.ComputedCostUSD.Value != "" {
		t.Fatalf("%+v", snap.Totals.ComputedCostUSD)
	}
	var pi ui.AgentRollup
	for _, agent := range snap.Agents {
		if agent.Tool == "pi" {
			pi = agent
		}
	}
	want := outcome.CostPerAccepted(report.Sessions[:2])
	if pi.CostPerAccepted != want || pi.CostPerAccepted.Status != "known" || pi.CostPerAccepted.USD != "2" {
		t.Fatalf("%+v want %+v", pi.CostPerAccepted, want)
	}
	if pi.ComputedCostUSD.Status != "known" || pi.ComputedCostUSD.Value != "6" || pi.Merged != 1 || pi.LinkedExact != 1 || pi.LinkedHeuristic != 1 {
		t.Fatalf("%+v", pi)
	}
}

func TestPageRendersDecisionsAndBudget(t *testing.T) {
	db := openDB(t)
	if _, err := db.Insert([]record.Record{{
		Signal: "traces", ServiceName: record.Str("pi"), SessionID: record.Str("sess-1"),
		Name: record.Str("chat flint"), StartUnixNano: record.Int64(5_000_000_000), EndUnixNano: record.Int64(6_000_000_000),
		Attributes: map[string]any{
			"gen_ai.request.model":       "recursive/flint-v1.0:free",
			"gen_ai.usage.input_tokens":  10,
			"gen_ai.usage.output_tokens": 4,
			"pi.cost.usd":                0,
		},
		Resource: map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := decision.Add(db, decision.RecordInput{
		ID: "keep-sqlite", Statement: "Keep the collector store in SQLite.", Status: decision.StatusDecision,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := decision.Add(db, decision.RecordInput{
		ID: "pure-go", Statement: "The driver stays pure Go.", Status: decision.StatusHypothesis,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := decision.Link(db, decision.EdgeInput{
		Relation: decision.RelImplements,
		From:     decision.Endpoint{Kind: decision.KindDecision, Ref: "pure-go"},
		To:       decision.Endpoint{Kind: decision.KindDecision, Ref: "keep-sqlite"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := decision.Link(db, decision.EdgeInput{
		Relation: decision.RelValidates,
		From:     decision.Endpoint{Kind: decision.KindSession, Ref: "sess-1"},
		To:       decision.Endpoint{Kind: decision.KindDecision, Ref: "keep-sqlite"},
	}); err != nil {
		t.Fatal(err)
	}
	budgetPath := filepath.Join(t.TempDir(), "budget.json")
	if err := os.WriteFile(budgetPath, []byte("{\"ceiling_usd\":\"2\",\"window\":\"store\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := startUIWithBudget(t, db, budgetPath)

	page := renderedPage(t, srv)
	if !strings.Contains(page, "Keep the collector store in SQLite.") || !strings.Contains(page, "decision") {
		t.Fatalf("decision record not rendered:\n%s", page)
	}
	if !strings.Contains(page, "The driver stays pure Go.") || !strings.Contains(page, "hypothesis") {
		t.Fatalf("hypothesis record not rendered:\n%s", page)
	}
	if !strings.Contains(page, "implements") || !strings.Contains(page, "pure-go") || !strings.Contains(page, "keep-sqlite") {
		t.Fatalf("decision edge not rendered:\n%s", page)
	}
	if !strings.Contains(page, "validates") || !strings.Contains(page, "sess-1") {
		t.Fatalf("session edge not rendered:\n%s", page)
	}
	if !budgetShown(page, "within", "0", "2") {
		t.Fatalf("within budget not rendered:\n%s", page)
	}
}

func TestPageOmitsDecisionsAndBudgetWhenAbsent(t *testing.T) {
	db := openDB(t)
	srv := startUI(t, db)
	page := renderedPage(t, srv)
	if strings.Contains(page, "hypothesis") || decisionShown(page) {
		t.Fatalf("empty page rendered a decision:\n%s", page)
	}
	if strings.Contains(page, "ceiling") {
		t.Fatalf("empty page rendered a budget:\n%s", page)
	}
}

func TestPageRendersOverAndUnknownBudgets(t *testing.T) {
	overPage := renderedPage(t, startUIWithBudget(t, pricedDB(t), writeBudget(t, "0.5")))
	if !budgetShown(overPage, "over", "1", "0.5") {
		t.Fatalf("over budget not rendered:\n%s", overPage)
	}
	unknownPage := renderedPage(t, startUIWithBudget(t, unpricedDB(t), writeBudget(t, "2")))
	if !strings.Contains(unknownPage, "unknown") {
		t.Fatalf("unknown budget not rendered:\n%s", unknownPage)
	}
	if budgetShown(unknownPage, "within", "", "") || budgetShown(unknownPage, "over", "", "") {
		t.Fatalf("unknown budget rendered a comparison:\n%s", unknownPage)
	}
}

func TestReadOnlyAndLoopback(t *testing.T) {
	db := openDB(t)
	srv := startUI(t, db)
	if srv.BindWarning() != "" || !strings.HasPrefix(srv.URL(), "http://127.0.0.1:") {
		t.Fatalf("url %s warning %q", srv.URL(), srv.BindWarning())
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req, err := http.NewRequest(method, srv.URL()+"api/summary", strings.NewReader("no"))
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
	status, _, _ := get(t, srv.URL()+"missing")
	if status != 404 {
		t.Fatal(status)
	}
	wide, err := ui.Start(ui.Config{Addr: "0.0.0.0:0", Store: db, Prices: "bundled"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = wide.Shutdown(ctx)
	})
	if wide.BindWarning() == "" || !strings.Contains(wide.BindWarning(), "prompts and paths") {
		t.Fatal(wide.BindWarning())
	}
	for _, name := range []string{"index.html", "app.css", "app.js"} {
		body := readAsset(t, name)
		for _, banned := range []string{"http://", "https://", "cdn.", "fonts.google", "analytics"} {
			if strings.Contains(body, banned) {
				t.Fatalf("%s contains %s", name, banned)
			}
		}
	}
}

type sessionView struct {
	Summary          summary.SessionSummary `json:"summary"`
	LinkMethod       string                 `json:"link_method"`
	LinkReason       string                 `json:"link_reason"`
	InputTokens      ui.Amount              `json:"input_tokens"`
	OutputTokens     ui.Amount              `json:"output_tokens"`
	ComputedCostUSD  ui.Amount              `json:"computed_cost_usd"`
	AgentReportedUSD ui.Amount              `json:"agent_reported_cost_usd"`
	ModelBreakdown   struct {
		Status string          `json:"status"`
		Reason string          `json:"reason"`
		Tokens json.RawMessage `json:"tokens"`
	} `json:"model_breakdown"`
	ToolCallsStatus string `json:"tool_calls_status"`
	ToolCounts      []struct {
		Name  string `json:"name"`
		Count string `json:"count"`
	} `json:"tool_counts"`
	Duration ui.Amount `json:"duration"`
}

type agentRollup struct {
	Tool            string                  `json:"tool"`
	CostPerAccepted summary.CostPerAccepted `json:"cost_per_accepted_outcome"`
	ComputedCostUSD ui.Amount               `json:"computed_cost_usd"`
	Merged          int                     `json:"merged"`
	LinkedExact     int                     `json:"linked_exact"`
	LinkedHeuristic int                     `json:"linked_heuristic"`
}

type snapshotDoc struct {
	Sessions        []sessionView           `json:"sessions"`
	CostPerAccepted summary.CostPerAccepted `json:"cost_per_accepted_outcome"`
	Budget          *summary.BudgetStatus   `json:"budget"`
	Totals          struct {
		ComputedCostUSD      ui.Amount `json:"computed_cost_usd"`
		AgentReportedCostUSD ui.Amount `json:"agent_reported_cost_usd"`
		InputTokens          ui.Amount `json:"input_tokens"`
	} `json:"totals"`
	Agents []agentRollup `json:"agents"`
}

func sessionsFor(snap snapshotDoc, tool string) []summary.SessionSummary {
	var out []summary.SessionSummary
	for _, view := range snap.Sessions {
		if view.Summary.Tool == tool {
			out = append(out, view.Summary)
		}
	}
	return out
}

func allSessions(snap snapshotDoc) []summary.SessionSummary {
	out := make([]summary.SessionSummary, 0, len(snap.Sessions))
	for _, view := range snap.Sessions {
		out = append(out, view.Summary)
	}
	return out
}

func openDB(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "telemetry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func startUI(t *testing.T, db *store.Store) *ui.Server {
	t.Helper()
	return startUIWithBudget(t, db, "")
}

func startUIWithBudget(t *testing.T, db *store.Store, budget string) *ui.Server {
	t.Helper()
	srv, err := ui.Start(ui.Config{Addr: "127.0.0.1:0", Store: db, Prices: "bundled", Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv
}

func summaryJSON(t *testing.T, db *store.Store) string {
	t.Helper()
	records, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	report := summary.SummarizeWith(records, false, pricing.Bundled())
	outcome.Attach(&report, outcome.Options{})
	var buf bytes.Buffer
	if err := summary.WriteJSON(&buf, report); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func apiSummary(t *testing.T, srv *ui.Server) string {
	t.Helper()
	_, _, body := get(t, srv.URL()+"api/summary")
	return body
}

func apiSnapshot(t *testing.T, srv *ui.Server) string {
	t.Helper()
	status, _, body := get(t, srv.URL()+"api/snapshot")
	if status != 200 {
		t.Fatalf("snapshot %d %s", status, body)
	}
	return body
}

func TestDecisionsRoute(t *testing.T) {
	db := openDB(t)
	srv := startUI(t, db)
	status, _, body := get(t, srv.URL()+"api/decisions")
	if status != 200 {
		t.Fatalf("status %d %s", status, body)
	}
	if body != "{\n  \"decisions\": [],\n  \"edges\": []\n}\n" {
		t.Fatalf("empty graph %q", body)
	}
	before := apiSummary(t, srv)

	if _, err := decision.Add(db, decision.RecordInput{
		ID: "h", Statement: "Maybe.", Status: decision.StatusHypothesis,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := decision.Add(db, decision.RecordInput{
		ID: "d", Statement: "Settled.", Status: decision.StatusDecision, Source: "notes",
	}); err != nil {
		t.Fatal(err)
	}
	if apiSummary(t, srv) != before {
		t.Fatal("summary changed after a decision was recorded")
	}
	status, _, body = get(t, srv.URL()+"api/decisions")
	if status != 200 {
		t.Fatalf("status %d %s", status, body)
	}
	var doc struct {
		Decisions []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Decisions) != 2 || doc.Decisions[0].Status != "hypothesis" || doc.Decisions[1].Status != "decision" {
		t.Fatalf("%+v", doc.Decisions)
	}
	post, _, posted := getPost(t, srv.URL()+"api/decisions")
	if post != 405 && post != 404 {
		t.Fatalf("write route answered %d %s", post, posted)
	}
}

func getPost(t *testing.T, url string) (int, http.Header, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(raw)
}

func decodeSnapshot(t *testing.T, body string) snapshotDoc {
	t.Helper()
	var snap snapshotDoc
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func get(t *testing.T, url string) (int, http.Header, string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last error
	for {
		resp, err := http.Get(url)
		if err == nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, resp.Header, string(body)
		}
		last = err
		if time.Now().After(deadline) {
			t.Fatal(last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// budgetShown reports whether the rendered page states a budget. The status
// comes first. The page then names the two figures, with words between the
// numbers and no gap after the status: "withintotal 0 USD · ceiling 2 USD".
// Each figure is the whole number after its own word, so 0 is not 0.50 and a
// session count earlier on the page is not the total.
func budgetShown(page, status, total, ceiling string) bool {
	pattern := `(?s)` + regexp.QuoteMeta(status) +
		`.*?total\s*(\d+(?:\.\d+)?)` +
		`.*?ceiling\s*(\d+(?:\.\d+)?)`
	found := regexp.MustCompile(pattern).FindStringSubmatch(page)
	if found == nil {
		return false
	}
	// An empty figure matches any number, so a caller can reject a comparison
	// without naming the total. A named figure must be that number.
	return (total == "" || found[1] == total) && (ceiling == "" || found[2] == ceiling)
}

// decisionShown reports a decision record as its own word. The navigation
// always contains "Overview", so the substrings "over" and "decision" are not
// a record.
func decisionShown(page string) bool {
	return regexp.MustCompile(`(?i)\bdecision\b`).MatchString(page)
}

// renderedPage is the text of the page after its own script has drawn it.
// The server is the one under test: the script fetches this server, and the
// result is the text a reader sees, not the JSON the routes return.
func renderedPage(t *testing.T, srv *ui.Server) string {
	t.Helper()
	status, _, html := get(t, srv.URL())
	if status != 200 {
		t.Fatalf("page %d", status)
	}
	_, _, script := get(t, srv.URL()+"app.js")
	return runPage(t, srv.URL(), html, script)
}

func runPage(t *testing.T, base, html, script string) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	page := filepath.Join(dir, "page.cjs")
	if err := os.WriteFile(page, []byte(pageRunner), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, page)
	cmd.Stdin = strings.NewReader(base + "\n" + html + "\n/*SDF_PAGE_SPLIT*/\n" + script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	return string(out)
}

func pricedDB(t *testing.T) *store.Store {
	t.Helper()
	db := openDB(t)
	if _, err := db.Insert([]record.Record{{
		Signal: "logs", ServiceName: record.Str("claude-code"), SessionID: record.Str("priced"),
		Name: record.Str("claude_code.api_request"), TimeUnixNano: record.Int64(1_000_000_000),
		Attributes: map[string]any{
			"model": "anthropic/claude-haiku-4.5", "input_tokens": 1000000, "output_tokens": 0,
		},
		Resource: map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}
	return db
}

func unpricedDB(t *testing.T) *store.Store {
	t.Helper()
	db := openDB(t)
	if _, err := db.Insert([]record.Record{{
		Signal: "logs", ServiceName: record.Str("claude-code"), SessionID: record.Str("unpriced"),
		Name: record.Str("claude_code.api_request"), TimeUnixNano: record.Int64(1_000_000_000),
		Attributes: map[string]any{
			"model": "not-a-real-model", "input_tokens": 5, "output_tokens": 2,
		},
		Resource: map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}
	return db
}

func writeBudget(t *testing.T, ceiling string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "budget.json")
	body := "{\"ceiling_usd\":\"" + ceiling + "\",\"window\":\"store\"}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// pageRunner executes the page script the browser would, then prints the text
// of the drawn page. It implements only the DOM surface app.js uses:
// createElement, setAttribute, className, addEventListener, append,
// replaceChildren, and textContent. fetch is pointed at the server under test.
const pageRunner = `"use strict";
const { createInterface } = require("node:readline");

function textOf(node) {
  if (node.nodeType === 3) return node.text;
  return (node.children || []).map(textOf).join("");
}

class Node {
  constructor() { this.children = []; }
  append() {
    for (const kid of arguments) this.children.push(kid);
  }
}
class Element extends Node {
  constructor(tag) {
    super();
    this.nodeType = 1;
    this.tag = tag;
    this.className = "";
    this.attrs = {};
    this.listeners = {};
  }
  setAttribute(key, value) { this.attrs[key] = String(value); }
  addEventListener(type, fn) { this.listeners[type] = fn; }
  replaceChildren() {
    this.children = [];
    this.append.apply(this, arguments);
  }
  get textContent() { return textOf(this); }
}
class Text extends Node {
  constructor(value) {
    super();
    this.nodeType = 3;
    this.text = String(value);
  }
}

const lines = createInterface({ input: process.stdin });
const chunks = [];
lines.on("line", (line) => chunks.push(line));
lines.on("close", async () => {
  const raw = chunks.join("\n");
  const splitAt = raw.indexOf("\n");
  const base = raw.slice(0, splitAt);
  const rest = raw.slice(splitAt + 1);
  const marker = "\n/*SDF_PAGE_SPLIT*/\n";
  const cut = rest.indexOf(marker);
  const script = rest.slice(cut + marker.length);
  const app = new Element("div");
  const location = { hash: "" };
  const document = {
    getElementById: function () { return app; },
    createElement: function (tag) { return new Element(tag); },
    createTextNode: function (value) { return new Text(value); },
  };
  const window = {
    document: document,
    location: location,
    addEventListener: function () {},
    fetch: async function (url) {
      const response = await fetch(new URL(url, base));
      const body = await response.text();
      return {
        ok: response.ok,
        status: response.status,
        json: async () => JSON.parse(body),
        text: async () => body,
      };
    },
  };
  // The page fetches /api/snapshot, /api/decisions, and /api/summary before it
  // draws. Those responses are not back within a fixed number of turns, so
  // count each fetch the page starts and read the page only once a whole turn
  // both starts and ends with none of them left in flight. That covers the
  // draw their callbacks queue, and a callback that starts another fetch.
  const pending = new Set();
  let idle = function () {};
  function fetching(url, options) {
    let finish;
    const done = new Promise((resolve) => { finish = resolve; });
    pending.add(done);
    return Promise.resolve(window.fetch(url, options)).finally(() => {
      finish();
      pending.delete(done);
      idle();
    });
  }
  const run = new Function("window", "document", "location", "fetch", "Node", script);
  run(window, document, location, fetching, Node);
  await new Promise((resolve) => {
    let quiet = false;
    function drain() {
      if (pending.size !== 0) {
        quiet = false;
        idle = drain;
        return;
      }
      if (!quiet) {
        quiet = true;
        setImmediate(drain);
        return;
      }
      resolve();
    }
    setImmediate(drain);
  });
  process.stdout.write(app.textContent);
});
`

func readAsset(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(file), "assets", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
