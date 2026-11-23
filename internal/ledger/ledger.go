// Package ledger is an append-only, hash-chained local record of canonical
// payloads, such as tasks, attempts, evidence, and verdicts.
//
// The ledger is its own SQLite file, separate from the telemetry store. It
// refers to telemetry (sessions, commits) only by id inside a payload.
//
// A payload is opaque. The caller canonicalizes it (the verifier uses JCS,
// RFC 8785) and passes its hash. The ledger checks that hash, stores the
// bytes as given, and never reserializes them:
//
//	hash = "sha256:" + hex(sha256(kind + "\n" + canonical))
//
// Every entry links to the one before it:
//
//	entry_hash = "sha256:" + hex(sha256("sdf.ledger.v1.entry\n" + J))
//
// where J is the RFC 8785 serialization of the object
// {"chained_at","hash","id","kind","prev_hash","seq"} (keys in that order,
// no whitespace, seq a JSON integer, the rest JSON strings). chained_at is
// UTC with exactly nine fraction digits, for example
// "2026-11-23T05:40:00.000000000Z". Seq 0 is a genesis entry whose
// prev_hash is "sha256:" followed by 64 zeros. Seq is contiguous.
//
// Opening a ledger and reading it never writes. The file, the schema, and
// the genesis entry are created by the first Append.
//
// The chain makes tampering detectable, not impossible. Anyone who can write
// the file can rebuild the whole chain or cut its tail. Only a head hash
// kept somewhere else (CI, a signed export) turns that into a detectable
// change. See docs/ledger.md.
package ledger

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

// Kind is a payload schema name, for example "sdf.task/v1".
type Kind string

// GenesisKind is the kind of the seq 0 entry.
const GenesisKind Kind = "sdf.ledger.genesis/v1"

// GenesisID is the id of the seq 0 entry.
const GenesisID = "genesis"

// GenesisPayload is the canonical payload of the seq 0 entry.
const GenesisPayload = `{"schema":"sdf.ledger/v1"}`

// ZeroHash is the prev_hash of the genesis entry.
var ZeroHash = "sha256:" + strings.Repeat("0", 64)

// TimeLayout is the stored and hashed form of chained_at.
const TimeLayout = "2006-01-02T15:04:05.000000000Z"

const entryDomain = "sdf.ledger.v1.entry\n"

// Entry is one link of the chain. Hash is the payload hash.
type Entry struct {
	Seq       int64
	Kind      Kind
	ID        string
	Hash      string
	PrevHash  string
	EntryHash string
	ChainedAt time.Time
}

// Ledger is what a writer of canonical records needs.
type Ledger interface {
	// Append stores canonical under (kind, id) and chains it. hash must equal
	// PayloadHash(kind, canonical), or ErrHashMismatch. Appending the same
	// (kind, id) with the same hash again returns the stored entry. A
	// different hash for a stored (kind, id) is ErrConflict.
	Append(ctx context.Context, kind Kind, id string, canonical []byte, hash string) (Entry, error)
	// Get returns the stored bytes and entry, or ErrNotFound.
	Get(ctx context.Context, kind Kind, id string) ([]byte, Entry, error)
}

var (
	// ErrHashMismatch means the hash passed to Append is not the hash of the payload.
	ErrHashMismatch = errors.New("ledger: hash does not match payload")
	// ErrConflict means (kind, id) is already stored with a different hash.
	ErrConflict = errors.New("ledger: id already stored with a different hash")
	// ErrNotFound means no entry has that (kind, id), or the ledger is empty.
	ErrNotFound = errors.New("ledger: not found")
)

const schema = `
CREATE TABLE IF NOT EXISTS ledger_entries (
    seq        INTEGER PRIMARY KEY,
    kind       TEXT NOT NULL,
    id         TEXT NOT NULL,
    payload    BLOB NOT NULL,
    hash       TEXT NOT NULL,
    prev_hash  TEXT NOT NULL,
    entry_hash TEXT NOT NULL,
    chained_at TEXT NOT NULL,
    UNIQUE (kind, id)
);
`

// Store is one ledger file. It implements Ledger.
type Store struct {
	path     string
	mu       sync.Mutex
	db       *sql.DB
	writable bool
}

var _ Ledger = (*Store)(nil)

// DefaultPath is $SDF_LEDGER, else $XDG_DATA_HOME/sdf/ledger.db, else
// ~/.local/share/sdf/ledger.db.
func DefaultPath() (string, error) {
	if p := os.Getenv("SDF_LEDGER"); p != "" {
		return p, nil
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, "sdf", "ledger.db"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("ledger: no default path: %w", err)
	}
	return filepath.Join(home, ".local", "share", "sdf", "ledger.db"), nil
}

// Open returns a ledger at path. It does not create or change the file.
// A missing file reads as an empty ledger until the first Append.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("ledger: empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: abs}, nil
}

// Path is the absolute ledger file path.
func (s *Store) Path() string { return s.path }

// Close releases the file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	s.writable = false
	return err
}

// PayloadHash is "sha256:" + hex(sha256(kind + "\n" + canonical)).
func PayloadHash(kind Kind, canonical []byte) string {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{'\n'})
	h.Write(canonical)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// EntryHash is the chain hash of e. It reads Seq, Kind, ID, Hash, PrevHash,
// and ChainedAt.
func EntryHash(e Entry) string {
	return entryHash(e.Seq, e.Kind, e.ID, e.Hash, e.PrevHash, formatTime(e.ChainedAt))
}

func entryHash(seq int64, kind Kind, id, hash, prev, chainedAt string) string {
	var b strings.Builder
	b.WriteString(`{"chained_at":`)
	writeJSONString(&b, chainedAt)
	b.WriteString(`,"hash":`)
	writeJSONString(&b, hash)
	b.WriteString(`,"id":`)
	writeJSONString(&b, id)
	b.WriteString(`,"kind":`)
	writeJSONString(&b, string(kind))
	b.WriteString(`,"prev_hash":`)
	writeJSONString(&b, prev)
	b.WriteString(`,"seq":`)
	b.WriteString(strconv.FormatInt(seq, 10))
	b.WriteString(`}`)
	sum := sha256.Sum256([]byte(entryDomain + b.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// writeJSONString writes s as RFC 8785 does: only '"', '\\', and control
// characters are escaped; \b \f \n \r \t use their short forms; other
// control characters are \u00xx with lowercase hex. s must be valid UTF-8.
func writeJSONString(b *strings.Builder, s string) {
	const hexDigits = "0123456789abcdef"
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hexDigits[r>>4])
				b.WriteByte(hexDigits[r&0xf])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

func formatTime(t time.Time) string { return t.UTC().Format(TimeLayout) }

// now is replaced in tests.
var now = time.Now

func checkKey(kind Kind, id string) error {
	if kind == "" {
		return errors.New("ledger: empty kind")
	}
	if id == "" {
		return errors.New("ledger: empty id")
	}
	if !utf8.ValidString(string(kind)) || !utf8.ValidString(id) {
		return errors.New("ledger: kind and id must be valid UTF-8")
	}
	return nil
}

func dsn(path, query string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: query}).String()
}

// readDB returns a handle for reading, or nil when there is no file yet.
// It opens the file read-only. Caller holds s.mu.
func (s *Store) readDB() (*sql.DB, error) {
	if s.db != nil {
		return s.db, nil
	}
	if _, err := os.Stat(s.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn(s.path, "mode=ro&_pragma=busy_timeout(5000)"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s.db = db
	return db, nil
}

// writeDB returns a read-write handle, creating the file. Caller holds s.mu.
func (s *Store) writeDB() (*sql.DB, error) {
	if s.db != nil && s.writable {
		return s.db, nil
	}
	if s.db != nil {
		_ = s.db.Close()
		s.db = nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(s.path); errors.Is(err, os.ErrNotExist) {
		f, err := os.OpenFile(s.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if f != nil {
			_ = f.Close()
		}
	}
	db, err := sql.Open("sqlite", dsn(s.path, "mode=rw&_pragma=busy_timeout(5000)"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s.db, s.writable = db, true
	return db, nil
}

// hasTable reports whether the ledger table exists.
func hasTable(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'ledger_entries'`).Scan(&n)
	return n > 0, err
}

type rowScanner interface{ Scan(...any) error }

type storedRow struct {
	entry     Entry
	payload   []byte
	chainedAt string
}

const selectCols = `seq, kind, id, payload, hash, prev_hash, entry_hash, chained_at`

func scanRow(r rowScanner) (storedRow, error) {
	var row storedRow
	var kind string
	if err := r.Scan(&row.entry.Seq, &kind, &row.entry.ID, &row.payload, &row.entry.Hash, &row.entry.PrevHash, &row.entry.EntryHash, &row.chainedAt); err != nil {
		return storedRow{}, err
	}
	row.entry.Kind = Kind(kind)
	t, err := time.Parse(TimeLayout, row.chainedAt)
	if err == nil {
		row.entry.ChainedAt = t
	}
	if row.payload == nil {
		row.payload = []byte{}
	}
	return row, nil
}

// Append implements Ledger.
func (s *Store) Append(ctx context.Context, kind Kind, id string, canonical []byte, hash string) (Entry, error) {
	if err := checkKey(kind, id); err != nil {
		return Entry{}, err
	}
	if canonical == nil {
		canonical = []byte{}
	}
	if hash != PayloadHash(kind, canonical) {
		return Entry{}, ErrHashMismatch
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.writeDB()
	if err != nil {
		return Entry{}, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return Entry{}, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return Entry{}, err
	}
	entry, err := appendLocked(ctx, conn, kind, id, canonical, hash)
	if err != nil {
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		return Entry{}, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		return Entry{}, err
	}
	return entry, nil
}

func appendLocked(ctx context.Context, conn *sql.Conn, kind Kind, id string, canonical []byte, hash string) (Entry, error) {
	if _, err := conn.ExecContext(ctx, schema); err != nil {
		return Entry{}, err
	}
	existing, err := scanRow(conn.QueryRowContext(ctx, `SELECT `+selectCols+` FROM ledger_entries WHERE kind = ? AND id = ?`, string(kind), id))
	switch {
	case err == nil:
		if existing.entry.Hash != hash {
			return Entry{}, ErrConflict
		}
		return existing.entry, nil
	case !errors.Is(err, sql.ErrNoRows):
		return Entry{}, err
	}
	head, err := scanRow(conn.QueryRowContext(ctx, `SELECT `+selectCols+` FROM ledger_entries ORDER BY seq DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		g := []byte(GenesisPayload)
		head.entry, err = insert(ctx, conn, 0, GenesisKind, GenesisID, g, PayloadHash(GenesisKind, g), ZeroHash)
		if err != nil {
			return Entry{}, err
		}
		if kind == GenesisKind && id == GenesisID {
			if hash != head.entry.Hash {
				return Entry{}, ErrConflict
			}
			return head.entry, nil
		}
	} else if err != nil {
		return Entry{}, err
	}
	return insert(ctx, conn, head.entry.Seq+1, kind, id, canonical, hash, head.entry.EntryHash)
}

func insert(ctx context.Context, conn *sql.Conn, seq int64, kind Kind, id string, canonical []byte, hash, prev string) (Entry, error) {
	at := formatTime(now())
	t, _ := time.Parse(TimeLayout, at)
	e := Entry{Seq: seq, Kind: kind, ID: id, Hash: hash, PrevHash: prev, ChainedAt: t}
	e.EntryHash = entryHash(seq, kind, id, hash, prev, at)
	_, err := conn.ExecContext(ctx, `
		INSERT INTO ledger_entries (seq, kind, id, payload, hash, prev_hash, entry_hash, chained_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, e.Seq, string(e.Kind), e.ID, canonical, e.Hash, e.PrevHash, e.EntryHash, at)
	if err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Get implements Ledger. It returns the bytes as stored; Verify is what
// checks them.
func (s *Store) Get(ctx context.Context, kind Kind, id string) ([]byte, Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.readDB()
	if err != nil || db == nil {
		return nil, Entry{}, errOrNotFound(err)
	}
	if ok, err := hasTable(ctx, db); err != nil || !ok {
		return nil, Entry{}, errOrNotFound(err)
	}
	row, err := scanRow(db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM ledger_entries WHERE kind = ? AND id = ?`, string(kind), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Entry{}, ErrNotFound
	}
	if err != nil {
		return nil, Entry{}, err
	}
	return row.payload, row.entry, nil
}

// Head returns the last stored entry, or ErrNotFound for an empty ledger.
// It does not verify the chain; Verify does.
func (s *Store) Head(ctx context.Context) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.readDB()
	if err != nil || db == nil {
		return Entry{}, errOrNotFound(err)
	}
	if ok, err := hasTable(ctx, db); err != nil || !ok {
		return Entry{}, errOrNotFound(err)
	}
	row, err := scanRow(db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM ledger_entries ORDER BY seq DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	return row.entry, nil
}

func errOrNotFound(err error) error {
	if err != nil {
		return err
	}
	return ErrNotFound
}

// Problem names how a chain is broken.
type Problem string

const (
	// ProblemModified: a payload, or an entry's own fields, no longer hash
	// to what was chained.
	ProblemModified Problem = "modified"
	// ProblemMissing: a seq is absent (a deleted row), or there is no genesis.
	ProblemMissing Problem = "missing"
	// ProblemReordered: an entry's prev_hash does not link to the entry
	// before it (a row moved, was inserted, or an earlier entry was rewritten).
	ProblemReordered Problem = "gap-reordered"
)

// Failure is the first broken link Verify found.
type Failure struct {
	Seq     int64   `json:"seq"`
	Kind    Kind    `json:"kind,omitempty"`
	ID      string  `json:"id,omitempty"`
	Problem Problem `json:"problem"`
	Detail  string  `json:"detail"`
}

// Report is the result of Verify. Head is the last stored entry; when OK is
// false it is not trustworthy. Entries counts stored rows, genesis included.
type Report struct {
	Path    string   `json:"path"`
	OK      bool     `json:"ok"`
	Empty   bool     `json:"empty"`
	Entries int64    `json:"entries"`
	Head    *Entry   `json:"-"`
	Failure *Failure `json:"failure,omitempty"`
}

// Verify recomputes every payload hash and entry hash and walks the links
// from genesis. It stops at the first failure. An absent or empty ledger is
// OK and Empty. Verify never writes.
func (s *Store) Verify(ctx context.Context) (Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rep := Report{Path: s.path, OK: true}
	db, err := s.readDB()
	if err != nil {
		return Report{}, err
	}
	if db == nil {
		rep.Empty = true
		return rep, nil
	}
	ok, err := hasTable(ctx, db)
	if err != nil {
		return Report{}, err
	}
	if !ok {
		rep.Empty = true
		return rep, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT `+selectCols+` FROM ledger_entries ORDER BY seq`)
	if err != nil {
		return Report{}, err
	}
	defer rows.Close()
	var prev *storedRow
	fail := func(row storedRow, p Problem, format string, args ...any) {
		if rep.Failure != nil {
			return
		}
		rep.OK = false
		rep.Failure = &Failure{Seq: row.entry.Seq, Kind: row.entry.Kind, ID: row.entry.ID, Problem: p, Detail: fmt.Sprintf(format, args...)}
	}
	for rows.Next() {
		row, err := scanRow(rows)
		if err != nil {
			return Report{}, err
		}
		rep.Entries++
		head := row.entry
		rep.Head = &head
		if rep.Failure != nil {
			continue
		}
		want := int64(0)
		if prev != nil {
			want = prev.entry.Seq + 1
		}
		switch {
		case row.entry.Seq != want:
			if want == 0 {
				fail(row, ProblemMissing, "seq 0 (genesis) is missing; the first stored seq is %d", row.entry.Seq)
			} else {
				fail(row, ProblemMissing, "seq %d is missing (seq %d follows seq %d)", want, row.entry.Seq, prev.entry.Seq)
				rep.Failure.Seq = want
				rep.Failure.Kind, rep.Failure.ID = "", ""
			}
		case want == 0 && (row.entry.Kind != GenesisKind || row.entry.ID != GenesisID || row.entry.PrevHash != ZeroHash):
			fail(row, ProblemModified, "seq 0 is not the genesis entry")
		case PayloadHash(row.entry.Kind, row.payload) != row.entry.Hash:
			fail(row, ProblemModified, "payload does not hash to the chained hash %s", row.entry.Hash)
		case prev != nil && row.entry.PrevHash != prev.entry.EntryHash:
			fail(row, ProblemReordered, "prev_hash does not match the entry_hash of seq %d (a row was moved or inserted, or an earlier entry was rewritten)", prev.entry.Seq)
		case entryHash(row.entry.Seq, row.entry.Kind, row.entry.ID, row.entry.Hash, row.entry.PrevHash, row.chainedAt) != row.entry.EntryHash:
			fail(row, ProblemModified, "entry fields do not hash to entry_hash %s", row.entry.EntryHash)
		}
		r := row
		prev = &r
	}
	if err := rows.Err(); err != nil {
		return Report{}, err
	}
	if rep.Entries == 0 {
		rep.Empty = true
	}
	return rep, nil
}
