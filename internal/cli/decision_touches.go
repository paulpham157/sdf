package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/store"
)

const decisionTouchesUsage = "usage: sdf decision touches [--db PATH] (--range A..B | --paths -)"

const exitTouchesMatch = 3

func cmdDecisionTouches(args []string, stdout, stderr io.Writer) int {
	dbPath := "sdf-telemetry.sqlite"
	rng := ""
	rangeSet := false
	pathsSet := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, decisionTouchesUsage)
			fmt.Fprintln(stdout, "Reports changed file names that match a path glob on an active decision. --range is a git revision range. --paths - reads NUL-separated paths from stdin. File content is not read. Exit 0 is no match, 3 is a match, 2 is usage, 1 when the store cannot be read.")
			return 0
		case arg == "--db":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision touches: --db requires a value")
				return 2
			}
			dbPath = v
		case strings.HasPrefix(arg, "--db="):
			dbPath = strings.TrimPrefix(arg, "--db=")
		case arg == "--range":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision touches: --range requires a value")
				return 2
			}
			rng, rangeSet = v, true
		case strings.HasPrefix(arg, "--range="):
			rng, rangeSet = strings.TrimPrefix(arg, "--range="), true
		case arg == "--paths":
			v, ok := take(&i, args)
			if !ok || v != "-" {
				fmt.Fprintln(stderr, "sdf decision touches: --paths requires -")
				return 2
			}
			pathsSet = true
		case arg == "--paths=-":
			pathsSet = true
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	if rangeSet == pathsSet {
		fmt.Fprintln(stderr, decisionTouchesUsage)
		return 2
	}
	if rangeSet {
		if err := validateTouchRange(rng); err != nil {
			fmt.Fprintf(stderr, "sdf decision touches: %s\n", displayErr(err))
			return 2
		}
	}
	if _, err := os.Stat(dbPath); err != nil {
		fmt.Fprintf(stderr, "sdf decision touches: %s\n", displayErr(err))
		return 1
	}
	var paths []decision.PushPath
	var commitsTrunc, pathsTrunc bool
	var err error
	var stdinTrunc bool
	if pathsSet {
		paths, stdinTrunc, err = readTouchPaths(os.Stdin)
		pathsTrunc = stdinTrunc
	} else {
		paths, commitsTrunc, pathsTrunc, err = touchRange(context.Background(), rng)
	}
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision touches: %s\n", displayErr(err))
		return 1
	}
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision touches: %s\n", displayErr(err))
		return 1
	}
	g, err := decision.Load(db)
	_ = db.Close()
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision touches: %s\n", displayErr(err))
		return 1
	}
	found := decision.CheckTouches(g, paths)
	opt := decision.TouchText{Prefix: "sdf: touches", Unit: "path"}
	if rangeSet {
		opt.Unit = "commit"
	}
	if err := decision.WriteTouchText(stdout, found, opt); err != nil {
		return 1
	}
	if pathsTrunc {
		fmt.Fprintf(stderr, "sdf: touches: only the first %d changed paths were checked.\n", prePushMaxPaths)
	}
	if commitsTrunc {
		fmt.Fprintf(stderr, "sdf: touches: only the newest %d commits were checked.\n", prePushMaxCommits)
	}
	if len(found.Decisions) > 0 {
		return exitTouchesMatch
	}
	return 0
}

func validateTouchRange(rng string) error {
	if len(rng) == 0 || len(rng) > 512 || strings.HasPrefix(rng, "-") || !strings.Contains(rng, "..") {
		return fmt.Errorf("invalid --range")
	}
	for _, r := range rng {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("invalid --range")
		}
	}
	return nil
}

func readTouchPaths(r io.Reader) ([]decision.PushPath, bool, error) {
	raw, err := io.ReadAll(io.LimitReader(r, (8<<20)+1))
	if err != nil {
		return nil, false, err
	}
	if len(raw) > 8<<20 {
		return nil, false, fmt.Errorf("path list is too long")
	}
	var out []decision.PushPath
	truncated := false
	for _, p := range bytes.Split(raw, []byte{0}) {
		if len(p) == 0 {
			continue
		}
		if len(out) == prePushMaxPaths {
			truncated = true
			break
		}
		out = append(out, decision.PushPath{Path: string(p)})
	}
	return out, truncated, nil
}

// touchRange lists file names changed by the non-merge commits in rng.
// rng is one git argv, already checked by validateTouchRange.
func touchRange(ctx context.Context, rng string) ([]decision.PushPath, bool, bool, error) {
	args := []string{"log", "--no-merges", "--name-status", "--no-renames", "-z",
		fmt.Sprintf("--max-count=%d", prePushMaxCommits+1),
		"--format=%H%x1f%(trailers:key=SDF-Decision,valueonly=true,unfold=true,separator=%x1f)%x1e",
		rng}
	raw, err := gitOutput(ctx, args)
	if err != nil {
		return nil, false, false, err
	}
	parsed, err := parseNameStatusLog(raw)
	if err != nil {
		return nil, false, false, err
	}
	var paths []decision.PushPath
	commitsTrunc, pathsTrunc := false, false
	n := 0
	for _, c := range parsed {
		if n == prePushMaxCommits {
			commitsTrunc = true
			break
		}
		n++
		for _, p := range c.Paths {
			if len(paths) == prePushMaxPaths {
				pathsTrunc = true
				break
			}
			paths = append(paths, decision.PushPath{SHA: c.SHA, Path: p, Trailers: c.Trailers})
		}
	}
	return paths, commitsTrunc, pathsTrunc, nil
}
