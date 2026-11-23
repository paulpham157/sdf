package task

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Ledger, Kind, Entry, and the errors below are a stand-in for the ledger
// package (internal/ledger, pull request #189), which is not merged. They
// follow the method set and hash rules agreed for it, but they are not its
// types: the real ledger.Store will differ in details (for example a
// corrupt-payload error from Get, and its own Open and path handling). When
// #189 merges, these are deleted and callers use the ledger package's
// types; anything that differs is fixed then. MemLedger is the only
// implementation here, and only tests use it.

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
//
// TODO(#189): when the ledger package merges, change errors.Is checks to its
// error variables, and its ValidID (which returns an error, not a bool).
var (
	ErrHashMismatch = errors.New("ledger: hash does not match payload")
	ErrConflict     = errors.New("ledger: id already holds a different payload")
	ErrNotFound     = errors.New("ledger: not found")
	ErrInvalidID    = errors.New("ledger: invalid id")
)

// ledgerIDPattern is the ledger's id rule, for every kind.
var ledgerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// PayloadHash is "sha256:" + hex(sha256(kind + "\n" + canonical)).
func PayloadHash(kind Kind, canonical []byte) string {
	h := sha256.New()
	h.Write([]byte(string(kind) + "\n"))
	h.Write(canonical)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Chain constants, matching the ledger package (PR #189).
const GenesisKind Kind = "sdf.ledger.genesis/v1"

// More chain constants, matching the ledger package.
const (
	GenesisID      = "genesis"
	GenesisPayload = `{"schema":"sdf.ledger/v1"}`
	TimeLayout     = "2006-01-02T15:04:05.000000000Z"
	entryDomain    = "sdf.ledger.v1.entry\n"
)

// ZeroHash is the prev_hash of the genesis entry.
var ZeroHash = "sha256:" + strings.Repeat("0", 64)

// EntryHash is "sha256:" + hex(sha256("sdf.ledger.v1.entry\n" + J)), where J
// is the RFC 8785 form of {chained_at, hash, id, kind, prev_hash, seq} and
// chained_at is UTC with nine fraction digits. Same formula as the ledger
// package.
func EntryHash(e Entry) (string, error) {
	j, err := Canonical(map[string]any{
		"chained_at": e.ChainedAt.UTC().Format(TimeLayout),
		"hash":       e.Hash,
		"id":         e.ID,
		"kind":       string(e.Kind),
		"prev_hash":  e.PrevHash,
		"seq":        e.Seq,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(entryDomain), j...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// MemLedger is an in-memory Ledger for tests. It chains the way the ledger
// package does: a genesis entry at seq 0, written by the first Append, then
// contiguous seq, each entry's PrevHash the EntryHash before it.
type MemLedger struct {
	mu      sync.Mutex
	now     func() time.Time
	entries []memEntry
	index   map[string]int
	folded  map[string]int
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
	return &MemLedger{now: now, index: map[string]int{}, folded: map[string]int{}}
}

func memKey(kind Kind, id string) string { return string(kind) + "\x00" + id }

func foldKey(kind Kind, id string) string { return string(kind) + "\x00" + strings.ToLower(id) }

// Append implements Ledger.
func (m *MemLedger) Append(ctx context.Context, kind Kind, id string, canonical []byte, hash string) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	if err := validKey(kind, id); err != nil {
		return Entry{}, err
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
	// Ids that differ only by case are one id to the ledger.
	if j, ok := m.folded[foldKey(kind, id)]; ok {
		return Entry{}, fmt.Errorf("%w: %q differs only by case from %q", ErrConflict, id, m.entries[j].entry.ID)
	}
	if len(m.entries) == 0 {
		g := []byte(GenesisPayload)
		if _, err := m.insert(GenesisKind, GenesisID, g, PayloadHash(GenesisKind, g)); err != nil {
			return Entry{}, err
		}
	}
	return m.insert(kind, id, canonical, hash)
}

func (m *MemLedger) insert(kind Kind, id string, canonical []byte, hash string) (Entry, error) {
	prev := ZeroHash
	if n := len(m.entries); n > 0 {
		prev = m.entries[n-1].entry.EntryHash
	}
	e := Entry{
		Seq:       int64(len(m.entries)),
		Kind:      kind,
		ID:        id,
		Hash:      hash,
		PrevHash:  prev,
		ChainedAt: m.now().UTC(),
	}
	h, err := EntryHash(e)
	if err != nil {
		return Entry{}, err
	}
	e.EntryHash = h
	m.index[memKey(kind, id)] = len(m.entries)
	m.folded[foldKey(kind, id)] = len(m.entries)
	m.entries = append(m.entries, memEntry{entry: e, payload: bytes.Clone(canonical)})
	return e, nil
}

// ReservedKindPrefix is the kind prefix only the ledger itself writes.
const ReservedKindPrefix = "sdf.ledger."

// validKey applies the ledger's rules to a caller's kind and id: the id
// matches ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ (else ErrInvalidID); the kind
// is valid UTF-8, not empty, has no control character (newline included),
// and is not under ReservedKindPrefix.
func validKey(kind Kind, id string) error {
	k := string(kind)
	switch {
	case !ledgerIDPattern.MatchString(id):
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	case k == "":
		return fmt.Errorf("ledger: kind is required")
	case !utf8.ValidString(k):
		return fmt.Errorf("ledger: kind must be valid UTF-8")
	case strings.IndexFunc(k, unicode.IsControl) >= 0:
		return fmt.Errorf("ledger: kind %q contains a control character", k)
	case strings.HasPrefix(k, ReservedKindPrefix):
		return fmt.Errorf("ledger: kind prefix %s is reserved", ReservedKindPrefix)
	}
	return nil
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

// Entries returns every entry in append order, genesis included.
func (m *MemLedger) Entries() []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Entry, len(m.entries))
	for i, e := range m.entries {
		out[i] = e.entry
	}
	return out
}
