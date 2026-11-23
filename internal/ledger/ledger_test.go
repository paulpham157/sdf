package ledger_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/ledger"
)

const taskKind ledger.Kind = "sdf.task/v1"

func open(t *testing.T, path string) *ledger.Store {
	t.Helper()
	s, err := ledger.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func appendOK(t *testing.T, s *ledger.Store, kind ledger.Kind, id, payload string) ledger.Entry {
	t.Helper()
	e, err := s.Append(context.Background(), kind, id, []byte(payload), ledger.PayloadHash(kind, []byte(payload)))
	if err != nil {
		t.Fatalf("append %s/%s: %v", kind, id, err)
	}
	return e
}

func seeded(t *testing.T) (string, *ledger.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.db")
	s := open(t, path)
	appendOK(t, s, taskKind, "task-1", `{"objective":"a"}`)
	appendOK(t, s, taskKind, "task-2", `{"objective":"b"}`)
	appendOK(t, s, "sdf.verdict/v1", "01JD0000000000000000000000", `{"result":"PASS"}`)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path, open(t, path)
}

func tamper(t *testing.T, path, stmt string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(stmt, args...); err != nil {
		t.Fatal(err)
	}
}

// The known answers below were computed outside Go (Python hashlib), so a
// writer in another language can check its encoding against them.
func TestPayloadHashKnownAnswer(t *testing.T) {
	got := ledger.PayloadHash(taskKind, []byte("{}"))
	if got != "sha256:1212580b02ee3822f380a3734f63b5346407ef19ef3f7b17c65940aa9f666925" {
		t.Fatalf("%q", got)
	}
	if got == ledger.PayloadHash("sdf.other/v1", []byte("{}")) {
		t.Fatal("kind is not part of the hash")
	}
}

func TestEntryHashKnownAnswer(t *testing.T) {
	at, err := time.Parse(time.RFC3339, "2026-11-23T05:40:00Z")
	if err != nil {
		t.Fatal(err)
	}
	e := ledger.Entry{
		Seq:       1,
		Kind:      taskKind,
		ID:        `t"1`,
		Hash:      ledger.PayloadHash(taskKind, []byte("{}")),
		PrevHash:  ledger.ZeroHash,
		ChainedAt: at,
	}
	if got := ledger.EntryHash(e); got != "sha256:24d4d7c969de143a67dc11243a10cacca1ba43c20eb18ad7775664cfd76ad9c0" {
		t.Fatalf("%q", got)
	}
}

func TestAppendChainsFromGenesis(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "nested", "dir", "ledger.db"))
	e1 := appendOK(t, s, taskKind, "task-1", `{"objective":"a"}`)
	e2 := appendOK(t, s, taskKind, "task-2", `{"objective":"b"}`)
	if e1.Seq != 1 || e2.Seq != 2 {
		t.Fatalf("seq %d %d", e1.Seq, e2.Seq)
	}
	if e2.PrevHash != e1.EntryHash {
		t.Fatal("e2 does not link to e1")
	}
	if ledger.EntryHash(e1) != e1.EntryHash || ledger.EntryHash(e2) != e2.EntryHash {
		t.Fatal("EntryHash does not reproduce the stored entry hash")
	}
	_, g, err := s.Get(ctx, ledger.GenesisKind, ledger.GenesisID)
	if err != nil {
		t.Fatal(err)
	}
	if g.Seq != 0 || g.PrevHash != ledger.ZeroHash || e1.PrevHash != g.EntryHash {
		t.Fatalf("genesis %+v", g)
	}
	head, err := s.Head(ctx)
	if err != nil || head != e2 {
		t.Fatalf("head %+v %v", head, err)
	}
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
}

func TestAppendIsIdempotentForTheSameHash(t *testing.T) {
	path, s := seeded(t)
	_ = path
	again := appendOK(t, s, taskKind, "task-1", `{"objective":"a"}`)
	if again.Seq != 1 {
		t.Fatalf("seq %d", again.Seq)
	}
	head, _ := s.Head(context.Background())
	if head.Seq != 3 {
		t.Fatalf("head seq %d after a repeated append", head.Seq)
	}
}

func TestAppendConflictAndMismatch(t *testing.T) {
	ctx := context.Background()
	_, s := seeded(t)
	other := []byte(`{"objective":"changed"}`)
	if _, err := s.Append(ctx, taskKind, "task-1", other, ledger.PayloadHash(taskKind, other)); !errors.Is(err, ledger.ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	p := []byte(`{"objective":"c"}`)
	if _, err := s.Append(ctx, taskKind, "task-3", p, ledger.PayloadHash(taskKind, []byte(`{"objective":"d"}`))); !errors.Is(err, ledger.ErrHashMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	if _, err := s.Append(ctx, taskKind, "task-3", p, ledger.PayloadHash("sdf.other/v1", p)); !errors.Is(err, ledger.ErrHashMismatch) {
		t.Fatalf("kind mismatch: %v", err)
	}
	if _, err := s.Append(ctx, taskKind, "", p, ledger.PayloadHash(taskKind, p)); err == nil {
		t.Fatal("empty id accepted")
	}
	head, _ := s.Head(ctx)
	if head.Seq != 3 {
		t.Fatalf("a rejected append wrote: head seq %d", head.Seq)
	}
}

func TestIDIsAnArbitraryString(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "ledger.db"))
	for _, id := range []string{"01JD5Z9X8Y7W6V5T4S3R2Q1P0N", "my task / \"quoted\"\n", "ünïcode ✓", "\u2028"} {
		appendOK(t, s, taskKind, id, `{}`)
		if _, _, err := s.Get(ctx, taskKind, id); err != nil {
			t.Fatalf("%q: %v", id, err)
		}
	}
	rep, err := s.Verify(ctx)
	if err != nil || !rep.OK {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestGetReturnsTheBytesAsGiven(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "ledger.db"))
	payload := `{"b":1,"a":"x"}` // not reordered, not reserialized
	e := appendOK(t, s, taskKind, "t", payload)
	got, ge, err := s.Get(ctx, taskKind, "t")
	if err != nil || string(got) != payload || ge != e {
		t.Fatalf("%q %+v %v", got, ge, err)
	}
	if _, _, err := s.Get(ctx, taskKind, "nope"); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("not found: %v", err)
	}
	if _, _, err := s.Get(ctx, "sdf.other/v1", "t"); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("other kind: %v", err)
	}
}

func TestReadsOnAMissingFileDoNotCreateIt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sub", "ledger.db")
	s := open(t, path)
	if _, _, err := s.Get(ctx, taskKind, "x"); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Head(ctx); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal(err)
	}
	rep, err := s.Verify(ctx)
	if err != nil || !rep.OK || !rep.Empty || rep.Entries != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read path created %s", filepath.Dir(path))
	}
}

func TestReadsDoNotWriteAnExistingFile(t *testing.T) {
	ctx := context.Background()
	path, s := seeded(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Head(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get(ctx, taskKind, "task-1"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("a read changed the file")
	}
	// An empty file (no table) reads as empty and stays empty.
	empty := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := open(t, empty).Verify(ctx)
	if err != nil || !rep.Empty || !rep.OK {
		t.Fatalf("%+v %v", rep, err)
	}
	if info, _ := os.Stat(empty); info.Size() != 0 {
		t.Fatal("verify wrote to an empty file")
	}
}

func TestVerifyIntact(t *testing.T) {
	_, s := seeded(t)
	rep, err := s.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.Empty || rep.Entries != 4 || rep.Head == nil || rep.Head.Seq != 3 || rep.Failure != nil {
		t.Fatalf("%+v", rep)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	cases := []struct {
		name    string
		stmt    string
		seq     int64
		problem ledger.Problem
	}{
		{"modified payload", `UPDATE ledger_entries SET payload = '{"objective":"z"}' WHERE seq = 2`, 2, ledger.ProblemModified},
		{"payload and its hash rewritten", `UPDATE ledger_entries SET payload = '{"objective":"z"}', hash = '` + ledger.PayloadHash(taskKind, []byte(`{"objective":"z"}`)) + `' WHERE seq = 2`, 2, ledger.ProblemModified},
		{"chained_at edited", `UPDATE ledger_entries SET chained_at = '2000-01-01T00:00:00.000000000Z' WHERE seq = 1`, 1, ledger.ProblemModified},
		{"id edited", `UPDATE ledger_entries SET id = 'task-9' WHERE seq = 1`, 1, ledger.ProblemModified},
		{"deleted row", `DELETE FROM ledger_entries WHERE seq = 2`, 2, ledger.ProblemMissing},
		{"deleted genesis", `DELETE FROM ledger_entries WHERE seq = 0`, 1, ledger.ProblemMissing},
		{"reordered rows", `UPDATE ledger_entries SET seq = 100 WHERE seq = 1; UPDATE ledger_entries SET seq = 1 WHERE seq = 2; UPDATE ledger_entries SET seq = 2 WHERE seq = 100`, 1, ledger.ProblemReordered},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, s := seeded(t)
			_ = s.Close()
			tamper(t, path, tc.stmt)
			rep, err := open(t, path).Verify(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if rep.OK || rep.Failure == nil {
				t.Fatalf("tampering not detected: %+v", rep)
			}
			if rep.Failure.Seq != tc.seq || rep.Failure.Problem != tc.problem {
				t.Fatalf("failure %+v, want seq %d %s", *rep.Failure, tc.seq, tc.problem)
			}
		})
	}
}

func TestVerifyDetectsARewrittenEntryAtTheNextLink(t *testing.T) {
	path, s := seeded(t)
	_, e1, err := s.Get(context.Background(), taskKind, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	// Rewrite seq 1 consistently: new payload, new hash, new entry hash.
	p := []byte(`{"objective":"forged"}`)
	e1.Hash = ledger.PayloadHash(taskKind, p)
	e1.EntryHash = ledger.EntryHash(e1)
	tamper(t, path, `UPDATE ledger_entries SET payload = ?, hash = ?, entry_hash = ? WHERE seq = 1`, p, e1.Hash, e1.EntryHash)
	rep, err := open(t, path).Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Failure.Seq != 2 || rep.Failure.Problem != ledger.ProblemReordered {
		t.Fatalf("%+v", rep.Failure)
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("SDF_LEDGER", "")
	t.Setenv("XDG_DATA_HOME", "/data")
	if p, _ := ledger.DefaultPath(); p != filepath.Join("/data", "sdf", "ledger.db") {
		t.Fatal(p)
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/u")
	if p, _ := ledger.DefaultPath(); p != filepath.Join("/home/u", ".local", "share", "sdf", "ledger.db") {
		t.Fatal(p)
	}
	t.Setenv("SDF_LEDGER", "/x/l.db")
	if p, _ := ledger.DefaultPath(); p != "/x/l.db" {
		t.Fatal(p)
	}
}

func TestAppendAfterAReadOnTheSameStore(t *testing.T) {
	ctx := context.Background()
	path, s := seeded(t)
	if _, err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	e := appendOK(t, s, taskKind, "task-4", `{}`)
	if e.Seq != 4 {
		t.Fatalf("seq %d", e.Seq)
	}
	rep, err := open(t, path).Verify(ctx)
	if err != nil || !rep.OK || rep.Entries != 5 {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestGetReportsAHandEditedPayload(t *testing.T) {
	ctx := context.Background()
	path, s := seeded(t)
	_ = s.Close()
	tamper(t, path, `UPDATE ledger_entries SET payload = '{"objective":"z"}' WHERE id = 'task-2'`)
	s = open(t, path)
	got, e, err := s.Get(ctx, taskKind, "task-2")
	if !errors.Is(err, ledger.ErrCorrupt) {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(err.Error(), `sdf.task/v1 "task-2"`) {
		t.Fatalf("error does not name kind and id: %v", err)
	}
	if got != nil || e != (ledger.Entry{}) {
		t.Fatalf("corrupt payload returned: %q %+v", got, e)
	}
	if _, _, err := s.Get(ctx, taskKind, "task-1"); err != nil {
		t.Fatalf("an untouched row: %v", err)
	}
}
