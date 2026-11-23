package ledger_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/ledger"
)

func verifyFails(t *testing.T, path string, seq int64, problem ledger.Problem, detail string) {
	t.Helper()
	rep, err := open(t, path).Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Failure == nil {
		t.Fatalf("not detected: %+v", rep)
	}
	f := *rep.Failure
	if f.Seq != seq || f.Problem != problem || !strings.Contains(f.Detail, detail) {
		t.Fatalf("failure %+v, want seq %d %s %q", f, seq, problem, detail)
	}
}

func TestAppendRejectsInvalidKindsAndIDs(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir()+"/ledger.db")
	for _, tc := range []struct {
		kind ledger.Kind
		id   string
	}{
		{"", "x"},
		{taskKind, ""},
		{"sdf.task/v1\xff", "x"},
		{taskKind, "a\xffb"},
		{"a\nb", "x"},
		{"a\tb", "x"},
		{"a\x7fb", "x"},
		{"a\u0085b", "x"},
		{ledger.GenesisKind, ledger.GenesisID},
		{"sdf.ledger.anything/v1", "x"},
	} {
		p := []byte("{}")
		_, err := s.Append(ctx, tc.kind, tc.id, p, ledger.PayloadHash(tc.kind, p))
		if !errors.Is(err, ledger.ErrInvalid) {
			t.Fatalf("%q %q: %v", tc.kind, tc.id, err)
		}
	}
	if rep, _ := s.Verify(ctx); !rep.Empty {
		t.Fatalf("a rejected append wrote: %+v", rep)
	}
	// "sdf.ledgerx" is not the reserved prefix.
	appendOK(t, s, "sdf.ledgerx/v1", "x", "{}")
}

// "a" + "\n" + "b\n{}" and "a\nb" + "\n" + "{}" are the same bytes, so the
// payload hash alone cannot tell the two apart. A kind with "\n" is never
// written, and Verify flags a row that has one.
func TestKindNewlineCollision(t *testing.T) {
	if ledger.PayloadHash("a", []byte("b\n{}")) != ledger.PayloadHash("a\nb", []byte("{}")) {
		t.Fatal("expected the formula to collide; this test documents why kinds may not contain \\n")
	}
	path, s := seeded(t)
	e := appendOK(t, s, "a", "x", "b\n{}")
	_ = s.Close()
	forged := e
	forged.Kind = "a\nb"
	forged.EntryHash = ledger.EntryHash(forged)
	tamper(t, path, `UPDATE ledger_entries SET kind = ?, payload = ?, entry_hash = ? WHERE seq = ?`,
		string(forged.Kind), []byte("{}"), forged.EntryHash, e.Seq)
	verifyFails(t, path, e.Seq, ledger.ProblemModified, "control character")
}

func TestVerifyFlagsAReservedKind(t *testing.T) {
	path, s := seeded(t)
	head, _ := s.Head(context.Background())
	_ = s.Close()
	forged := head
	forged.Kind = "sdf.ledger.admin/v1"
	payload := []byte("{}")
	forged.Hash = ledger.PayloadHash(forged.Kind, payload)
	forged.EntryHash = ledger.EntryHash(forged)
	tamper(t, path, `UPDATE ledger_entries SET kind = ?, payload = ?, hash = ?, entry_hash = ? WHERE seq = ?`,
		string(forged.Kind), payload, forged.Hash, forged.EntryHash, head.Seq)
	verifyFails(t, path, head.Seq, ledger.ProblemModified, "reserved prefix")
}

// JSON cannot carry invalid UTF-8, so an id "a\xffb" and "a\uFFFDb" would
// encode the same and share an entry hash. Append never writes either (the
// id rules are ASCII), and Verify refuses a stored one instead of hashing
// it, so a pinned head does not hide the swap.
func TestInvalidUTF8IDIsNotHiddenByTheEntryHash(t *testing.T) {
	ctx := context.Background()
	a, b := ledger.Entry{Seq: 1, Kind: taskKind, ID: "a\uFFFDb"}, ledger.Entry{Seq: 1, Kind: taskKind, ID: "a\xffb"}
	if ledger.EntryHash(a) != ledger.EntryHash(b) {
		t.Fatal("expected the lossy encoding to collide; this test documents why ids are checked")
	}
	path, s := seeded(t)
	e := appendOK(t, s, taskKind, "aXb", `{}`)
	_ = s.Close()
	tamper(t, path, `UPDATE ledger_entries SET id = CAST(X'61ff62' AS TEXT) WHERE seq = ?`, e.Seq)
	verifyFails(t, path, e.Seq, ledger.ProblemModified, "not valid UTF-8")
	if _, _, err := open(t, path).Get(ctx, taskKind, "a\xffb"); !errors.Is(err, ledger.ErrInvalidID) {
		t.Fatalf("get: %v", err)
	}
}

func TestVerifyFlagsANonTextColumn(t *testing.T) {
	path, s := seeded(t)
	_ = s.Close()
	tamper(t, path, `UPDATE ledger_entries SET id = CAST(id AS BLOB) WHERE seq = 2`)
	verifyFails(t, path, 2, ledger.ProblemModified, "not TEXT")
}

func TestVerifyChecksGenesisExactly(t *testing.T) {
	path, s := seeded(t)
	_ = s.Close()
	payload := []byte(`{"schema":"other"}`)
	tamper(t, path, `UPDATE ledger_entries SET payload = ?, hash = ? WHERE seq = 0`, payload, ledger.PayloadHash(ledger.GenesisKind, payload))
	verifyFails(t, path, 0, ledger.ProblemModified, "not the genesis entry")
}

func TestVerifyFlagsADuplicateKindAndID(t *testing.T) {
	path, s := seeded(t)
	head, _ := s.Head(context.Background())
	_, first, err := s.Get(context.Background(), taskKind, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	// Rebuild the table without its unique index, then chain a second
	// task-1 correctly after the head.
	tamper(t, path, `
		ALTER TABLE ledger_entries RENAME TO old;
		CREATE TABLE ledger_entries (seq INTEGER PRIMARY KEY, kind TEXT NOT NULL, id TEXT NOT NULL,
			payload BLOB NOT NULL, hash TEXT NOT NULL, prev_hash TEXT NOT NULL, entry_hash TEXT NOT NULL, chained_at TEXT NOT NULL);
		INSERT INTO ledger_entries SELECT * FROM old;
		DROP TABLE old;`)
	dup := first
	dup.Seq = head.Seq + 1
	dup.PrevHash = head.EntryHash
	dup.EntryHash = ledger.EntryHash(dup)
	tamper(t, path, `INSERT INTO ledger_entries SELECT ?, kind, id, payload, hash, ?, ?, chained_at FROM ledger_entries WHERE seq = 1`,
		dup.Seq, dup.PrevHash, dup.EntryHash)
	verifyFails(t, path, dup.Seq, ledger.ProblemDuplicate, "already stored at seq 1")
}

func TestGetChecksTheEntryHash(t *testing.T) {
	path, s := seeded(t)
	_ = s.Close()
	// The payload still matches its hash; only the entry's own field changed.
	tamper(t, path, `UPDATE ledger_entries SET chained_at = '2000-01-01T00:00:00.000000000Z' WHERE id = 'task-1'`)
	_, _, err := open(t, path).Get(context.Background(), taskKind, "task-1")
	if !errors.Is(err, ledger.ErrCorrupt) || !strings.Contains(err.Error(), "entry_hash") {
		t.Fatalf("%v", err)
	}
}

func TestNewLedgerUsesWAL(t *testing.T) {
	path, s := seeded(t)
	_ = s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode %q %v", mode, err)
	}
}
