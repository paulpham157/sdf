package decision_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
)

type staticQuery struct {
	recs []decision.Record
	err  error
}

func (q staticQuery) Active() ([]decision.Record, error) {
	return q.recs, q.err
}

func rec(id, by, statement string) decision.Record {
	return decision.Record{ID: id, DecidedBy: by, Statement: statement}
}

func TestCheckActionMatch(t *testing.T) {
	push := rec("dont-push", decision.DecidedByUser, "Do not push until the user says so.")
	tests := []struct {
		name    string
		action  string
		recs    []decision.Record
		kind    string
		allowed bool
		id      string
		match   string
	}{
		{name: "phrase", action: "push", recs: []decision.Record{push}, kind: "push", id: "dont-push", match: "do not push"},
		{name: "case and whitespace", action: "  PUSH ", recs: []decision.Record{push}, kind: "push", id: "dont-push", match: "do not push"},
		{name: "dont ascii", action: "push", recs: []decision.Record{rec("a", "user", "Don't push.")}, kind: "push", id: "a", match: "don't push"},
		{name: "dont curly", action: "push", recs: []decision.Record{rec("a", "agent", "Don’t push.")}, kind: "push", id: "a", match: "don’t push"},
		{name: "dont smashed", action: "push", recs: []decision.Record{rec("a", "user", "DONT PUSH")}, kind: "push", id: "a", match: "dont push"},
		{name: "never", action: "merge", recs: []decision.Record{rec("a", "user", "Never merge to main.")}, kind: "merge", id: "a", match: "never merge"},
		{name: "must not", action: "deploy", recs: []decision.Record{rec("a", "agent", "Must not deploy this build.")}, kind: "deploy", id: "a", match: "must not deploy"},
		{name: "forbidden to", action: "release", recs: []decision.Record{rec("a", "user", "Forbidden to release today.")}, kind: "release", id: "a", match: "forbidden to release"},
		{name: "forbid word", action: "release", recs: []decision.Record{rec("a", "user", "Forbid release.")}, kind: "release", id: "a", match: "forbid release"},
		{name: "tag", action: "push", recs: []decision.Record{rec("a", "", "Ship it. forbid:push stays.")}, kind: "push", id: "a", match: "forbid:push"},
		{name: "tag wins over phrase", action: "push", recs: []decision.Record{rec("a", "user", "Do not push (forbid:PUSH).")}, kind: "push", id: "a", match: "forbid:push"},
		{name: "tag is a whole token", action: "push", recs: []decision.Record{rec("a", "user", "forbid:pushing is not a tag")}, kind: "push", allowed: true},
		{name: "no at end", action: "release", recs: []decision.Record{rec("a", "user", "no release")}, kind: "release", id: "a", match: "no release"},
		{name: "no before punctuation", action: "release", recs: []decision.Record{rec("a", "user", "no release.")}, kind: "release", id: "a", match: "no release"},
		{name: "no followed by a word", action: "push", recs: []decision.Record{rec("a", "user", "no push notification")}, kind: "push", allowed: true},
		{name: "cue not followed by the action", action: "push", recs: []decision.Record{rec("a", "user", "Do not wait; we will push later.")}, kind: "push", allowed: true},
		{name: "gerund", action: "push", recs: []decision.Record{rec("a", "user", "Do not pushing.")}, kind: "push", allowed: true},
		{name: "other action", action: "merge", recs: []decision.Record{push}, kind: "merge", allowed: true},
		{name: "reason is not the statement", action: "push", recs: []decision.Record{{ID: "a", Statement: "Ship it.", Reason: "Do not push.", DecidedBy: "user"}}, kind: "push", allowed: true},
		{name: "unrelated", action: "push", recs: []decision.Record{rec("use-sqlite", "agent", "Keep the collector store in SQLite.")}, kind: "push", allowed: true},
		{name: "git push is architecture", action: "git push", recs: []decision.Record{push}, kind: "architecture", allowed: true},
		{name: "architecture phrase", action: "replace sqlite", recs: []decision.Record{rec("arch", "user", "Do not change the architecture.")}, kind: "architecture", id: "arch", match: "do not change the architecture"},
		{name: "architecture tag", action: "rename the package", recs: []decision.Record{rec("arch", "agent", "forbid:architecture")}, kind: "architecture", id: "arch", match: "forbid:architecture"},
		{name: "architecture word itself", action: "architecture", recs: []decision.Record{rec("arch", "user", "Never change architecture.")}, kind: "architecture", id: "arch", match: "never change architecture"},
		{name: "no architecture change", action: "swap the driver", recs: []decision.Record{rec("arch", "user", "no architecture change")}, kind: "architecture", id: "arch", match: "no architecture change"},
		{name: "no architecture changes", action: "swap the driver", recs: []decision.Record{rec("arch", "user", "No architecture changes.")}, kind: "architecture", id: "arch", match: "no architecture changes"},
		{name: "no architecture decision is not a forbid", action: "swap the driver", recs: []decision.Record{rec("arch", "user", "There is no architecture decision yet.")}, kind: "architecture", allowed: true},
		{name: "architecture window", action: "swap the driver", recs: []decision.Record{rec("arch", "user", "Do not document the public architecture.")}, kind: "architecture", id: "arch", match: "do not document the public architecture"},
		{name: "outside the architecture window", action: "swap the driver", recs: []decision.Record{rec("arch", "user", "Do not rewrite every public exported method in the architecture.")}, kind: "architecture", allowed: true},
		{name: "architecture forbid does not block push", action: "push", recs: []decision.Record{rec("arch", "user", "Do not change the architecture.")}, kind: "push", allowed: true},
		{name: "push forbid does not block architecture", action: "replace sqlite", recs: []decision.Record{push}, kind: "architecture", allowed: true},
		{name: "empty store", action: "deploy", kind: "deploy", allowed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decision.CheckAction(staticQuery{recs: tt.recs}, tt.action)
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != tt.kind || got.Allowed != tt.allowed {
				t.Fatalf("kind %q allowed %v, want %q %v\n%+v", got.Kind, got.Allowed, tt.kind, tt.allowed, got)
			}
			if tt.allowed {
				if len(got.Blocking) != 0 {
					t.Fatalf("blocking %+v", got.Blocking)
				}
				return
			}
			if len(got.Blocking) != 1 || got.Blocking[0].ID != tt.id || got.Blocking[0].Match != tt.match {
				t.Fatalf("blocking %+v, want id %s match %s", got.Blocking, tt.id, tt.match)
			}
		})
	}
}

func TestCheckActionListsEveryBlockerInOrder(t *testing.T) {
	recs := []decision.Record{
		rec("later", "agent", "Keep SQLite."),
		rec("user-push", "user", "forbid:push"),
		rec("agent-push", "agent", "Do not push the branch."),
	}
	got, err := decision.CheckAction(staticQuery{recs: recs}, "push")
	if err != nil {
		t.Fatal(err)
	}
	if got.Allowed || len(got.Blocking) != 2 {
		t.Fatalf("%+v", got)
	}
	if got.Blocking[0].ID != "user-push" || got.Blocking[0].DecidedBy != "user" {
		t.Fatalf("first %+v", got.Blocking[0])
	}
	if got.Blocking[1].ID != "agent-push" || got.Blocking[1].DecidedBy != "agent" || got.Blocking[1].Match != "do not push" {
		t.Fatalf("second %+v", got.Blocking[1])
	}
}

func TestCheckActionEmptyAndQueryError(t *testing.T) {
	if _, err := decision.CheckAction(staticQuery{}, "  "); !errors.Is(err, decision.ErrEmptyAction) {
		t.Fatalf("%v", err)
	}
	boom := errors.New("read failed")
	if _, err := decision.CheckAction(staticQuery{err: boom}, "push"); !errors.Is(err, boom) {
		t.Fatalf("%v", err)
	}
}

func TestWriteCheckTextAndJSON(t *testing.T) {
	v, err := decision.CheckAction(staticQuery{recs: []decision.Record{
		{ID: "dont-push", DecidedBy: "", Statement: "Do not push\tnow."},
	}}, "push")
	if err != nil {
		t.Fatal(err)
	}
	var text bytes.Buffer
	if err := decision.WriteCheckText(&text, v); err != nil {
		t.Fatal(err)
	}
	want := "forbidden: push\nID\tDECIDED BY\tMATCH\tSTATEMENT\ndont-push\t-\tdo not push\t\"Do not push\\tnow.\"\n"
	if text.String() != want {
		t.Fatalf("text\n%q\nwant\n%q", text.String(), want)
	}
	var js bytes.Buffer
	if err := decision.WriteCheckJSON(&js, v); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(js.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["allowed"] != false || raw["action"] != "push" || raw["kind"] != "push" {
		t.Fatalf("%s", js.String())
	}
	blocking, _ := raw["blocking"].([]any)
	if len(blocking) != 1 {
		t.Fatalf("%s", js.String())
	}
	hit := blocking[0].(map[string]any)
	if hit["decided_by"] != "" || hit["statement"] != "Do not push\tnow." || hit["id"] != "dont-push" {
		t.Fatalf("%s", js.String())
	}

	ok, err := decision.CheckAction(staticQuery{}, "merge")
	if err != nil {
		t.Fatal(err)
	}
	var allowed bytes.Buffer
	if err := decision.WriteCheckText(&allowed, ok); err != nil {
		t.Fatal(err)
	}
	if allowed.String() != "allowed: merge\n" {
		t.Fatalf("%q", allowed.String())
	}
	var empty bytes.Buffer
	if err := decision.WriteCheckJSON(&empty, ok); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(empty.String(), `"blocking": []`) || !strings.Contains(empty.String(), `"allowed": true`) {
		t.Fatalf("%s", empty.String())
	}
	arch, err := decision.CheckAction(staticQuery{}, "replace sqlite")
	if err != nil {
		t.Fatal(err)
	}
	var archText bytes.Buffer
	if err := decision.WriteCheckText(&archText, arch); err != nil {
		t.Fatal(err)
	}
	if archText.String() != "allowed: replace sqlite (architecture)\n" {
		t.Fatalf("%q", archText.String())
	}
}

func TestQueryKeepsOnlyInForceDecisions(t *testing.T) {
	db := openDB(t)
	add := func(id, statement, status, by string) {
		t.Helper()
		if _, err := decision.Add(db, decision.RecordInput{
			ID: id, Statement: statement, Status: status, DecidedBy: by,
		}); err != nil {
			t.Fatal(err)
		}
	}
	link := func(rel, from, to string) {
		t.Helper()
		if _, err := decision.Link(db, decision.EdgeInput{
			Relation: rel,
			From:     decision.Endpoint{Kind: decision.KindDecision, Ref: from},
			To:       decision.Endpoint{Kind: decision.KindDecision, Ref: to},
		}); err != nil {
			t.Fatal(err)
		}
	}
	add("dont-push", "Do not push.", decision.StatusDecision, decision.DecidedByUser)
	add("maybe", "Do not push either.", decision.StatusHypothesis, decision.DecidedByAgent)
	add("old-merge", "Do not merge.", decision.StatusDecision, decision.DecidedByUser)
	add("new-merge", "Merging is allowed.", decision.StatusDecision, decision.DecidedByUser)
	link(decision.RelSupersedes, "new-merge", "old-merge")
	add("no-release", "Do not release.", decision.StatusDecision, decision.DecidedByAgent)
	if _, err := decision.Retract(db, decision.RetractInput{ID: "no-release", RetractedBy: decision.DecidedByUser}); err != nil {
		t.Fatal(err)
	}
	add("wait", "Waiting on review.", decision.StatusHypothesis, decision.DecidedByUser)
	add("no-deploy", "Do not deploy.", decision.StatusDecision, decision.DecidedByAgent)
	link(decision.RelBlocks, "wait", "no-deploy")

	q := decision.NewQuery(db)
	active, err := q.Active()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range active {
		ids = append(ids, d.ID)
	}
	if strings.Join(ids, ",") != "dont-push,new-merge,no-deploy" {
		t.Fatalf("active %v", ids)
	}

	push, err := decision.CheckAction(q, "push")
	if err != nil || push.Allowed || len(push.Blocking) != 1 || push.Blocking[0].ID != "dont-push" || push.Blocking[0].DecidedBy != "user" {
		t.Fatalf("%+v %v", push, err)
	}
	merge, err := decision.CheckAction(q, "merge")
	if err != nil || !merge.Allowed {
		t.Fatalf("superseded merge still blocks: %+v %v", merge, err)
	}
	release, err := decision.CheckAction(q, "release")
	if err != nil || !release.Allowed {
		t.Fatalf("retracted release still blocks: %+v %v", release, err)
	}
	deploy, err := decision.CheckAction(q, "deploy")
	if err != nil || deploy.Allowed || deploy.Blocking[0].ID != "no-deploy" || deploy.Blocking[0].DecidedBy != "agent" {
		t.Fatalf("blocked deploy: %+v %v", deploy, err)
	}
	again, err := q.Active()
	if err != nil || len(again) != len(active) {
		t.Fatal("query wrote or dropped a record")
	}
}
