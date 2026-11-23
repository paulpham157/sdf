package brief_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

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
		{ID: "dont-push", Kind: brief.KindConstraint, Statement: "Do not push until the user says so.", Status: decision.StatusDecision, DecidedBy: decision.DecidedByUser, Locked: true},
		{ID: "keep-local", Kind: brief.KindConstraint, Statement: "Keep the store on this machine.", Status: decision.StatusDecision, DecidedBy: decision.DecidedByAgent, Locked: false},
		{ID: "ship-it", Kind: brief.KindBlocked, Statement: "Push the branch now.", Status: decision.StatusDecision, DecidedBy: decision.DecidedByAgent, Locked: false},
		{ID: "use-postgres", Kind: brief.KindConflicting, Statement: "Store telemetry in Postgres.", Status: decision.StatusDecision, DecidedBy: decision.DecidedByAgent, Locked: false},
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
	// The omitted note is kept even when it alone is over the cap.
	note := brief.Trim(items, 15, 1, render)
	if note.Omitted != 3 || len(note.Items) != 0 || brief.FormatText(note) != "3 more: run sdf decision list\n" {
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
      "locked": true
    },
    {
      "id": "ship-it",
      "kind": "blocked",
      "statement": "Push the branch now.",
      "status": "decision",
      "decided_by": "agent",
      "locked": false
    },
    {
      "id": "use-postgres",
      "kind": "conflicting",
      "statement": "Store telemetry in Postgres.",
      "status": "decision",
      "decided_by": "agent",
      "locked": false
    }
  ],
  "omitted": 0
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

func TestWriteJSONEmptyIsAnArray(t *testing.T) {
	var buf bytes.Buffer
	if err := brief.WriteJSON(&buf, brief.Result{}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "{\n  \"items\": [],\n  \"omitted\": 0\n}\n" {
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
