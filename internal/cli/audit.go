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

// Exit codes of sdf audit: 0 intact, 1 broken or a failed check, 2 usage,
// 3 the ledger or an --expect-head-file could not be opened or read.
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
		return 0
	default:
		fmt.Fprintf(stderr, "unrecognized arguments: %s\n", args[0])
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
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
			return "", false, 2, true
		}
	}
	if path == "" {
		p, err := ledger.DefaultPath()
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
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

func verifyLedger(name, path string, stderr io.Writer) (ledger.Report, bool) {
	l, err := ledger.Open(path)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return ledger.Report{}, false
	}
	defer l.Close()
	rep, err := l.Verify(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "%s: %s: %v\n", name, path, err)
		return ledger.Report{}, false
	}
	return rep, true
}

func describeFailure(f *ledger.Failure) string {
	where := fmt.Sprintf("seq %d", f.Seq)
	if f.Kind != "" || f.ID != "" {
		where += fmt.Sprintf(" (%s %q)", f.Kind, f.ID)
	}
	return fmt.Sprintf("%s: %s: %s", where, f.Problem, f.Detail)
}

const auditVerifyUsage = "usage: sdf audit verify [--ledger PATH] [--json] [--require-nonempty] [--expect-head HASH | --expect-head-file PATH]"

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

func cmdAuditVerify(args []string, stdout, stderr io.Writer) int {
	const name = "sdf audit verify"
	path := ""
	asJSON, requireNonEmpty := false, false
	expect, expectFile := "", ""
	expectSet, expectFileSet := false, false
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
		default:
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
			return 2
		}
	}
	if expectSet && expectFileSet {
		fmt.Fprintf(stderr, "%s: --expect-head and --expect-head-file cannot be used together\n", name)
		return 2
	}
	if expectFileSet {
		v, err := readExpectedHead(expectFile)
		if err != nil {
			fmt.Fprintf(stderr, "%s: --expect-head-file: %v\n", name, err)
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
	if path == "" {
		p, err := ledger.DefaultPath()
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return exitIO
		}
		path = p
	}
	rep, ok := verifyLedger(name, path, stderr)
	if !ok {
		return exitIO
	}

	ok = rep.OK
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

	if asJSON {
		var expected *string
		if expect != "" {
			expected = &expect
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
			Failure         *ledger.Failure `json:"failure"`
		}{rep.Path, ok, rep.OK, rep.Empty, requireNonEmpty, rep.Entries, toAuditEntry(rep.Head), expected, headMatch, rep.Failure})
	} else {
		switch {
		case rep.Empty && emptyFail:
			fmt.Fprintf(stdout, "ledger empty: %s has no entries, and --require-nonempty was given\n", rep.Path)
		case rep.Empty:
			fmt.Fprintf(stdout, "ledger empty: %s has no entries\n", rep.Path)
		case rep.OK:
			fmt.Fprintf(stdout, "ledger intact: %s, %d entries\n", rep.Path, rep.Entries)
			fmt.Fprintf(stdout, "head seq %d entry_hash %s\n", rep.Head.Seq, rep.Head.EntryHash)
		default:
			fmt.Fprintf(stdout, "ledger broken: %s, %d entries\n", rep.Path, rep.Entries)
			fmt.Fprintf(stdout, "first failure: %s\n", describeFailure(rep.Failure))
			if rep.Head != nil {
				fmt.Fprintf(stdout, "stored head seq %d entry_hash %s (not trustworthy)\n", rep.Head.Seq, rep.Head.EntryHash)
			}
		}
		if headMatch != nil {
			switch {
			case *headMatch:
				fmt.Fprintf(stdout, "head matches expected %s\n", expect)
			case !rep.OK:
				fmt.Fprintf(stdout, "head not accepted: expected %s, but the chain is broken, so the stored head %s proves nothing\n", expect, actual)
			case actual == "":
				fmt.Fprintf(stdout, "head mismatch: expected %s, actual none (the ledger is empty)\n", expect)
			default:
				fmt.Fprintf(stdout, "head mismatch: expected %s, actual %s (entries were removed, rewritten, or appended since the head was pinned)\n", expect, actual)
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
	rep, ok := verifyLedger(name, path, stderr)
	if !ok {
		return exitIO
	}
	if !rep.OK {
		fmt.Fprintf(stderr, "%s: ledger broken, no head printed: %s (run sdf audit verify)\n", name, describeFailure(rep.Failure))
		return 1
	}
	if rep.Empty || rep.Head == nil {
		fmt.Fprintf(stderr, "%s: ledger empty: %s has no entries\n", name, rep.Path)
		return 1
	}
	if asJSON {
		writeJSON(stdout, struct {
			Path string      `json:"path"`
			Head *auditEntry `json:"head"`
		}{rep.Path, toAuditEntry(rep.Head)})
		return 0
	}
	fmt.Fprintf(stdout, "seq %d entry_hash %s\n", rep.Head.Seq, rep.Head.EntryHash)
	return 0
}
