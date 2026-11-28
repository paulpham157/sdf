package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/store"
)

const decisionScopeUsage = "usage: sdf decision scope --id ID --path GLOB... [--db PATH]"

func cmdDecisionScope(args []string, stdout, stderr io.Writer) int {
	dbPath := "sdf-telemetry.sqlite"
	id := ""
	idSet := false
	var paths []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, decisionScopeUsage)
			fmt.Fprintln(stdout, "Adds path globs to a decision. Globs are not removed or changed; record a new decision to replace them.")
			return 0
		case arg == "--db":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision scope: --db requires a value")
				return 2
			}
			dbPath = v
		case strings.HasPrefix(arg, "--db="):
			dbPath = strings.TrimPrefix(arg, "--db=")
		case arg == "--id":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision scope: --id requires a value")
				return 2
			}
			id, idSet = v, true
		case strings.HasPrefix(arg, "--id="):
			id, idSet = strings.TrimPrefix(arg, "--id="), true
		case arg == "--path":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision scope: --path requires a value")
				return 2
			}
			paths = append(paths, v)
		case strings.HasPrefix(arg, "--path="):
			paths = append(paths, strings.TrimPrefix(arg, "--path="))
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	if !idSet || len(paths) == 0 {
		fmt.Fprintln(stderr, decisionScopeUsage)
		return 2
	}
	if err := decision.ValidatePaths(paths); err != nil {
		fmt.Fprintf(stderr, "sdf decision scope: %s\n", displayErr(err))
		return 1
	}
	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision scope: %s\n", displayErr(err))
		return 1
	}
	defer db.Close()
	if err := decision.AddPaths(db, id, paths); err != nil {
		fmt.Fprintf(stderr, "sdf decision scope: %s\n", displayErr(err))
		return 1
	}
	return 0
}
