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

const decisionBriefUsage = "usage: sdf decision brief [--db PATH] [--max-items N] [--max-bytes N] [--format text|json]"

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
// item lines. A missing database, a database that cannot be read, a stdout
// write error, or any other store error prints nothing and exits 0. A usage
// error still exits 2.
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
			fmt.Fprintln(stdout, "Prints active constraints (locked user decisions first), then blocked decisions, then conflicting decisions.")
			fmt.Fprintf(stdout, "Defaults: --max-items %d, --max-bytes %d, --format text. Retracted and superseded decisions are omitted.\n", defaultBriefItems, defaultBriefBytes)
			fmt.Fprintln(stdout, "Output is printed when any decision matched. When items are cut, the text ends with \"N more: run sdf decision list\". An empty brief, a missing database, or a store or write error prints nothing and exits 0.")
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
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
			return 2
		}
	}

	// Same default as the other sdf decision commands: a path relative to the
	// process working directory, no environment variable. Stat before open
	// so a missing file is not created. OpenReadOnlyBusy is mode=ro either way.
	info, err := os.Stat(dbPath)
	if err != nil || info.IsDir() {
		return 0
	}
	db, err := store.OpenReadOnlyBusy(dbPath, int(briefBusyTimeout/time.Millisecond))
	if err != nil {
		return 0
	}
	graph, err := loadBrief(db)
	_ = db.Close()
	if err != nil {
		return 0
	}

	ranked := brief.Classify(graph)
	if len(ranked) == 0 {
		return 0
	}
	asJSON := format == "json"
	render := func(r brief.Result) []byte {
		if asJSON {
			return brief.JSONBytes(r)
		}
		return []byte(brief.FormatText(r))
	}
	out := render(brief.Trim(ranked, maxItems, maxBytes, render))
	if len(out) == 0 {
		return 0
	}
	if _, err := stdout.Write(out); err != nil {
		return 0
	}
	return 0
}

// loadBrief reads the graph under a short deadline. The deadline covers the
// queries themselves; briefBusyTimeout covers a lock wait. Either expiring
// is a store error, and the command then prints nothing.
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
