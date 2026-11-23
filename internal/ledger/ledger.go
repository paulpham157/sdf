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
// A kind is non-empty UTF-8 with no control characters, and the prefix
// "sdf.ledger." is reserved for genesis. An id matches IDPattern (see
// ValidID). Append rejects anything else (ErrInvalid); Verify and Get treat a
// stored row that breaks these rules as modified or corrupt, because the
// JSON encoding above cannot represent invalid UTF-8 without loss.
//
// The file is in WAL mode. Opening a ledger and reading it never writes the
// ledger file (SQLite may create its -wal and -shm sidecars). The file, the
// schema, and the genesis entry are created by the first Append (Prepare
// creates the file and schema only). AppendAll chains several payloads in
// one transaction.
//
// The chain makes tampering detectable, not impossible. Anyone who can write
// the file can rebuild the whole chain or cut its tail. Only a head or
// prefix hash kept somewhere else (CI, a signed export) turns that into a
// detectable change. See docs/ledger.md.
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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
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

// Item is one payload for AppendAll: the arguments of one Append.
type Item struct {
	Kind      Kind
	ID        string
	Canonical []byte
	Hash      string
}

// BatchLedger is a Ledger that can chain several payloads in one
// transaction. Store implements it.
type BatchLedger interface {
	Ledger
	// AppendAll appends items in order, all or none: when any item fails,
	// nothing from the call is stored. Each item follows Append's rules.
	AppendAll(ctx context.Context, items []Item) ([]Entry, error)
}

var (
	// ErrHashMismatch means the hash passed to Append is not the hash of the payload.
	ErrHashMismatch = errors.New("ledger: hash does not match payload")
	// ErrConflict means (kind, id) is already stored with a different hash,
	// or the kind already holds an id that differs from id only in ASCII case.
	ErrConflict = errors.New("ledger: conflicts with a stored entry")
	// ErrNotFound means no entry has that (kind, id), or the ledger is empty.
	ErrNotFound = errors.New("ledger: not found")
	// ErrCorrupt means a stored row no longer matches its payload hash or its
	// entry hash, or holds a field the ledger would never write. Get wraps it
	// with the kind and id; GetBySeq wraps it with the seq, kind, and id.
	ErrCorrupt = errors.New("ledger: stored entry does not match its hash")
	// ErrInvalid means Append was given a kind or id the ledger does not accept.
	ErrInvalid = errors.New("ledger: invalid kind or id")
	// ErrInvalidID means an id breaks the id rules (see ValidID). It wraps
	// ErrInvalid, so errors.Is(err, ErrInvalid) holds too.
	ErrInvalidID = fmt.Errorf("%w: invalid id", ErrInvalid)
	// ErrCorruptFile means the file is not a readable SQLite database: not
	// SQLite at all, truncated, or damaged (SQLITE_NOTADB, SQLITE_CORRUPT).
	// It is wrapped together with the driver error. Other failures to open
	// or read (permissions, a missing directory, EIO) are returned as is.
	ErrCorruptFile = errors.New("ledger: file is not a readable ledger database")
	// ErrPrefixMismatch means a prefix pin does not match the stored entry:
	// the entry_hash differs, or the seq is missing. A missing seq also
	// wraps ErrNotFound, so both errors.Is(err, ErrPrefixMismatch) and
	// errors.Is(err, ErrNotFound) hold.
	ErrPrefixMismatch = errors.New("ledger: prefix pin does not match")
	// ErrChainBroken means Verify found a broken chain. VerifyPrefix
	// returns a *ChainError that unwraps to this and carries the first
	// Failure.
	ErrChainBroken = errors.New("ledger: chain is broken")
)

// classify wraps a SQLite NOTADB or CORRUPT error with ErrCorruptFile.
func classify(err error) error {
	if err == nil || errors.Is(err, ErrCorruptFile) {
		return err
	}
	switch sqliteCode(err) & 0xff {
	case 11, 26: // SQLITE_CORRUPT, SQLITE_NOTADB
		return fmt.Errorf("%w: %w", ErrCorruptFile, err)
	}
	return err
}

// IDPattern is the id rule, the same pattern as task.ValidID in the
// verifier (internal/task): 1 to MaxIDLen ASCII letters, digits, ".", "_",
// or "-", starting with a letter or digit. Case is preserved and
// significant.
const IDPattern = `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`

// MaxIDLen is the longest id, in bytes.
const MaxIDLen = 128

var idPattern = regexp.MustCompile(IDPattern)

// ReservedKindPrefix is reserved for the ledger's own entries. Only
// GenesisKind uses it; Append rejects any other kind that starts with it.
const ReservedKindPrefix = "sdf.ledger."

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
-- Not UNIQUE on purpose: a ledger from an earlier build may already hold
-- two ids that differ only in case, and the index must still build there.
-- Append refuses a new case variant (ErrConflict); Verify flags old ones.
CREATE INDEX IF NOT EXISTS ledger_entries_kind_id_nocase
    ON ledger_entries (kind, id COLLATE NOCASE);
`

// Store is one ledger file. It implements Ledger.
type Store struct {
	path     string
	mu       sync.Mutex
	db       *sql.DB
	writable bool
}

var _ BatchLedger = (*Store)(nil)

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

// kindProblem says why kind is not a valid kind, or "" when it is. A kind is
// non-empty valid UTF-8 with no control characters (so no "\n", which would
// let two kind/payload splits share a payload hash).
func kindProblem(kind Kind) string {
	switch {
	case kind == "":
		return "empty kind"
	case !utf8.ValidString(string(kind)):
		return "kind is not valid UTF-8"
	case strings.IndexFunc(string(kind), unicode.IsControl) >= 0:
		return "kind contains a control character"
	}
	return ""
}

// ValidID returns nil when id matches IDPattern, else an error wrapping
// ErrInvalidID that names the rule it breaks. The pattern alone implies
// valid UTF-8, no control character, no whitespace, no path separator, no
// leading "-" or ".", and at most MaxIDLen bytes. ULIDs (uppercase
// Crockford base32) and lowercase task ids both match.
func ValidID(id string) error {
	if p := idProblem(id); p != "" {
		return fmt.Errorf("%w %q: %s", ErrInvalidID, id, p)
	}
	return nil
}

// idProblem says which id rule id breaks, or "".
func idProblem(id string) string {
	switch {
	case id == "":
		return "empty"
	case len(id) > MaxIDLen:
		return fmt.Sprintf("longer than %d bytes", MaxIDLen)
	case !utf8.ValidString(id):
		return "not valid UTF-8"
	case strings.IndexFunc(id, unicode.IsControl) >= 0:
		return "contains a control character"
	case strings.IndexFunc(id, unicode.IsSpace) >= 0:
		return "contains whitespace"
	case strings.ContainsAny(id, `/\`):
		return "contains a path separator"
	case !isIDStart(id[0]):
		return "must start with an ASCII letter or digit"
	case !idPattern.MatchString(id):
		return "may contain only ASCII letters, digits, '.', '_', and '-'"
	}
	return ""
}

// Safe returns s unchanged when every rune in it is printable (no control
// characters, so no terminal escape sequences), and as a Go-quoted ASCII
// string otherwise. Use it for any stored string shown to a person that is
// not already quoted with %q.
func Safe(s string) string {
	if utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) < 0 {
		return s
	}
	return strconv.QuoteToASCII(s)
}

// asciiLower folds A-Z to a-z and leaves every other byte alone, as
// SQLite's NOCASE does.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func isIDStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func checkKey(kind Kind, id string) error {
	if p := kindProblem(kind); p != "" {
		return fmt.Errorf("%w: %s", ErrInvalid, p)
	}
	if strings.HasPrefix(string(kind), ReservedKindPrefix) {
		return fmt.Errorf("%w: kind prefix %q is reserved", ErrInvalid, ReservedKindPrefix)
	}
	return ValidID(id)
}

func dsn(path, query string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: query}).String()
}

// SQLite extended result codes readDB handles.
const (
	sqliteReadonlyRollback = 776  // SQLITE_READONLY_ROLLBACK: hot journal
	sqliteReadonlyCantInit = 1544 // SQLITE_READONLY_CANTINIT: no -shm, cannot create one
)

func sqliteCode(err error) int {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		return coded.Code()
	}
	return 0
}

// readDB returns a handle for reading, or nil when there is no file yet.
//
// It opens the file read-only (mode=ro), so a read never changes the ledger
// file. A WAL ledger may get its -wal and -shm sidecar files created by
// SQLite. When those cannot be created (a read-only directory) and there is
// no -wal file, every committed entry is in the main file, and it is read
// with immutable=1 instead. Caller holds s.mu.
func (s *Store) readDB(ctx context.Context) (*sql.DB, error) {
	if s.db != nil {
		return s.db, nil
	}
	if _, err := os.Stat(s.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	db, err := openProbe(ctx, dsn(s.path, "mode=ro&_pragma=busy_timeout(5000)"))
	if sqliteCode(err) == sqliteReadonlyCantInit {
		if _, werr := os.Stat(s.path + "-wal"); errors.Is(werr, os.ErrNotExist) {
			db, err = openProbe(ctx, dsn(s.path, "mode=ro&immutable=1"))
		}
	}
	if err != nil {
		if sqliteCode(err) == sqliteReadonlyRollback {
			return nil, fmt.Errorf("ledger: %q has a hot journal from an interrupted write in the old rollback-journal mode; "+
				"reading it would mean rolling that write back, and reads do not write. The next append rolls it back "+
				"and switches the file to WAL: %w", s.path, err)
		}
		return nil, err
	}
	s.db = db
	return db, nil
}

// openProbe opens dsn with one connection and reads the schema, so that an
// unreadable file fails here and not halfway through a query.
func openProbe(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master`).Scan(&n); err != nil {
		_ = db.Close()
		return nil, err
	}
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
	// synchronous(FULL) keeps a committed append durable across power loss.
	// WAL is set by Append (setWAL), not here: a journal_mode pragma run while
	// a connection opens can fail with SQLITE_BUSY before the busy timeout
	// applies.
	db, err := sql.Open("sqlite", dsn(s.path, "mode=rw&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)"))
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
	textOK    bool // every text column holds a TEXT value
}

const selectCols = `seq, kind, id, payload, hash, prev_hash, entry_hash, chained_at,
	(typeof(kind) = 'text' AND typeof(id) = 'text' AND typeof(hash) = 'text' AND
	 typeof(prev_hash) = 'text' AND typeof(entry_hash) = 'text' AND typeof(chained_at) = 'text')`

func scanRow(r rowScanner) (storedRow, error) {
	var row storedRow
	var kind string
	if err := r.Scan(&row.entry.Seq, &kind, &row.entry.ID, &row.payload, &row.entry.Hash, &row.entry.PrevHash, &row.entry.EntryHash, &row.chainedAt, &row.textOK); err != nil {
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

// fieldProblem says why a stored row holds something Append would never
// write, or "". The entry hash encodes strings as JSON, which cannot carry
// invalid UTF-8, so a row whose fields are not valid UTF-8 (or a chained_at
// that is not in TimeLayout) is rejected here rather than hashed lossily.
func fieldProblem(row storedRow) string {
	e := row.entry
	switch {
	case !row.textOK:
		return "a text column holds a value that is not TEXT"
	case kindProblem(e.Kind) != "":
		return kindProblem(e.Kind)
	case idProblem(e.ID) != "":
		return "id " + idProblem(e.ID)
	case e.Seq != 0 && strings.HasPrefix(string(e.Kind), ReservedKindPrefix):
		return fmt.Sprintf("kind uses the reserved prefix %q", ReservedKindPrefix)
	case formatTime(e.ChainedAt) != row.chainedAt:
		return fmt.Sprintf("chained_at %q is not in the form %s", row.chainedAt, TimeLayout)
	}
	return ""
}

func payloadOK(row storedRow) bool {
	return PayloadHash(row.entry.Kind, row.payload) == row.entry.Hash
}

func entryOK(row storedRow) bool {
	e := row.entry
	return entryHash(e.Seq, e.Kind, e.ID, e.Hash, e.PrevHash, row.chainedAt) == e.EntryHash
}

// Append implements Ledger.
func (s *Store) Append(ctx context.Context, kind Kind, id string, canonical []byte, hash string) (Entry, error) {
	entries, err := s.AppendAll(ctx, []Item{{Kind: kind, ID: id, Canonical: canonical, Hash: hash}})
	if err != nil {
		return Entry{}, err
	}
	return entries[0], nil
}

// AppendAll implements BatchLedger. Every item is checked before the file
// is touched, and all of them are chained in one BEGIN IMMEDIATE
// transaction, so a failure part way leaves the chain as it was. An item
// that repeats a stored (kind, id) with the same hash returns the stored
// entry, as Append does.
func (s *Store) AppendAll(ctx context.Context, items []Item) ([]Entry, error) {
	if len(items) == 0 {
		return nil, nil
	}
	checked := make([]Item, len(items))
	for i, it := range items {
		if err := checkKey(it.Kind, it.ID); err != nil {
			return nil, err
		}
		if it.Canonical == nil {
			it.Canonical = []byte{}
		}
		if it.Hash != PayloadHash(it.Kind, it.Canonical) {
			return nil, ErrHashMismatch
		}
		checked[i] = it
	}
	// s.mu is held for one attempt at a time, never across a retry sleep.
	var entries []Entry
	err := retryBusy(ctx, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		db, err := s.writeDB()
		if err != nil {
			return err
		}
		entries, err = appendOnce(ctx, db, checked)
		return err
	})
	return entries, classify(err)
}

// Prepare makes sure the ledger can be written: it creates the file and its
// table if they are missing, in one transaction, without appending an
// entry (the genesis entry still comes with the first Append). A ledger
// that cannot be created, opened for writing, or locked fails here, before
// a caller does work whose record would then be lost. A file that is not a
// ledger database is ErrCorruptFile.
func (s *Store) Prepare(ctx context.Context) error {
	err := retryBusy(ctx, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		db, err := s.writeDB()
		if err != nil {
			return err
		}
		_, err = appendOnce(ctx, db, nil)
		return err
	})
	return classify(err)
}

// busyWait bounds how long Append retries SQLITE_BUSY and SQLITE_LOCKED,
// on top of each connection's own busy_timeout.
var busyWait = 10 * time.Second

func isBusy(err error) bool {
	c := sqliteCode(err) & 0xff
	return c == 5 || c == 6 // SQLITE_BUSY, SQLITE_LOCKED
}

// retryBusy runs f until it returns something other than a busy error, the
// context ends, or busyWait passes. A busy attempt has rolled back, so
// running f again is safe.
func retryBusy(ctx context.Context, f func() error) error {
	deadline := time.Now().Add(busyWait)
	delay := time.Millisecond
	for {
		err := f()
		if !isBusy(err) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}
		if delay < 100*time.Millisecond {
			delay *= 2
		}
	}
}

// setWAL switches the file to WAL unless it already is. WAL is a property
// of the file, so this runs once per file in practice. In WAL mode a reader
// never needs to roll back a hot journal, so reads stay read-only even after
// a writer crashed. An older rollback-journal ledger is switched here, after
// SQLite has rolled back any hot journal it had.
func setWAL(ctx context.Context, conn *sql.Conn) error {
	var mode string
	if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		return err
	}
	if strings.EqualFold(mode, "wal") {
		return nil
	}
	if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode = WAL`).Scan(&mode); err != nil {
		return err
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("ledger: could not switch the ledger to WAL (journal_mode is %q)", mode)
	}
	return nil
}

// appendOnce chains items in one transaction. With no items it only
// creates the schema (Prepare).
func appendOnce(ctx context.Context, db *sql.DB, items []Item) ([]Entry, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := setWAL(ctx, conn); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, err
	}
	rollback := func(err error) ([]Entry, error) {
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, schema); err != nil {
		return rollback(err)
	}
	entries := make([]Entry, 0, len(items))
	for _, it := range items {
		entry, err := appendLocked(ctx, conn, it.Kind, it.ID, it.Canonical, it.Hash)
		if err != nil {
			return rollback(err)
		}
		entries = append(entries, entry)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return rollback(err)
	}
	return entries, nil
}

func appendLocked(ctx context.Context, conn *sql.Conn, kind Kind, id string, canonical []byte, hash string) (Entry, error) {
	existing, err := scanRow(conn.QueryRowContext(ctx, `SELECT `+selectCols+` FROM ledger_entries WHERE kind = ? AND id = ?`, string(kind), id))
	switch {
	case err == nil:
		if existing.entry.Hash != hash {
			return Entry{}, fmt.Errorf("%w: %q %q is stored with hash %q", ErrConflict, kind, id, existing.entry.Hash)
		}
		return existing.entry, nil
	case !errors.Is(err, sql.ErrNoRows):
		return Entry{}, err
	}
	// NOCASE folds ASCII only, which is all an id may contain.
	var other string
	var otherSeq int64
	err = conn.QueryRowContext(ctx, `SELECT id, seq FROM ledger_entries WHERE kind = ? AND id = ? COLLATE NOCASE ORDER BY seq LIMIT 1`,
		string(kind), id).Scan(&other, &otherSeq)
	switch {
	case err == nil:
		return Entry{}, fmt.Errorf("%w: %q id %q differs only in case from stored id %q (seq %d)", ErrConflict, kind, id, other, otherSeq)
	case !errors.Is(err, sql.ErrNoRows):
		return Entry{}, err
	}
	// Genesis and the first entry are written in one transaction with one
	// timestamp, so genesis is never later than seq 1.
	at := formatTime(now())
	head, err := scanRow(conn.QueryRowContext(ctx, `SELECT `+selectCols+` FROM ledger_entries ORDER BY seq DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		g := []byte(GenesisPayload)
		head.entry, err = insert(ctx, conn, at, 0, GenesisKind, GenesisID, g, PayloadHash(GenesisKind, g), ZeroHash)
		if err != nil {
			return Entry{}, err
		}
	} else if err != nil {
		return Entry{}, err
	}
	return insert(ctx, conn, at, head.entry.Seq+1, kind, id, canonical, hash, head.entry.EntryHash)
}

func insert(ctx context.Context, conn *sql.Conn, at string, seq int64, kind Kind, id string, canonical []byte, hash, prev string) (Entry, error) {
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

// Get implements Ledger. Get matches the id exactly, case included (Append is
// what refuses an id that differs from a stored one only in case). A kind
// that is empty, not UTF-8, or has a control character is ErrInvalid, and
// an id that breaks the id rules is ErrInvalidID, both without a lookup. It checks the row it reads: its fields, its payload
// hash, and its entry hash. A row that fails is ErrCorrupt, wrapped with the
// kind and id. Get does not walk the chain; Verify does.
func (s *Store) Get(ctx context.Context, kind Kind, id string) ([]byte, Entry, error) {
	if p := kindProblem(kind); p != "" {
		return nil, Entry{}, fmt.Errorf("%w: %s", ErrInvalid, p)
	}
	if err := ValidID(id); err != nil {
		return nil, Entry{}, err
	}
	payload, e, err := s.get(ctx, kind, id)
	return payload, e, classify(err)
}

func (s *Store) get(ctx context.Context, kind Kind, id string) ([]byte, Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.readDB(ctx)
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
	problem := fieldProblem(row)
	switch {
	case problem != "":
	case !payloadOK(row):
		problem = "payload does not hash to the chained hash"
	case !entryOK(row):
		problem = "entry fields do not hash to entry_hash"
	}
	if problem != "" {
		return nil, Entry{}, fmt.Errorf("%w: %q %q: %s", ErrCorrupt, kind, id, problem)
	}
	return row.payload, row.entry, nil
}

// Head returns the last stored entry, or ErrNotFound for an empty ledger.
// It does not verify the chain; Verify does.
func (s *Store) Head(ctx context.Context) (Entry, error) {
	e, err := s.head(ctx)
	return e, classify(err)
}

func (s *Store) head(ctx context.Context) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.readDB(ctx)
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

// GetBySeq returns the stored payload and entry at seq, or ErrNotFound.
// It checks the row the way Get does (fields, payload hash, entry hash).
// A row that fails is ErrCorrupt, wrapped with the seq, kind, and id.
// GetBySeq does not walk the chain; Verify and VerifyPrefix do.
func (s *Store) GetBySeq(ctx context.Context, seq int64) ([]byte, Entry, error) {
	payload, e, err := s.getBySeq(ctx, seq)
	return payload, e, classify(err)
}

func (s *Store) getBySeq(ctx context.Context, seq int64) ([]byte, Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.readDB(ctx)
	if err != nil || db == nil {
		return nil, Entry{}, errOrNotFound(err)
	}
	if ok, err := hasTable(ctx, db); err != nil || !ok {
		return nil, Entry{}, errOrNotFound(err)
	}
	row, err := scanRow(db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM ledger_entries WHERE seq = ?`, seq))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Entry{}, ErrNotFound
	}
	if err != nil {
		return nil, Entry{}, err
	}
	problem := fieldProblem(row)
	switch {
	case problem != "":
	case !payloadOK(row):
		problem = "payload does not hash to the chained hash"
	case !entryOK(row):
		problem = "entry fields do not hash to entry_hash"
	}
	if problem != "" {
		return nil, Entry{}, fmt.Errorf("%w: seq %d (%q %q): %s", ErrCorrupt, seq, row.entry.Kind, row.entry.ID, problem)
	}
	return row.payload, row.entry, nil
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
	// ProblemDuplicate: a (kind, id) is stored twice (the unique index was
	// dropped and a row inserted by hand), or two ids of one kind differ only
	// in ASCII case.
	ProblemDuplicate Problem = "duplicate"
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
// from genesis. It also checks each row's fields (valid UTF-8, no control
// characters in kind, the reserved kind prefix, chained_at form), genesis
// exactly, and that no (kind, id) appears twice. It stops at the first
// failure. An absent or empty ledger is OK and Empty. Verify never writes
// the ledger file (see readDB).
//
// A file that is not a readable SQLite database is an error wrapping
// ErrCorruptFile.
func (s *Store) Verify(ctx context.Context) (Report, error) {
	rep, err := s.verify(ctx)
	return rep, classify(err)
}

func (s *Store) verify(ctx context.Context) (Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rep := Report{Path: s.path, OK: true}
	db, err := s.readDB(ctx)
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
	type seenID struct {
		id  string
		seq int64
	}
	seen := map[[2]string]seenID{}
	genesisHash := PayloadHash(GenesisKind, []byte(GenesisPayload))
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
		case want == 0 && (row.entry.Kind != GenesisKind || row.entry.ID != GenesisID ||
			string(row.payload) != GenesisPayload || row.entry.Hash != genesisHash || row.entry.PrevHash != ZeroHash):
			fail(row, ProblemModified, "seq 0 is not the genesis entry (kind %s, id %q, payload %s, prev_hash %s)", GenesisKind, GenesisID, GenesisPayload, ZeroHash)
		case fieldProblem(row) != "":
			fail(row, ProblemModified, "%s", fieldProblem(row))
		case !payloadOK(row):
			fail(row, ProblemModified, "payload does not hash to the chained hash %q", row.entry.Hash)
		case prev != nil && row.entry.PrevHash != prev.entry.EntryHash:
			fail(row, ProblemReordered, "prev_hash does not match the entry_hash of seq %d (a row was moved or inserted, or an earlier entry was rewritten)", prev.entry.Seq)
		case !entryOK(row):
			fail(row, ProblemModified, "entry fields do not hash to entry_hash %q", row.entry.EntryHash)
		case want == 1 && prev.chainedAt > row.chainedAt:
			// Genesis is written with seq 1, in the same transaction. Its
			// exact time cannot be known in advance, but it is never later.
			fail(*prev, ProblemModified, "genesis chained_at %q is later than seq 1 chained_at %q", prev.chainedAt, row.chainedAt)
		default:
			key := [2]string{string(row.entry.Kind), asciiLower(row.entry.ID)}
			if first, dup := seen[key]; dup {
				if first.id == row.entry.ID {
					fail(row, ProblemDuplicate, "(kind, id) is already stored at seq %d", first.seq)
				} else {
					fail(row, ProblemDuplicate, "id %q differs only in case from id %q at seq %d", row.entry.ID, first.id, first.seq)
				}
			} else {
				seen[key] = seenID{row.entry.ID, row.entry.Seq}
			}
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
