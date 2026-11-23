package task

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// The names below mirror the interface the ledger package will export
// (internal/ledger, owned separately). When it lands, callers switch from
// task.Ledger to ledger.Ledger with no change in semantics. Until then
// MemLedger is the only implementation, and only tests use it.

// Kind is the record type and the hash domain, for example "sdf.task/v1".
type Kind string

// Record kinds.
const (
	KindTask     Kind = "sdf.task/v1"
	KindEvidence Kind = "sdf.evidence/v1"
	KindVerdict  Kind = "sdf.verdict/v1"
)

// Entry is the ledger's receipt for one appended payload.
type Entry struct {
	Seq       int64
	Kind      Kind
	ID        string
	Hash      string
	PrevHash  string
	EntryHash string
	ChainedAt time.Time
}

// Ledger is an append-only store of canonical payloads.
type Ledger interface {
	// Append checks hash == PayloadHash(kind, canonical), else
	// ErrHashMismatch. The same (kind, id) with the same hash is idempotent
	// and returns the earlier Entry. The same (kind, id) with a different
	// hash is ErrConflict.
	Append(ctx context.Context, kind Kind, id string, canonical []byte, hash string) (Entry, error)
	// Get returns the payload and its Entry, or ErrNotFound.
	Get(ctx context.Context, kind Kind, id string) ([]byte, Entry, error)
}

// Ledger errors.
var (
	ErrHashMismatch = errors.New("ledger: hash does not match payload")
	ErrConflict     = errors.New("ledger: id already holds a different payload")
	ErrNotFound     = errors.New("ledger: not found")
)

// PayloadHash is "sha256:" + hex(sha256(kind + "\n" + canonical)).
func PayloadHash(kind Kind, canonical []byte) string {
	h := sha256.New()
	h.Write([]byte(string(kind) + "\n"))
	h.Write(canonical)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// MemLedger is an in-memory Ledger for tests. It chains entries the simple
// way: EntryHash = sha256 over seq, kind, id, hash, and the previous
// EntryHash. That chain format is this fake's, not the real ledger's.
type MemLedger struct {
	mu      sync.Mutex
	now     func() time.Time
	entries []memEntry
	index   map[string]int
}

type memEntry struct {
	entry   Entry
	payload []byte
}

// NewMemLedger returns an empty MemLedger. now may be nil.
func NewMemLedger(now func() time.Time) *MemLedger {
	if now == nil {
		now = time.Now
	}
	return &MemLedger{now: now, index: map[string]int{}}
}

func memKey(kind Kind, id string) string { return string(kind) + "\x00" + id }

// Append implements Ledger.
func (m *MemLedger) Append(ctx context.Context, kind Kind, id string, canonical []byte, hash string) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	if kind == "" || id == "" {
		return Entry{}, fmt.Errorf("ledger: kind and id are required")
	}
	if PayloadHash(kind, canonical) != hash {
		return Entry{}, ErrHashMismatch
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if i, ok := m.index[memKey(kind, id)]; ok {
		old := m.entries[i]
		if old.entry.Hash == hash {
			return old.entry, nil
		}
		return Entry{}, ErrConflict
	}
	prev := ""
	if n := len(m.entries); n > 0 {
		prev = m.entries[n-1].entry.EntryHash
	}
	e := Entry{
		Seq:       int64(len(m.entries) + 1),
		Kind:      kind,
		ID:        id,
		Hash:      hash,
		PrevHash:  prev,
		ChainedAt: m.now().UTC(),
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\n%s\n%s\n%s\n%s", e.Seq, e.Kind, e.ID, e.Hash, e.PrevHash)))
	e.EntryHash = "sha256:" + hex.EncodeToString(sum[:])
	m.index[memKey(kind, id)] = len(m.entries)
	m.entries = append(m.entries, memEntry{entry: e, payload: bytes.Clone(canonical)})
	return e, nil
}

// Get implements Ledger.
func (m *MemLedger) Get(ctx context.Context, kind Kind, id string) ([]byte, Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, Entry{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.index[memKey(kind, id)]
	if !ok {
		return nil, Entry{}, ErrNotFound
	}
	return bytes.Clone(m.entries[i].payload), m.entries[i].entry, nil
}

// Entries returns every entry in append order.
func (m *MemLedger) Entries() []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Entry, len(m.entries))
	for i, e := range m.entries {
		out[i] = e.entry
	}
	return out
}
