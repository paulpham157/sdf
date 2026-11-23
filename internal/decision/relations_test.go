package decision_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/store"
)

func addHypotheses(t *testing.T, db *store.Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := decision.Add(db, decision.RecordInput{ID: id, Statement: "Hypothesis " + id, Status: decision.StatusHypothesis}); err != nil {
			t.Fatal(err)
		}
	}
}

func loadByID(t *testing.T, db *store.Store) map[string]decision.Record {
	t.Helper()
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]decision.Record{}
	for _, d := range g.Decisions {
		out[d.ID] = d
	}
	return out
}

func lastEdge(t *testing.T, db *store.Store) decision.Edge {
	t.Helper()
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	return g.Edges[len(g.Edges)-1]
}

func TestBlockedByAndDependsOnStoreTheBlocksDirection(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "choose-db", "design-schema", "write-migrations")
	for _, c := range []struct {
		rel      string
		from, to string
	}{
		{decision.AliasBlockedBy, "design-schema", "choose-db"},
		{decision.AliasDependsOn, "write-migrations", "design-schema"},
	} {
		link(t, db, c.rel, dec(c.from), dec(c.to))
		e := lastEdge(t, db)
		if e.Relation != decision.RelBlocks || e.From != dec(c.to) || e.To != dec(c.from) {
			t.Fatalf("%s stored %+v, want %s blocks %s", c.rel, e, c.to, c.from)
		}
	}
	// The alias and the canonical form are the same edge.
	rejects(t, db, decision.RelBlocks, dec("choose-db"), dec("design-schema"), decision.ErrDuplicateEdge,
		"edge already recorded: decision:choose-db blocks decision:design-schema")
	rejects(t, db, decision.AliasDependsOn, dec("design-schema"), dec("choose-db"), decision.ErrDuplicateEdge,
		"edge already recorded: decision:choose-db blocks decision:design-schema")
}

func TestBlocksRejectsACycle(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "a", "b", "c")
	link(t, db, decision.RelBlocks, dec("a"), dec("b"))
	link(t, db, decision.RelBlocks, dec("b"), dec("c"))
	rejects(t, db, decision.RelBlocks, dec("c"), dec("a"), decision.ErrBlocksCycle,
		"blocks cycle: decision:a already reaches decision:c through blocks (a -> b -> c), so decision:c cannot block decision:a")
	rejects(t, db, decision.AliasBlockedBy, dec("a"), dec("b"), decision.ErrBlocksCycle,
		"blocks cycle: decision:a already reaches decision:b through blocks (a -> b), so decision:b cannot block decision:a")
	rejects(t, db, decision.AliasBlockedBy, dec("a"), dec("c"), decision.ErrBlocksCycle,
		"blocks cycle: decision:a already reaches decision:c through blocks (a -> b -> c), so decision:c cannot block decision:a")
	// Other relations along the chain do not count.
	link(t, db, decision.RelSupersedes, dec("c"), dec("a"))
	link(t, db, decision.RelBlocks, dec("a"), dec("c"))
}

func TestRefinesRejectsACycleAndASupersededPair(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "a", "b", "c", "d")
	link(t, db, decision.RelRefines, dec("b"), dec("a"))
	link(t, db, decision.RelRefines, dec("c"), dec("b"))
	rejects(t, db, decision.RelRefines, dec("a"), dec("c"), decision.ErrRefinesCycle,
		"refines cycle: decision:c already reaches decision:a through refines (c -> b -> a), so decision:a cannot refine decision:c")
	link(t, db, decision.RelSupersedes, dec("d"), dec("a"))
	rejects(t, db, decision.RelRefines, dec("a"), dec("d"), decision.ErrAlreadySuperseded,
		"pair already linked by supersedes: decision:a and decision:d; one already replaces the other, so refines is not recorded")
}

func TestCycleChecksSeeAnEdgeAddedJustBeforeTheTransaction(t *testing.T) {
	for _, rel := range []string{decision.RelBlocks, decision.RelRefines} {
		db := openDB(t)
		addAll(t, db, "a", "b")
		other, err := store.Open(db.Path())
		if err != nil {
			t.Fatal(err)
		}
		fired := false
		restore := decision.SetBeforeAppendEdgeForTest(func() {
			if fired {
				return // the concurrent link below runs this hook too
			}
			fired = true
			link(t, other, rel, dec("b"), dec("a"))
		})
		_, _, err = decision.LinkWithWarnings(db, decision.EdgeInput{Relation: rel, From: dec("a"), To: dec("b")})
		restore()
		_ = other.Close()
		if !errors.Is(err, decision.ErrBlocksCycle) && !errors.Is(err, decision.ErrRefinesCycle) {
			t.Fatalf("%s: err %v, want a cycle error", rel, err)
		}
		if n := edgeCount(t, db); n != 1 {
			t.Fatalf("%s: %d edges, want only the concurrent one", rel, n)
		}
	}
}

func TestConflictsWithIsStoredInCanonicalOrderOnce(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "use-sqlite", "use-postgres")
	link(t, db, decision.RelConflictsWith, dec("use-sqlite"), dec("use-postgres"))
	e := lastEdge(t, db)
	if e.From != dec("use-postgres") || e.To != dec("use-sqlite") {
		t.Fatalf("stored %+v, want the smaller endpoint first", e)
	}
	for _, pair := range [][2]string{{"use-sqlite", "use-postgres"}, {"use-postgres", "use-sqlite"}} {
		rejects(t, db, decision.RelConflictsWith, dec(pair[0]), dec(pair[1]), decision.ErrDuplicateEdge,
			"edge already recorded: decision:use-postgres conflicts_with decision:use-sqlite")
	}
}

func TestConflictsWithRejectsASupersededPair(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "a", "b")
	link(t, db, decision.RelSupersedes, dec("b"), dec("a"))
	rejects(t, db, decision.RelConflictsWith, dec("b"), dec("a"), decision.ErrAlreadySuperseded,
		"pair already linked by supersedes: decision:a and decision:b; one already replaces the other, so conflicts_with is not recorded")
}

func TestBlocksWarnsWhenThePairIsAlreadyLinked(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "a", "b")
	link(t, db, decision.RelConflictsWith, dec("a"), dec("b"))
	_, warnings, err := decision.LinkWithWarnings(db, decision.EdgeInput{Relation: decision.RelBlocks, From: dec("b"), To: dec("a")})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"decision:b and decision:a also have a conflicts_with edge"}
	if !reflect.DeepEqual(warnings, want) {
		t.Fatalf("warnings %q, want %q", warnings, want)
	}
}

func TestEndpointKindsPerRelation(t *testing.T) {
	db := openDB(t)
	seedSession(t, db)
	addAll(t, db, "a", "b")
	bad := []struct {
		rel      string
		from, to decision.Endpoint
		msg      string
	}{
		{decision.RelBlocks, session("sess-1"), dec("a"), "blocks links a decision to a decision: from is session:sess-1"},
		{decision.RelBlocks, dec("a"), commit(sha), "blocks links a decision to a decision: to is commit:" + sha},
		{decision.AliasBlockedBy, dec("a"), session("sess-1"), "blocks links a decision to a decision: from is session:sess-1"},
		{decision.RelConflictsWith, dec("a"), session("sess-1"), "conflicts_with links a decision to a decision: to is session:sess-1"},
		{decision.RelRefines, commit(sha), dec("a"), "refines links a decision to a decision: from is commit:" + sha},
		{decision.RelImplements, dec("a"), dec("b"), "implements links a session or commit to a decision: from is decision:a"},
		{decision.RelImplements, session("sess-1"), commit(sha), "implements links a session or commit to a decision: to is commit:" + sha},
		{decision.RelValidates, dec("a"), session("sess-1"), "validates points to a decision: to is session:sess-1"},
		{decision.RelContradicts, session("sess-1"), commit(sha), "contradicts points to a decision: to is commit:" + sha},
		{decision.RelConstrains, session("sess-1"), dec("a"), "constrains starts from a decision: from is session:sess-1"},
	}
	for _, c := range bad {
		rejects(t, db, c.rel, c.from, c.to, decision.ErrEndpointKind, c.msg)
	}
	// A supersedes kind error still matches ErrSupersedesKind too.
	_, err := decision.Link(db, decision.EdgeInput{Relation: decision.RelSupersedes, From: session("sess-1"), To: dec("a")})
	if !errors.Is(err, decision.ErrSupersedesKind) || !errors.Is(err, decision.ErrEndpointKind) {
		t.Fatalf("err %v", err)
	}
	// The kind check runs before the refs are looked up.
	rejects(t, db, decision.RelBlocks, session("no-such"), dec("a"), decision.ErrEndpointKind,
		"blocks links a decision to a decision: from is session:no-such")
	good := []struct {
		rel      string
		from, to decision.Endpoint
	}{
		{decision.RelImplements, session("sess-1"), dec("a")},
		{decision.RelImplements, commit(sha), dec("a")},
		{decision.RelValidates, session("sess-1"), dec("a")},
		{decision.RelValidates, dec("b"), dec("a")},
		{decision.RelContradicts, commit(sha), dec("b")},
		{decision.RelConstrains, dec("a"), session("sess-1")},
		{decision.RelConstrains, dec("a"), commit(sha)},
		{decision.RelConstrains, dec("a"), dec("b")},
	}
	for _, c := range good {
		link(t, db, c.rel, c.from, c.to)
	}
}

func TestUnknownRelationListsTheNewOnes(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "a", "b")
	_, err := decision.Link(db, decision.EdgeInput{Relation: "relates_to", From: dec("a"), To: dec("b")})
	if err == nil || !strings.Contains(err.Error(), "blocks, conflicts_with, or refines (aliases: blocked_by, depends_on)") {
		t.Fatalf("err %v", err)
	}
}

func TestBlockedClearsWhenTheHypothesisIsSettled(t *testing.T) {
	db := openDB(t)
	addHypotheses(t, db, "which-db")
	addAll(t, db, "design-schema", "plain-blocker", "settled-target")
	link(t, db, decision.RelBlocks, dec("which-db"), dec("design-schema"))
	link(t, db, decision.RelBlocks, dec("plain-blocker"), dec("settled-target"))

	got := loadByID(t, db)
	ds := got["design-schema"]
	if !ds.Blocked || !reflect.DeepEqual(ds.BlockedBy, []string{"which-db"}) || !reflect.DeepEqual(ds.OpenBlockers, []string{"which-db"}) {
		t.Fatalf("design-schema %+v, want blocked by which-db", ds)
	}
	if ds.EffectiveStatus != decision.EffectiveActive {
		t.Fatalf("effective_status %q must stay active", ds.EffectiveStatus)
	}
	// A blocker that is already a decision is settled.
	st := got["settled-target"]
	if st.Blocked || len(st.OpenBlockers) != 0 || !reflect.DeepEqual(st.BlockedBy, []string{"plain-blocker"}) {
		t.Fatalf("settled-target %+v", st)
	}

	// Superseding the hypothesis with another hypothesis keeps the block.
	addHypotheses(t, db, "which-db-2")
	link(t, db, decision.RelSupersedes, dec("which-db-2"), dec("which-db"))
	if d := loadByID(t, db)["design-schema"]; !d.Blocked || !reflect.DeepEqual(d.OpenBlockers, []string{"which-db"}) {
		t.Fatalf("after hypothesis replacement %+v, want still blocked", d)
	}
	// Superseding it with a decision clears the block; blocked_by remains.
	addAll(t, db, "use-sqlite")
	link(t, db, decision.RelSupersedes, dec("use-sqlite"), dec("which-db-2"))
	d := loadByID(t, db)["design-schema"]
	if d.Blocked || len(d.OpenBlockers) != 0 || !reflect.DeepEqual(d.BlockedBy, []string{"which-db"}) {
		t.Fatalf("after settling %+v, want not blocked", d)
	}
}

func TestBlockedFollowsEveryReplacementAndIgnoresRetracted(t *testing.T) {
	db := openDB(t)
	addHypotheses(t, db, "h", "h-alt", "drop-me")
	addAll(t, db, "target", "settled", "other-target")
	link(t, db, decision.RelBlocks, dec("h"), dec("target"))
	// Two replacements: one decision, one hypothesis. Any open head blocks.
	link(t, db, decision.RelSupersedes, dec("settled"), dec("h"))
	link(t, db, decision.RelSupersedes, dec("h-alt"), dec("h"))
	if d := loadByID(t, db)["target"]; !d.Blocked {
		t.Fatalf("%+v, want blocked: h-alt is still a hypothesis", d)
	}
	retract(t, db, "h-alt", "", "")
	if d := loadByID(t, db)["target"]; d.Blocked {
		t.Fatalf("%+v, want not blocked once the open head is retracted", d)
	}
	// A retracted blocker blocks nothing.
	link(t, db, decision.RelBlocks, dec("drop-me"), dec("other-target"))
	if d := loadByID(t, db)["other-target"]; !d.Blocked {
		t.Fatalf("%+v, want blocked", d)
	}
	retract(t, db, "drop-me", "", "")
	if d := loadByID(t, db)["other-target"]; d.Blocked || len(d.OpenBlockers) != 0 {
		t.Fatalf("%+v, want not blocked", d)
	}
}

func TestASupersededOrRetractedTargetIsNotBlocked(t *testing.T) {
	db := openDB(t)
	addHypotheses(t, db, "h")
	addAll(t, db, "t1", "t2", "t1-new")
	link(t, db, decision.RelBlocks, dec("h"), dec("t1"))
	link(t, db, decision.RelBlocks, dec("h"), dec("t2"))
	link(t, db, decision.RelSupersedes, dec("t1-new"), dec("t1"))
	retract(t, db, "t2", "", "")
	got := loadByID(t, db)
	for _, id := range []string{"t1", "t2"} {
		if got[id].Blocked {
			t.Fatalf("%s %+v must not be blocked", id, got[id])
		}
		if !reflect.DeepEqual(got[id].OpenBlockers, []string{"h"}) {
			t.Fatalf("%s open blockers %v, still listed", id, got[id].OpenBlockers)
		}
	}
}

func TestConflictedWhileBothSidesAreActive(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "use-sqlite", "use-postgres", "use-duckdb", "use-wal")
	link(t, db, decision.RelConflictsWith, dec("use-sqlite"), dec("use-postgres"))
	link(t, db, decision.RelConflictsWith, dec("use-duckdb"), dec("use-sqlite"))
	got := loadByID(t, db)
	if !got["use-sqlite"].Conflicted || !reflect.DeepEqual(got["use-sqlite"].ConflictsWith, []string{"use-postgres", "use-duckdb"}) {
		t.Fatalf("use-sqlite %+v", got["use-sqlite"])
	}
	if !got["use-postgres"].Conflicted || !reflect.DeepEqual(got["use-postgres"].ConflictsWith, []string{"use-sqlite"}) {
		t.Fatalf("use-postgres %+v", got["use-postgres"])
	}
	// Superseding one side and retracting the other clears both conflicts.
	link(t, db, decision.RelSupersedes, dec("use-wal"), dec("use-postgres"))
	retract(t, db, "use-duckdb", "", "")
	got = loadByID(t, db)
	for _, id := range []string{"use-sqlite", "use-postgres", "use-duckdb"} {
		if got[id].Conflicted {
			t.Fatalf("%s %+v must not be conflicted", id, got[id])
		}
	}
	if !reflect.DeepEqual(got["use-sqlite"].ConflictsWith, []string{"use-postgres", "use-duckdb"}) {
		t.Fatalf("conflicts_with must still list the pair: %v", got["use-sqlite"].ConflictsWith)
	}
	if got["use-sqlite"].EffectiveStatus != decision.EffectiveActive {
		t.Fatalf("effective_status %q", got["use-sqlite"].EffectiveStatus)
	}
}

func TestRefinedByKeepsTheTargetActive(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "record-rejections", "bucket-per-minute", "keep-7-days")
	link(t, db, decision.RelRefines, dec("bucket-per-minute"), dec("record-rejections"))
	link(t, db, decision.RelRefines, dec("keep-7-days"), dec("record-rejections"))
	d := loadByID(t, db)["record-rejections"]
	if d.EffectiveStatus != decision.EffectiveActive || !reflect.DeepEqual(d.RefinedBy, []string{"bucket-per-minute", "keep-7-days"}) {
		t.Fatalf("%+v", d)
	}
}

func TestChallengedFollowsTheLatestEvidence(t *testing.T) {
	db := openDB(t)
	seedSession(t, db)
	addAll(t, db, "a", "b", "c")
	link(t, db, decision.RelContradicts, session("sess-1"), dec("a"))
	link(t, db, decision.RelValidates, session("sess-1"), dec("b"))
	link(t, db, decision.RelContradicts, commit(sha), dec("b"))
	link(t, db, decision.RelContradicts, session("sess-1"), dec("c"))
	link(t, db, decision.RelValidates, commit(sha), dec("c"))
	got := loadByID(t, db)
	if !got["a"].Challenged || !got["b"].Challenged || got["c"].Challenged {
		t.Fatalf("challenged a=%v b=%v c=%v, want true true false", got["a"].Challenged, got["b"].Challenged, got["c"].Challenged)
	}
	retract(t, db, "a", "", "")
	if loadByID(t, db)["a"].Challenged {
		t.Fatal("a retracted decision is not challenged")
	}
}

// TestLegacyRowsThatBreakTheKindRulesStayReadable: an older sdf stored
// edges that today's rules refuse. They are listed as stored and drive no
// derived field.
func TestLegacyRowsThatBreakTheKindRulesStayReadable(t *testing.T) {
	db := openDB(t)
	seedSession(t, db)
	addAll(t, db, "a", "b")
	legacyEdge(t, db, decision.RelImplements, dec("a"), dec("b"))
	legacyEdge(t, db, decision.RelValidates, dec("a"), session("sess-1"))
	legacyEdge(t, db, decision.RelContradicts, dec("a"), commit(sha))
	legacyEdge(t, db, decision.RelConstrains, session("sess-1"), dec("a"))
	legacyEdge(t, db, decision.RelBlocks, session("sess-1"), dec("a"))
	legacyEdge(t, db, decision.RelBlocks, dec("ghost"), dec("a"))
	legacyEdge(t, db, decision.RelConflictsWith, dec("b"), dec("b"))
	legacyEdge(t, db, decision.RelRefines, commit(sha), dec("b"))
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Edges) != 8 {
		t.Fatalf("%d edges, want all 8 listed", len(g.Edges))
	}
	for _, d := range g.Decisions {
		if d.EffectiveStatus != decision.EffectiveActive || d.Blocked || d.Conflicted || d.Challenged ||
			len(d.BlockedBy)+len(d.ConflictsWith)+len(d.RefinedBy) != 0 {
			t.Fatalf("%s %+v: legacy rows must drive nothing", d.ID, d)
		}
	}
	var text strings.Builder
	if err := decision.WriteText(&text, g); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "\nimplements ") || !strings.Contains(text.String(), "\nrefines ") {
		t.Fatalf("legacy edge not listed:\n%s", text.String())
	}
}

func TestWriteTextShowsTheDerivedRelations(t *testing.T) {
	g := decision.Graph{Decisions: []decision.Record{
		{ID: "design-schema", Status: decision.StatusDecision, RecordedAt: "t0", Statement: "S", EffectiveStatus: decision.EffectiveActive,
			BlockedBy: []string{"which-db", "auth"}, OpenBlockers: []string{"which-db"}, Blocked: true,
			ConflictsWith: []string{"use-postgres"}, Conflicted: true, RefinedBy: []string{"fk-names"}, Challenged: true},
		{ID: "x", Status: decision.StatusDecision, RecordedAt: "t1", Statement: "X", EffectiveStatus: decision.EffectiveActive,
			BlockedBy: []string{"auth"}},
	}}
	var out strings.Builder
	if err := decision.WriteText(&out, g); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"active, blocked, conflicted, challenged",
		"blocked by: which-db, auth (open: which-db)",
		"conflicts with: use-postgres",
		"refined by: fk-names",
		"blocked by: auth (settled)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
}

func TestJSONOmitsTheNewFieldsWhenEmpty(t *testing.T) {
	var out strings.Builder
	if err := decision.WriteJSON(&out, decision.Graph{Decisions: []decision.Record{{ID: "a", Status: "decision", EffectiveStatus: "active"}}}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"blocked", "open_blockers", "conflicts_with", "conflicted", "refined_by", "challenged"} {
		if strings.Contains(out.String(), `"`+key+`"`) {
			t.Fatalf("%s present in %s", key, out.String())
		}
	}
}
