package decision_test

import (
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
)

func TestActiveSettledInForceOnly(t *testing.T) {
	blocked := recActive("ship-it", decision.StatusDecision, decision.DecidedByAgent)
	blocked.Blocked = true
	blocked.Conflicted = true
	g := decision.Graph{Decisions: []decision.Record{
		recActive("keep-local", decision.StatusDecision, decision.DecidedByAgent),
		blocked,
		recActive("maybe", decision.StatusHypothesis, decision.DecidedByUser),
		rec("old-ban", decision.StatusDecision, decision.DecidedByUser, decision.EffectiveSuperseded),
		rec("withdrawn", decision.StatusDecision, decision.DecidedByUser, decision.EffectiveRetracted),
		recActive("dont-push", decision.StatusDecision, decision.DecidedByUser),
	}}
	got := decision.Active(g)
	want := []string{"keep-local", "ship-it", "dont-push"}
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d: %v", len(got), len(want), idsOf(got))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("index %d = %s, want %s (%v)", i, got[i].ID, id, idsOf(got))
		}
	}
	if got[1].DecidedBy != decision.DecidedByAgent || !got[1].Blocked || got[1].Statement == "" {
		t.Fatalf("blocked agent record %+v", got[1])
	}
	if got[0].DecidedBy != decision.DecidedByAgent || got[2].DecidedBy != decision.DecidedByUser {
		t.Fatalf("decided_by %+v %+v", got[0], got[2])
	}
}

func TestActiveEmptyIsNotNil(t *testing.T) {
	got := decision.Active(decision.Graph{})
	if got == nil || len(got) != 0 {
		t.Fatalf("%#v", got)
	}
}

func recActive(id, status, by string) decision.Record {
	return rec(id, status, by, decision.EffectiveActive)
}

func rec(id, status, by, effective string) decision.Record {
	return decision.Record{
		ID:              id,
		Statement:       "Statement " + id,
		Status:          status,
		DecidedBy:       by,
		EffectiveStatus: effective,
	}
}

func idsOf(records []decision.Record) []string {
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r.ID
	}
	return out
}
