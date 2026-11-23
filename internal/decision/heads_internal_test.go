package decision

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

// naiveHeads is the path-by-path walk headResolver replaced, kept as the
// reference: it visits every path, so it is exponential on diamonds, but
// it is obviously right on small graphs.
func naiveHeads(id string, next func(string) []string, alive func(string) bool, onPath map[string]bool, out map[string]bool) bool {
	if onPath[id] {
		return true
	}
	if !alive(id) {
		return false
	}
	nx := next(id)
	if len(nx) == 0 {
		out[id] = true
		return false
	}
	onPath[id] = true
	cyclic := false
	for _, n := range nx {
		if naiveHeads(n, next, alive, onPath, out) {
			cyclic = true
		}
	}
	delete(onPath, id)
	return cyclic
}

// The memoized resolver agrees with the naive walk on random supersedes
// graphs with cycles, diamonds, shared cycle members, and retractions,
// for every decision, whatever order the decisions are resolved in.
func TestHeadResolverMatchesTheNaiveWalk(t *testing.T) {
	rng := rand.New(rand.NewSource(278))
	for trial := 0; trial < 2000; trial++ {
		n := 2 + rng.Intn(9)
		ids := make([]string, n)
		for i := range ids {
			ids[i] = fmt.Sprintf("d%d", i)
		}
		edges := map[string][]string{}
		for k := rng.Intn(2 * n); k > 0; k-- {
			from, to := ids[rng.Intn(n)], ids[rng.Intn(n)]
			if from == to {
				continue
			}
			dup := false
			for _, x := range edges[from] {
				dup = dup || x == to
			}
			if !dup {
				edges[from] = append(edges[from], to)
			}
		}
		retracted := map[string]bool{}
		for _, id := range ids {
			retracted[id] = rng.Intn(5) == 0
		}
		next := func(id string) []string { return edges[id] }
		alive := func(id string) bool { return !retracted[id] }
		r := newHeadResolver(next, alive)
		order := rng.Perm(n)
		for _, i := range order {
			id := ids[i]
			want := map[string]bool{}
			wantCyclic := naiveHeads(id, next, alive, map[string]bool{}, want)
			got := r.resolve(id)
			gotSet := map[string]bool{}
			for _, h := range got.heads {
				if gotSet[h] {
					t.Fatalf("trial %d %s: head %s listed twice: %v", trial, id, h, got.heads)
				}
				gotSet[h] = true
			}
			if got.cyclic != wantCyclic || !reflect.DeepEqual(gotSet, want) {
				t.Fatalf("trial %d %s (edges %v, retracted %v): got %v cyclic=%v, want %v cyclic=%v",
					trial, id, edges, retracted, sorted(gotSet), got.cyclic, sorted(want), wantCyclic)
			}
		}
	}
}

func sorted(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ladder builds a supersedes diamond ladder of the given depth: each level
// has two decisions that supersede the previous join and one join that
// supersedes both. next maps a decision to those that supersede it.
func ladder(levels int) map[string][]string {
	next := map[string][]string{}
	prev := "j0"
	for i := 1; i <= levels; i++ {
		p, q, j := fmt.Sprintf("p%d", i), fmt.Sprintf("q%d", i), fmt.Sprintf("j%d", i)
		next[prev] = []string{p, q}
		next[p] = []string{j}
		next[q] = []string{j}
		prev = j
	}
	return next
}

func BenchmarkHeadResolverDiamondLadder(b *testing.B) {
	for _, levels := range []int{26, 1000} {
		next := ladder(levels)
		b.Run(fmt.Sprintf("levels=%d", levels), func(b *testing.B) {
			for b.Loop() {
				r := newHeadResolver(func(id string) []string { return next[id] }, func(string) bool { return true })
				if info := r.resolve("j0"); len(info.heads) != 1 || info.cyclic {
					b.Fatalf("%+v", info)
				}
			}
		})
	}
}
