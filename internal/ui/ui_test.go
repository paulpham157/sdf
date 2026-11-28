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
	"strconv"
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
		Relation: decision.RelConstrains,
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
	if !strings.Contains(page, "constrains") || !strings.Contains(page, "pure-go") || !strings.Contains(page, "keep-sqlite") {
		t.Fatalf("decision edge not rendered:\n%s", page)
	}
	if !strings.Contains(page, "validates") || !strings.Contains(page, "sess-1") {
		t.Fatalf("session edge not rendered:\n%s", page)
	}
	if !budgetShown(page, "within", "0", "2") {
		t.Fatalf("within budget not rendered:\n%s", page)
	}
}

func TestPageShowsSupersededDecisions(t *testing.T) {
	db := openDB(t)
	for _, id := range []string{"keep-sqlite", "sqlite-wal"} {
		if _, err := decision.Add(db, decision.RecordInput{ID: id, Statement: "Statement " + id + ".", Status: decision.StatusDecision}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := decision.Link(db, decision.EdgeInput{
		Relation: decision.RelSupersedes,
		From:     decision.Endpoint{Kind: decision.KindDecision, Ref: "sqlite-wal"},
		To:       decision.Endpoint{Kind: decision.KindDecision, Ref: "keep-sqlite"},
	}); err != nil {
		t.Fatal(err)
	}
	srv := startUIWithBudget(t, db, writeBudget(t, "2"))
	_, _, api := get(t, srv.URL()+"api/decisions")
	if !strings.Contains(api, `"effective_status": "superseded"`) || !strings.Contains(api, `"superseded_by": [`) {
		t.Fatalf("api:\n%s", api)
	}
	for _, hash := range []string{"", "#/sessions"} {
		page := renderedPageAt(t, srv, hash)
		if !strings.Contains(page, "superseded by sqlite-wal") {
			t.Fatalf("%q did not render the superseded state:\n%s", hash, page)
		}
		if strings.Count(page, "superseded by") != 1 {
			t.Fatalf("%q: only keep-sqlite is superseded:\n%s", hash, page)
		}
	}
}

func TestPageShowsBlockedConflictedRefinedAndChallenged(t *testing.T) {
	db := openDB(t)
	adds := []decision.RecordInput{
		{ID: "which-db", Statement: "Pick the store.", Status: decision.StatusHypothesis},
		{ID: "design-schema", Statement: "Design the schema.", Status: decision.StatusDecision},
		{ID: "use-sqlite", Statement: "Use SQLite.", Status: decision.StatusDecision},
		{ID: "use-postgres", Statement: "Use Postgres.", Status: decision.StatusDecision},
		{ID: "bucket-per-minute", Statement: "Bucket per minute.", Status: decision.StatusDecision},
	}
	for _, in := range adds {
		if _, err := decision.Add(db, in); err != nil {
			t.Fatal(err)
		}
	}
	d := func(id string) decision.Endpoint { return decision.Endpoint{Kind: decision.KindDecision, Ref: id} }
	for _, e := range []decision.EdgeInput{
		{Relation: decision.AliasBlockedBy, From: d("design-schema"), To: d("which-db")},
		{Relation: decision.RelConflictsWith, From: d("use-sqlite"), To: d("use-postgres")},
		{Relation: decision.RelRefines, From: d("bucket-per-minute"), To: d("use-sqlite")},
		{Relation: decision.RelContradicts, From: d("bucket-per-minute"), To: d("design-schema")},
	} {
		if _, err := decision.Link(db, e); err != nil {
			t.Fatal(err)
		}
	}
	srv := startUIWithBudget(t, db, writeBudget(t, "2"))
	page := renderedPage(t, srv)
	for _, want := range []string{
		"blocked by which-db",
		"conflicts with use-postgres",
		"conflicts with use-sqlite",
		"refined by bucket-per-minute",
		"challenged",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("missing %q:\n%s", want, page)
		}
	}
	if strings.Count(page, "blocked by") != 1 {
		t.Fatalf("only design-schema is blocked:\n%s", page)
	}
}

// A superseded record is not blocked, but its blocker is still open: the
// detail line must not call it settled.
func TestPageDoesNotCallAnOpenBlockerOfASupersededRecordSettled(t *testing.T) {
	db := openDB(t)
	adds := []decision.RecordInput{
		{ID: "which-db", Statement: "Pick the store.", Status: decision.StatusHypothesis},
		{ID: "design-schema", Statement: "Design the schema.", Status: decision.StatusDecision},
		{ID: "design-schema-v2", Statement: "Design the schema again.", Status: decision.StatusDecision},
	}
	for _, in := range adds {
		if _, err := decision.Add(db, in); err != nil {
			t.Fatal(err)
		}
	}
	d := func(id string) decision.Endpoint { return decision.Endpoint{Kind: decision.KindDecision, Ref: id} }
	for _, e := range []decision.EdgeInput{
		{Relation: decision.RelBlocks, From: d("which-db"), To: d("design-schema")},
		{Relation: decision.RelSupersedes, From: d("design-schema-v2"), To: d("design-schema")},
	} {
		if _, err := decision.Link(db, e); err != nil {
			t.Fatal(err)
		}
	}
	srv := startUIWithBudget(t, db, writeBudget(t, "2"))
	page := renderedPage(t, srv)
	if strings.Contains(page, "(settled)") {
		t.Fatalf("which-db is still a hypothesis, not settled:\n%s", page)
	}
	if !strings.Contains(page, "blocked by which-db (open: which-db)") {
		t.Fatalf("missing the open blocker:\n%s", page)
	}
}

// Like sdf decision list, the UI lists every blocker and marks the open
// ones: a settled blocker next to an open one is still shown.
func TestPageListsEveryBlockerAndMarksTheOpenOnes(t *testing.T) {
	db := openDB(t)
	adds := []decision.RecordInput{
		{ID: "which-db", Statement: "Pick the store.", Status: decision.StatusHypothesis},
		{ID: "which-auth", Statement: "Pick auth.", Status: decision.StatusDecision},
		{ID: "design-schema", Statement: "Design the schema.", Status: decision.StatusDecision},
		{ID: "write-docs", Statement: "Write the docs.", Status: decision.StatusDecision},
	}
	for _, in := range adds {
		if _, err := decision.Add(db, in); err != nil {
			t.Fatal(err)
		}
	}
	d := func(id string) decision.Endpoint { return decision.Endpoint{Kind: decision.KindDecision, Ref: id} }
	for _, e := range []decision.EdgeInput{
		{Relation: decision.RelBlocks, From: d("which-db"), To: d("design-schema")},
		{Relation: decision.RelBlocks, From: d("which-auth"), To: d("design-schema")},
		{Relation: decision.RelBlocks, From: d("which-auth"), To: d("write-docs")},
	} {
		if _, err := decision.Link(db, e); err != nil {
			t.Fatal(err)
		}
	}
	srv := startUIWithBudget(t, db, writeBudget(t, "2"))
	page := renderedPage(t, srv)
	for _, want := range []string{
		"blocked by which-db, which-auth (open: which-db)",
		"blocked by which-auth (settled)",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("missing %q:\n%s", want, page)
		}
	}
	if strings.Count(page, "blocked by") != 2 {
		t.Fatalf("each blocked record lists its blockers once:\n%s", page)
	}
}

// The "conflicts with" badge lists only the conflicts that are still active;
// a side that was superseded is shown as resolved on its own detail line.
func TestPageConflictBadgeListsOnlyActiveConflicts(t *testing.T) {
	db := openDB(t)
	for _, id := range []string{"use-sqlite", "use-postgres", "use-mysql", "use-postgres-16"} {
		if _, err := decision.Add(db, decision.RecordInput{ID: id, Statement: "Statement " + id + ".", Status: decision.StatusDecision}); err != nil {
			t.Fatal(err)
		}
	}
	d := func(id string) decision.Endpoint { return decision.Endpoint{Kind: decision.KindDecision, Ref: id} }
	for _, e := range []decision.EdgeInput{
		{Relation: decision.RelConflictsWith, From: d("use-sqlite"), To: d("use-postgres")},
		{Relation: decision.RelConflictsWith, From: d("use-sqlite"), To: d("use-mysql")},
		{Relation: decision.RelSupersedes, From: d("use-postgres-16"), To: d("use-postgres")},
	} {
		if _, err := decision.Link(db, e); err != nil {
			t.Fatal(err)
		}
	}
	srv := startUIWithBudget(t, db, writeBudget(t, "2"))
	page := renderedPage(t, srv)
	for _, want := range []string{"conflicts with use-mysql", "conflicts with use-sqlite (resolved)"} {
		if !strings.Contains(page, want) {
			t.Fatalf("missing %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "conflicts with use-postgres, use-mysql") || strings.Contains(page, "conflicts with use-mysql, use-postgres") {
		t.Fatalf("badge lists the superseded use-postgres:\n%s", page)
	}
}

func TestPageShowsRetractedDecisions(t *testing.T) {
	db := openDB(t)
	for _, id := range []string{"keep-sqlite", "pure-go"} {
		if _, err := decision.Add(db, decision.RecordInput{ID: id, Statement: "Statement " + id + ".", Status: decision.StatusDecision}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := decision.Retract(db, decision.RetractInput{ID: "keep-sqlite", Reason: "Benchmark was wrong."}); err != nil {
		t.Fatal(err)
	}
	srv := startUIWithBudget(t, db, writeBudget(t, "2"))
	_, _, api := get(t, srv.URL()+"api/decisions")
	if !strings.Contains(api, `"effective_status": "retracted"`) || !strings.Contains(api, `"retraction": {`) {
		t.Fatalf("api:\n%s", api)
	}
	for _, hash := range []string{"", "#/sessions"} {
		page := renderedPageAt(t, srv, hash)
		if !strings.Contains(page, "retracted") || !strings.Contains(page, "Benchmark was wrong.") {
			t.Fatalf("%q did not render the retraction:\n%s", hash, page)
		}
		if strings.Count(page, "retracted") != 1 {
			t.Fatalf("%q: only keep-sqlite is retracted:\n%s", hash, page)
		}
	}
}

func TestPageRendersUnsetCategoryAndLegacyDecidedBy(t *testing.T) {
	db := openDB(t)
	if _, err := decision.Add(db, decision.RecordInput{
		ID: "keep-sqlite", Statement: "Keep the collector store in SQLite.", Status: decision.StatusDecision,
		DecidedBy: decision.DecidedByUser,
	}); err != nil {
		t.Fatal(err)
	}
	srv := startUI(t, db)
	_, _, api := get(t, srv.URL()+"api/decisions")
	if strings.Contains(api, `"category"`) {
		t.Fatalf("JSON should omit an empty category:\n%s", api)
	}
	if !strings.Contains(api, `"decided_by": "user"`) {
		t.Fatalf("JSON should keep the stored word user:\n%s", api)
	}
	page := renderedPage(t, srv)
	if !strings.Contains(page, "implementation") {
		t.Fatalf("empty category did not render as implementation:\n%s", page)
	}
	if !strings.Contains(page, "user (legacy)") {
		t.Fatalf("user did not render as user (legacy):\n%s", page)
	}
	if !strings.Contains(page, "category") {
		t.Fatalf("category prefix missing:\n%s", page)
	}
}

func TestPageRendersDecisionActorAlternativesAndReason(t *testing.T) {
	db := openDB(t)
	if _, err := decision.Add(db, decision.RecordInput{
		ID:           "dont-push",
		Statement:    "Do not push until the user says so.",
		Status:       decision.StatusDecision,
		DecidedBy:    decision.DecidedByUser,
		Alternatives: "push the branch now",
		Reason:       "The user said don't push.",
	}); err != nil {
		t.Fatal(err)
	}
	srv := startUI(t, db)
	page := renderedPage(t, srv)
	if !strings.Contains(page, "Do not push until the user says so.") {
		t.Fatalf("statement not rendered:\n%s", page)
	}
	if !strings.Contains(page, "user") || !strings.Contains(page, "push the branch now") || !strings.Contains(page, "The user said don't push.") {
		t.Fatalf("optional decision fields not rendered:\n%s", page)
	}
}

func TestSessionsAndDetailShowDecisionsAndBudgetWhenEmpty(t *testing.T) {
	db := openDB(t)
	if _, err := decision.Add(db, decision.RecordInput{
		ID: "keep-sqlite", Statement: "Keep the collector store in SQLite.", Status: decision.StatusDecision,
	}); err != nil {
		t.Fatal(err)
	}
	srv := startUIWithBudget(t, db, writeBudget(t, "2"))
	for _, hash := range []string{"#/sessions", "#/sessions/missing"} {
		page := renderedPageAt(t, srv, hash)
		if !decisionShown(page) || !strings.Contains(page, "Keep the collector store in SQLite.") {
			t.Fatalf("%s did not render the decision:\n%s", hash, page)
		}
		// A window with no sessions is unknown, not a comparison. It names the
		// reason and draws neither total nor ceiling.
		if !strings.Contains(page, "unknown") || !strings.Contains(page, "no sessions") {
			t.Fatalf("%s did not render the unknown budget:\n%s", hash, page)
		}
		if budgetShown(page, "within", "", "") || budgetShown(page, "over", "", "") {
			t.Fatalf("%s rendered a comparison:\n%s", hash, page)
		}
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

func TestSessionPageShowsSpanTreeAndErrors(t *testing.T) {
	db := openDB(t)
	// Status messages are error text, dropped by default; keep them so the
	// page's escaping of a hostile message is still exercised.
	db.KeepContent(func(record.Record) bool { return true })
	xssName := "<script>alert(1)</script>"
	xssMsg := "</li><img src=x onerror=alert(1)>"
	parent := record.Record{
		Signal: "traces", ServiceName: record.Str("opencode"), SessionID: record.Str("ses-error"),
		Name: record.Str("SessionStep.attempt"), SpanID: record.Str("step"),
		StartUnixNano: record.Int64(0), EndUnixNano: record.Int64(2_000_000),
		StatusCode: "ERROR", StatusMessage: record.Str("turn failed"),
		Attributes: map[string]any{}, Resource: map[string]any{},
	}
	child := record.Record{
		Signal: "traces", ServiceName: record.Str("opencode"), SessionID: record.Str("ses-error"),
		Name: record.Str("Tool.execute"), SpanID: record.Str("tool"), ParentSpanID: record.Str("step"),
		StartUnixNano: record.Int64(0), EndUnixNano: record.Int64(1_000_000),
		StatusCode: "ERROR", StatusMessage: record.Str("write failed"),
		Attributes: map[string]any{"gen_ai.tool.name": "write", "gen_ai.tool.call.id": "call_1", "error.type": "EACCES"},
		Resource:   map[string]any{},
	}
	injected := record.Record{
		Signal: "traces", ServiceName: record.Str("opencode"), SessionID: record.Str("ses-error"),
		Name: record.Str(xssName), SpanID: record.Str("xss"), ParentSpanID: record.Str("step"),
		StartUnixNano: record.Int64(1_000_000), EndUnixNano: record.Int64(1_500_000),
		StatusCode: "ERROR", StatusMessage: record.Str(xssMsg),
		Attributes: map[string]any{"gen_ai.tool.call.id": "call_xss"},
		Resource:   map[string]any{},
	}
	if _, err := db.Insert([]record.Record{parent, child, injected}); err != nil {
		t.Fatal(err)
	}
	srv := startUI(t, db)

	var summaryDoc map[string]any
	if err := json.Unmarshal([]byte(apiSummary(t, srv)), &summaryDoc); err != nil {
		t.Fatal(err)
	}
	sumSessions, _ := summaryDoc["sessions"].([]any)
	if len(sumSessions) != 1 {
		t.Fatalf("summary sessions %d", len(sumSessions))
	}
	sumRoot, _ := sumSessions[0].(map[string]any)["spans"].([]any)
	if len(sumRoot) != 1 {
		t.Fatalf("summary roots %+v", sumRoot)
	}
	sumNode, _ := sumRoot[0].(map[string]any)
	if sumNode["name"] != "SessionStep.attempt" || sumNode["status_code"] != "ERROR" || sumNode["status_message"] != "turn failed" {
		t.Fatalf("summary root %+v", sumNode)
	}
	sumKids, _ := sumNode["children"].([]any)
	if len(sumKids) != 2 {
		t.Fatalf("summary children %+v", sumKids)
	}

	snap := decodeSnapshot(t, apiSnapshot(t, srv))
	if len(snap.Sessions) != 1 {
		t.Fatalf("snapshot sessions %d", len(snap.Sessions))
	}
	spans := snap.Sessions[0].Summary.Spans
	if len(spans) != 1 || spans[0].Name != "SessionStep.attempt" || spans[0].StatusCode != "ERROR" || spanText(spans[0].StatusMessage) != "turn failed" {
		t.Fatalf("snapshot root %+v", spans)
	}
	if len(spans[0].Children) != 2 {
		t.Fatalf("snapshot children %+v", spans[0].Children)
	}
	byName := map[string]summary.SpanNode{}
	for _, node := range spans[0].Children {
		byName[node.Name] = node
	}
	tool := byName["Tool.execute"]
	if tool.StatusCode != "ERROR" || spanText(tool.StatusMessage) != "write failed" || spanText(tool.ToolName) != "write" {
		t.Fatalf("tool child %+v", tool)
	}
	xss := byName[xssName]
	if xss.StatusCode != "ERROR" || spanText(xss.StatusMessage) != xssMsg {
		t.Fatalf("xss child %+v", xss)
	}

	script := readAsset(t, "app.js")
	for _, banned := range []string{"innerHTML", "document.write", "insertAdjacentHTML", "outerHTML"} {
		if strings.Contains(script, banned) {
			t.Fatalf("span rendering must stay on text nodes: %s", banned)
		}
	}
	if !strings.Contains(script, "function spanSection") || !strings.Contains(script, "status=ERROR") || !strings.Contains(script, "Show ") || !strings.Contains(script, "SPAN_SIBLING_LIMIT") {
		t.Fatal("app.js should render the span tree, errors, and a show-more cap")
	}

	page := renderedPageAt(t, srv, "#/sessions/ses-error")
	for _, want := range []string{
		"Spans",
		"SessionStep.attempt",
		"Tool.execute",
		"status=ERROR (turn failed)",
		"status=ERROR (write failed) error_type=EACCES",
		"This session has 3 spans with status=ERROR.",
		"turn failed",
		"write failed",
		xssName,
		xssMsg,
		"call_id=call_1",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("missing %q on session page:\n%s", want, page)
		}
	}
}

// The default path: error text is dropped at ingest, and the page shows the
// message sdf builds from the kept error class and HTTP code, the
// error_type field, and the error banner, with none of the sent text.
func TestSessionPageShowsErrorsWithoutContent(t *testing.T) {
	db := openDB(t)
	span := func(name, id, code, msg string, attrs map[string]any) record.Record {
		return record.Record{
			Signal: "traces", ServiceName: record.Str("opencode"), SessionID: record.Str("ses-default"),
			Name: record.Str(name), SpanID: record.Str(id), ParentSpanID: record.Str("step"),
			StartUnixNano: record.Int64(0), EndUnixNano: record.Int64(1_000_000),
			StatusCode: code, StatusMessage: record.Str(msg), Attributes: attrs, Resource: map[string]any{},
		}
	}
	root := span("SessionStep.attempt", "step", "ERROR", "turn failed SDF-UI-SECRET-1", map[string]any{})
	root.ParentSpanID = nil
	recs := []record.Record{
		root,
		span("Tool.execute", "tool", "ERROR", "ENOENT: /home/me/SDF-UI-SECRET-2", map[string]any{"gen_ai.tool.name": "write", "gen_ai.tool.call.id": "call_1", "error.type": "FileNotFoundError", "error.message": "SDF-UI-SECRET-3"}),
		span("chat m-1", "chat", "ERROR", "429 slow down SDF-UI-SECRET-4", map[string]any{"http.response.status_code": int64(429)}),
		span("Tool.execute", "ok", "OK", "fine SDF-UI-SECRET-5", map[string]any{"gen_ai.tool.name": "read", "gen_ai.tool.call.id": "call_2"}),
	}
	if _, err := db.Insert(recs); err != nil {
		t.Fatal(err)
	}
	srv := startUI(t, db)
	snap := decodeSnapshot(t, apiSnapshot(t, srv))
	if len(snap.Sessions) != 1 || len(snap.Sessions[0].Summary.Spans) != 1 {
		t.Fatalf("snapshot %+v", snap.Sessions)
	}
	rootNode := snap.Sessions[0].Summary.Spans[0]
	if rootNode.StatusCode != "ERROR" || spanText(rootNode.StatusMessage) != "error" || rootNode.ErrorType != nil {
		t.Fatalf("root %+v", rootNode)
	}
	got := map[string]summary.SpanNode{}
	for _, node := range rootNode.Children {
		got[spanText(node.CallID)+node.Name] = node
	}
	if n := got["call_1Tool.execute"]; n.StatusCode != "ERROR" || spanText(n.StatusMessage) != "tool error: FileNotFoundError" || spanText(n.ErrorType) != "FileNotFoundError" {
		t.Fatalf("tool %+v", n)
	}
	if n := got["chat m-1"]; n.StatusCode != "ERROR" || spanText(n.StatusMessage) != "error: HTTP 429 Too Many Requests" {
		t.Fatalf("chat %+v", n)
	}
	if n := got["call_2Tool.execute"]; n.StatusCode != "OK" || n.StatusMessage != nil {
		t.Fatalf("an OK span must not get an error message: %+v", n)
	}
	page := renderedPageAt(t, srv, "#/sessions/ses-default")
	for _, want := range []string{
		"status=ERROR (error)",
		"status=ERROR (tool error: FileNotFoundError)",
		"status=ERROR (error: HTTP 429 Too Many Requests)",
		"This session has 3 spans with status=ERROR.",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("missing %q on session page:\n%s", want, page)
		}
	}
	for _, body := range []string{page, apiSummary(t, srv), apiSnapshot(t, srv)} {
		if strings.Contains(body, "SDF-UI-SECRET") || strings.Contains(body, "error_type=FileNotFoundError") {
			t.Fatalf("sent error text, or a repeated type, on the page:\n%s", body)
		}
	}
}

func TestSessionPageCapsLongSpanTrees(t *testing.T) {
	db := openDB(t)
	var recs []record.Record
	for i := 0; i < 45; i++ {
		id := "span" + strconv.Itoa(i)
		recs = append(recs, record.Record{
			Signal: "traces", ServiceName: record.Str("opencode"), SessionID: record.Str("ses-wide"),
			Name: record.Str("Tool.execute"), SpanID: record.Str(id),
			StartUnixNano: record.Int64(int64(i) * 1_000_000), EndUnixNano: record.Int64(int64(i+1) * 1_000_000),
			StatusCode: "OK",
			Attributes: map[string]any{"gen_ai.tool.name": "read", "gen_ai.tool.call.id": id},
			Resource:   map[string]any{},
		})
	}
	if _, err := db.Insert(recs); err != nil {
		t.Fatal(err)
	}
	srv := startUI(t, db)
	page := renderPage(t, srv, "#/sessions/ses-wide", "")
	if !strings.Contains(page.Text, "Show 5 more spans") {
		t.Fatalf("expected a show-more cap on a 45-span tree:\n%s", page.Text)
	}
	if !strings.Contains(page.Text, "call_id=span0") {
		t.Fatalf("visible spans missing span0:\n%s", page.Text)
	}
	if strings.Contains(page.Text, "call_id=span44") {
		t.Fatalf("hidden spans should be built lazily, but span44 was already in the page:\n%s", page.Text)
	}
	opened := renderPage(t, srv, "#/sessions/ses-wide", "open-more")
	if !strings.Contains(opened.Text, "call_id=span44") {
		t.Fatalf("opening show-more should build the remaining spans:\n%s", opened.Text)
	}
}

func TestSessionPageRendersDeepSpanTrees(t *testing.T) {
	db := openDB(t)
	// 2000 levels: the recursive renderer before this fix overflows node's
	// call stack from about 1100 levels. The server now flattens the tree
	// below depth 32 (#267), so the API response stays linear in span
	// count; TestSessionPageDrawsAChainPastTheEncoderLimit covers 5500.
	const depth = 2000
	var recs []record.Record
	var parent *string
	for i := 0; i < depth; i++ {
		id := "d" + strconv.Itoa(i)
		rec := record.Record{
			Signal: "traces", ServiceName: record.Str("opencode"), SessionID: record.Str("ses-deep"),
			Name: record.Str("Tool.execute"), SpanID: record.Str(id), ParentSpanID: parent,
			StartUnixNano: record.Int64(int64(i) * 1_000_000), EndUnixNano: record.Int64(int64(i+1) * 1_000_000),
			StatusCode: "OK",
			Attributes: map[string]any{"gen_ai.tool.name": "read", "gen_ai.tool.call.id": id},
			Resource:   map[string]any{},
		}
		if i == depth-1 {
			rec.StatusCode = "ERROR"
			rec.StatusMessage = record.Str("deep chain failed")
		}
		recs = append(recs, rec)
		idCopy := id
		parent = &idCopy
	}
	if _, err := db.Insert(recs); err != nil {
		t.Fatal(err)
	}
	srv := startUI(t, db)
	page := renderPage(t, srv, "#/sessions/ses-deep", "")
	if strings.Contains(page.Text, "Maximum call stack size exceeded") || strings.Contains(page.Text, "The page could not be drawn") {
		t.Fatalf("deep tree crashed the page:\n%s", page.Text)
	}
	if !strings.Contains(page.Text, "Deeper spans not shown (depth limit 32).") {
		t.Fatalf("expected a truncated-depth note:\n%s", page.Text)
	}
	if !strings.Contains(page.Text, "call_id=d0") {
		t.Fatalf("missing the root of the deep tree:\n%s", page.Text)
	}
	// The leaf's status message is redacted at insert (#224): a Tool.execute
	// ERROR span keeps only "tool error", so the banner names the span and
	// that text, never the sent message.
	if !strings.Contains(page.Text, "This session has a span with status=ERROR.") ||
		!strings.Contains(page.Markup, `<p class="span-error-message">Tool.execute: tool error</p>`) {
		t.Fatalf("banner should still report the deep ERROR, redacted:\n%s", page.Markup)
	}
	if strings.Contains(page.Text, "deep chain failed") {
		t.Fatalf("the sent status message reached the page:\n%s", page.Text)
	}
}

func TestSessionPageOpensErrorAncestors(t *testing.T) {
	db := openDB(t)
	recs := []record.Record{
		{
			Signal: "traces", ServiceName: record.Str("opencode"), SessionID: record.Str("ses-open"),
			Name: record.Str("SessionRunner.runStep"), SpanID: record.Str("root"),
			StartUnixNano: record.Int64(0), EndUnixNano: record.Int64(8_000_000),
			StatusCode: "OK", Attributes: map[string]any{}, Resource: map[string]any{},
		},
		{
			Signal: "traces", ServiceName: record.Str("opencode"), SessionID: record.Str("ses-open"),
			Name: record.Str("SessionStep.attempt"), SpanID: record.Str("mid"), ParentSpanID: record.Str("root"),
			StartUnixNano: record.Int64(0), EndUnixNano: record.Int64(7_000_000),
			StatusCode: "OK", Attributes: map[string]any{}, Resource: map[string]any{},
		},
		{
			Signal: "traces", ServiceName: record.Str("opencode"), SessionID: record.Str("ses-open"),
			Name: record.Str("FileWrite.write"), SpanID: record.Str("err-parent"), ParentSpanID: record.Str("mid"),
			StartUnixNano: record.Int64(0), EndUnixNano: record.Int64(3_000_000),
			StatusCode: "OK", Attributes: map[string]any{}, Resource: map[string]any{},
		},
		{
			Signal: "traces", ServiceName: record.Str("opencode"), SessionID: record.Str("ses-open"),
			Name: record.Str("POST"), SpanID: record.Str("err-leaf"), ParentSpanID: record.Str("err-parent"),
			StartUnixNano: record.Int64(0), EndUnixNano: record.Int64(2_000_000),
			StatusCode: "ERROR", StatusMessage: record.Str("boom"),
			Attributes: map[string]any{}, Resource: map[string]any{},
		},
		{
			Signal: "traces", ServiceName: record.Str("opencode"), SessionID: record.Str("ses-open"),
			Name: record.Str("Tool.execute"), SpanID: record.Str("ok-parent"), ParentSpanID: record.Str("mid"),
			StartUnixNano: record.Int64(4_000_000), EndUnixNano: record.Int64(6_000_000),
			StatusCode: "OK",
			Attributes: map[string]any{"gen_ai.tool.name": "read", "gen_ai.tool.call.id": "ok-parent"},
			Resource:   map[string]any{},
		},
		{
			Signal: "traces", ServiceName: record.Str("opencode"), SessionID: record.Str("ses-open"),
			Name: record.Str("Tool.execute"), SpanID: record.Str("ok-leaf"), ParentSpanID: record.Str("ok-parent"),
			StartUnixNano: record.Int64(4_000_000), EndUnixNano: record.Int64(5_000_000),
			StatusCode: "OK",
			Attributes: map[string]any{"gen_ai.tool.name": "read", "gen_ai.tool.call.id": "ok-leaf"},
			Resource:   map[string]any{},
		},
	}
	if _, err := db.Insert(recs); err != nil {
		t.Fatal(err)
	}
	srv := startUI(t, db)
	page := renderPage(t, srv, "#/sessions/ses-open", "")
	for _, name := range []string{"SessionRunner.runStep", "SessionStep.attempt", "FileWrite.write"} {
		open, found := detailsState(page.Markup, name)
		if !found {
			t.Fatalf("missing details for %s:\n%s", name, page.Markup)
		}
		if !open {
			t.Fatalf("ancestor %s should have the open attribute:\n%s", name, page.Markup)
		}
	}
	open, found := detailsState(page.Markup, "call_id=ok-parent")
	if !found {
		t.Fatalf("missing details for the non-error branch:\n%s", page.Markup)
	}
	if open {
		t.Fatalf("non-error node at depth 2 should stay collapsed:\n%s", page.Markup)
	}
}

func spanText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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
	for _, name := range []string{"index.html", "tokens.css", "app.css", "app.js", "favicon.svg", "favicon.ico"} {
		body := readAsset(t, name)
		if body == "" {
			t.Fatalf("%s is empty", name)
		}
		check := body
		if name == "favicon.svg" {
			// The SVG namespace URI is not a network load.
			check = strings.ReplaceAll(check, `xmlns="http://www.w3.org/2000/svg"`, "")
		}
		if name == "favicon.ico" {
			continue
		}
		for _, banned := range []string{"http://", "https://", "cdn.", "fonts.google", "analytics"} {
			if strings.Contains(check, banned) {
				t.Fatalf("%s contains %s", name, banned)
			}
		}
	}
}

func TestFaviconRoutes(t *testing.T) {
	db := openDB(t)
	srv := startUI(t, db)
	pageStatus, _, page := get(t, srv.URL())
	if pageStatus != 200 {
		t.Fatalf("page %d", pageStatus)
	}
	if !strings.Contains(page, `rel="icon" href="/favicon.svg" type="image/svg+xml"`) {
		t.Fatalf("missing svg icon link: %q", page)
	}
	if !strings.Contains(page, `rel="icon" href="/favicon.ico"`) {
		t.Fatalf("missing ico icon link: %q", page)
	}
	if strings.Contains(page, "http://") || strings.Contains(page, "https://") {
		t.Fatalf("page loads a remote URL: %q", page)
	}

	cases := []struct {
		path        string
		contentType string
		file        string
	}{
		{"favicon.svg", "image/svg+xml; charset=utf-8", "favicon.svg"},
		{"favicon.ico", "image/x-icon", "favicon.ico"},
	}
	for _, tc := range cases {
		status, hdr, body := get(t, srv.URL()+tc.path)
		if status != 200 {
			t.Fatalf("GET /%s status %d", tc.path, status)
		}
		if got := hdr.Get("Content-Type"); got != tc.contentType {
			t.Fatalf("GET /%s Content-Type %q, want %q", tc.path, got, tc.contentType)
		}
		want := readAsset(t, tc.file)
		if body == "" || body != want {
			t.Fatalf("GET /%s body did not match assets/%s (got %d bytes, want %d)", tc.path, tc.file, len(body), len(want))
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

// decisionShown reports a decision record. The navigation always contains
// "Overview", so the substrings "over" and "decision" are not a record, and
// neither is "indecision". The heading "Decisions" is followed immediately by
// the status word "decision", so the match starts at a word and does not
// require a boundary after the optional s.
func decisionShown(page string) bool {
	return regexp.MustCompile(`(?i)\bdecisions?`).MatchString(page)
}

// renderedPage is the text of the page after its own script has drawn it.
// The server is the one under test: the script fetches this server, and the
// result is the text a reader sees, not the JSON the routes return.
func renderedPage(t *testing.T, srv *ui.Server) string {
	t.Helper()
	return renderedPageAt(t, srv, "")
}

func renderedPageAt(t *testing.T, srv *ui.Server, hash string) string {
	t.Helper()
	return renderPage(t, srv, hash, "").Text
}

type pageResult struct {
	Text   string
	Markup string
	// Active is the opening tag of document.activeElement after the run
	// ("" when it is the body), Outside the markup of the body outside the
	// app root, Hash the final location.hash, and Title document.title.
	Active  string
	Outside string
	Hash    string
	Title   string
}

func renderPage(t *testing.T, srv *ui.Server, hash, flags string) pageResult {
	t.Helper()
	status, _, html := get(t, srv.URL())
	if status != 200 {
		t.Fatalf("page %d", status)
	}
	_, _, script := get(t, srv.URL()+"app.js")
	return runPage(t, srv.URL(), html, script, hash, flags)
}

func runPage(t *testing.T, base, html, script, hash, flags string) pageResult {
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
	cmd.Stdin = strings.NewReader(base + "\n" + hash + "\n" + flags + "\n" + html + "\n/*SDF_PAGE_SPLIT*/\n" + script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	parts := strings.SplitN(string(out), "\n/*SDF_PAGE_RESULT*/\n", 2)
	res := pageResult{Text: parts[0]}
	if len(parts) == 2 {
		res.Markup = parts[1]
		if markup, extra, ok := strings.Cut(res.Markup, "\n/*SDF_PAGE_EXTRA*/\n"); ok {
			res.Markup = markup
			var more struct {
				Active  string `json:"active"`
				Outside string `json:"outside"`
				Hash    string `json:"hash"`
				Title   string `json:"title"`
			}
			if err := json.Unmarshal([]byte(extra), &more); err != nil {
				t.Fatalf("page extra: %v\n%s", err, extra)
			}
			res.Active, res.Outside, res.Hash, res.Title = more.Active, more.Outside, more.Hash, more.Title
		}
	}
	return res
}

func detailsState(markup, needle string) (open bool, found bool) {
	pieces := strings.Split(markup, "<details")
	for i, part := range pieces {
		if i == 0 {
			continue
		}
		gt := strings.Index(part, ">")
		sumEnd := strings.Index(part, "</summary>")
		if gt < 0 || sumEnd < 0 {
			continue
		}
		if !strings.Contains(part[:sumEnd], needle) {
			continue
		}
		return strings.Contains(part[:gt], "open"), true
	}
	return false, false
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
// createElement, createElementNS, setAttribute, className, addEventListener,
// removeEventListener, append, replaceChildren, textContent, the details
// open property, focus and document.activeElement, and querySelector for
// simple selectors (tag, .class, [attr], [attr="v"], descendant and child
// combinators). fetch is pointed at the server under test.
//
// flags is a comma-separated list. "open-more" opens every "Show …"
// details. The other entries are actions run in order after the first
// draw; a selector starting with "~" matches the element whose own text
// is the rest:
//
//	click=<sel>       click the element (bubbles; hashchange fires after)
//	focus=<sel>       focus the element
//	key=<key>[@<sel>] keydown on the element, or on the focused element;
//	                  <key> may carry ctrl+, meta+, alt+ or shift+ prefixes
//	hash=<hash>       set location.hash and fire hashchange
//	input=<text>      set the focused element's value and fire input
//	wait=<ms>         let timers run for ms milliseconds (replay playback)
const pageRunner = `"use strict";
const { createInterface } = require("node:readline");

function textOf(node) {
  if (node.nodeType === 3) return node.text;
  return (node.children || []).map(textOf).join("");
}

class Node {
  constructor() { this.children = []; this.parentNode = null; }
  // As ParentNode.append and replaceChildren do: a kid that is not a node
  // (null, undefined, a string, a number) becomes a text node of its
  // String(), so a stray null draws "null" here as it does in a browser.
  append() {
    for (let kid of arguments) {
      if (!(kid instanceof Node)) kid = new Text(String(kid));
      if (kid.parentNode) kid.parentNode.removeChild(kid);
      kid.parentNode = this;
      this.children.push(kid);
    }
  }
  appendChild(kid) { this.append(kid); return kid; }
  removeChild(kid) {
    this.children = this.children.filter(function (c) { return c !== kid; });
    kid.parentNode = null;
    return kid;
  }
  replaceWith(next) {
    const parent = this.parentNode;
    if (!parent) return;
    if (!(next instanceof Node)) next = new Text(String(next));
    const at = parent.children.indexOf(this);
    if (next.parentNode) next.parentNode.removeChild(next);
    next.parentNode = parent;
    parent.children[at] = next;
    this.parentNode = null;
  }
}
function openTag(node) {
  const attrs = Object.assign({}, node.attrs);
  if (node.className) attrs.class = node.className;
  let bits = "";
  Object.keys(attrs).forEach(function (key) {
    bits += " " + key + '="' + String(attrs[key]).replace(/&/g, "&amp;").replace(/"/g, "&quot;") + '"';
  });
  return "<" + node.tag + bits + ">";
}
function markup(node) {
  if (!node) return "";
  if (node.nodeType === 3) {
    return String(node.text).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  }
  const inner = (node.children || []).map(markup).join("");
  return openTag(node) + inner + "</" + node.tag + ">";
}

// Selectors: compound parts joined by " " or ">".
function parseSelector(sel) {
  const steps = [];
  let comb = " ";
  const re = /\s*(>)\s*|\s+|((?:[^\s>\[]|\[[^\]]*\])+)/g;
  let m;
  while ((m = re.exec(sel.trim()))) {
    if (m[1]) { comb = ">"; continue; }
    if (!m[2]) { if (comb !== ">") comb = " "; continue; }
    steps.push({ comb: comb, part: parseCompound(m[2]) });
    comb = " ";
  }
  return steps;
}
function parseCompound(text) {
  const out = { tag: null, classes: [], attrs: [] };
  const re = /^([a-zA-Z][\w-]*)|\.([\w-]+)|\[([\w-]+)(?:="((?:[^"\\]|\\.)*)")?\]/g;
  let m;
  let used = 0;
  while ((m = re.exec(text)) && m[0]) {
    used += m[0].length;
    if (m[1]) out.tag = m[1].toLowerCase();
    else if (m[2]) out.classes.push(m[2]);
    else out.attrs.push([m[3], m[4] == null ? null : m[4].replace(/\\(.)/g, "$1")]);
  }
  if (used !== text.length) throw new Error("runner: unsupported selector " + text);
  return out;
}
function matchCompound(el, part) {
  if (!el || el.nodeType !== 1) return false;
  if (part.tag && el.tag.toLowerCase() !== part.tag) return false;
  const cls = el.classList();
  for (const c of part.classes) if (cls.indexOf(c) < 0) return false;
  for (const a of part.attrs) {
    const v = el.getAttribute(a[0]);
    if (v == null) return false;
    if (a[1] != null && v !== a[1]) return false;
  }
  return true;
}
function matchSteps(el, steps, i, root) {
  if (!matchCompound(el, steps[i].part)) return false;
  if (i === 0) return true;
  const comb = steps[i].comb;
  let up = el.parentNode;
  while (up && up !== root.parentNode) {
    if (matchSteps(up, steps, i - 1, root)) return true;
    if (comb === ">") return false;
    up = up.parentNode;
  }
  return false;
}
function walk(node, fn) {
  (node.children || []).forEach(function (kid) {
    if (kid && kid.nodeType === 1) { fn(kid); walk(kid, fn); }
  });
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
  get tagName() { return String(this.tag).toUpperCase(); }
  classList() { return String(this.className || this.attrs.class || "").split(/\s+/).filter(Boolean); }
  setAttribute(key, value) { this.attrs[key] = String(value); }
  getAttribute(key) {
    if (key === "class") return this.className || (this.attrs.class != null ? this.attrs.class : null);
    return Object.prototype.hasOwnProperty.call(this.attrs, key) ? this.attrs[key] : null;
  }
  hasAttribute(key) { return this.getAttribute(key) != null; }
  removeAttribute(key) { delete this.attrs[key]; }
  get value() { return this._value != null ? this._value : (this.attrs.value || ""); }
  set value(v) { this._value = String(v); }
  addEventListener(type, fn) {
    if (!this.listeners[type]) this.listeners[type] = [];
    this.listeners[type].push(fn);
  }
  removeEventListener(type, fn) {
    const list = this.listeners[type];
    if (!list) return;
    this.listeners[type] = list.filter(function (item) { return item !== fn; });
  }
  fire(type, event) {
    const list = (this.listeners[type] || []).slice();
    for (let i = 0; i < list.length; i++) list[i].call(this, event || { type: type, target: this, preventDefault() {} });
  }
  focus() { focusState.active = this; }
  blur() { if (focusState.active === this) focusState.active = null; }
  contains(other) {
    for (let n = other; n; n = n.parentNode) if (n === this) return true;
    return false;
  }
  querySelectorAll(sel) {
    const steps = parseSelector(sel);
    const root = this;
    const out = [];
    walk(this, function (el) { if (matchSteps(el, steps, steps.length - 1, root)) out.push(el); });
    return out;
  }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
  closest(sel) {
    const steps = parseSelector(sel);
    for (let n = this; n && n.nodeType === 1; n = n.parentNode) if (matchSteps(n, steps, steps.length - 1, { parentNode: null })) return n;
    return null;
  }
  get open() { return Object.prototype.hasOwnProperty.call(this.attrs, "open"); }
  set open(value) {
    const next = !!value;
    const was = this.open;
    if (next) this.attrs.open = "true";
    else delete this.attrs.open;
    if (next !== was) this.fire("toggle");
  }
  replaceChildren() {
    this.children.forEach(function (kid) { if (kid) kid.parentNode = null; });
    this.children = [];
    this.append.apply(this, arguments);
  }
  get textContent() { return textOf(this); }
  set textContent(value) { this.replaceChildren(new Text(value)); }
}
class Text extends Node {
  constructor(value) {
    super();
    this.nodeType = 3;
    this.text = String(value);
  }
}
const focusState = { active: null };

const lines = createInterface({ input: process.stdin });
const chunks = [];
lines.on("line", (line) => chunks.push(line));
lines.on("close", async () => {
  const raw = chunks.join("\n");
  const splitAt = raw.indexOf("\n");
  const base = raw.slice(0, splitAt);
  const rest = raw.slice(splitAt + 1);
  const hashAt = rest.indexOf("\n");
  const hash = rest.slice(0, hashAt);
  const afterHash = rest.slice(hashAt + 1);
  const flagAt = afterHash.indexOf("\n");
  const flags = afterHash.slice(0, flagAt);
  const page = afterHash.slice(flagAt + 1);
  const marker = "\n/*SDF_PAGE_SPLIT*/\n";
  const cut = page.indexOf(marker);
  const script = page.slice(cut + marker.length);
  const body = new Element("body");
  const app = new Element("div");
  app.setAttribute("id", "app");
  body.append(app);
  const location = { hash: hash };
  const winListeners = {};
  const docListeners = {};
  const document = {
    body: body,
    get activeElement() {
      const el = focusState.active;
      return el && body.contains(el) ? el : body;
    },
    getElementById: function () { return app; },
    createElement: function (tag) { return new Element(tag); },
    createElementNS: function (ns, tag) { return new Element(tag); },
    createTextNode: function (value) { return new Text(value); },
    addEventListener: function (type, fn) { (docListeners[type] = docListeners[type] || []).push(fn); },
    removeEventListener: function () {},
  };
  const window = {
    document: document,
    location: location,
    addEventListener: function (type, fn) { (winListeners[type] = winListeners[type] || []).push(fn); },
    removeEventListener: function () {},
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
  function dispatch(target, type, init) {
    const event = Object.assign({
      type: type, target: target, defaultPrevented: false, propagationStopped: false,
      ctrlKey: false, metaKey: false, altKey: false, shiftKey: false,
    }, init || {});
    event.preventDefault = function () { event.defaultPrevented = true; };
    event.stopPropagation = function () { event.propagationStopped = true; };
    for (let n = target; n && !event.propagationStopped; n = n.parentNode) {
      event.currentTarget = n;
      if (n.fire) n.fire(type, event);
    }
    if (!event.propagationStopped) (docListeners[type] || []).slice().forEach(function (fn) { fn(event); });
    if (!event.propagationStopped) (winListeners[type] || []).slice().forEach(function (fn) { fn(event); });
    return event;
  }
  function find(sel) {
    if (sel.charAt(0) === "~") {
      const want = sel.slice(1);
      let hit = null;
      walk(body, function (el) {
        if (!hit && (el.tag === "button" || el.getAttribute("role")) && textOf(el) === want) hit = el;
      });
      if (!hit) throw new Error("runner: no element with text " + want);
      return hit;
    }
    const el = body.querySelector(sel);
    if (!el) throw new Error("runner: no element matches " + sel);
    return el;
  }
  // The page fetches /api/snapshot, /api/decisions, /api/summary,
  // /api/decisions/graph, and /api/overview-charts before it
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
  function settle() {
    return new Promise((resolve) => {
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
  }
  const run = new Function("window", "document", "location", "fetch", "Node", "addEventListener", script);
  run(window, document, location, fetching, Node, window.addEventListener);
  await settle();
  const list = flags.split(",").filter(Boolean);
  if (list.indexOf("open-more") >= 0) {
    function openMore(node) {
      if (!node || node.nodeType === 3) return;
      if (node.tag === "details") {
        const sum = (node.children || []).find(function (child) { return child.tag === "summary"; });
        if (sum && textOf(sum).indexOf("Show ") === 0) node.open = true;
      }
      (node.children || []).forEach(openMore);
    }
    openMore(app);
  }
  for (const item of list) {
    const eq = item.indexOf("=");
    if (eq < 0) continue;
    const verb = item.slice(0, eq);
    const arg = item.slice(eq + 1);
    const before = location.hash;
    if (verb === "click") dispatch(find(arg), "click");
    else if (verb === "focus") find(arg).focus();
    else if (verb === "hash") location.hash = arg;
    else if (verb === "wait") await new Promise((resolve) => setTimeout(resolve, parseInt(arg, 10)));
    else if (verb === "input") {
      const el = document.activeElement;
      el.value = arg;
      dispatch(el, "input");
    } else if (verb === "key") {
      const at = arg.lastIndexOf("@");
      let key = at > 0 ? arg.slice(0, at) : arg;
      const target = at > 0 ? find(arg.slice(at + 1)) : document.activeElement;
      const init = {};
      let mod;
      while ((mod = /^(ctrl|meta|alt|shift)\+(.+)$/.exec(key))) { init[mod[1] + "Key"] = true; key = mod[2]; }
      init.key = key === "comma" ? "," : key;
      dispatch(target, "keydown", init);
    } else throw new Error("runner: unknown action " + item);
    if (location.hash !== before || verb === "hash") (winListeners.hashchange || []).slice().forEach(function (fn) { fn({ type: "hashchange" }); });
    await settle();
  }
  const active = document.activeElement === body ? "" : openTag(document.activeElement);
  const outside = body.children.filter(function (kid) { return kid !== app; }).map(markup).join("");
  process.stdout.write(app.textContent + "\n/*SDF_PAGE_RESULT*/\n" + markup(app) +
    "\n/*SDF_PAGE_EXTRA*/\n" + JSON.stringify({ active: active, outside: outside, hash: location.hash, title: document.title || "" }),
    // A playing replay leaves an interval running: exit once written.
    () => process.exit(0));
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
