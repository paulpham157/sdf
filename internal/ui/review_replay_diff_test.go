package ui_test

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/decisiongraph"
	"github.com/paulpham157/sdf/internal/store"
)

// replayOp is one store write; each becomes one replay event.
type replayOp func(t *testing.T, db *store.Store)

func opAdd(id, status, by string) replayOp {
	return func(t *testing.T, db *store.Store) {
		t.Helper()
		if _, err := decision.Add(db, decision.RecordInput{ID: id, Statement: "Decision " + id + ".", Status: status, DecidedBy: by}); err != nil {
			t.Fatal(err)
		}
	}
}

func opLink(rel, from, to string) replayOp {
	return func(t *testing.T, db *store.Store) {
		t.Helper()
		if _, err := decision.Link(db, decision.EdgeInput{Relation: rel,
			From: decision.Endpoint{Kind: decision.KindDecision, Ref: from},
			To:   decision.Endpoint{Kind: decision.KindDecision, Ref: to}}); err != nil {
			t.Fatal(err)
		}
	}
}

func opRetract(id string) replayOp {
	return func(t *testing.T, db *store.Store) {
		t.Helper()
		if _, err := decision.Retract(db, decision.RetractInput{ID: id, Reason: "withdrawn"}); err != nil {
			t.Fatal(err)
		}
	}
}

// replayScenario exercises every recomputed rule: a hypothesis blocker
// settled by a supersede, a retracted blocker, conflicts and contradicting
// evidence cleared by retracting their source, a refinement, and a
// superseded decision.
func replayScenario() []replayOp {
	return []replayOp{
		opAdd("a", decision.StatusDecision, "user"),
		opAdd("h", decision.StatusHypothesis, "agent"),
		opLink("blocks", "h", "a"),
		opAdd("b", decision.StatusDecision, "agent"),
		opLink("constrains", "b", "a"),
		opAdd("c", decision.StatusDecision, "user"),
		opLink("supersedes", "c", "h"),
		opAdd("x", decision.StatusDecision, "agent"),
		opLink("conflicts_with", "x", "a"),
		opLink("contradicts", "x", "b"),
		opRetract("x"),
		opAdd("r", decision.StatusDecision, "agent"),
		opLink("refines", "r", "b"),
		opAdd("y", decision.StatusHypothesis, "agent"),
		opLink("blocks", "y", "b"),
		opAdd("z", decision.StatusDecision, "user"),
		opLink("supersedes", "z", "c"),
		opRetract("y"),
	}
}

func applyOps(t *testing.T, db *store.Store, ops []replayOp) {
	t.Helper()
	for _, op := range ops {
		op(t, db)
		// Keep events in distinct milliseconds: the replay orders by
		// Date.parse, which has millisecond resolution.
		time.Sleep(3 * time.Millisecond)
	}
}

// expectedAt is what the server computes for a store holding only the
// first k writes: each decision's role, and the chapter facts summed.
func expectedAt(t *testing.T, ops []replayOp) (map[string]string, [5]int) {
	t.Helper()
	db := openDB(t)
	applyOps(t, db, ops)
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	view := decisiongraph.Build(g, nil)
	roles := map[string]string{}
	var facts [5]int
	for _, ch := range view.Chapters {
		facts[0] += ch.Facts.Locked
		facts[1] += ch.Facts.Conflicts
		facts[2] += ch.Facts.Challenged
		facts[3] += ch.Explored.Decided
		facts[4] += ch.Explored.Total
		for _, n := range ch.Nodes {
			if n.Kind == "decision" {
				roles[n.ID] = n.Role
			}
		}
	}
	return roles, facts
}

var replayNodeTag = regexp.MustCompile(`<g [^>]*class="dg-node dg-kind-decision[^"]*"[^>]*>`)
var replayRole = regexp.MustCompile(`dg-role-([a-z]+)`)
var replayID = regexp.MustCompile(`data-node="([^"]+)"`)

func factCount(text, word string) int {
	m := regexp.MustCompile(`(\d+) ` + word).FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// At every replay point k, the page's recomputed roles and facts match what
// the server computes for a store that holds only the first k writes.
func TestReviewReplayMatchesServerPrefix(t *testing.T) {
	ops := replayScenario()
	db := openDB(t)
	applyOps(t, db, ops)
	srv := startUI(t, db)
	for k := 1; k <= len(ops); k++ {
		want, wantFacts := expectedAt(t, ops[:k])
		page := renderPage(t, srv, fmt.Sprintf("#/decisions/ch-a?at=%d", k), "")
		if !strings.Contains(page.Markup, fmt.Sprintf(`aria-valuenow="%d"`, k)) {
			t.Fatalf("at=%d: slider not at %d", k, k)
		}
		got := map[string]string{}
		for _, tag := range replayNodeTag.FindAllString(page.Markup, -1) {
			if !strings.Contains(tag, "dg-future") {
				got[replayID.FindStringSubmatch(tag)[1]] = replayRole.FindStringSubmatch(tag)[1]
			}
		}
		for id, role := range want {
			if got[id] != role {
				t.Errorf("at=%d: %s role %q, server prefix says %q", k, id, got[id], role)
			}
		}
		for id := range got {
			if _, ok := want[id]; !ok {
				t.Errorf("at=%d: %s drawn but not recorded yet", k, id)
			}
		}
		panel := regexp.MustCompile(`(?s)<aside [^>]*class="dg-panel".*?</aside>`).FindString(page.Markup)
		gotFacts := [5]int{factCount(panel, "locked"), factCount(panel, "conflicts?"), factCount(panel, "challenged")}
		if m := regexp.MustCompile(`(\d+) of (\d+) choice points? decided`).FindStringSubmatch(panel); m != nil {
			gotFacts[3], _ = strconv.Atoi(m[1])
			gotFacts[4], _ = strconv.Atoi(m[2])
		}
		if gotFacts != wantFacts {
			t.Errorf("at=%d: locked/conflicts/challenged/decided/total %v, server prefix says %v", k, gotFacts, wantFacts)
		}
	}
}
