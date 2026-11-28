package decision

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func active(id, statement, by string, paths ...string) Record {
	return Record{
		ID: id, Statement: statement, Status: StatusDecision,
		EffectiveStatus: EffectiveActive, DecidedBy: by, DecidedBySource: DecidedBySourceFlag,
		Paths: paths,
	}
}

func TestCheckTouchesStatuses(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	path := PushPath{SHA: sha, Path: "internal/store/store.go"}
	g := Graph{Decisions: []Record{
		active("no-cgo", "Do not add cgo to the store", "ada", "internal/store/**"),
		{ID: "gone", Statement: "retracted", Status: StatusDecision, EffectiveStatus: EffectiveRetracted, Paths: []string{"internal/store/**"}},
		{ID: "old", Statement: "superseded", Status: StatusDecision, EffectiveStatus: EffectiveSuperseded, Paths: []string{"**/*.go"}},
		{ID: "guess", Statement: "hypothesis", Status: StatusHypothesis, EffectiveStatus: EffectiveActive, Paths: []string{"**/*.go"}},
		active("other", "elsewhere", "ada", "docs/**"),
	}}
	got := CheckTouches(g, []PushPath{path})
	if len(got.Decisions) != 1 || got.Decisions[0].ID != "no-cgo" || !got.Decisions[0].Locked {
		t.Fatalf("%+v", got)
	}
	agent := g
	agent.Decisions[0].DecidedBy = "agent"
	if CheckTouches(agent, []PushPath{path}).Decisions[0].Locked {
		t.Fatal("agent decision labelled locked")
	}
}

func TestCheckTouchesRefinesDoesNotInherit(t *testing.T) {
	g := Graph{
		Decisions: []Record{
			active("parent", "parent rule", "ada", "docs/**"),
			active("child", "child rule", "ada", "internal/**"),
		},
		Edges: []Edge{{
			Relation: RelRefines,
			From:     Endpoint{Kind: KindDecision, Ref: "child"},
			To:       Endpoint{Kind: KindDecision, Ref: "parent"},
		}},
	}
	got := CheckTouches(g, []PushPath{{SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Path: "internal/a.go"}})
	if len(got.Decisions) != 1 || got.Decisions[0].ID != "child" {
		t.Fatalf("%+v", got)
	}
	both := CheckTouches(g, []PushPath{
		{SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Path: "internal/a.go"},
		{SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Path: "docs/a.md"},
	})
	if len(both.Decisions) != 2 || both.Decisions[0].ID != "parent" || both.Decisions[1].ID != "child" {
		t.Fatalf("%+v", both)
	}
}

func TestCheckTouchesLinkedSuppression(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	other := "fedcba9876543210fedcba9876543210fedcba98"
	g := Graph{
		Decisions: []Record{
			active("no-cgo", "no cgo", "ada", "internal/**"),
			active("style", "style", "ada", "**/*.go"),
		},
		Edges: []Edge{{
			Relation: RelImplements,
			From:     Endpoint{Kind: KindCommit, Ref: strings.ToUpper(sha)},
			To:       Endpoint{Kind: KindDecision, Ref: "no-cgo"},
		}},
	}
	got := CheckTouches(g, []PushPath{{
		SHA: sha, Path: "internal/a.go", Trailers: []string{"style"},
	}})
	if len(got.Decisions) != 0 {
		t.Fatalf("linked and trailer should suppress both, got %+v", got)
	}
	left := CheckTouches(g, []PushPath{{SHA: other, Path: "internal/a.go"}})
	if len(left.Decisions) != 2 {
		t.Fatalf("%+v", left)
	}
	only := CheckTouches(g, []PushPath{{SHA: sha, Path: "internal/a.go"}})
	if len(only.Decisions) != 1 || only.Decisions[0].ID != "style" {
		t.Fatalf("implements suppresses only no-cgo, got %+v", only)
	}
}

func TestWriteTouchTextSamplesAndEscapes(t *testing.T) {
	sha := "1a2b3c4d5e6f7890abcdef1234567890abcdef12"
	matches := []TouchMatch{
		{Glob: "internal/store/**", Path: "internal/store/store.go", SHA: sha},
		{Glob: "internal/store/**", Path: "internal/store/a.go", SHA: sha},
		{Glob: "internal/store/**", Path: "a\nb\x1b", SHA: sha},
		{Glob: "internal/store/**", Path: "internal/store/c.go", SHA: sha},
		{Glob: "internal/store/**", Path: "internal/store/d.go", SHA: sha},
	}
	var buf bytes.Buffer
	err := WriteTouchText(&buf, TouchReport{Decisions: []TouchDecision{{
		ID: "no-cgo", Statement: "Do not add cgo to the store", Locked: true, Matches: matches,
	}}}, TouchForPush(true))
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	wantHead := "sdf: pre-push: 1 pushed commit touches paths scoped by 1 active decision:\n"
	if !strings.HasPrefix(got, wantHead) {
		t.Fatalf("%q", got)
	}
	if !strings.Contains(got, `  decision:no-cgo (locked) "Do not add cgo to the store"`) {
		t.Fatalf("%q", got)
	}
	if !strings.Contains(got, "internal/store/** matches internal/store/store.go (commit 1a2b3c4d), internal/store/a.go (commit 1a2b3c4d)") {
		t.Fatalf("%q", got)
	}
	if !strings.Contains(got, ", and 2 more\n") {
		t.Fatalf("%q", got)
	}
	if strings.Contains(got, "\x1b") || strings.Contains(got, "a\nb") {
		t.Fatalf("raw control in output: %q", got)
	}
	if !strings.Contains(got, `\n`) || !strings.Contains(got, `\x1b`) {
		t.Fatalf("quoted control missing: %q", got)
	}
	if !strings.Contains(got, "Touching is not a violation; link the commit if it follows the decision (SDF-Decision: no-cgo). The push continues. Silence: git config sdf.prePushTouches off.\n") {
		t.Fatalf("%q", got)
	}
	buf.Reset()
	if err := WriteTouchText(&buf, TouchReport{Decisions: []TouchDecision{{
		ID: "no-cgo", Statement: "Do not add cgo", Locked: true, Matches: matches[:1],
	}}}, TouchForPush(false)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "The push continues.") {
		t.Fatalf("%q", buf.String())
	}
}

func TestCheckTouchesPerf(t *testing.T) {
	// A decision stores at most 32 globs. Two decisions hold 50 patterns,
	// and the matching one is last, so every path tries all 50. 20_000
	// paths is the pre-push cap, spread across 1_000 commits.
	miss := func(n int, prefix string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = prefix + two(i) + "/**"
		}
		return out
	}
	first := active("wide", "wide", "ada", miss(32, "zz-nomatch-")...)
	secondGlobs := append(miss(17, "yy-nomatch-"), "**/*.go")
	second := active("hit", "hit", "ada", secondGlobs...)
	pushed := make([]PushPath, 20000)
	for i := range pushed {
		pushed[i] = PushPath{
			SHA:  strings.Repeat("0123456789abcdef", 3)[:37] + three(i%1000),
			Path: fmt.Sprintf("src/f%d.go", i),
		}
	}
	start := time.Now()
	got := CheckTouches(Graph{Decisions: []Record{first, second}}, pushed)
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	if len(got.Decisions) != 1 || got.Decisions[0].ID != "hit" || len(got.Decisions[0].Matches) != 20000 {
		n := 0
		if len(got.Decisions) == 1 {
			n = len(got.Decisions[0].Matches)
		}
		t.Fatalf("%d decisions, matches %d", len(got.Decisions), n)
	}
}

func two(i int) string {
	const d = "0123456789"
	return string([]byte{d[i/10], d[i%10]})
}

func three(i int) string {
	const d = "0123456789abcdef"
	return string([]byte{d[(i/256)%16], d[(i/16)%16], d[i%16]})
}
