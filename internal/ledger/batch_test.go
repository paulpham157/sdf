package ledger_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/paulpham157/sdf/internal/ledger"
)

func item(kind ledger.Kind, id, payload string) ledger.Item {
	return ledger.Item{Kind: kind, ID: id, Canonical: []byte(payload), Hash: ledger.PayloadHash(kind, []byte(payload))}
}

func TestAppendAllChainsInOrder(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ledger.db"))
	ctx := context.Background()
	entries, err := s.AppendAll(ctx, []ledger.Item{
		item("sdf.evidence/v1", "e1", `{"n":1}`),
		item("sdf.evidence/v1", "e2", `{"n":2}`),
		item("sdf.verdict/v1", "v1", `{"v":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Seq != 1 || entries[2].Seq != 3 ||
		entries[1].PrevHash != entries[0].EntryHash || entries[2].PrevHash != entries[1].EntryHash {
		t.Fatalf("%+v", entries)
	}
	rep, err := s.Verify(ctx)
	if err != nil || !rep.OK || rep.Entries != 4 {
		t.Fatalf("%+v %v", rep, err)
	}
	// Repeating the same items returns the stored entries and adds nothing.
	again, err := s.AppendAll(ctx, []ledger.Item{item("sdf.evidence/v1", "e1", `{"n":1}`)})
	if err != nil || again[0] != entries[0] {
		t.Fatalf("%+v %v", again, err)
	}
	if empty, err := s.AppendAll(ctx, nil); err != nil || empty != nil {
		t.Fatalf("%v %v", empty, err)
	}
}

func TestAppendAllIsAllOrNothing(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ledger.db"))
	ctx := context.Background()
	appendOK(t, s, "sdf.verdict/v1", "v1", `{"v":1}`)
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The second item conflicts with a stored entry, so the first one,
	// which on its own would be fine, must not be stored either.
	_, err = s.AppendAll(ctx, []ledger.Item{
		item("sdf.evidence/v1", "e1", `{"n":1}`),
		item("sdf.verdict/v1", "v1", `{"v":2}`),
	})
	if !errors.Is(err, ledger.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	after, err := s.Head(ctx)
	if err != nil || after != head {
		t.Fatalf("head moved: %+v -> %+v (%v)", head, after, err)
	}
	if _, _, err := s.Get(ctx, "sdf.evidence/v1", "e1"); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("e1 stored: %v", err)
	}
	// A bad item is refused before the file is touched.
	bad := item("sdf.evidence/v1", "e2", `{}`)
	bad.Hash = "sha256:00"
	if _, err := s.AppendAll(ctx, []ledger.Item{item("sdf.evidence/v1", "e3", `{}`), bad}); !errors.Is(err, ledger.ErrHashMismatch) {
		t.Fatalf("want ErrHashMismatch, got %v", err)
	}
	if after, _ := s.Head(ctx); after != head {
		t.Fatal("head moved after a hash mismatch")
	}
}

func TestPrepareCreatesAnEmptyWritableLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "ledger.db")
	s := open(t, path)
	ctx := context.Background()
	if err := s.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Head(ctx); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("prepared ledger has a head: %v", err)
	}
	rep, err := s.Verify(ctx)
	if err != nil || !rep.OK || rep.Entries != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	// The first append after Prepare still writes genesis.
	e := appendOK(t, s, taskKind, "t1", `{}`)
	if e.Seq != 1 {
		t.Fatalf("%+v", e)
	}
	if err := s.Prepare(ctx); err != nil {
		t.Fatalf("prepare on a ledger with entries: %v", err)
	}
}

func TestPrepareFailsOnAFileThatIsNotALedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	junk := []byte("this is not a sqlite database, it is a hand-written test file\n")
	if err := os.WriteFile(path, junk, 0o600); err != nil {
		t.Fatal(err)
	}
	s := open(t, path)
	if err := s.Prepare(context.Background()); !errors.Is(err, ledger.ErrCorruptFile) {
		t.Fatalf("want ErrCorruptFile, got %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(junk) {
		t.Fatal("Prepare changed a file that is not a ledger")
	}
}

func TestPrepareFailsInAReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	path := filepath.Join(dir, "ledger.db")
	if err := open(t, path).Prepare(context.Background()); err == nil {
		t.Fatal("Prepare succeeded in a read-only directory")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file created: %v", err)
	}
}
