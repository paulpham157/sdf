//go:build unix

package setup

import (
	"os"
	"syscall"
)

// keepOwner gives tmp the uid and gid of target when they differ and the
// process is allowed to change them (root, or a group the user belongs to).
// A failed chown is ignored: the new file then belongs to the current user.
func keepOwner(tmp *os.File, target string) {
	info, err := os.Stat(target)
	if err != nil {
		return
	}
	want, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	have, err := tmp.Stat()
	if err != nil {
		return
	}
	got, ok := have.Sys().(*syscall.Stat_t)
	if !ok || (got.Uid == want.Uid && got.Gid == want.Gid) {
		return
	}
	_ = tmp.Chown(int(want.Uid), int(want.Gid))
}

// hardLinks is the link count of path's target, 0 when it does not exist.
func hardLinks(path string) uint64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 0
}
