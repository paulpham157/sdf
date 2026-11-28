package brief_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/decision/brief"
)

func TestClassifyRanksLockedConstraintsFirst(t *testing.T) {
	ship := rec("ship-it", decision.StatusDecision, decision.DecidedByAgent, decision.EffectiveActive, "Push the branch now.")
	ship.Blocked = true
	postgres := rec("use-postgres", decision.StatusDecision, decision.DecidedByAgent, decision.EffectiveActive, "Store telemetry in Postgres.")
	postgres.Conflicted = true
	old := rec("old-ban", decision.StatusDecision, decision.DecidedByUser, decision.EffectiveSuperseded, "Do not push, ever.")
	old.Blocked = true
	withdrawn := rec("withdrawn", decision.StatusDecision, decision.DecidedByUser, decision.EffectiveRetracted, "Do not merge.")
	withdrawn.Conflicted = true
	gone := rec("gone", decision.StatusDecision, decision.DecidedByAgent, decision.EffectiveSuperseded, "Use a remote database.")
	gone.Conflicted = true
	g := decision.Graph{
		Decisions: []decision.Record{
			rec("keep-local", decision.StatusDecision, decision.DecidedByAgent, decision.EffectiveActive, "Keep the store on this machine."),
			ship,
			postgres,
			rec("dont-push", decision.StatusDecision, decision.DecidedByUser, decision.EffectiveActive, "Do not push until the user says so."),
			old,
			withdrawn,
			rec("maybe", decision.StatusHypothesis, decision.DecidedByUser, decision.EffectiveActive, "Maybe ban deploys."),
			rec("plain", decision.StatusDecision, decision.DecidedByAgent, decision.EffectiveActive, "Rename the binary later."),
			gone,
		},
		Edges: []decision.Edge{
			{Relation: decision.RelConstrains, From: decision.Endpoint{Kind: decision.KindDecision, Ref: "keep-local"}, To: decision.Endpoint{Kind: decision.KindSession, Ref: "sess-1"}},
			edge(decision.RelBlocks, "which-db", "ship-it"),
			edge(decision.RelConflictsWith, "use-postgres", "ship-it"),
			edge(decision.RelSupersedes, "dont-push", "old-ban"),
		},
	}
	got := brief.Classify(g)
	want := []brief.Item{
		{ID: "dont-push", Kind: brief.KindConstraint, Statement: "Do not push until the user says so.", Status: decision.StatusDecision, DecidedBy: decision.DecidedByUser, DecidedByLegacy: true, Locked: true},
		{ID: "keep-local", Kind: brief.KindConstraint, Statement: "Keep the store on this machine.", Status: decision.StatusDecision, DecidedBy: decision.DecidedByAgent, DecidedByLegacy: true, Locked: false},
		{ID: "ship-it", Kind: brief.KindBlocked, Statement: "Push the branch now.", Status: decision.StatusDecision, DecidedBy: decision.DecidedByAgent, DecidedByLegacy: true, Locked: false},
		{ID: "use-postgres", Kind: brief.KindConflicting, Statement: "Store telemetry in Postgres.", Status: decision.StatusDecision, DecidedBy: decision.DecidedByAgent, DecidedByLegacy: true, Locked: false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d: %+v", len(got), len(want), ids(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestClassifyLoginIsLockedAndAgentIsNot(t *testing.T) {
	login := rec("dont-push", decision.StatusDecision, "paulpham157", decision.EffectiveActive, "Do not push until the user says so.")
	login.Blocked = true
	agent := rec("rename", decision.StatusDecision, decision.DecidedByAgent, decision.EffectiveActive, "Rename the binary later.")
	keep := rec("keep-local", decision.StatusDecision, decision.DecidedByAgent, decision.EffectiveActive, "Keep the store on this machine.")
	hyp := rec("maybe", decision.StatusHypothesis, "paulpham157", decision.EffectiveActive, "Maybe ban deploys.")
	hyp.Blocked = true
	bad := rec("spaces", decision.StatusDecision, "not a login", decision.EffectiveActive, "Store it somewhere.")
	g := decision.Graph{
		Decisions: []decision.Record{
			rec("old", decision.StatusDecision, decision.DecidedByUser, decision.EffectiveActive, "Do not merge."),
			login,
			agent,
			keep,
			hyp,
			bad,
		},
		Edges: []decision.Edge{
			{Relation: decision.RelConstrains, From: decision.Endpoint{Kind: decision.KindDecision, Ref: "keep-local"}, To: decision.Endpoint{Kind: decision.KindSession, Ref: "sess"}},
		},
	}
	got := brief.Classify(g)
	want := []brief.Item{
		{ID: "dont-push", Kind: brief.KindConstraint, Statement: "Do not push until the user says so.", Status: decision.StatusDecision, DecidedBy: "paulpham157", Locked: true},
		{ID: "old", Kind: brief.KindConstraint, Statement: "Do not merge.", Status: decision.StatusDecision, DecidedBy: decision.DecidedByUser, DecidedByLegacy: true, Locked: true},
		{ID: "keep-local", Kind: brief.KindConstraint, Statement: "Keep the store on this machine.", Status: decision.StatusDecision, DecidedBy: decision.DecidedByAgent, DecidedByLegacy: true, Locked: false},
		{ID: "maybe", Kind: brief.KindBlocked, Statement: "Maybe ban deploys.", Status: decision.StatusHypothesis, DecidedBy: "paulpham157", Locked: false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	trimmed := brief.Trim(got, 0, -1, nil)
	if trimmed.OmittedLocked != 0 || len(trimmed.Items) != 2 || trimmed.Items[0].ID != "dont-push" || !trimmed.Items[0].Locked || trimmed.Items[1].ID != "old" || trimmed.Omitted != 2 {
		t.Fatalf("max-items 0 dropped a locked login: %+v", trimmed)
	}
}

func TestOSSourceIsNotALockedConstraint(t *testing.T) {
	local := rec("from-os", decision.StatusDecision, "ada", decision.EffectiveActive, "Keep notes in the repo.")
	local.DecidedBySource = decision.DecidedBySourceOS
	routine := rec("routine", decision.StatusDecision, "ada", decision.EffectiveActive, "Write the note down.")
	routine.DecidedBySource = decision.DecidedBySourceOS
	login := rec("dont-push", decision.StatusDecision, "paulpham157", decision.EffectiveActive, "Do not push until the user says so.")
	flagged := rec("keep-go", decision.StatusDecision, "octocat", decision.EffectiveActive, "Keep the driver pure Go.")
	flagged.DecidedBySource = decision.DecidedBySourceFlag
	g := decision.Graph{
		Decisions: []decision.Record{routine, local, login, flagged},
		Edges: []decision.Edge{
			{Relation: decision.RelConstrains, From: decision.Endpoint{Kind: decision.KindDecision, Ref: "from-os"}, To: decision.Endpoint{Kind: decision.KindSession, Ref: "sess"}},
		},
	}
	got := brief.Classify(g)
	if len(got) != 3 || got[0].ID != "keep-go" || !got[0].Locked || got[0].DecidedBySource != decision.DecidedBySourceFlag || got[1].ID != "dont-push" || !got[1].Locked || got[1].DecidedBySource != "" || got[2].ID != "from-os" || got[2].Locked || got[2].Kind != brief.KindConstraint || got[2].DecidedBySource != decision.DecidedBySourceOS {
		t.Fatalf("%+v", got)
	}
	trimmed := brief.Trim(got, 0, -1, nil)
	if trimmed.Omitted != 1 || trimmed.OmittedLocked != 0 || len(trimmed.Items) != 2 || trimmed.Items[0].ID != "keep-go" || trimmed.Items[1].ID != "dont-push" {
		t.Fatalf("os-sourced row was exempt from max-items: %+v", trimmed)
	}
	text := brief.FormatText(brief.Trim([]brief.Item{got[2]}, 15, -1, nil))
	if !strings.Contains(text, "ada (local)") {
		t.Fatalf("local marker missing:\n%s", text)
	}
	js := brief.JSONBytes(brief.Trim([]brief.Item{got[2]}, 15, -1, nil))
	if !strings.Contains(string(js), `"decided_by_source": "os"`) {
		t.Fatalf("json omitted the os source: %s", js)
	}
}

func TestClassifyUserDecisionStaysAConstraintWhenItAlsoConflicts(t *testing.T) {
	user := rec("use-sqlite", decision.StatusDecision, decision.DecidedByUser, decision.EffectiveActive, "Keep the collector store in SQLite.")
	user.Conflicted = true
	user.Blocked = true
	other := rec("use-postgres", decision.StatusDecision, decision.DecidedByAgent, decision.EffectiveActive, "Store telemetry in Postgres.")
	other.Conflicted = true
	g := decision.Graph{
		Decisions: []decision.Record{user, other},
		Edges: []decision.Edge{
			edge(decision.RelConflictsWith, "use-postgres", "use-sqlite"),
		},
	}
	got := brief.Classify(g)
	if len(got) != 2 || got[0].ID != "use-sqlite" || got[0].Kind != brief.KindConstraint || !got[0].Locked || got[1].ID != "use-postgres" || got[1].Kind != brief.KindConflicting {
		t.Fatalf("%+v", got)
	}
}

func TestClassifyIncludesBlockedAndConflictingHypotheses(t *testing.T) {
	blockedQ := rec("which-db", decision.StatusHypothesis, decision.DecidedByAgent, decision.EffectiveActive, "Pick the store.")
	blockedQ.Blocked = true
	conflictQ := rec("maybe-postgres", decision.StatusHypothesis, decision.DecidedByAgent, decision.EffectiveActive, "Maybe use Postgres.")
	conflictQ.Conflicted = true
	userQ := rec("maybe-ban", decision.StatusHypothesis, decision.DecidedByUser, decision.EffectiveActive, "Maybe ban deploys.")
	userQ.Blocked = true
	old := rec("old-ban", decision.StatusHypothesis, decision.DecidedByUser, decision.EffectiveSuperseded, "Do not push, ever.")
	old.Blocked = true
	gone := rec("withdrawn", decision.StatusHypothesis, decision.DecidedByAgent, decision.EffectiveRetracted, "Forget the ban.")
	gone.Conflicted = true
	g := decision.Graph{Decisions: []decision.Record{
		blockedQ,
		userQ,
		rec("dont-push", decision.StatusDecision, decision.DecidedByUser, decision.EffectiveActive, "Do not push until the user says so."),
		conflictQ,
		old,
		gone,
	}}
	got := brief.Classify(g)
	want := []string{"constraint:dont-push", "blocked:which-db", "blocked:maybe-ban", "conflicting:maybe-postgres"}
	if gotIDs := strings.Join(ids(got), ","); gotIDs != strings.Join(want, ",") {
		t.Fatalf("got %s", gotIDs)
	}
	for _, item := range got {
		if item.Status == decision.StatusHypothesis && item.Kind == brief.KindConstraint {
			t.Fatalf("hypothesis classified as a constraint: %+v", item)
		}
	}
	if got[0].Locked != true || got[1].Status != decision.StatusHypothesis || got[2].DecidedBy != decision.DecidedByUser {
		t.Fatalf("%+v", got)
	}
}

func TestClassifyEmptyGraph(t *testing.T) {
	got := brief.Classify(decision.Graph{})
	if got == nil || len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestTrimItemCapAndOmittedLine(t *testing.T) {
	items := []brief.Item{
		{ID: "a", Kind: brief.KindConstraint, Statement: "one", Status: "decision", DecidedBy: "user", Locked: true},
		{ID: "b", Kind: brief.KindConstraint, Statement: "two", Status: "decision", DecidedBy: "agent"},
		{ID: "c", Kind: brief.KindBlocked, Statement: "three", Status: "decision", DecidedBy: "agent"},
	}
	got := brief.Trim(items, 2, -1, nil)
	if got.Omitted != 1 || len(got.Items) != 2 || got.Items[0].ID != "a" || got.Items[1].ID != "b" {
		t.Fatalf("%+v", got)
	}
	text := brief.FormatText(got)
	if !strings.HasSuffix(text, "1 more: run sdf decision list\n") {
		t.Fatalf("%q", text)
	}
	if strings.Contains(text, "three") {
		t.Fatalf("dropped item still present: %q", text)
	}
	full := brief.Trim(items, 15, -1, nil)
	if full.Omitted != 0 || strings.Contains(brief.FormatText(full), "more:") {
		t.Fatalf("uncapped %+v %q", full, brief.FormatText(full))
	}
}

func TestTrimByteCapDropsAnotherItem(t *testing.T) {
	items := []brief.Item{
		{ID: "a", Kind: brief.KindConstraint, Statement: strings.Repeat("A", 40), Status: "decision", DecidedBy: "user", Locked: true},
		{ID: "b", Kind: brief.KindBlocked, Statement: strings.Repeat("B", 40), Status: "decision", DecidedBy: "agent"},
		{ID: "c", Kind: brief.KindConflicting, Statement: strings.Repeat("C", 40), Status: "decision", DecidedBy: "agent"},
	}
	render := func(r brief.Result) []byte { return []byte(brief.FormatText(r)) }
	one := brief.FormatText(brief.Result{Items: items[:1], Omitted: 2})
	two := brief.FormatText(brief.Result{Items: items[:2], Omitted: 1})
	if len(one) >= len(two) {
		t.Fatalf("one %d two %d", len(one), len(two))
	}
	got := brief.Trim(items, 15, len(one), render)
	if got.Omitted != 2 || len(got.Items) != 1 || got.Items[0].ID != "a" {
		t.Fatalf("%+v", got)
	}
	if brief.FormatText(got) != one {
		t.Fatalf("got %q want %q", brief.FormatText(got), one)
	}
	// The omitted note is kept even when it alone is over the cap. The locked
	// rule is among the drops, so the footer names it.
	note := brief.Trim(items, 15, 1, render)
	if note.Omitted != 3 || note.OmittedLocked != 1 || len(note.Items) != 0 || brief.FormatText(note) != "3 more (1 locked): run sdf decision list\n" {
		t.Fatalf("%+v %q", note, brief.FormatText(note))
	}
}

func TestFormatJSONShape(t *testing.T) {
	ship := rec("ship-it", decision.StatusDecision, decision.DecidedByAgent, decision.EffectiveActive, "Push the branch now.")
	ship.Blocked = true
	postgres := rec("use-postgres", decision.StatusDecision, decision.DecidedByAgent, decision.EffectiveActive, "Store telemetry in Postgres.")
	postgres.Conflicted = true
	g := decision.Graph{
		Decisions: []decision.Record{
			ship,
			postgres,
			rec("dont-push", decision.StatusDecision, decision.DecidedByUser, decision.EffectiveActive, "Do not push until the user says so."),
		},
	}
	got := brief.JSONBytes(brief.Select(g, 15, -1, nil))
	want := `{
  "items": [
    {
      "id": "dont-push",
      "kind": "constraint",
      "statement": "Do not push until the user says so.",
      "status": "decision",
      "decided_by": "user",
      "decided_by_legacy": true,
      "locked": true
    },
    {
      "id": "ship-it",
      "kind": "blocked",
      "statement": "Push the branch now.",
      "status": "decision",
      "decided_by": "agent",
      "decided_by_legacy": true,
      "locked": false
    },
    {
      "id": "use-postgres",
      "kind": "conflicting",
      "statement": "Store telemetry in Postgres.",
      "status": "decision",
      "decided_by": "agent",
      "decided_by_legacy": true,
      "locked": false
    }
  ],
  "omitted": 0,
  "omitted_locked": 0
}
`
	if string(got) != want {
		t.Fatalf("json\n%s\nwant\n%s", got, want)
	}
	var doc struct {
		Items []struct {
			ID        string `json:"id"`
			Kind      string `json:"kind"`
			Statement string `json:"statement"`
			Status    string `json:"status"`
			DecidedBy string `json:"decided_by"`
			Locked    bool   `json:"locked"`
		} `json:"items"`
		Omitted int `json:"omitted"`
	}
	if err := json.Unmarshal(got, &doc); err != nil || doc.Omitted != 0 || len(doc.Items) != 3 {
		t.Fatalf("%v %+v", err, doc)
	}
}

func TestFormatTextQuotesATab(t *testing.T) {
	text := brief.FormatText(brief.Result{Items: []brief.Item{{
		ID: "a", Kind: brief.KindConstraint, Statement: "keep\tit", Status: "decision", DecidedBy: "user", Locked: true,
	}}})
	if !strings.Contains(text, `"keep\tit"`) || strings.Contains(text, "keep\tit") {
		t.Fatalf("%q", text)
	}
}

func TestClassifyNewestLockedRuleSurvivesTheCaps(t *testing.T) {
	textRender := func(r brief.Result) []byte { return []byte(brief.FormatText(r)) }

	// Repro A: four long user rules, then the ban. The ban is newest, so it
	// is kept, and each long statement is cut to the per-item budget.
	var long decision.Graph
	for i := 1; i <= 4; i++ {
		long.Decisions = append(long.Decisions, rec(fmt.Sprintf("rule%d", i), decision.StatusDecision, decision.DecidedByUser, decision.EffectiveActive, strings.Repeat("x", 480)))
	}
	long.Decisions = append(long.Decisions, rec("no-push", decision.StatusDecision, decision.DecidedByUser, decision.EffectiveActive, "Never push without explicit OK"))
	ranked := brief.Classify(long)
	if ranked[0].ID != "no-push" {
		t.Fatalf("newest locked rule first, got %v", ids(ranked))
	}
	for _, item := range ranked[1:] {
		if len(item.Statement) > 200 || !strings.HasSuffix(item.Statement, "…") {
			t.Fatalf("statement not cut: %d %q", len(item.Statement), item.Statement)
		}
	}
	text := brief.FormatText(brief.Trim(ranked, 15, 2048, textRender))
	if !strings.Contains(text, "\tno-push\t") {
		t.Fatalf("no-push missing:\n%s", text)
	}

	// Newest-first still puts the ban ahead of older locked rules, and the
	// item cap does not drop any locked rule.
	var many decision.Graph
	for i := 0; i < 20; i++ {
		many.Decisions = append(many.Decisions, rec(fmt.Sprintf("u%05d", i), decision.StatusDecision, decision.DecidedByUser, decision.EffectiveActive, "a rule"))
	}
	many.Decisions = append(many.Decisions, rec("no-push", decision.StatusDecision, decision.DecidedByUser, decision.EffectiveActive, "Never push without explicit OK"))
	ranked = brief.Classify(many)
	got := brief.Trim(ranked, 15, 2048, textRender)
	text = brief.FormatText(got)
	if got.Omitted != 0 || got.OmittedLocked != 0 || !strings.HasPrefix(text, "constraint\tno-push\t") || !strings.Contains(text, "u00000") {
		t.Fatalf("omitted %d locked %d\n%s", got.Omitted, got.OmittedLocked, text)
	}
}

func TestEveryLockedRuleSurvivesDefaultCaps(t *testing.T) {
	textRender := func(r brief.Result) []byte { return []byte(brief.FormatText(r)) }
	jsonRender := func(r brief.Result) []byte { return brief.JSONBytes(r) }

	// Repro 1: the ban is the oldest of 21 short user rules. The item cap
	// used to drop it. It stays, in text and JSON.
	var older decision.Graph
	older.Decisions = append(older.Decisions, rec("no-push", decision.StatusDecision, decision.DecidedByUser, decision.EffectiveActive, "Never push without explicit OK"))
	for i := 0; i < 20; i++ {
		older.Decisions = append(older.Decisions, rec(fmt.Sprintf("u%05d", i), decision.StatusDecision, decision.DecidedByUser, decision.EffectiveActive, "a rule"))
	}
	ranked := brief.Classify(older)
	textGot := brief.Trim(ranked, 15, 2048, textRender)
	text := brief.FormatText(textGot)
	// Twenty-one indented JSON records do not fit in 2048 bytes even at the
	// statement floor, so the JSON check uses a byte cap that is not the
	// limiting factor. The item cap of 15 would have dropped no-push before.
	js := brief.JSONBytes(brief.Trim(ranked, 15, -1, jsonRender))
	if textGot.OmittedLocked != 0 || !strings.Contains(text, "\tno-push\t") || !strings.Contains(string(js), `"id": "no-push"`) {
		t.Fatalf("text omitted %d\n%s\njson %s", textGot.OmittedLocked, text, js)
	}

	// Repro 2: the ban is oldest, then long rules. The byte cap used to
	// drop it. Statements shrink so every locked rule still appears.
	var wide decision.Graph
	wide.Decisions = append(wide.Decisions, rec("no-push", decision.StatusDecision, decision.DecidedByUser, decision.EffectiveActive, "Never push without explicit OK"))
	for i := 0; i < 10; i++ {
		wide.Decisions = append(wide.Decisions, rec(fmt.Sprintf("w%02d", i), decision.StatusDecision, decision.DecidedByUser, decision.EffectiveActive, strings.Repeat("x", 200)))
	}
	ranked = brief.Classify(wide)
	textGot = brief.Trim(ranked, 15, 2048, textRender)
	text = brief.FormatText(textGot)
	// JSON of the text selection carries no-push. A JSON trim at 2048 bytes
	// may still drop the oldest locked rules: eleven indented records do not
	// fit there even at the 40-byte statement floor.
	js = brief.JSONBytes(textGot)
	if textGot.OmittedLocked != 0 || len(textGot.Items) != 11 || !strings.Contains(text, "\tno-push\t") || !strings.Contains(string(js), `"id": "no-push"`) {
		t.Fatalf("text kept %d locked %d\n%s\njson %s", len(textGot.Items), textGot.OmittedLocked, text, js)
	}
	shrunk := false
	for _, item := range textGot.Items {
		if item.ID != "no-push" && len(item.Statement) < 200 {
			shrunk = true
		}
	}
	if !shrunk {
		t.Fatal("locked statements were not shortened to fit")
	}
}

func TestPathologicalLockedDropNamesThem(t *testing.T) {
	items := make([]brief.Item, 5)
	for i := range items {
		items[i] = brief.Item{
			ID: fmt.Sprintf("n%d", i), Kind: brief.KindConstraint,
			Statement: strings.Repeat("x", 200), Status: "decision",
			DecidedBy: "user", Locked: true,
		}
	}
	render := func(r brief.Result) []byte { return []byte(brief.FormatText(r)) }
	got := brief.Trim(items, 15, 120, render)
	text := brief.FormatText(got)
	if got.OmittedLocked == 0 || !strings.Contains(text, "\tn0\t") || strings.Contains(text, "\tn4\t") {
		t.Fatalf("%+v\n%s", got, text)
	}
	wantFoot := fmt.Sprintf("%d more (%d locked): run sdf decision list\n", got.Omitted, got.OmittedLocked)
	if !strings.HasSuffix(text, wantFoot) || got.Omitted < got.OmittedLocked {
		t.Fatalf("footer %q omitted %d locked %d", text, got.Omitted, got.OmittedLocked)
	}
	jsGot := brief.Trim(items, 15, 800, func(r brief.Result) []byte { return brief.JSONBytes(r) })
	js := brief.JSONBytes(jsGot)
	if jsGot.OmittedLocked == 0 || !strings.Contains(string(js), `"id": "n0"`) || strings.Contains(string(js), `"id": "n4"`) {
		t.Fatalf("%+v\n%s", jsGot, js)
	}
	if !strings.Contains(string(js), fmt.Sprintf("\"omitted_locked\": %d", jsGot.OmittedLocked)) {
		t.Fatalf("%s", js)
	}
}

func TestStatementBudgetCountsTheQuotedField(t *testing.T) {
	tabs := strings.Repeat("\t", 500)
	esc := strings.Repeat("\x1b", 500)
	for _, statement := range []string{tabs, esc} {
		g := decision.Graph{Decisions: []decision.Record{
			rec("noisy", decision.StatusDecision, decision.DecidedByUser, decision.EffectiveActive, statement),
		}}
		got := brief.Classify(g)
		if len(got) != 1 {
			t.Fatal(got)
		}
		text := brief.FormatText(brief.Result{Items: got})
		field := quotedOrRaw(got[0].Statement)
		if len(field) > 200 || strings.Contains(field, "\t") || strings.Contains(field, "\x1b") {
			t.Fatalf("rendered %d %q\nline %q", len(field), field, text)
		}
	}
	zwj := "👩\u200d💻"
	text := brief.FormatText(brief.Result{Items: []brief.Item{{
		ID: "emoji", Kind: brief.KindConstraint, Statement: zwj, Status: "decision", DecidedBy: "user", Locked: true,
	}}})
	if !strings.Contains(text, zwj) || strings.Contains(text, `\u200d`) {
		t.Fatalf("%q", text)
	}
}

func quotedOrRaw(s string) string {
	if strings.Contains(s, "\t") || strings.Contains(s, "\x1b") || strings.HasPrefix(s, `"`) {
		return fmt.Sprintf("%q", s)
	}
	return s
}

func TestLimitStatementCutsOnARune(t *testing.T) {
	// 'à' is two bytes. 101 of them is 202 bytes, past the 200-byte budget.
	statement := strings.Repeat("à", 101)
	g := decision.Graph{Decisions: []decision.Record{
		rec("ban", decision.StatusDecision, decision.DecidedByUser, decision.EffectiveActive, statement),
	}}
	got := brief.Classify(g)
	if len(got) != 1 {
		t.Fatal(got)
	}
	s := got[0].Statement
	if !utf8.ValidString(s) || len(s) > 200 || !strings.HasSuffix(s, "…") || strings.Contains(s, "\u00c3") && !strings.Contains(s, "à") {
		t.Fatalf("cut %d %q", len(s), s)
	}
}

func TestFormatTextKeepsVietnameseAndQuotesALeadingQuote(t *testing.T) {
	text := brief.FormatText(brief.Result{Items: []brief.Item{{
		ID: "khong-day", Kind: brief.KindConstraint, Statement: "Không đẩy khi chưa được đồng ý", Status: "decision", DecidedBy: "user", Locked: true,
	}}})
	if !strings.Contains(text, "Không đẩy khi chưa được đồng ý") || strings.Contains(text, `\u`) {
		t.Fatalf("%q", text)
	}
	quoted := brief.FormatText(brief.Result{Items: []brief.Item{{
		ID: "q", Kind: brief.KindConstraint, Statement: `"hello"`, Status: "decision", DecidedBy: "user", Locked: true,
	}}})
	if !strings.Contains(quoted, `"\"hello\""`) {
		t.Fatalf("%q", quoted)
	}
}

func TestTrimSkipsAnItemThatDoesNotFit(t *testing.T) {
	items := []brief.Item{
		{ID: "huge", Kind: brief.KindConstraint, Statement: strings.Repeat("H", 400), Status: "decision", DecidedBy: "agent"},
		{ID: "small", Kind: brief.KindBlocked, Statement: "ok", Status: "decision", DecidedBy: "agent"},
	}
	render := func(r brief.Result) []byte { return []byte(brief.FormatText(r)) }
	// Room for the footer and the short line, not the long one.
	small := brief.FormatText(brief.Result{Items: items[1:2], Omitted: 1})
	got := brief.Trim(items, 15, len(small), render)
	text := brief.FormatText(got)
	if got.Omitted != 1 || !strings.Contains(text, "\tsmall\t") || strings.Contains(text, "huge") {
		t.Fatalf("%+v %q", got, text)
	}
}

func TestTrimTenThousandDecisions(t *testing.T) {
	items := make([]brief.Item, 10000)
	for i := range items {
		items[i] = brief.Item{
			ID: fmt.Sprintf("id-%d", i), Kind: brief.KindConstraint,
			Statement: strings.Repeat("s", 80), Status: "decision", DecidedBy: "user", Locked: true,
		}
	}
	render := func(r brief.Result) []byte { return brief.JSONBytes(r) }
	start := time.Now()
	got := brief.Trim(items, len(items), 1<<30, render)
	elapsed := time.Since(start)
	if got.Omitted != 0 || len(got.Items) != len(items) {
		t.Fatalf("omitted %d kept %d", got.Omitted, len(got.Items))
	}
	if testing.Short() {
		t.Logf("trim of 10000 decisions: %s (timing skipped, short)", elapsed)
		return
	}
	// 250ms is a CI timing guard for this in-memory Trim, not a runtime
	// requirement or product SLA. The race detector is allowed 10x that.
	budget := 250 * time.Millisecond
	if raceEnabled {
		budget *= 10
	}
	if elapsed > budget {
		t.Fatalf("trim took %s, budget %s", elapsed, budget)
	}
	t.Logf("trim of 10000 decisions: %s", elapsed)
}

func TestJSONItemCostMatchesTheDocument(t *testing.T) {
	items := []brief.Item{
		{ID: "dont-push", Kind: brief.KindConstraint, Statement: "Do not push.", Status: "decision", DecidedBy: "user", Locked: true},
		{ID: "ship-it", Kind: brief.KindBlocked, Statement: "Push now.", Status: "decision", DecidedBy: "agent"},
	}
	doc := brief.JSONBytes(brief.Result{Items: items, Omitted: 3})
	// The rendered document must fit in the single-pass estimate.
	render := func(r brief.Result) []byte { return brief.JSONBytes(r) }
	got := brief.Trim(items, 15, len(doc), render)
	if got.Omitted != 0 || len(got.Items) != 2 {
		t.Fatalf("estimate too high, dropped items: %+v\n%s", got, doc)
	}
}

func TestWriteJSONEmptyIsAnArray(t *testing.T) {
	var buf bytes.Buffer
	if err := brief.WriteJSON(&buf, brief.Result{}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "{\n  \"items\": [],\n  \"omitted\": 0,\n  \"omitted_locked\": 0\n}\n" {
		t.Fatalf("%q", buf.String())
	}
}

func rec(id, status, by, effective, statement string) decision.Record {
	return decision.Record{
		ID:              id,
		Statement:       statement,
		Status:          status,
		DecidedBy:       by,
		EffectiveStatus: effective,
	}
}

func edge(relation, from, to string) decision.Edge {
	return decision.Edge{
		Relation: relation,
		From:     decision.Endpoint{Kind: decision.KindDecision, Ref: from},
		To:       decision.Endpoint{Kind: decision.KindDecision, Ref: to},
	}
}

func ids(items []brief.Item) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.Kind + ":" + item.ID
	}
	return out
}
