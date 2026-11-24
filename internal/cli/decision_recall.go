package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/store"
)

const decisionRecallUsage = "usage: sdf decision recall [--db PATH] [--json] [--since DURATION|RFC3339] [--until RFC3339]"

// cmdDecisionRecall reports link recall by proxy: commits in a session
// that has a decision but no implements edge, divided into the commits
// in those sessions. It opens the store read-only and writes nothing.
// Exit 0 is a written report, including a ratio of zero. Exit 2 is a
// usage error. Exit 3 is a store that could not be opened or read.
func cmdDecisionRecall(args []string, stdout, stderr io.Writer) int {
	dbPath := "sdf-telemetry.sqlite"
	asJSON := false
	var since, until string
	sinceSet, untilSet := false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		flag, value, hasValue := arg, "", false
		if j := strings.IndexByte(arg, '='); j > 0 && strings.HasPrefix(arg, "--") {
			flag, value, hasValue = arg[:j], arg[j+1:], true
		}
		switch flag {
		case "--help", "-h":
			fmt.Fprintln(stdout, decisionRecallUsage)
			return 0
		case "--json":
			if hasValue {
				unrecognizedArgs(stderr, arg)
				return 2
			}
			asJSON = true
		case "--db", "--since", "--until":
			if !hasValue {
				v, ok := take(&i, args)
				if !ok {
					fmt.Fprintf(stderr, "sdf decision recall: %s requires a value\n", flag)
					return 2
				}
				value = v
			}
			switch flag {
			case "--db":
				dbPath = value
			case "--since":
				since, sinceSet = value, true
			case "--until":
				until, untilSet = value, true
			}
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	var opt decision.AuditOptions
	if sinceSet {
		t, err := parseRecallSince(since, time.Now().UTC())
		if err != nil {
			fmt.Fprintf(stderr, "sdf decision recall: --since %s\n", err.Error())
			return 2
		}
		opt.Since = t
	}
	if untilSet {
		t, err := time.Parse(time.RFC3339Nano, until)
		if err != nil {
			fmt.Fprintln(stderr, "sdf decision recall: --until must be RFC3339, for example 2026-11-23T00:00:00Z")
			return 2
		}
		opt.Until = t
	}
	if !opt.Since.IsZero() && !opt.Until.IsZero() && !opt.Since.Before(opt.Until) {
		fmt.Fprintln(stderr, "sdf decision recall: --since must be before --until")
		return 2
	}
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision recall: %s\n", displayErr(err))
		return exitAuditError
	}
	report, err := decision.Audit(db, opt)
	_ = db.Close()
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision recall: %s\n", displayErr(err))
		return exitAuditError
	}
	proxy := decision.LinkRecallByProxy(report)
	write := decision.WriteProxyText
	if asJSON {
		write = decision.WriteProxyJSON
	}
	if err := write(stdout, proxy); err != nil {
		fmt.Fprintf(stderr, "sdf decision recall: %s\n", displayErr(err))
		return exitAuditError
	}
	return 0
}

func parseRecallSince(v string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(v); err == nil {
		if d < 0 {
			return time.Time{}, fmt.Errorf("must be a duration such as 24h, or RFC3339")
		}
		return now.Add(-d), nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("must be a duration such as 24h, or RFC3339")
	}
	return t, nil
}
