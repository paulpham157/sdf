package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/paulpham157/sdf/internal/ledger"
)

// Exit codes of sdf audit: 0 intact; 1 broken, a failed check, or a file
// that is not a readable ledger database; 2 usage; 3 the ledger or an
// --expect-head-file / --expect-prefix-file could not be opened or read
// (permissions, a missing path, EIO, a hot journal).
const exitIO = 3

// ioError marks an error reading an input file (exit 3, not 2).
type ioError struct{ err error }

func (e *ioError) Error() string { return e.err.Error() }
func (e *ioError) Unwrap() error { return e.err }

const auditUsage = "usage: sdf audit <verify|head> [--ledger PATH] [--json]; see sdf audit verify --help"

func cmdAudit(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, auditUsage)
		return 2
	}
	switch args[0] {
	case "verify":
		return cmdAuditVerify(args[1:], stdout, stderr)
	case "head":
		return cmdAuditHead(args[1:], stdout, stderr)
	case "--help", "-h":
		fmt.Fprintln(stdout, auditUsage)
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, auditHelp)
		return 0
	default:
		unrecognizedArgs(stderr, args[0])
		return 2
	}
}

// parseAuditFlags reads --ledger PATH and --json. The path defaults to
// ledger.DefaultPath ($SDF_LEDGER, then $XDG_DATA_HOME/sdf/ledger.db, then
// ~/.local/share/sdf/ledger.db). done is true when the caller should return code.
func parseAuditFlags(name, usage string, args []string, stdout, stderr io.Writer) (path string, asJSON bool, code int, done bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, usage)
			return "", false, 0, true
		case arg == "--json":
			asJSON = true
		case arg == "--ledger":
			v, ok := take(&i, args)
			if !ok || v == "" {
				fmt.Fprintf(stderr, "%s: --ledger requires a value\n", name)
				return "", false, 2, true
			}
			path = v
		case strings.HasPrefix(arg, "--ledger="):
			path = strings.TrimPrefix(arg, "--ledger=")
			if path == "" {
				fmt.Fprintf(stderr, "%s: --ledger requires a value\n", name)
				return "", false, 2, true
			}
		default:
			unrecognizedArgs(stderr, arg)
			return "", false, 2, true
		}
	}
	if path == "" {
		p, err := ledger.DefaultPath()
		if err != nil {
			fmt.Fprintf(stderr, "%s: %s\n", name, ledger.Safe(err.Error()))
			return "", false, exitIO, true
		}
		path = p
	}
	return path, asJSON, 0, false
}

type auditEntry struct {
	Seq       int64  `json:"seq"`
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Hash      string `json:"hash"`
	PrevHash  string `json:"prev_hash"`
	EntryHash string `json:"entry_hash"`
	ChainedAt string `json:"chained_at"`
}

func toAuditEntry(e *ledger.Entry) *auditEntry {
	if e == nil {
		return nil
	}
	return &auditEntry{
		Seq:       e.Seq,
		Kind:      string(e.Kind),
		ID:        e.ID,
		Hash:      e.Hash,
		PrevHash:  e.PrevHash,
		EntryHash: e.EntryHash,
		ChainedAt: e.ChainedAt.UTC().Format(ledger.TimeLayout),
	}
}

func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// openVerified opens the ledger and runs Verify once. On success the
// caller must Close the store. An exit code of 1 is a file that is not
// a readable ledger database (garbage, truncated, damaged); 3 is any
// other failure to open or read it.
func openVerified(name, path string, stderr io.Writer) (*ledger.Store, ledger.Report, int) {
	l, err := ledger.Open(path)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %s\n", name, ledger.Safe(err.Error()))
		return nil, ledger.Report{}, exitIO
	}
	rep, err := l.Verify(context.Background())
	if err != nil {
		_ = l.Close()
		if errors.Is(err, ledger.ErrCorruptFile) {
			fmt.Fprintf(stderr, "%s: ledger broken: %s: %s\n", name, ledger.Safe(path), ledger.Safe(err.Error()))
			return nil, ledger.Report{}, 1
		}
		fmt.Fprintf(stderr, "%s: %s: %s\n", name, ledger.Safe(path), ledger.Safe(err.Error()))
		return nil, ledger.Report{}, exitIO
	}
	return l, rep, 0
}

func verifyLedger(name, path string, stderr io.Writer) (ledger.Report, int) {
	l, rep, code := openVerified(name, path, stderr)
	if l != nil {
		_ = l.Close()
	}
	return rep, code
}

func describeFailure(f *ledger.Failure) string {
	where := fmt.Sprintf("seq %d", f.Seq)
	if f.Kind != "" || f.ID != "" {
		where += fmt.Sprintf(" (%q %q)", f.Kind, f.ID)
	}
	return fmt.Sprintf("%s: %s: %s", where, ledger.Safe(string(f.Problem)), ledger.Safe(f.Detail))
}

const auditVerifyUsage = "usage: sdf audit verify [--ledger PATH] [--json] [--require-nonempty] [--expect-head HASH | --expect-head-file PATH | --expect-prefix SEQ:HASH | --expect-prefix-file PATH]"

var entryHashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// readExpectedHead reads a pinned head from a file. The file may hold the
// bare hash, the text `sdf audit head` prints ("seq N entry_hash HASH"), or
// the JSON `sdf audit head --json` prints.
func readExpectedHead(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", &ioError{err}
	}
	text := strings.TrimSpace(string(data))
	var doc struct {
		Head struct {
			EntryHash string `json:"entry_hash"`
		} `json:"head"`
	}
	if strings.HasPrefix(text, "{") {
		if err := json.Unmarshal([]byte(text), &doc); err != nil {
			return "", fmt.Errorf("%s: %v", path, err)
		}
		return doc.Head.EntryHash, nil
	}
	if fields := strings.Fields(text); len(fields) == 4 && fields[0] == "seq" && fields[2] == "entry_hash" {
		return fields[3], nil
	}
	return text, nil
}

// readExpectedPrefix reads a pin file and parses it with ledger.ParsePin.
// A failure to read the file is an ioError (exit 3). A malformed pin is a
// plain error (exit 2).
func readExpectedPrefix(path string) (int64, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, "", &ioError{err}
	}
	seq, hash, err := ledger.ParsePin(string(data))
	if err != nil {
		return 0, "", err
	}
	return seq, hash, nil
}

func cmdAuditVerify(args []string, stdout, stderr io.Writer) int {
	const name = "sdf audit verify"
	path := ""
	asJSON, requireNonEmpty := false, false
	expect, expectFile := "", ""
	expectSet, expectFileSet := false, false
	prefix, prefixFile := "", ""
	prefixSet, prefixFileSet := false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := func(flag string) (string, bool) {
			if strings.HasPrefix(arg, flag+"=") {
				v := strings.TrimPrefix(arg, flag+"=")
				return v, v != ""
			}
			v, ok := take(&i, args)
			return v, ok && v != ""
		}
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, auditVerifyUsage)
			return 0
		case arg == "--json":
			asJSON = true
		case arg == "--require-nonempty":
			requireNonEmpty = true
		case arg == "--ledger" || strings.HasPrefix(arg, "--ledger="):
			v, ok := value("--ledger")
			if !ok {
				fmt.Fprintf(stderr, "%s: --ledger requires a value\n", name)
				return 2
			}
			path = v
		case arg == "--expect-head" || strings.HasPrefix(arg, "--expect-head="):
			v, ok := value("--expect-head")
			if !ok {
				fmt.Fprintf(stderr, "%s: --expect-head requires a value\n", name)
				return 2
			}
			expect, expectSet = v, true
		case arg == "--expect-head-file" || strings.HasPrefix(arg, "--expect-head-file="):
			v, ok := value("--expect-head-file")
			if !ok {
				fmt.Fprintf(stderr, "%s: --expect-head-file requires a value\n", name)
				return 2
			}
			expectFile, expectFileSet = v, true
		case arg == "--expect-prefix" || strings.HasPrefix(arg, "--expect-prefix="):
			v, ok := value("--expect-prefix")
			if !ok {
				fmt.Fprintf(stderr, "%s: --expect-prefix requires a value\n", name)
				return 2
			}
			prefix, prefixSet = v, true
		case arg == "--expect-prefix-file" || strings.HasPrefix(arg, "--expect-prefix-file="):
			v, ok := value("--expect-prefix-file")
			if !ok {
				fmt.Fprintf(stderr, "%s: --expect-prefix-file requires a value\n", name)
				return 2
			}
			prefixFile, prefixFileSet = v, true
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	nExpect := 0
	for _, set := range []bool{expectSet, expectFileSet, prefixSet, prefixFileSet} {
		if set {
			nExpect++
		}
	}
	if nExpect > 1 {
		fmt.Fprintf(stderr, "%s: --expect-head, --expect-head-file, --expect-prefix, and --expect-prefix-file cannot be used together\n", name)
		return 2
	}
	if expectFileSet {
		v, err := readExpectedHead(expectFile)
		if err != nil {
			fmt.Fprintf(stderr, "%s: --expect-head-file: %s\n", name, ledger.Safe(err.Error()))
			var ioErr *ioError
			if errors.As(err, &ioErr) {
				return exitIO
			}
			return 2
		}
		expect = v
	}
	if (expectSet || expectFileSet) && !entryHashPattern.MatchString(expect) {
		fmt.Fprintf(stderr, "%s: expected head %q is not sha256: followed by 64 lowercase hex digits\n", name, expect)
		return 2
	}
	var prefixSeq int64
	var prefixHash string
	if prefixFileSet {
		seq, hash, err := readExpectedPrefix(prefixFile)
		if err != nil {
			fmt.Fprintf(stderr, "%s: --expect-prefix-file: %s\n", name, ledger.Safe(err.Error()))
			var ioErr *ioError
			if errors.As(err, &ioErr) {
				return exitIO
			}
			return 2
		}
		prefixSeq, prefixHash = seq, hash
	}
	if prefixSet {
		seq, hash, err := ledger.ParsePin(prefix)
		if err != nil {
			fmt.Fprintf(stderr, "%s: expected prefix %s\n", name, ledger.Safe(err.Error()))
			return 2
		}
		prefixSeq, prefixHash = seq, hash
	}
	prefixEnabled := prefixSet || prefixFileSet
	if path == "" {
		p, err := ledger.DefaultPath()
		if err != nil {
			fmt.Fprintf(stderr, "%s: %s\n", name, ledger.Safe(err.Error()))
			return exitIO
		}
		path = p
	}
	l, rep, code := openVerified(name, path, stderr)
	if code != 0 {
		return code
	}
	defer l.Close()

	ok := rep.OK
	emptyFail := requireNonEmpty && rep.Empty
	if emptyFail {
		ok = false
	}
	var headMatch *bool
	actual := ""
	if rep.Head != nil {
		actual = rep.Head.EntryHash
	}
	if expect != "" {
		m := rep.OK && !rep.Empty && actual == expect
		headMatch = &m
		if !m {
			ok = false
		}
	}

	var prefixMatch *bool
	var prefixEntry *ledger.Entry
	var prefixActual string // "none" when the seq is absent
	if prefixEnabled {
		prefixActual = "none"
		_, got, err := l.GetBySeq(context.Background(), prefixSeq)
		switch {
		case err == nil:
			prefixEntry = &got
			prefixActual = ledger.FormatPin(got.Seq, got.EntryHash)
		case errors.Is(err, ledger.ErrNotFound), errors.Is(err, ledger.ErrCorrupt):
			// truncated, never written, or the row itself is not trustworthy
		default:
			fmt.Fprintf(stderr, "%s: %s: %s\n", name, ledger.Safe(path), ledger.Safe(err.Error()))
			if errors.Is(err, ledger.ErrCorruptFile) {
				return 1
			}
			return exitIO
		}
		// Reuse the Verify report already in hand; same condition as VerifyPrefix.
		m := rep.OK && prefixEntry != nil && prefixEntry.EntryHash == prefixHash
		prefixMatch = &m
		if !m {
			ok = false
		}
	}

	if asJSON {
		var expected *string
		if expect != "" {
			expected = &expect
		}
		var expectedPrefix *string
		if prefixEnabled {
			p := ledger.FormatPin(prefixSeq, prefixHash)
			expectedPrefix = &p
		}
		writeJSON(stdout, struct {
			Path            string          `json:"path"`
			OK              bool            `json:"ok"`
			ChainOK         bool            `json:"chain_ok"`
			Empty           bool            `json:"empty"`
			RequireNonEmpty bool            `json:"require_nonempty"`
			Entries         int64           `json:"entries"`
			Head            *auditEntry     `json:"head"`
			ExpectedHead    *string         `json:"expected_head"`
			HeadMatch       *bool           `json:"head_match"`
			ExpectedPrefix  *string         `json:"expected_prefix"`
			PrefixMatch     *bool           `json:"prefix_match"`
			Failure         *ledger.Failure `json:"failure"`
		}{rep.Path, ok, rep.OK, rep.Empty, requireNonEmpty, rep.Entries, toAuditEntry(rep.Head), expected, headMatch, expectedPrefix, prefixMatch, rep.Failure})
	} else {
		switch {
		case rep.Empty && emptyFail:
			fmt.Fprintf(stdout, "ledger empty: %s has no entries, and --require-nonempty was given\n", ledger.Safe(rep.Path))
		case rep.Empty:
			fmt.Fprintf(stdout, "ledger empty: %s has no entries\n", ledger.Safe(rep.Path))
		case rep.OK:
			fmt.Fprintf(stdout, "ledger intact: %s, %d entries\n", ledger.Safe(rep.Path), rep.Entries)
			fmt.Fprintf(stdout, "head seq %d entry_hash %s\n", rep.Head.Seq, ledger.Safe(rep.Head.EntryHash))
		default:
			fmt.Fprintf(stdout, "ledger broken: %s, %d entries\n", ledger.Safe(rep.Path), rep.Entries)
			fmt.Fprintf(stdout, "first failure: %s\n", describeFailure(rep.Failure))
			if rep.Head != nil {
				fmt.Fprintf(stdout, "stored head seq %d entry_hash %s (not trustworthy)\n", rep.Head.Seq, ledger.Safe(rep.Head.EntryHash))
			}
		}
		if headMatch != nil {
			switch {
			case *headMatch:
				fmt.Fprintf(stdout, "head matches expected %s\n", expect)
			case !rep.OK:
				fmt.Fprintf(stdout, "head not accepted: expected %s, but the chain is broken, so the stored head %s proves nothing\n", expect, ledger.Safe(actual))
			case actual == "":
				fmt.Fprintf(stdout, "head mismatch: expected %s, actual none (the ledger is empty)\n", expect)
			default:
				fmt.Fprintf(stdout, "head mismatch: expected %s, actual %s (entries were removed, rewritten, or appended since the head was pinned)\n", expect, ledger.Safe(actual))
			}
		}
		if prefixMatch != nil {
			want := ledger.FormatPin(prefixSeq, prefixHash)
			switch {
			case *prefixMatch:
				fmt.Fprintf(stdout, "prefix matches %s\n", want)
			case !rep.OK:
				fmt.Fprintf(stdout, "prefix not accepted: expected %s, but the chain is broken, so the stored prefix %s proves nothing\n", want, prefixActual)
			case prefixEntry == nil && rep.Empty:
				fmt.Fprintf(stdout, "prefix mismatch: expected %s, actual none (the ledger is empty)\n", want)
			case prefixEntry == nil:
				fmt.Fprintf(stdout, "prefix mismatch: expected %s, actual none (the ledger was truncated below that seq, or the entry was removed)\n", want)
			default:
				fmt.Fprintf(stdout, "prefix mismatch: expected %s, actual %s (the ledger was rewritten at or through that point)\n", want, prefixActual)
			}
		}
	}
	if !ok {
		return 1
	}
	return 0
}

func cmdAuditHead(args []string, stdout, stderr io.Writer) int {
	const name = "sdf audit head"
	path, asJSON, code, done := parseAuditFlags(name, "usage: sdf audit head [--ledger PATH] [--json]", args, stdout, stderr)
	if done {
		return code
	}
	rep, code := verifyLedger(name, path, stderr)
	if code != 0 {
		return code
	}
	if !rep.OK {
		fmt.Fprintf(stderr, "%s: ledger broken, no head printed: %s (run sdf audit verify)\n", name, describeFailure(rep.Failure))
		return 1
	}
	if rep.Empty || rep.Head == nil {
		fmt.Fprintf(stderr, "%s: ledger empty: %s has no entries\n", name, ledger.Safe(rep.Path))
		return 1
	}
	if asJSON {
		writeJSON(stdout, struct {
			Path string      `json:"path"`
			Head *auditEntry `json:"head"`
		}{rep.Path, toAuditEntry(rep.Head)})
		return 0
	}
	fmt.Fprintf(stdout, "seq %d entry_hash %s\n", rep.Head.Seq, ledger.Safe(rep.Head.EntryHash))
	return 0
}
