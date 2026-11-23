package ledger_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/ledger"
)

func TestVerifyPrefixAcceptsThePinnedHead(t *testing.T) {
	ctx := context.Background()
	_, s := seeded(t)
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyPrefix(ctx, head.Seq, head.EntryHash); err != nil {
		t.Fatalf("pin at head: %v", err)
	}
}

func TestVerifyPrefixAcceptsALedgerThatGrewAfterThePin(t *testing.T) {
	ctx := context.Background()
	_, s := seeded(t)
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	appendOK(t, s, taskKind, "task-3", `{"objective":"c"}`)
	if err := s.VerifyPrefix(ctx, head.Seq, head.EntryHash); err != nil {
		t.Fatalf("grew after pin: %v", err)
	}
	newHead, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if newHead.Seq <= head.Seq {
		t.Fatalf("expected growth, head still seq %d", newHead.Seq)
	}
	if err := s.VerifyPrefix(ctx, newHead.Seq, newHead.EntryHash); err != nil {
		t.Fatalf("pin at new head: %v", err)
	}
}

func TestVerifyPrefixRejectsTruncationBelowThePin(t *testing.T) {
	ctx := context.Background()
	path, s := seeded(t)
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	tamper(t, path, `DELETE FROM ledger_entries WHERE seq >= ?`, head.Seq)
	s = open(t, path)
	err = s.VerifyPrefix(ctx, head.Seq, head.EntryHash)
	if !errors.Is(err, ledger.ErrPrefixMismatch) || !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("truncation: %v", err)
	}
	if errors.Is(err, ledger.ErrChainBroken) {
		t.Fatalf("truncation should not be a broken chain: %v", err)
	}
	if !strings.Contains(err.Error(), "actual none") {
		t.Fatalf("error should say actual none: %v", err)
	}
}

func TestVerifyPrefixRejectsARewrittenPinnedEntry(t *testing.T) {
	ctx := context.Background()
	path, s := seeded(t)
	_, e, err := s.GetBySeq(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	rewriteAndRehashLedger(t, path, 2, `{"objective":"forged-task-2"}`)
	s = open(t, path)
	if rep, err := s.Verify(ctx); err != nil || !rep.OK {
		t.Fatalf("rehashed chain should still verify: %+v %v", rep, err)
	}
	err = s.VerifyPrefix(ctx, e.Seq, e.EntryHash)
	if !errors.Is(err, ledger.ErrPrefixMismatch) {
		t.Fatalf("rewrite: %v", err)
	}
	if errors.Is(err, ledger.ErrNotFound) || errors.Is(err, ledger.ErrChainBroken) {
		t.Fatalf("rewrite should be a hash mismatch, not truncation or a broken chain: %v", err)
	}
	if !strings.Contains(err.Error(), "expected 2:"+e.EntryHash) || !strings.Contains(err.Error(), "actual 2:sha256:") {
		t.Fatalf("error should name expected and actual: %v", err)
	}
}

func TestVerifyPrefixRejectsABrokenChainAfterThePin(t *testing.T) {
	ctx := context.Background()
	path, s := seeded(t)
	_, e, err := s.GetBySeq(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	tamper(t, path, `UPDATE ledger_entries SET payload = '{"objective":"z"}' WHERE seq = 2`)
	s = open(t, path)
	err = s.VerifyPrefix(ctx, e.Seq, e.EntryHash)
	if !errors.Is(err, ledger.ErrChainBroken) {
		t.Fatalf("broken chain: %v", err)
	}
	if errors.Is(err, ledger.ErrPrefixMismatch) {
		t.Fatalf("a broken chain should not also be a prefix mismatch: %v", err)
	}
	var ce *ledger.ChainError
	if !errors.As(err, &ce) || ce.Failure.Seq != 2 || ce.Failure.Problem != ledger.ProblemModified {
		t.Fatalf("ChainError should carry the first failure: %+v %v", ce, err)
	}
}

func TestParsePinFormats(t *testing.T) {
	hash := "sha256:" + strings.Repeat("ab", 32)
	jsonPin, err := json.Marshal(map[string]any{
		"path": "/tmp/ledger.db",
		"head": map[string]any{
			"seq":        3,
			"kind":       "sdf.task/v1",
			"id":         "task-3",
			"hash":       hash,
			"prev_hash":  hash,
			"entry_hash": hash,
			"chained_at": "2026-01-02T03:04:05.000000Z",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ok := []struct {
		name string
		in   string
		seq  int64
	}{
		{"bare", "3:" + hash, 3},
		{"bare genesis", "0:" + hash, 0},
		{"bare trimmed", "  3:" + hash + "\n", 3},
		{"head text", "seq 3 entry_hash " + hash, 3},
		{"head text trimmed", "  seq 3 entry_hash " + hash + "\n", 3},
		{"head text genesis", "seq 0 entry_hash " + hash, 0},
		{"head json", string(jsonPin), 3},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			seq, got, err := ledger.ParsePin(tc.in)
			if err != nil || seq != tc.seq || got != hash {
				t.Fatalf("ParsePin(%q) = %d %q %v", tc.in, seq, got, err)
			}
		})
	}

	bad := []string{
		"",
		"not a pin",
		hash,
		"3:sha256:" + strings.Repeat("AB", 32),
		"3:sha256:xyz",
		"3:sha256:" + strings.Repeat("a", 63),
		"03:" + hash,
		"-1:" + hash,
		"seq 03 entry_hash " + hash,
		"seq -1 entry_hash " + hash,
		"seq 3 hash " + hash,
		`{"head":{"entry_hash":"` + hash + `"}}`,
		`{"head":{"seq":3}}`,
		"{not json",
	}
	for _, in := range bad {
		t.Run("bad "+in, func(t *testing.T) {
			if _, _, err := ledger.ParsePin(in); err == nil {
				t.Fatalf("ParsePin(%q) accepted", in)
			}
		})
	}
}

// rewriteAndRehashLedger rewrites the payload at seq and recomputes hashes
// from that point on, so the chain remains internally valid.
func rewriteAndRehashLedger(t *testing.T, path string, seq int64, payload string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT seq, kind, id, payload, hash, prev_hash, entry_hash, chained_at FROM ledger_entries ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	type chainRow struct {
		seq       int64
		kind      string
		id        string
		payload   []byte
		hash      string
		prevHash  string
		entryHash string
		chainedAt string
	}
	var all []chainRow
	for rows.Next() {
		var r chainRow
		if err := rows.Scan(&r.seq, &r.kind, &r.id, &r.payload, &r.hash, &r.prevHash, &r.entryHash, &r.chainedAt); err != nil {
			t.Fatal(err)
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()

	var prevHash string
	found := false
	for i := range all {
		r := &all[i]
		if r.seq == seq {
			r.payload = []byte(payload)
			r.hash = ledger.PayloadHash(ledger.Kind(r.kind), r.payload)
			found = true
		}
		if found {
			if r.seq != seq {
				r.prevHash = prevHash
			}
			at, err := time.Parse(ledger.TimeLayout, r.chainedAt)
			if err != nil {
				t.Fatal(err)
			}
			r.entryHash = ledger.EntryHash(ledger.Entry{
				Seq:       r.seq,
				Kind:      ledger.Kind(r.kind),
				ID:        r.id,
				Hash:      r.hash,
				PrevHash:  r.prevHash,
				ChainedAt: at,
			})
			if _, err := db.Exec(`UPDATE ledger_entries SET payload = ?, hash = ?, prev_hash = ?, entry_hash = ? WHERE seq = ?`,
				r.payload, r.hash, r.prevHash, r.entryHash, r.seq); err != nil {
				t.Fatal(err)
			}
		}
		prevHash = r.entryHash
	}
	if !found {
		t.Fatalf("seq %d not in ledger", seq)
	}
}
