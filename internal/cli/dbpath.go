package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/paulpham157/sdf/internal/store"
)

// summaryMissingHint tells a reader of a database file that does not exist
// where sdf looked. sdf summary still reads it as an empty store and exits
// 0 (docs/outcomes.md); the hint goes to stderr only, so stdout and --json
// are unchanged.
func summaryMissingHint(path string, explicit bool, stderr io.Writer) {
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	abs := store.AbsPath(path)
	if explicit {
		fmt.Fprintf(stderr, "sdf summary: no database at %s yet (from --db); nothing was created. Pass the path sdf serve printed after db=.\n", abs)
		return
	}
	fmt.Fprintf(stderr, "sdf summary: no database at %s yet; nothing was created. sdf serve writes sdf-telemetry.sqlite in the directory it was started from and prints the path after db=. Run sdf summary there, or pass --db PATH.\n", abs)
}
