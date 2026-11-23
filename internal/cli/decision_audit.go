package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/store"
)

const decisionAuditUsage = "usage: sdf decision audit [--db PATH] [--json] [--since RFC3339] [--until RFC3339]"

// Exit codes of sdf decision audit: 0 no findings; 1 at least one finding;
// 2 usage; 3 the store could not be opened or read. A gate (CI, an agent
// Stop hook) can tell "something to fix" (1) from "could not check" (3).
const (
	exitAuditFindings = 1
	exitAuditError    = exitIO
)

// cmdDecisionAudit reports gaps in the decision graph: active decisions no
// implements edge points at, commits whose session is not linked to any
// decision, and edges at a retracted or superseded decision. It opens the
// store with store.OpenReadOnly, as sdf decision list does, so it never
// writes, migrates, or creates the file: a missing file in an existing
// directory audits as empty (exit 0), a missing directory exits 3.
func cmdDecisionAudit(args []string, stdout, stderr io.Writer) int {
	dbPath := "sdf-telemetry.sqlite"
	asJSON := false
	var opt decision.AuditOptions
	parseTime := func(flag, v string) (time.Time, bool) {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			fmt.Fprintf(stderr, "sdf decision audit: %s must be RFC3339, for example 2026-11-23T00:00:00Z\n", flag)
			return time.Time{}, false
		}
		return t, true
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		flag, value, hasValue := arg, "", false
		if j := strings.IndexByte(arg, '='); j > 0 && strings.HasPrefix(arg, "--") {
			flag, value, hasValue = arg[:j], arg[j+1:], true
		}
		switch flag {
		case "--help", "-h":
			fmt.Fprintln(stdout, decisionAuditUsage)
			return 0
		case "--json":
			if hasValue {
				fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
				return 2
			}
			asJSON = true
		case "--db", "--since", "--until":
			if !hasValue {
				v, ok := take(&i, args)
				if !ok {
					fmt.Fprintf(stderr, "sdf decision audit: %s requires a value\n", flag)
					return 2
				}
				value = v
			}
			switch flag {
			case "--db":
				dbPath = value
			case "--since":
				t, ok := parseTime(flag, value)
				if !ok {
					return 2
				}
				opt.Since = t
			case "--until":
				t, ok := parseTime(flag, value)
				if !ok {
					return 2
				}
				opt.Until = t
			}
		default:
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
			return 2
		}
	}
	if !opt.Since.IsZero() && !opt.Until.IsZero() && !opt.Since.Before(opt.Until) {
		fmt.Fprintln(stderr, "sdf decision audit: --since must be before --until")
		return 2
	}
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision audit: %v\n", err)
		return exitAuditError
	}
	report, err := decision.Audit(db, opt)
	_ = db.Close()
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision audit: %v\n", err)
		return exitAuditError
	}
	write := decision.WriteAuditText
	if asJSON {
		write = decision.WriteAuditJSON
	}
	if err := write(stdout, report); err != nil {
		fmt.Fprintf(stderr, "sdf decision audit: %v\n", err)
		return exitAuditError
	}
	if report.FindingCount > 0 {
		return exitAuditFindings
	}
	return 0
}
