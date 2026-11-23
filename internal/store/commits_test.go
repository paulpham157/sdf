package store_test

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

// TestLoadCommitEventsMatchesKnownCommit: every SHA LoadCommitEvents returns
// is one KnownCommit accepts, and the rows it skips name no SHA KnownCommit
// would accept, so sdf decision audit counts exactly the commits sdf
// decision link can link.
func TestLoadCommitEventsMatchesKnownCommit(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const (
		hook    = "3f2a9c1e5b7d4a6c8e0f1a2b3c4d5e6f7a8b9c0d"
		upper   = "A1B2C3D4E5F60718293A4B5C6D7E8F9012345678"
		object  = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"
		encoded = "9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a291807"
	)
	nano := time.Date(2026, 11, 24, 11, 50, 0, 0, time.UTC).UnixNano()
	sess := "5d1c9b7e-2a4f-4e3b-8c6d-9f0a1b2c3d4e"
	recs := []record.Record{
		{Signal: "logs", Name: record.Str("sdf.git.commit"), SessionID: record.Str("grok-7f3c"), TimeUnixNano: &nano,
			Attributes: map[string]any{"vcs.ref.head.revision": hook, "sdf.git.repo": "/home/dev/sdf", "sdf.git.branch": "main"}},
		{Signal: "logs", Name: record.Str("sdf.git.commit"),
			Attributes: map[string]any{"vcs.ref.head.revision": "3f2a9c1"}}, // abbreviated: not a commit
		{Signal: "logs", Name: record.Str("claude_code.tool_result"), SessionID: &sess,
			Attributes: map[string]any{"tool_name": "Bash", "git_commit_id": upper}},
		{Signal: "logs", Name: record.Str("claude_code.tool_result"), SessionID: &sess,
			Attributes: map[string]any{"tool_name": "Bash", "tool_parameters": map[string]any{"git_commit_id": object}}},
		{Signal: "logs", Name: record.Str("claude_code.tool_result"), SessionID: &sess,
			Attributes: map[string]any{"tool_name": "Bash", "tool_parameters": `{"git_commit_id":"` + encoded + `"}`}},
		{Signal: "logs", Name: record.Str("claude_code.tool_result"), SessionID: &sess,
			Attributes: map[string]any{"tool_name": "Read", "tool_parameters": "not json"}},
		{Signal: "logs", Name: record.Str("claude_code.api_request"), SessionID: &sess,
			Attributes: map[string]any{"git_commit_id": hook}}, // not a commit event
	}
	for i := range recs {
		recs[i].Resource = map[string]any{}
	}
	if _, err := s.Insert(recs); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadCommitEvents()
	if err != nil {
		t.Fatal(err)
	}
	want := []store.CommitEvent{
		{Name: "sdf.git.commit", SHA: hook, SessionID: "grok-7f3c", Time: "2026-11-24T11:50:00.000000+00:00", Repo: "/home/dev/sdf", Branch: "main"},
		{Name: "claude_code.tool_result", SHA: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", SessionID: sess},
		{Name: "claude_code.tool_result", SHA: object, SessionID: sess},
		{Name: "claude_code.tool_result", SHA: encoded, SessionID: sess},
	}
	for i := 1; i < len(got) && i < len(want); i++ {
		want[i].Time = got[i].Time // received_at, set at insert
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	for _, ev := range got {
		if ev.Time == "" {
			t.Fatalf("%s has no time", ev.SHA)
		}
		if ok, err := s.KnownCommit(ev.SHA); err != nil || !ok {
			t.Fatalf("KnownCommit(%s) = %v, %v", ev.SHA, ok, err)
		}
	}
}
