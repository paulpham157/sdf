package store

import (
	"path/filepath"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
)

// legacyDB writes the leak samples with their content, as an sdf from before
// the default content drop did, in WAL mode, and clears the policy stamp.
func legacyDB(t *testing.T) (string, *Store) {
	t.Helper()
	db, _, s := insertLeakSamples(t, func(record.Record) bool { return true })
	if _, err := s.db.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		t.Fatal(err)
	}
	// One more content row lands in the WAL.
	s.KeepContent(func(record.Record) bool { return true })
	if _, err := s.Insert([]record.Record{{Signal: "logs", Name: record.Str("claude_code.user_prompt"), ServiceName: record.Str("claude-code"),
		Attributes: map[string]any{"prompt": "SDF-LEAK-WAL-prompt"}, Resource: map[string]any{}}}); err != nil {
		t.Fatal(err)
	}
	s.KeepContent(nil)
	return db, s
}

func TestOpenNeverScrubsAndStampsOnlyNewDatabases(t *testing.T) {
	fresh, err := Open(filepath.Join(t.TempDir(), "new.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if needs, err := fresh.NeedsScrub(); err != nil || needs {
		t.Fatalf("a new database needs no scrub: %v %v", needs, err)
	}
	_ = fresh.Close()
	db, s := legacyDB(t)
	defer s.Close()
	if needs, err := s.NeedsScrub(); err != nil || !needs {
		t.Fatalf("legacy database: %v %v", needs, err)
	}
	if !dbContains(t, db, "SDF-LEAK-01-") || !dbContains(t, db, "SDF-LEAK-WAL-prompt") {
		t.Fatal("Open changed stored rows")
	}
}

func TestScrubRemovesStoredContent(t *testing.T) {
	db, s := legacyDB(t)
	defer s.Close()
	dry, err := s.Scrub(true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Changed == 0 || dry.Rows < dry.Changed || !dbContains(t, db, "SDF-LEAK-") {
		t.Fatalf("dry run %+v wrote or found nothing", dry)
	}
	res, err := s.Scrub(false)
	if err != nil {
		t.Fatal(err)
	}
	if res != dry {
		t.Fatalf("scrub %+v, dry run %+v", res, dry)
	}
	if dbContains(t, db, "SDF-LEAK-") {
		t.Fatal("content left in the database files (main file, WAL, or free pages)")
	}
	if needs, err := s.NeedsScrub(); err != nil || needs {
		t.Fatalf("still needs scrub: %v %v", needs, err)
	}
	known, err := s.KnownCommit("0123456789abcdef0123456789abcdef01234567")
	if err != nil || !known {
		t.Fatalf("commit lost: %v %v", known, err)
	}
	rows, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	tokens := 0
	for _, rec := range rows {
		if rec.Attributes[record.ContentKept] != nil {
			t.Fatalf("kept mark left: %v", rec.Attributes)
		}
		if n, ok := rec.Attributes["input_tokens"].(int64); ok && n == 1200 {
			tokens++
		}
	}
	if tokens != 5 {
		t.Fatalf("%d log rows kept input_tokens", tokens)
	}
	again, err := s.Scrub(false)
	if err != nil || again.Changed != 0 {
		t.Fatalf("second scrub %+v %v", again, err)
	}
}
