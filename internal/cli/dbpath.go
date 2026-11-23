package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/paulpham157/sdf/internal/store"
)

// summaryStoreExists keeps sdf summary from creating an empty database. A
// missing file is reported with the path that was tried and how to point
// at the store sdf serve writes. It returns an exit code, 0 to go on.
func summaryStoreExists(path string, explicit bool, stderr io.Writer) int {
	_, err := os.Stat(path)
	if err == nil {
		return 0
	}
	abs := store.AbsPath(path)
	if !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(stderr, "sdf summary: database %s: %v\n", abs, err)
		return 1
	}
	if explicit {
		fmt.Fprintf(stderr, "sdf summary: no database at %s (from --db). Pass the path sdf serve printed after db=.\n", abs)
		return 1
	}
	fmt.Fprintf(stderr, "sdf summary: no database at %s. sdf serve writes sdf-telemetry.sqlite in the directory it was started from and prints the path after db=. Run sdf summary there, or pass --db PATH.\n", abs)
	return 1
}
