package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/paulpham157/sdf/internal/ledger"
)

const auditUsage = "usage: sdf audit <verify|head> [--ledger PATH] [--json]"

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
			return "", false, 1, true
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

func cmdAuditVerify(args []string, stdout, stderr io.Writer) int {
	const name = "sdf audit verify"
	path, asJSON, code, done := parseAuditFlags(name, "usage: sdf audit verify [--ledger PATH] [--json]", args, stdout, stderr)
	if done {
		return code
	}
	rep, ok := verifyLedger(name, path, stderr)
	if !ok {
		return 1
	}
	if asJSON {
		writeJSON(stdout, struct {
			Path    string          `json:"path"`
			OK      bool            `json:"ok"`
			Empty   bool            `json:"empty"`
			Entries int64           `json:"entries"`
			Head    *auditEntry     `json:"head"`
			Failure *ledger.Failure `json:"failure"`
		}{rep.Path, rep.OK, rep.Empty, rep.Entries, toAuditEntry(rep.Head), rep.Failure})
	} else {
		switch {
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
	}
	if !rep.OK {
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
		return 1
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
