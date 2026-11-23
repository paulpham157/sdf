//go:build unix

package setup

import (
	"os"
	"syscall"
)

// qwenKeepOwner gives the new file the old file's owner and group when the
// process may (for example root editing another user's file). Errors are
// ignored: an ordinary user cannot chown and the file stays theirs.
func qwenKeepOwner(tmp *os.File, old os.FileInfo) {
	if old == nil {
		return
	}
	if st, ok := old.Sys().(*syscall.Stat_t); ok {
		_ = tmp.Chown(int(st.Uid), int(st.Gid))
	}
}
