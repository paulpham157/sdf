package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/decision/brief"
	"github.com/paulpham157/sdf/internal/store"
)

const decisionBriefUsage = "usage: sdf decision brief [--db PATH] [--max-items N] [--max-bytes N] [--format text|json] [--json]"

const (
	defaultBriefItems = 15
	defaultBriefBytes = 2048
	// briefBusyTimeout is how long the read waits on another connection's
	// lock. It is far under the session hook's 2s budget. The other decision
	// commands use the store's 5s timeout; brief does not.
	briefBusyTimeout = 200 * time.Millisecond
	// briefLoadTimeout bounds the whole read, including the busy wait.
	briefLoadTimeout = 500 * time.Millisecond
)

// cmdDecisionBrief prints the ranked brief. It opens the store read-only.
// Output is written when any decision matched, even when the caps leave no
// item lines. A path that does not exist prints nothing and exits 0. A path
// that exists but cannot be read (not a regular file, or the open or the
// query fails) prints the unavailable line and exits 0. A stdout write
// error exits 0 with nothing on stderr. A usage error still exits 2.
func cmdDecisionBrief(args []string, stdout, stderr io.Writer) int {
	dbPath := "sdf-telemetry.sqlite"
	maxItems := defaultBriefItems
	maxBytes := defaultBriefBytes
	format := "text"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, decisionBriefUsage)
			fmt.Fprintln(stdout, "Prints active constraints (locked decisions recorded with a login or legacy user first), then blocked decisions, then conflicting decisions.")
			fmt.Fprintf(stdout, "Defaults: --max-items %d, --max-bytes %d, --format text. Retracted and superseded decisions are omitted.\n", defaultBriefItems, defaultBriefBytes)
			fmt.Fprintln(stdout, "Locked rules (a GitHub login, or legacy user, except a name sourced from the OS username) are exempt from --max-items, including --max-items 0. The exact word agent is not locked, and neither is an OS-sourced name. When the locked tier does not fit, statements shrink to a 40-byte floor before the oldest locked rules are dropped. Output is printed when any decision matched. When items are cut, the text ends with \"N more: run sdf decision list\", or \"N more (K locked): run sdf decision list\" when locked rules were dropped. JSON always includes omitted_locked. A missing database prints nothing and exits 0. When the path exists but cannot be read, the text is \"sdf decision brief unavailable: run sdf decision list\".")
			fmt.Fprintln(stdout, "The sdf-decisions skill ships scripts/brief, a fail-open helper that runs this command.")
			return 0
		case arg == "--db":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision brief: --db requires a value")
				return 2
			}
			dbPath = v
		case strings.HasPrefix(arg, "--db="):
			dbPath = strings.TrimPrefix(arg, "--db=")
		case arg == "--max-items":
			n, code := briefLimit(&i, args, stderr, "--max-items")
			if code != 0 {
				return code
			}
			maxItems = n
		case strings.HasPrefix(arg, "--max-items="):
			n, err := parseNonNeg(strings.TrimPrefix(arg, "--max-items="), true)
			if err != nil {
				fmt.Fprintln(stderr, "sdf decision brief: --max-items must be a non-negative integer")
				return 2
			}
			maxItems = n
		case arg == "--max-bytes":
			n, code := briefLimit(&i, args, stderr, "--max-bytes")
			if code != 0 {
				return code
			}
			maxBytes = n
		case strings.HasPrefix(arg, "--max-bytes="):
			n, err := parseNonNeg(strings.TrimPrefix(arg, "--max-bytes="), true)
			if err != nil {
				fmt.Fprintln(stderr, "sdf decision brief: --max-bytes must be a non-negative integer")
				return 2
			}
			maxBytes = n
		case arg == "--json":
			format = "json"
		case arg == "--format":
			v, ok := take(&i, args)
			if !ok || (v != "text" && v != "json") {
				fmt.Fprintln(stderr, "sdf decision brief: --format must be text or json")
				return 2
			}
			format = v
		case strings.HasPrefix(arg, "--format="):
			v := strings.TrimPrefix(arg, "--format=")
			if v != "text" && v != "json" {
				fmt.Fprintln(stderr, "sdf decision brief: --format must be text or json")
				return 2
			}
			format = v
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}

	// Same default as the other sdf decision commands: a path relative to the
	// process working directory, no environment variable. Stat before open
	// so a missing file is not created and a FIFO, directory, or device is
	// never opened. A path whose parent is a file, such as
	// existing-file/x.sqlite, fails Stat with ENOTDIR and prints the
	// unavailable line. A 0-byte or non-sdf file that cannot be opened does
	// too. A file that opens and has no decision rows prints nothing: there
	// are no rules to apply.
	asJSON := format == "json"
	info, err := os.Stat(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		return writeBrief(stdout, unavailableBrief(asJSON))
	}
	if !info.Mode().IsRegular() {
		return writeBrief(stdout, unavailableBrief(asJSON))
	}
	db, err := store.OpenReadOnlyBusy(dbPath, int(briefBusyTimeout/time.Millisecond))
	if err != nil {
		return writeBrief(stdout, unavailableBrief(asJSON))
	}
	graph, err := loadBrief(db)
	_ = db.Close()
	if err != nil {
		return writeBrief(stdout, unavailableBrief(asJSON))
	}

	ranked := brief.Classify(graph)
	if len(ranked) == 0 {
		return 0
	}
	render := func(r brief.Result) []byte {
		if asJSON {
			return brief.JSONBytes(r)
		}
		return []byte(brief.FormatText(r))
	}
	return writeBrief(stdout, render(brief.Trim(ranked, maxItems, maxBytes, render)))
}

const (
	briefUnavailableText = "sdf decision brief unavailable: run sdf decision list\n"
	briefUnavailableJSON = "{\"unavailable\":true}\n"
)

func unavailableBrief(asJSON bool) []byte {
	if asJSON {
		return []byte(briefUnavailableJSON)
	}
	return []byte(briefUnavailableText)
}

// writeBrief prints b. A write error is silent and still exits 0. An empty
// b prints nothing.
func writeBrief(stdout io.Writer, b []byte) int {
	if len(b) == 0 {
		return 0
	}
	_, _ = stdout.Write(b)
	return 0
}

// loadBrief reads the graph under a short deadline. The deadline covers the
// queries themselves; briefBusyTimeout covers a lock wait. Either expiring
// is a read error, and the command then prints the unavailable line.
func loadBrief(db *store.Store) (decision.Graph, error) {
	ctx, cancel := context.WithTimeout(context.Background(), briefLoadTimeout)
	defer cancel()
	return decision.LoadContext(ctx, db)
}

func briefLimit(i *int, args []string, stderr io.Writer, flag string) (int, int) {
	v, ok := take(i, args)
	if !ok {
		fmt.Fprintf(stderr, "sdf decision brief: %s requires a value\n", flag)
		return 0, 2
	}
	n, err := parseNonNeg(v, true)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision brief: %s must be a non-negative integer\n", flag)
		return 0, 2
	}
	return n, 0
}

// parseNonNeg reads a non-negative decimal with no sign and no leading zero.
func parseNonNeg(v string, ok bool) (int, error) {
	if !ok || v == "" || len(v) > 9 {
		return 0, strconv.ErrSyntax
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0, strconv.ErrSyntax
		}
	}
	if len(v) > 1 && v[0] == '0' {
		return 0, strconv.ErrSyntax
	}
	return strconv.Atoi(v)
}
