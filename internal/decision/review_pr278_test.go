//go:build reviewbugs

package decision_test

// Review test for PR #278 (1001dc5). It FAILS (times out) on 1001dc5 and
// passes in well under a millisecond on c3fc1f2, whose heads() kept one
// visited set. Run: go test -tags reviewbugs -run PR278 ./internal/decision/

import (
	"fmt"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
)

// A supersedes "diamond ladder": each level has two decisions that both
// supersede the previous join, and one join that supersedes both. It is a
// valid graph (no cycle), so Link accepts every edge. heads() in
// resolveRelations walks every path without memoizing, so the number of
// paths, and Load's time, doubles with each level.
func TestReviewPR278LoadStaysFastOnASupersedesDiamondLadder(t *testing.T) {
	const levels = 26
	db := openDB(t)
	addHypotheses(t, db, "j0")
	addAll(t, db, "target")
	link(t, db, decision.RelBlocks, dec("j0"), dec("target"))
	prev := "j0"
	for i := 1; i <= levels; i++ {
		p, q, j := fmt.Sprintf("p%d", i), fmt.Sprintf("q%d", i), fmt.Sprintf("j%d", i)
		addHypotheses(t, db, p, q, j)
		link(t, db, decision.RelSupersedes, dec(p), dec(prev))
		link(t, db, decision.RelSupersedes, dec(q), dec(prev))
		link(t, db, decision.RelSupersedes, dec(j), dec(p))
		link(t, db, decision.RelSupersedes, dec(j), dec(q))
		prev = j
	}
	done := make(chan decision.Record, 1)
	start := time.Now()
	go func() { done <- loadByID(t, db)["target"] }()
	select {
	case d := <-done:
		t.Logf("Load took %v", time.Since(start))
		if !d.Blocked {
			t.Fatalf("%+v: the only head is a hypothesis, want blocked", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Load did not return within 5s on a %d-level supersedes diamond ladder (%d paths)", levels, 1<<levels)
	}
}
