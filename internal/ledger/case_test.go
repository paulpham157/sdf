package ledger_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/ledger"
)

func TestAppendRefusesAnIDThatDiffersOnlyInCase(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "ledger.db"))
	first := appendOK(t, s, taskKind, "TASK-1", `{"objective":"a"}`)

	// The same id and hash is still idempotent.
	if again := appendOK(t, s, taskKind, "TASK-1", `{"objective":"a"}`); again != first {
		t.Fatalf("%+v != %+v", again, first)
	}
	// A case variant is a conflict, with the same payload or another one.
	for _, payload := range []string{`{"objective":"a"}`, `{"objective":"b"}`} {
		for _, id := range []string{"task-1", "Task-1"} {
			p := []byte(payload)
			_, err := s.Append(ctx, taskKind, id, p, ledger.PayloadHash(taskKind, p))
			if !errors.Is(err, ledger.ErrConflict) {
				t.Fatalf("%s %s: %v", id, payload, err)
			}
			if !strings.Contains(err.Error(), `"`+id+`"`) || !strings.Contains(err.Error(), `"TASK-1"`) {
				t.Fatalf("error does not name both ids: %v", err)
			}
		}
	}
	// Ids stay case-preserving: Get is exact.
	if _, _, err := s.Get(ctx, taskKind, "task-1"); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("get task-1: %v", err)
	}
	// Another kind may use the other case.
	appendOK(t, s, "sdf.verdict/v1", "task-1", `{}`)
	// Digits and punctuation have no case: distinct ids stay distinct.
	appendOK(t, s, taskKind, "TASK-2", `{}`)

	rep, err := s.Verify(ctx)
	if err != nil || !rep.OK || rep.Entries != 4 {
		t.Fatalf("%+v %v", rep, err)
	}
}

// A ledger written before this rule may hold case variants. It still
// opens and takes appends, and Verify flags the pair.
func TestVerifyFlagsAHandInsertedCaseDuplicate(t *testing.T) {
	ctx := context.Background()
	path, s := seeded(t)
	head, _ := s.Head(ctx)
	_, first, err := s.Get(ctx, taskKind, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	dup := first
	dup.ID = "TASK-1"
	dup.Seq = head.Seq + 1
	dup.PrevHash = head.EntryHash
	dup.EntryHash = ledger.EntryHash(dup)
	tamper(t, path, `INSERT INTO ledger_entries SELECT ?, kind, ?, payload, hash, ?, ?, chained_at FROM ledger_entries WHERE seq = 1`,
		dup.Seq, dup.ID, dup.PrevHash, dup.EntryHash)
	verifyFails(t, path, dup.Seq, ledger.ProblemDuplicate, `id "TASK-1" differs only in case from id "task-1" at seq 1`)

	s = open(t, path)
	appendOK(t, s, taskKind, "task-9", `{}`)
	p := []byte(`{}`)
	if _, err := s.Append(ctx, taskKind, "Task-1", p, ledger.PayloadHash(taskKind, p)); !errors.Is(err, ledger.ErrConflict) {
		t.Fatalf("%v", err)
	}
}
