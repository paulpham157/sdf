package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

func TestRecordGenerationAdvancesOncePerInsert(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	snap, ok, err := s.RecordSnapshot()
	if err != nil || !ok || snap.Generation != 0 || snap.MaxID != 0 {
		t.Fatalf("empty snapshot %+v ok %v err %v", snap, ok, err)
	}
	if _, err := s.Insert([]record.Record{logRow("a"), logRow("b")}); err != nil {
		t.Fatal(err)
	}
	snap, ok, err = s.RecordSnapshot()
	if err != nil || !ok || snap.Generation != 1 || snap.MaxID != 2 {
		t.Fatalf("after two rows %+v ok %v err %v", snap, ok, err)
	}
	if _, err := s.Insert([]record.Record{logRow("c")}); err != nil {
		t.Fatal(err)
	}
	snap, ok, err = s.RecordSnapshot()
	if err != nil || !ok || snap.Generation != 2 || snap.MaxID != 3 {
		t.Fatalf("after third row %+v ok %v err %v", snap, ok, err)
	}
	recs, loaded, lok, err := s.LoadSnapshot()
	if err != nil || !lok || loaded != snap || len(recs) != 3 {
		t.Fatalf("LoadSnapshot %+v ok %v len %d err %v", loaded, lok, len(recs), err)
	}
	plain, err := s.Load()
	if err != nil || len(plain) != len(recs) || plain[2].ID != recs[2].ID {
		t.Fatalf("Load and LoadSnapshot diverged: %v %d", err, len(plain))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, ok, err := reopened.RecordSnapshot()
	if err != nil || !ok || again != snap {
		t.Fatalf("reopen reset the generation: %+v ok %v err %v, want %+v", again, ok, err, snap)
	}
}

func TestRecordGenerationAdvancesOnScrub(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.KeepContent(func(record.Record) bool { return true })
	body := "hello-secret-body"
	if _, err := s.Insert([]record.Record{{
		Signal:     "logs",
		Name:       record.Str("claude_code.user_prompt"),
		SessionID:  record.Str("s"),
		Body:       &body,
		Attributes: map[string]any{"prompt": "secret"},
		Resource:   map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}
	before, ok, err := s.RecordSnapshot()
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if _, err := s.Scrub(true); err != nil {
		t.Fatal(err)
	}
	dry, ok, err := s.RecordSnapshot()
	if err != nil || !ok || dry != before {
		t.Fatalf("dry run changed the snapshot: %+v want %+v (%v)", dry, before, err)
	}
	res, err := s.Scrub(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed == 0 {
		t.Fatal("scrub changed nothing")
	}
	after, ok, err := s.RecordSnapshot()
	if err != nil || !ok || after.Generation != before.Generation+1 || after.MaxID != before.MaxID {
		t.Fatalf("after scrub %+v, before %+v, err %v", after, before, err)
	}
}

func TestRecordSnapshotWithoutRevisionTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO records (received_at, signal, attributes_json, resource_json) VALUES ('t', 'logs', '{}', '{}')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok, err := s.RecordSnapshot(); err != nil || ok {
		t.Fatalf("snapshot ok %v err %v, want not cacheable", ok, err)
	}
	recs, _, ok, err := s.LoadSnapshot()
	if err != nil || ok || len(recs) != 1 {
		t.Fatalf("LoadSnapshot ok %v len %d err %v", ok, len(recs), err)
	}
}

func logRow(session string) record.Record {
	return record.Record{
		Signal:     "logs",
		Name:       record.Str("claude_code.api_request"),
		SessionID:  &session,
		Attributes: map[string]any{"session.id": session},
		Resource:   map[string]any{},
	}
}
