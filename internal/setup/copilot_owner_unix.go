//go:build unix

package setup

import (
	"os"
	"syscall"
)

// copilotKeepOwner gives tmp the uid and gid of target when they differ and
// the process may change them. A failed chown is ignored; the new file then
// belongs to the current user.
func copilotKeepOwner(tmp *os.File, target string) {
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

// copilotHardLinks is target's link count, 0 when it does not exist.
func copilotHardLinks(target string) uint64 {
	info, err := os.Stat(target)
	if err != nil {
		return 0
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 0
}
