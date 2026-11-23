//go:build linux

package verify

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func peerPID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if cerr != nil {
		return 0, cerr
	}
	if cred.Pid <= 0 {
		return 0, fmt.Errorf("peer pid %d", cred.Pid)
	}
	return int(cred.Pid), nil
}

func ppidOf(pid int) (int, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, fmt.Errorf("bad stat")
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, fmt.Errorf("bad stat")
	}
	return strconv.Atoi(f[1])
}

func exeOf(pid int) (string, error) {
	return os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
}

// checkTestBinary accepts pid only if it runs a test binary (*.test) whose
// parent is a go command started directly by the verifier (not isolated)
// or by the namespace's pid 1 (isolated). A process the code under test
// forks has the test binary or pid 1 as its parent, so it is refused.
func checkTestBinary(pid, root int, isolated bool) error {
	exe, err := exeOf(pid)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(exe, ".test") {
		return fmt.Errorf("%s is not a test binary", exe)
	}
	parent, err := ppidOf(pid)
	if err != nil {
		return err
	}
	pexe, err := exeOf(parent)
	if err != nil {
		return err
	}
	if b := filepath.Base(pexe); b != "go" && b != "go.exe" {
		return fmt.Errorf("parent %s is not the go command", pexe)
	}
	want := parent
	if isolated {
		if want, err = ppidOf(parent); err != nil {
			return err
		}
	}
	if want != root {
		return fmt.Errorf("the go command that started it was not started by the verifier")
	}
	return nil
}
