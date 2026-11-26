package decision

import (
	"bytes"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/store"
)

func TestCheckPushClassifiesCommits(t *testing.T) {
	sha := func(c byte) string { return strings.Repeat(string(c), 40) }
	g := Graph{
		Decisions: []Record{
			{ID: "live", EffectiveStatus: EffectiveActive},
			{ID: "old", EffectiveStatus: EffectiveSuperseded},
			{ID: "gone", EffectiveStatus: EffectiveRetracted},
			{ID: "other", EffectiveStatus: EffectiveActive},
		},
		Edges: []Edge{
			{Relation: RelImplements, From: Endpoint{KindCommit, sha('a')}, To: Endpoint{KindDecision, "live"}},
			{Relation: RelImplements, From: Endpoint{KindCommit, sha('b')}, To: Endpoint{KindDecision, "old"}},
			{Relation: RelImplements, From: Endpoint{KindCommit, sha('c')}, To: Endpoint{KindDecision, "gone"}},
			{Relation: RelValidates, From: Endpoint{KindCommit, sha('d')}, To: Endpoint{KindDecision, "live"}},
			{Relation: RelImplements, From: Endpoint{KindSession, "s1"}, To: Endpoint{KindDecision, "live"}},
			{Relation: RelImplements, From: Endpoint{KindSession, "s1"}, To: Endpoint{KindDecision, "gone"}},
		},
	}
	events := []store.CommitEvent{
		{SHA: sha('a'), SessionID: "s1"},
		{SHA: sha('b')},
		{SHA: sha('c'), SessionID: "s1"},
		{SHA: sha('d'), SessionID: "s1"},
		{SHA: sha('e'), SessionID: "s2"},
	}
	commits := []PushCommit{
		{SHA: strings.ToUpper(sha('a'))}, // implements an active decision
		{SHA: sha('b')},                  // implements a superseded one: still covered
		{SHA: sha('c')},                  // implements only a retracted one
		{SHA: sha('d')},                  // validates is not implements
		{SHA: sha('e'), Trailers: []string{" other "}},          // trailer naming a stored decision
		{SHA: sha('f'), Trailers: []string{"nope", "gone", ""}}, // unknown to SDF, trailers only
		{SHA: sha('9')}, // unknown to SDF, no trailer: not judged
	}
	r := checkPush(g, events, commits)
	if r.Pushed != 7 || r.Recorded != 6 || r.Covered != 3 || len(r.Missing) != 3 {
		t.Fatalf("%+v", r)
	}
	c, d, f := r.Missing[0], r.Missing[1], r.Missing[2]
	if c.SHA != sha('c') || strings.Join(c.Retracted, ",") != "gone" || strings.Join(c.SessionDecisions, ",") != "live" || strings.Join(c.Sessions, ",") != "s1" {
		t.Fatalf("c %+v", c)
	}
	if d.SHA != sha('d') || len(d.Retracted) != 0 || strings.Join(d.SessionDecisions, ",") != "live" {
		t.Fatalf("d %+v", d)
	}
	if f.SHA != sha('f') || strings.Join(f.UnknownTrailers, ",") != "nope" || strings.Join(f.Retracted, ",") != "gone" || len(f.Sessions) != 0 {
		t.Fatalf("f %+v", f)
	}

	var buf bytes.Buffer
	if err := WritePushText(&buf, r, " --db /tmp/x.sqlite", true); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	for _, want := range []string{
		"3 of 6 pushed commits recorded in SDF implement no decision",
		"--db /tmp/x.sqlite --relation implements --from commit:" + sha('d') + " --to decision:live",
		"--from commit:" + sha('f') + " --to decision:ID",
		"SDF-Decision trailer names nope",
		"Push blocked",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
}

func TestWritePushTextQuietUnlessSomethingWasRecorded(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePushText(&buf, PushReport{Pushed: 4}, "", false); err != nil || buf.Len() != 0 {
		t.Fatalf("%v %q", err, buf.String())
	}
	if err := WritePushText(&buf, PushReport{Pushed: 4, Recorded: 2, Covered: 2}, "", false); err != nil ||
		buf.String() != "sdf: pre-push: 2 of 4 pushed commits are recorded in SDF, and each implements a decision.\n" {
		t.Fatalf("%v %q", err, buf.String())
	}
	buf.Reset()
	r := PushReport{Pushed: 1, Recorded: 1, Missing: []PushMissing{{SHA: strings.Repeat("a", 40), UnknownTrailers: []string{"x\x1b[31m"}}}}
	if err := WritePushText(&buf, r, "", false); err != nil || strings.Contains(buf.String(), "\x1b") || !strings.Contains(buf.String(), "warn mode") {
		t.Fatalf("%v %q", err, buf.String())
	}
}
