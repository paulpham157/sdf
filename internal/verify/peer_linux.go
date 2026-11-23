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

// checkLauncher accepts pid only if it runs this very executable (the
// launcher; compared by inode, not name) and its parent is the criterion's
// go command: isolated, the process pid 1 (root) started, whose pid in the
// namespace pid 1 reported (goNsPid); not isolated, root itself (the go the
// verifier started). It returns the
// launcher's working directory, the package directory go runs the test in.
//
// Code under test cannot produce such a process: anything it starts
// descends from its test binary. clone(CLONE_PARENT) and clone3 are refused
// by the hook's seccomp filter, and isolated, a test binary runs in its
// own pid namespace, so nothing it starts can have go as its parent. An
// orphan is reparented to pid 1, not to go, and cannot take go's pid, which
// go holds for as long as the namespace exists (pid 1 exits with go).
func checkLauncher(pid int, self string, root, goNsPid int, isolated bool) (string, error) {
	exe, err := os.Stat("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return "", err
	}
	me, err := os.Stat(self)
	if err != nil {
		return "", err
	}
	if !os.SameFile(exe, me) {
		name, _ := exeOf(pid)
		return "", fmt.Errorf("%s is not the verifier's launcher", name)
	}
	parent, err := ppidOf(pid)
	if err != nil {
		return "", err
	}
	if isolated {
		// go's pid in the check's namespace: the level at which root is
		// pid 1. The parent must be in that very namespace (as deep as
		// root), not in one nested below it, where the same number can
		// name another process.
		rootNs, err := nsPids(root)
		if err != nil {
			return "", err
		}
		ns, err := nsPids(parent)
		if err != nil {
			return "", err
		}
		gp, err := ppidOf(parent)
		if err != nil {
			return "", err
		}
		level := len(rootNs) - 1
		if level < 1 || rootNs[level] != 1 || len(ns) != len(rootNs) || goNsPid <= 1 || ns[level] != goNsPid || gp != root {
			return "", fmt.Errorf("its parent (pids %v) is not the go command pid 1 started (pid %d in the namespace of pid 1, %v)", ns, goNsPid, rootNs)
		}
	} else if parent != root {
		return "", fmt.Errorf("its parent %d is not the go command the verifier started", parent)
	}
	pexe, err := exeOf(parent)
	if err != nil {
		return "", err
	}
	if b := filepath.Base(pexe); b != "go" && b != "go.exe" {
		return "", fmt.Errorf("parent %s is not the go command", pexe)
	}
	return os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
}

// nsPids is pid's process id in each pid namespace it is in, from the
// namespace of the /proc this process reads (its own) to its innermost.
func nsPids(pid int) ([]int, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "NSpid:"); ok {
			var out []int
			for _, f := range strings.Fields(rest) {
				n, err := strconv.Atoi(f)
				if err != nil {
					return nil, fmt.Errorf("process %d: NSpid %q", pid, rest)
				}
				out = append(out, n)
			}
			if len(out) == 0 {
				break
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("no NSpid for process %d", pid)
}

// socketPair is a connected pair of unix stream sockets.
func socketPair() (mine net.Conn, theirs *os.File, err error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	a := os.NewFile(uintptr(fds[0]), "harness")
	defer a.Close()
	if mine, err = net.FileConn(a); err != nil {
		syscall.Close(fds[1])
		return nil, nil, err
	}
	return mine, os.NewFile(uintptr(fds[1]), "test-binary"), nil
}

// sendFD passes f over the unix connection c.
func sendFD(c net.Conn, f *os.File) error {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("not a unix socket")
	}
	_, _, err := uc.WriteMsgUnix([]byte{'f'}, syscall.UnixRights(int(f.Fd())), nil)
	return err
}

// recvFD receives one file passed with sendFD.
func recvFD(c *net.UnixConn) (*os.File, error) {
	buf, oob := make([]byte, 1), make([]byte, syscall.CmsgSpace(4))
	_, oobn, _, _, err := c.ReadMsgUnix(buf, oob)
	if err != nil {
		return nil, err
	}
	msgs, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) != 1 {
		return nil, fmt.Errorf("no file passed (%v)", err)
	}
	fds, err := syscall.ParseUnixRights(&msgs[0])
	if err != nil || len(fds) != 1 {
		return nil, fmt.Errorf("no file passed (%v)", err)
	}
	syscall.CloseOnExec(fds[0])
	return os.NewFile(uintptr(fds[0]), "harness"), nil
}
