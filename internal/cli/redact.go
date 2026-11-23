package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/paulpham157/sdf/internal/store"
)

// cmdRedact is sdf redact --db PATH [--dry-run]: apply the default content
// drop to rows already stored, then VACUUM.
func cmdRedact(args []string, stdout, stderr io.Writer) int {
	dbPath := ""
	dryRun := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, "usage: sdf redact --db PATH [--dry-run]")
			fmt.Fprintln(stdout, redactHelp)
			return 0
		case arg == "--db":
			v, ok := take(&i, args)
			if !ok || v == "" {
				fmt.Fprintln(stderr, "sdf redact: --db requires a path")
				return 2
			}
			dbPath = v
		case strings.HasPrefix(arg, "--db="):
			dbPath = strings.TrimPrefix(arg, "--db=")
		case arg == "--dry-run":
			dryRun = true
		default:
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
			return 2
		}
	}
	if dbPath == "" {
		fmt.Fprintln(stderr, "sdf redact: --db PATH is required")
		return 2
	}
	info, err := os.Stat(dbPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderr, "sdf redact: %s does not exist\n", dbPath)
			return 1
		}
		fmt.Fprintf(stderr, "sdf redact: %v\n", err)
		return 1
	}
	if info.IsDir() {
		fmt.Fprintf(stderr, "sdf redact: %s is a directory\n", dbPath)
		return 1
	}
	if !dryRun {
		// sdf serve holds this lock shared while it runs; holding it
		// exclusive keeps a serve from writing (content, under
		// --store-content) into the file while it is rewritten and stamped.
		lock, err := store.LockForScrub(dbPath)
		if errors.Is(err, store.ErrLocked) {
			fmt.Fprintf(stderr, "sdf redact: sdf serve (or another sdf redact%s) is using %s; stop it and run sdf redact again\n", store.LockHolder(dbPath), dbPath)
			return 1
		}
		if err != nil {
			fmt.Fprintf(stderr, "sdf redact: cannot take the lock file %s: %v\n", store.LockPath(dbPath), err)
			return 1
		}
		defer lock.Release()
	}
	db, err := store.OpenForScrub(dbPath, dryRun)
	if err != nil {
		fmt.Fprintf(stderr, "sdf redact: %s: %v\n", dbPath, err)
		return 1
	}
	defer db.Close()
	res, err := db.Scrub(dryRun)
	if err != nil {
		fmt.Fprintf(stderr, "sdf redact: %v\n", err)
		return 1
	}
	if dryRun {
		fmt.Fprintf(stdout, "%s: %d of %d rows would change; nothing written\n", dbPath, res.Changed, res.Rows)
		return 0
	}
	fmt.Fprintf(stdout, "%s: %d of %d rows scrubbed; file vacuumed\n", dbPath, res.Changed, res.Rows)
	return 0
}

const redactHelp = `Drops prompt, response, tool, and error text from rows already in the
database, by the rules sdf serve applies to new rows (rows kept with
--store-content too), then runs VACUUM so the text does not stay in free
pages, and marks the file as scrubbed. The file must be an existing sdf
database. It refuses while sdf serve is running on the file; stop it first.
Needs free disk of about twice the file's size while it runs.

  --db PATH              the SQLite file (required)
  --dry-run              only count the rows that would change; opens the
                         file read-only and writes nothing`

// warnUnscrubbed prints one line at serve start when the database holds
// rows from an sdf that stored content, until sdf redact has run on it.
func warnUnscrubbed(db *store.Store, path string, stderr io.Writer) {
	needs, err := db.NeedsScrub()
	if err != nil || !needs {
		return
	}
	fmt.Fprintf(stderr, "warning: %s has rows written before sdf dropped prompt, tool, and error text by default; they may hold that text. Stop sdf serve and run: sdf redact --db %s\n", path, shellWord(path))
}

func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
