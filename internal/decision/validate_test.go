package decision_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

func session(id string) decision.Endpoint {
	return decision.Endpoint{Kind: decision.KindSession, Ref: id}
}

func commit(ref string) decision.Endpoint {
	return decision.Endpoint{Kind: decision.KindCommit, Ref: ref}
}

func seedSession(t *testing.T, db *store.Store) {
	t.Helper()
	if _, err := db.Insert([]record.Record{{
		Signal: "logs", SessionID: record.Str("sess-1"), Name: record.Str("sdf.git.commit"),
		Attributes: map[string]any{"vcs.ref.head.revision": sha}, Resource: map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}
}

func edgeCount(t *testing.T, db *store.Store) int {
	t.Helper()
	edges, err := db.LoadDecisionEdges()
	if err != nil {
		t.Fatal(err)
	}
	return len(edges)
}

// rejects links once and checks the error, its message, and that nothing
// was written.
func rejects(t *testing.T, db *store.Store, rel string, from, to decision.Endpoint, want error, msg string) {
	t.Helper()
	before := edgeCount(t, db)
	_, err := decision.Link(db, decision.EdgeInput{Relation: rel, From: from, To: to})
	if !errors.Is(err, want) {
		t.Fatalf("%s %s %s: err %v, want %v", from.Ref, rel, to.Ref, err, want)
	}
	if err.Error() != msg {
		t.Fatalf("message %q\nwant    %q", err.Error(), msg)
	}
	if after := edgeCount(t, db); after != before {
		t.Fatalf("rejected edge was written: %d -> %d", before, after)
	}
}

func TestLinkRejectsASelfLink(t *testing.T) {
	db := openDB(t)
	seedSession(t, db)
	addAll(t, db, "a")
	for _, rel := range []string{decision.RelConstrains, decision.RelImplements, decision.RelSupersedes, decision.RelValidates, decision.RelContradicts, decision.RelBlocks, decision.RelConflictsWith, decision.RelRefines, decision.AliasBlockedBy, decision.AliasDependsOn} {
		rejects(t, db, rel, dec("a"), dec("a"), decision.ErrSelfLink,
			"an edge cannot link an endpoint to itself: decision:a")
	}
	rejects(t, db, decision.RelValidates, session("sess-1"), session(" sess-1 "), decision.ErrSelfLink,
		"an edge cannot link an endpoint to itself: session:sess-1")
	rejects(t, db, decision.RelValidates, commit(strings.ToUpper(sha)), commit(sha), decision.ErrSelfLink,
		"an edge cannot link an endpoint to itself: commit:"+sha)
}

func TestLinkRejectsSupersedesWithASessionOrCommit(t *testing.T) {
	db := openDB(t)
	seedSession(t, db)
	addAll(t, db, "a")
	rejects(t, db, decision.RelSupersedes, session("sess-1"), dec("a"), decision.ErrSupersedesKind,
		"supersedes links a decision to a decision: from is session:sess-1")
	rejects(t, db, decision.RelSupersedes, dec("a"), commit(sha), decision.ErrSupersedesKind,
		"supersedes links a decision to a decision: to is commit:"+sha)
	// The kind is checked before the store is asked whether the ref exists.
	rejects(t, db, decision.RelSupersedes, dec("a"), session("never-seen"), decision.ErrSupersedesKind,
		"supersedes links a decision to a decision: to is session:never-seen")
	// Other relations still take session and commit endpoints.
	link(t, db, decision.RelValidates, session("sess-1"), dec("a"))
	link(t, db, decision.RelImplements, commit(sha), dec("a"))
}

func TestLinkRejectsASupersedesCycle(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "a", "b", "c", "d")
	link(t, db, decision.RelSupersedes, dec("b"), dec("a"))
	rejects(t, db, decision.RelSupersedes, dec("a"), dec("b"), decision.ErrSupersedesCycle,
		"supersedes cycle: decision:b already reaches decision:a through supersedes (b -> a), so decision:a cannot supersede decision:b")

	link(t, db, decision.RelSupersedes, dec("c"), dec("b"))
	rejects(t, db, decision.RelSupersedes, dec("a"), dec("c"), decision.ErrSupersedesCycle,
		"supersedes cycle: decision:c already reaches decision:a through supersedes (c -> b -> a), so decision:a cannot supersede decision:c")

	// Two replacements for one decision, and other relations backwards along
	// the chain, are not cycles.
	link(t, db, decision.RelSupersedes, dec("d"), dec("a"))
	link(t, db, decision.RelSupersedes, dec("c"), dec("a"))
	link(t, db, decision.RelConstrains, dec("a"), dec("c"))
	link(t, db, decision.RelConstrains, dec("a"), dec("b"))
}

func TestLinkRejectsAnExactDuplicate(t *testing.T) {
	db := openDB(t)
	seedSession(t, db)
	addAll(t, db, "a", "b")
	link(t, db, decision.RelConstrains, dec("a"), dec("b"))
	rejects(t, db, decision.RelConstrains, dec("a"), dec("b"), decision.ErrDuplicateEdge,
		"edge already recorded: decision:a constrains decision:b")
	link(t, db, decision.RelValidates, commit(sha), dec("a"))
	rejects(t, db, decision.RelValidates, commit(strings.ToUpper(sha)), dec("a"), decision.ErrDuplicateEdge,
		"edge already recorded: commit:"+sha+" validates decision:a")
	// Another relation, or the other direction, is a different edge.
	link(t, db, decision.RelRefines, dec("a"), dec("b"))
	link(t, db, decision.RelConstrains, dec("b"), dec("a"))
}

func TestLinkWarnsWhenAPairBothValidatesAndContradicts(t *testing.T) {
	db := openDB(t)
	seedSession(t, db)
	addAll(t, db, "a", "b")
	cases := []struct {
		rel      string
		from, to decision.Endpoint
		warn     string
	}{
		{decision.RelValidates, session("sess-1"), dec("a"), ""},
		{decision.RelContradicts, session("sess-1"), dec("a"), "session:sess-1 and decision:a have both validates and contradicts edges"},
		// Contradicts first, then validates, in the other direction.
		{decision.RelContradicts, dec("a"), dec("b"), ""},
		{decision.RelValidates, dec("b"), dec("a"), "decision:b and decision:a have both validates and contradicts edges"},
		{decision.RelConstrains, dec("a"), dec("b"), ""},
	}
	for _, c := range cases {
		_, warnings, err := decision.LinkWithWarnings(db, decision.EdgeInput{Relation: c.rel, From: c.from, To: c.to})
		if err != nil {
			t.Fatalf("%s: a warning must not block: %v", c.rel, err)
		}
		got := strings.Join(warnings, "\n")
		if got != c.warn {
			t.Fatalf("%s %s %s: warnings %q, want %q", c.from.Ref, c.rel, c.to.Ref, got, c.warn)
		}
	}
	if n := edgeCount(t, db); n != len(cases) {
		t.Fatalf("%d edges, want %d", n, len(cases))
	}
}

// TestConcurrentLinksCheckAndInsertAtomically opens the file several times,
// like separate sdf processes, and races the same edge, and the two halves
// of a supersedes 2-cycle. Exactly one write of each may land.
func TestConcurrentLinksCheckAndInsertAtomically(t *testing.T) {
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
	for round := 0; round < 5; round++ {
		x, y := fmt.Sprintf("x%d", round), fmt.Sprintf("y%d", round)
		addAll(t, stores[0], x, y)

		var wg sync.WaitGroup
		errs := make([]error, writers)
		for i := range stores {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				from, to := dec(x), dec(y)
				if i%2 == 1 {
					from, to = to, from
				}
				_, errs[i] = decision.Link(stores[i], decision.EdgeInput{Relation: decision.RelSupersedes, From: from, To: to})
			}(i)
		}
		wg.Wait()
		ok := 0
		for i, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, decision.ErrDuplicateEdge), errors.Is(err, decision.ErrSupersedesCycle):
			default:
				t.Fatalf("round %d writer %d: %v", round, i, err)
			}
		}
		if ok != 1 {
			t.Fatalf("round %d: %d writes landed, want 1 (%v)", round, ok, errs)
		}
	}
	g, err := decision.Load(stores[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Edges) != 5 {
		t.Fatalf("%d edges, want 5", len(g.Edges))
	}
}

func TestOlderInvalidEdgesStayReadable(t *testing.T) {
	db := openDB(t)
	seedSession(t, db)
	addAll(t, db, "a", "b")
	legacyEdge(t, db, decision.RelSupersedes, dec("a"), dec("a"))
	legacyEdge(t, db, decision.RelSupersedes, session("sess-1"), dec("b"))
	legacyEdge(t, db, decision.RelImplements, dec("a"), dec("b"))
	legacyEdge(t, db, decision.RelImplements, dec("a"), dec("b"))
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Edges) != 4 {
		t.Fatalf("%d edges listed, want all 4", len(g.Edges))
	}
	for _, d := range g.Decisions {
		if d.EffectiveStatus != decision.EffectiveActive {
			t.Fatalf("%s is %s: invalid supersedes edges must change no status", d.ID, d.EffectiveStatus)
		}
	}
	// New links are checked against what is already there, old rows
	// included. A row that breaks today's endpoint kinds is not written again:
	// the kind check runs first.
	rejects(t, db, decision.RelImplements, dec("a"), dec("b"), decision.ErrEndpointKind,
		"implements links a session or commit to a decision: from is decision:a")
	legacyEdge(t, db, decision.RelConstrains, dec("a"), dec("b"))
	rejects(t, db, decision.RelConstrains, dec("a"), dec("b"), decision.ErrDuplicateEdge,
		"edge already recorded: decision:a constrains decision:b")
	link(t, db, decision.RelSupersedes, dec("b"), dec("a"))
}

// TestCycleCheckIgnoresEdgesThroughAnUnknownDecision: an older database
// holds a -> ghost -> b, where ghost was never stored. Read time ignores
// both edges, so b supersedes a is not a cycle.
func TestCycleCheckIgnoresEdgesThroughAnUnknownDecision(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "a", "b")
	legacyEdge(t, db, decision.RelSupersedes, dec("a"), dec("ghost"))
	legacyEdge(t, db, decision.RelSupersedes, dec("ghost"), dec("b"))
	link(t, db, decision.RelSupersedes, dec("b"), dec("a"))
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	if g.Decisions[0].EffectiveStatus != decision.EffectiveSuperseded || g.Decisions[1].EffectiveStatus != decision.EffectiveActive {
		t.Fatalf("%+v", g.Decisions)
	}
}

// TestCycleCheckSeesADecisionAddedJustBeforeTheTransaction: "ghost" is
// stored after the link's pre-checks but before its transaction, turning
// a -> ghost -> b into a real supersedes path. The cycle check reads the
// decision ids inside the transaction, so b supersedes a is refused.
func TestCycleCheckSeesADecisionAddedJustBeforeTheTransaction(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "a", "b")
	legacyEdge(t, db, decision.RelSupersedes, dec("a"), dec("ghost"))
	legacyEdge(t, db, decision.RelSupersedes, dec("ghost"), dec("b"))
	other, err := store.Open(db.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	defer decision.SetBeforeAppendEdgeForTest(func() {
		addAll(t, other, "ghost")
	})()
	before := edgeCount(t, db)
	_, _, err = decision.LinkWithWarnings(db, decision.EdgeInput{Relation: decision.RelSupersedes, From: dec("b"), To: dec("a")})
	if !errors.Is(err, decision.ErrSupersedesCycle) {
		t.Fatalf("err %v", err)
	}
	if got := edgeCount(t, db); got != before {
		t.Fatalf("%d edges, want %d", got, before)
	}
}
