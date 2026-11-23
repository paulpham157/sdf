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
	all := l.Entries()
	if len(all) != 2 || all[0].Seq != 0 || all[0].Kind != GenesisKind || all[0].PrevHash != ZeroHash {
		t.Fatalf("genesis %+v", all)
	}
	if e1.Seq != 1 || e1.PrevHash != all[0].EntryHash || e1.Hash != h || e1.EntryHash == "" || !e1.ChainedAt.Equal(at) {
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
	if n := len(l.Entries()); n != 3 {
		t.Fatalf("%d entries", n)
	}
}

// Known answers from the ledger package's tests (PR #189), computed there
// with Python hashlib and recomputed here with Python independently. They
// tie this package's JCS encoder, PayloadHash, and EntryHash to the real
// ledger's, so a payload this package hashes is one the ledger accepts.
func TestHashesMatchLedgerKnownAnswers(t *testing.T) {
	if got := PayloadHash(KindTask, []byte("{}")); got != "sha256:1212580b02ee3822f380a3734f63b5346407ef19ef3f7b17c65940aa9f666925" {
		t.Fatalf("payload hash %s", got)
	}
	at, err := time.Parse(time.RFC3339, "2026-11-23T05:40:00Z")
	if err != nil {
		t.Fatal(err)
	}
	got, err := EntryHash(Entry{
		Seq:       1,
		Kind:      KindTask,
		ID:        `t"1`,
		Hash:      PayloadHash(KindTask, []byte("{}")),
		PrevHash:  ZeroHash,
		ChainedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "sha256:24d4d7c969de143a67dc11243a10cacca1ba43c20eb18ad7775664cfd76ad9c0" {
		t.Fatalf("entry hash %s", got)
	}
}

func TestMemLedgerRefusesBadKeys(t *testing.T) {
	ctx := context.Background()
	l := NewMemLedger(nil)
	p := []byte("{}")
	for name, kind := range map[string]Kind{
		"empty":       "",
		"newline":     "sdf.task/v1\nx",
		"control":     "sdf.task/v1\x01",
		"bad utf8":    Kind([]byte{0xff}),
		"reserved":    "sdf.ledger.genesis/v1",
		"reserved v2": "sdf.ledger.other",
	} {
		if _, err := l.Append(ctx, kind, "id", p, PayloadHash(kind, p)); err == nil {
			t.Errorf("kind %s accepted", name)
		}
	}
	for name, id := range map[string]string{"empty": "", "bad utf8": string([]byte{'a', 0xff})} {
		if _, err := l.Append(ctx, KindTask, id, p, PayloadHash(KindTask, p)); err == nil {
			t.Errorf("id %s accepted", name)
		}
	}
	if len(l.Entries()) != 0 {
		t.Fatal("a refused append wrote")
	}
}
