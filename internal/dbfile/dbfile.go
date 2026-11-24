// Package dbfile keeps sdf's SQLite files private to the user who owns
// them: the telemetry store and the ledger.
//
// A database file is created 0600 before SQLite opens it. On unix SQLite
// creates the -wal, -shm and -journal files beside it with the main file's
// mode, whatever the umask, so they are 0600 too. An existing file that
// group or others can access (one an older sdf created 0644) is tightened
// to owner-only when it is opened. When that fails (a read-only mount,
// another user's file) a warning is printed and the open goes on, so a
// read-only command still reads. A directory sdf creates for a database is
// 0700; an existing directory is never changed, only warned about when
// group or others can write it.
package dbfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	// FileMode is the mode of a new database file.
	FileMode os.FileMode = 0o600
	// DirMode is the mode of a directory created for a database.
	DirMode os.FileMode = 0o700
)

// sidecars are the files SQLite keeps beside a database.
var sidecars = []string{"-wal", "-shm", "-journal"}

// Warnings receives one line for each file whose mode was tightened or
// could not be, and for a directory group or others can write.
var Warnings io.Writer = os.Stderr

// chmod is os.Chmod; tests replace it to simulate a failure.
var chmod = os.Chmod

// Create prepares path for a read-write open: it creates a missing parent
// directory with DirMode, creates the file with FileMode if it does not
// exist (O_EXCL, so an existing file is never truncated), and tightens an
// existing file and its sidecars as Tighten does.
func Create(path string) error {
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(dir, DirMode); err != nil {
			return err
		}
	case err != nil:
		return err
	case info.IsDir() && info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0:
		// A sticky world-writable directory such as /tmp keeps others
		// from replacing the file, so it is not worth a warning.
		fmt.Fprintf(Warnings, "sdf: warning: directory %s is mode %#o: group or others can write it, so they can replace the database files in it\n", dir, info.Mode().Perm())
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, FileMode)
	if err == nil {
		return f.Close()
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	Tighten(path)
	return nil
}

// Tighten removes group and other access from path and its -wal, -shm and
// -journal files, where they exist. It keeps the owner bits, so a 0644
// file becomes 0600 and a 0444 one 0400. It never fails: a file it cannot
// change gets a warning.
func Tighten(path string) {
	for _, p := range append([]string{path}, sidecarPaths(path)...) {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		old := info.Mode().Perm()
		if old&0o077 == 0 {
			continue
		}
		want := old &^ 0o077
		if err := chmod(p, want); err != nil {
			fmt.Fprintf(Warnings, "sdf: warning: %s is mode %#o (group or others can access it) and could not be changed to %#o: %v\n", p, old, want, err)
			continue
		}
		fmt.Fprintf(Warnings, "sdf: %s was mode %#o; changed it to %#o (owner only)\n", p, old, want)
	}
}

func sidecarPaths(path string) []string {
	out := make([]string, len(sidecars))
	for i, s := range sidecars {
		out[i] = path + s
	}
	return out
}
