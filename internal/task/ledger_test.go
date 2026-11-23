package task

import (
	"context"
	"errors"
	"testing"

	"github.com/paulpham157/sdf/internal/ledger/ledgertest"
)

// Known answer from the ledger package's tests (PR #189), computed there
// with Python hashlib. A task payload this package hashes is one the ledger
// accepts.
func TestPayloadHashMatchesLedgerKnownAnswer(t *testing.T) {
	if got := PayloadHash(KindTask, []byte("{}")); got != "sha256:1212580b02ee3822f380a3734f63b5346407ef19ef3f7b17c65940aa9f666925" {
		t.Fatalf("payload hash %s", got)
	}
}

// The record kinds this package and internal/verify write are kinds the
// real ledger accepts, and its conflict and case rules apply to them.
func TestTaskKindsGoThroughTheRealLedger(t *testing.T) {
	ctx := context.Background()
	l := ledgertest.New(t)
	p := []byte("{}")
	for _, kind := range []Kind{KindTask, KindEvidence, KindVerdict} {
		if _, err := l.Append(ctx, kind, "Feat", p, PayloadHash(kind, p)); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	if _, err := l.Append(ctx, KindTask, "feat", p, PayloadHash(KindTask, p)); !errors.Is(err, ErrConflict) {
		t.Fatalf("case variant: %v", err)
	}
	if _, err := l.Append(ctx, KindTask, "a/b", p, PayloadHash(KindTask, p)); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := l.Append(ctx, KindTask, "t2", p, "sha256:00"); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("bad hash: %v", err)
	}
	if _, _, err := l.Get(ctx, KindTask, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if n := ledgertest.Entries(t, l); n != 4 { // genesis + three kinds
		t.Fatalf("%d entries", n)
	}
}
