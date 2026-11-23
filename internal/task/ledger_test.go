package task

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemLedgerSemantics(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 23, 5, 0, 0, 0, time.UTC)
	l := NewMemLedger(func() time.Time { return at })
	payload := []byte(`{"a":1}`)
	h := PayloadHash(KindTask, payload)

	if _, err := l.Append(ctx, KindTask, "t1", payload, "sha256:00"); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("wrong hash: %v", err)
	}
	// The kind is part of the hash domain.
	if _, err := l.Append(ctx, KindVerdict, "t1", payload, h); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("hash from another kind: %v", err)
	}
	if _, _, err := l.Get(ctx, KindTask, "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get before append: %v", err)
	}
	e1, err := l.Append(ctx, KindTask, "t1", payload, h)
	if err != nil {
		t.Fatal(err)
	}
	if e1.Seq != 1 || e1.PrevHash != "" || e1.Hash != h || e1.EntryHash == "" || !e1.ChainedAt.Equal(at) {
		t.Fatalf("%+v", e1)
	}
	again, err := l.Append(ctx, KindTask, "t1", payload, h)
	if err != nil || again != e1 {
		t.Fatalf("idempotent append: %+v %v", again, err)
	}
	other := []byte(`{"a":2}`)
	if _, err := l.Append(ctx, KindTask, "t1", other, PayloadHash(KindTask, other)); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	// Same id under another kind is a different record.
	e2, err := l.Append(ctx, KindEvidence, "t1", other, PayloadHash(KindEvidence, other))
	if err != nil {
		t.Fatal(err)
	}
	if e2.Seq != 2 || e2.PrevHash != e1.EntryHash || e2.EntryHash == e1.EntryHash {
		t.Fatalf("chain: %+v", e2)
	}
	b, e, err := l.Get(ctx, KindTask, "t1")
	if err != nil || string(b) != string(payload) || e != e1 {
		t.Fatalf("get: %s %+v %v", b, e, err)
	}
	b[0] = 'X'
	b2, _, _ := l.Get(ctx, KindTask, "t1")
	if string(b2) != string(payload) {
		t.Fatal("Get returned the stored slice")
	}
	if n := len(l.Entries()); n != 2 {
		t.Fatalf("%d entries", n)
	}
}
