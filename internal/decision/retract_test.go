package decision_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/store"
)

func retract(t *testing.T, db *store.Store, id, reason, by string) decision.Retraction {
	t.Helper()
	r, err := decision.Retract(db, decision.RetractInput{ID: id, Reason: reason, RetractedBy: by})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRetractMarksTheRecordAtReadTimeAndChangesNothingStored(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "a", "b")
	before, err := db.LoadDecisions()
	if err != nil {
		t.Fatal(err)
	}
	r := retract(t, db, "a", "  The benchmark was wrong.  ", decision.DecidedByUser)
	if r.Reason != "The benchmark was wrong." || r.RetractedBy != "user" || r.RetractedAt == "" {
		t.Fatalf("%+v", r)
	}
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	a, b := g.Decisions[0], g.Decisions[1]
	if a.EffectiveStatus != decision.EffectiveRetracted || a.Retraction == nil || *a.Retraction != r || a.Status != decision.StatusDecision {
		t.Fatalf("a %+v", a)
	}
	if b.EffectiveStatus != decision.EffectiveActive || b.Retraction != nil {
		t.Fatalf("b %+v", b)
	}
	after, err := db.LoadDecisions()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("stored decisions changed")
	}
	var buf bytes.Buffer
	if err := decision.WriteJSON(&buf, g); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Decisions []map[string]any `json:"decisions"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"retracted_at": r.RetractedAt, "reason": "The benchmark was wrong.", "retracted_by": "user"}
	if doc.Decisions[0]["effective_status"] != "retracted" || !reflect.DeepEqual(doc.Decisions[0]["retraction"], want) {
		t.Fatalf("%#v", doc.Decisions[0])
	}
	if _, ok := doc.Decisions[1]["retraction"]; ok {
		t.Fatalf("active record carries retraction: %#v", doc.Decisions[1])
	}
}

func TestRetractedTakesPrecedenceOverSuperseded(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "old", "new")
	link(t, db, decision.RelSupersedes, dec("new"), dec("old"))
	retract(t, db, "old", "", "")
	retract(t, db, "new", "", "")
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	old, cur := g.Decisions[0], g.Decisions[1]
	if old.EffectiveStatus != decision.EffectiveRetracted || !reflect.DeepEqual(old.SupersededBy, []string{"new"}) {
		t.Fatalf("old %+v", old)
	}
	// Retracting the replacement does not reinstate what it replaced.
	if cur.EffectiveStatus != decision.EffectiveRetracted {
		t.Fatalf("new %+v", cur)
	}
	var text bytes.Buffer
	if err := decision.WriteText(&text, g); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "retracted, superseded by new") {
		t.Fatalf("%s", text.String())
	}
}

func TestRetractedNotSupersededStaysSupersededForOthers(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "old", "new")
	link(t, db, decision.RelSupersedes, dec("new"), dec("old"))
	retract(t, db, "new", "", "")
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	if g.Decisions[0].EffectiveStatus != decision.EffectiveSuperseded {
		t.Fatalf("%+v", g.Decisions[0])
	}
}

func TestRetractRejections(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "a")
	first := retract(t, db, "a", "first", "")
	cases := []struct {
		in   decision.RetractInput
		want error
		msg  string
	}{
		{decision.RetractInput{ID: "never-recorded"}, decision.ErrUnknownRef, "unknown reference: decision never-recorded"},
		{decision.RetractInput{ID: "a", Reason: "again"}, decision.ErrAlreadyRetracted, "decision already retracted: a at " + first.RetractedAt},
	}
	for _, c := range cases {
		_, err := decision.Retract(db, c.in)
		if !errors.Is(err, c.want) || err.Error() != c.msg {
			t.Fatalf("%+v: %v, want %q", c.in, err, c.msg)
		}
	}
	for _, in := range []decision.RetractInput{
		{ID: "bad id"},
		{ID: "a", Reason: "two\nlines"},
		{ID: "a", RetractedBy: "robot"},
	} {
		if _, err := decision.Retract(db, in); err == nil {
			t.Fatalf("%+v accepted", in)
		}
	}
	got, err := db.LoadDecisionRetractions()
	if err != nil || len(got) != 1 || got[0].Reason != "first" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestARetractedDecisionCannotBeLinked(t *testing.T) {
	db := openDB(t)
	seedSession(t, db)
	addAll(t, db, "gone", "b")
	link(t, db, decision.RelImplements, dec("b"), dec("gone"))
	retract(t, db, "gone", "", "")
	rejects(t, db, decision.RelSupersedes, dec("b"), dec("gone"), decision.ErrRetracted,
		"decision retracted: decision:gone takes no new edges")
	rejects(t, db, decision.RelSupersedes, dec("gone"), dec("b"), decision.ErrRetracted,
		"decision retracted: decision:gone takes no new edges")
	rejects(t, db, decision.RelValidates, session("sess-1"), dec("gone"), decision.ErrRetracted,
		"decision retracted: decision:gone takes no new edges")
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Edges) != 1 {
		t.Fatalf("the edge from before the retraction must stay: %d", len(g.Edges))
	}
}

func TestConcurrentRetractsLandOnce(t *testing.T) {
	path := openDB(t).Path()
	const writers = 6
	stores := make([]*store.Store, writers)
	for i := range stores {
		s, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		stores[i] = s
	}
	addAll(t, stores[0], "a")
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range stores {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = decision.Retract(stores[i], decision.RetractInput{ID: "a"})
		}(i)
	}
	wg.Wait()
	ok := 0
	for i, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, decision.ErrAlreadyRetracted):
		default:
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d retractions landed: %v", ok, errs)
	}
}
