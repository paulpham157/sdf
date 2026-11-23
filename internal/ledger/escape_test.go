package ledger_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/ledger"
)

// spoofKind would erase the line and print "ledger intact" on a terminal.
const spoofKind = "sdf.task/v1\x1b[2K\rledger intact"

// rechainWithKind rewrites the head with kind, payload hash and entry hash
// recomputed, so only the field rules can catch it.
func rechainWithKind(t *testing.T, path string, s *ledger.Store, kind ledger.Kind) ledger.Entry {
	t.Helper()
	head, err := s.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	forged := head
	forged.Kind = kind
	payload := []byte("{}")
	forged.Hash = ledger.PayloadHash(kind, payload)
	forged.EntryHash = ledger.EntryHash(forged)
	tamper(t, path, `UPDATE ledger_entries SET kind = ?, payload = ?, hash = ?, entry_hash = ? WHERE seq = ?`,
		string(kind), payload, forged.Hash, forged.EntryHash, head.Seq)
	return forged
}

func TestAnEscapeSequenceKindIsCaughtAndNeverEchoedRaw(t *testing.T) {
	ctx := context.Background()
	path, s := seeded(t)
	forged := rechainWithKind(t, path, s, spoofKind)
	l := open(t, path)

	rep, err := l.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Failure == nil || rep.Failure.Seq != forged.Seq || rep.Failure.Problem != ledger.ProblemModified {
		t.Fatalf("%+v", rep.Failure)
	}
	if strings.ContainsAny(rep.Failure.Detail, "\x1b\r") {
		t.Fatalf("detail holds raw control bytes: %q", rep.Failure.Detail)
	}

	// Get refuses the kind before any lookup, and its error is quoted.
	_, _, err = l.Get(ctx, spoofKind, forged.ID)
	if !errors.Is(err, ledger.ErrInvalid) || strings.ContainsAny(err.Error(), "\x1b\r") {
		t.Fatalf("%q", err)
	}
}

func TestGetRejectsAnInvalidKind(t *testing.T) {
	_, s := seeded(t)
	for _, kind := range []ledger.Kind{"", "a\nb", "sdf.task/v1\xff"} {
		if _, _, err := s.Get(context.Background(), kind, "task-1"); !errors.Is(err, ledger.ErrInvalid) {
			t.Fatalf("%q: %v", kind, err)
		}
	}
	// The genesis kind is reserved for Append, not for Get.
	if _, _, err := s.Get(context.Background(), ledger.GenesisKind, ledger.GenesisID); err != nil {
		t.Fatal(err)
	}
}

func TestSafe(t *testing.T) {
	for in, want := range map[string]string{
		"sha256:abc":         "sha256:abc",
		"/tmp/a b/ledger.db": "/tmp/a b/ledger.db",
		"ünï":                "ünï",
		"a\x1b[2K\rb":        `"a\x1b[2K\rb"`,
		"a\xffb":             `"a\xffb"`,
		"line\nbreak":        `"line\nbreak"`,
		"bidi\u202eoverride": `"bidi\u202eoverride"`,
	} {
		if got := ledger.Safe(in); got != want {
			t.Errorf("Safe(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestVerifyChecksGenesisIsNotLaterThanSeqOne(t *testing.T) {
	path, s := seeded(t)
	_, g, err := s.Get(context.Background(), ledger.GenesisKind, ledger.GenesisID)
	if err != nil {
		t.Fatal(err)
	}
	_, e1, err := s.Get(context.Background(), "sdf.task/v1", "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if !g.ChainedAt.Equal(e1.ChainedAt) {
		t.Fatalf("genesis %v and seq 1 %v were written with different times", g.ChainedAt, e1.ChainedAt)
	}
	_ = s.Close()
	// Move genesis later and rechain everything after it, so only the time
	// order can catch it.
	g.ChainedAt = e1.ChainedAt.Add(1e9)
	g.EntryHash = ledger.EntryHash(g)
	e1.PrevHash = g.EntryHash
	e1.EntryHash = ledger.EntryHash(e1)
	tamper(t, path, `UPDATE ledger_entries SET chained_at = ?, entry_hash = ? WHERE seq = 0`, g.ChainedAt.UTC().Format(ledger.TimeLayout), g.EntryHash)
	tamper(t, path, `DELETE FROM ledger_entries WHERE seq > 1`)
	tamper(t, path, `UPDATE ledger_entries SET prev_hash = ?, entry_hash = ? WHERE seq = 1`, e1.PrevHash, e1.EntryHash)
	verifyFails(t, path, 0, ledger.ProblemModified, "genesis chained_at")
}
