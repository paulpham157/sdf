//go:build linux

package verify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// isolatedCommand runs command (argv[0] shown as argv0) as the child of a
// re-executed copy of this binary that is pid 1 in new user, pid, mount
// and (unless cfg.hostNet) network namespaces. When pid 1 exits, the
// kernel kills every process left in the namespace, including ones that
// called setsid or double-forked, before the parent's wait returns; so
// when cmd.Wait returns nothing the check started is still running.
// Killing pid 1 on timeout does the same.
//
// Inside, pid 1 mounts a private /proc, mounts empty tmpfs over cfg.mask
// (/tmp, /run, /dev/shm, the real home, ...: where sockets to processes
// outside the namespace live, such as a tmux server, docker, ssh-agent or
// a D-Bus session), binds cfg.keep (the verifier's own directories) back,
// brings up loopback in the new network namespace, drops its
// capabilities and runs the check.
func isolatedCommand(ctx context.Context, command string, argv []string, cfg nsConfig) (*exec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, self, append(cfg.args(), append([]string{"--", command}, argv...)...)...)
	cmd.SysProcAttr = nsAttr(!cfg.hostNet)
	return cmd, nil
}

func (c nsConfig) args() []string {
	a := []string{nsInitArg}
	if !c.hostNet {
		a = append(a, "--lo")
	}
	for _, m := range c.mask {
		a = append(a, "--mask", m)
	}
	for _, k := range c.keep {
		a = append(a, "--keep", k)
	}
	return a
}

func nsAttr(newNet bool) *syscall.SysProcAttr {
	uid, gid := os.Getuid(), os.Getgid()
	flags := uintptr(syscall.CLONE_NEWUSER | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS)
	if newNet {
		flags |= syscall.CLONE_NEWNET
	}
	return &syscall.SysProcAttr{
		Cloneflags:                 flags,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
		GidMappingsEnableSetgroups: false,
		// pid 1 keeps the same uid inside, so execve would drop every
		// capability; it needs CAP_SYS_ADMIN and CAP_NET_ADMIN (in its own
		// namespaces only) for the mounts and loopback, and clears them
		// before starting the check.
		AmbientCaps: []uintptr{capSysAdmin, capNetAdmin},
	}
}

const (
	capNetAdmin          = 12
	capSysAdmin          = 21
	prCapAmbient         = 47
	prCapAmbientClearAll = 4
	oPath                = 0x200000
)

// probeIsolation starts pid 1 the way a check does (new network namespace,
// a tmpfs over /tmp, loopback up) with nothing to run.
func probeIsolation() (bool, string) {
	self, err := os.Executable()
	if err != nil {
		return false, err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, nsInitArg, "--lo", "--mask", "/tmp", "--probe")
	cmd.SysProcAttr = nsAttr(true)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Sprintf("cannot create user+pid+mount+network namespaces: %v %s (on Ubuntu 24.04+: sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0)", err, strings.TrimSpace(string(out)))
	}
	return true, ""
}

// nsInitMain is pid 1 of a criterion's namespace. See isolatedCommand.
func nsInitMain(args []string) int {
	if os.Getpid() != 1 {
		fmt.Fprintln(os.Stderr, "sdf verify: namespace init is not pid 1")
		return 125
	}
	// Ambient capabilities are per thread: clear them on the thread that
	// will fork the check, and stay on it.
	runtime.LockOSThread()
	var masks, keeps, command []string
	lo, probe := false, false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--lo":
			lo = true
		case a == "--probe":
			probe = true
		case (a == "--mask" || a == "--keep") && i+1 < len(args):
			if a == "--mask" {
				masks = append(masks, args[i+1])
			} else {
				keeps = append(keeps, args[i+1])
			}
			i++
		case a == "--":
			command = args[i+1:]
			i = len(args)
		default:
			fmt.Fprintf(os.Stderr, "sdf verify: namespace init: bad argument %q\n", a)
			return 125
		}
	}
	die := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "sdf verify: namespace init: "+format+"\n", a...)
		return 125
	}
	// Every step is required, and the probe does them too: a user namespace
	// without capabilities (Ubuntu's AppArmor restriction on unprivileged
	// user namespaces still lets one be created, confined) cannot, and is
	// reported as no isolation.
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return die("make / private: %v", err)
	}
	if err := syscall.Mount("proc", "/proc", "proc", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, ""); err != nil {
		return die("mount /proc: %v", err)
	}
	// Open what to keep before anything is covered.
	sort.Slice(keeps, func(i, j int) bool { return len(keeps[i]) < len(keeps[j]) })
	keepFDs := map[string]int{}
	for _, k := range keeps {
		fd, err := syscall.Open(k, oPath|syscall.O_CLOEXEC, 0)
		if err == nil {
			keepFDs[k] = fd
		}
	}
	sort.Slice(masks, func(i, j int) bool { return len(masks[i]) < len(masks[j]) })
	for _, m := range masks {
		fi, err := os.Lstat(m)
		if err != nil || !fi.IsDir() {
			continue // absent, or a symlink the verifier did not resolve
		}
		mode := "0755"
		if m == "/tmp" || m == "/var/tmp" || m == "/dev/shm" {
			mode = "1777"
		}
		if err := syscall.Mount("tmpfs", m, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode="+mode); err != nil {
			return die("tmpfs over %s: %v", m, err)
		}
	}
	for _, k := range keeps {
		fd, ok := keepFDs[k]
		if !ok || !under(k, masks) {
			continue
		}
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			return die("keep %s: %v", k, err)
		}
		if st.Mode&syscall.S_IFMT == syscall.S_IFDIR {
			if err := os.MkdirAll(k, 0o755); err != nil {
				return die("keep %s: %v", k, err)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(k), 0o755); err != nil {
				return die("keep %s: %v", k, err)
			}
			if f, err := os.OpenFile(k, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				f.Close()
			}
		}
		if err := syscall.Mount("/proc/self/fd/"+strconv.Itoa(fd), k, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
			return die("bind %s: %v", k, err)
		}
	}
	for _, fd := range keepFDs {
		syscall.Close(fd)
	}
	if lo {
		if err := loopbackUp(); err != nil {
			return die("loopback: %v", err)
		}
	}
	if probe {
		return 0
	}
	if len(command) < 2 {
		return die("no command")
	}
	// The check runs with no capabilities: no ambient set, and a uid that
	// is not 0, so execve leaves its permitted set empty.
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, prCapAmbient, prCapAmbientClearAll, 0); e != 0 {
		return die("clear ambient capabilities: %v", e)
	}
	cmd := exec.Command(command[0], command[2:]...)
	cmd.Args[0] = command[1]
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "sdf verify: %v\n", err)
		return 126
	}
	// Reap orphans reparented to pid 1 while the command runs.
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, 0, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return 125
		}
		if pid == cmd.Process.Pid {
			switch {
			case ws.Exited():
				return ws.ExitStatus()
			case ws.Signaled():
				return 128 + int(ws.Signal())
			}
			return 125
		}
	}
}

func under(p string, roots []string) bool {
	for _, r := range roots {
		if strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

// loopbackUp sets IFF_UP on "lo" (SIOCGIFFLAGS / SIOCSIFFLAGS).
func loopbackUp() error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var ifr struct {
		name  [16]byte
		flags uint16
		_     [22]byte
	}
	copy(ifr.name[:], "lo")
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCGIFFLAGS, uintptr(unsafe.Pointer(&ifr))); e != 0 {
		return e
	}
	ifr.flags |= syscall.IFF_UP | syscall.IFF_RUNNING
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCSIFFLAGS, uintptr(unsafe.Pointer(&ifr))); e != 0 {
		return e
	}
	return nil
}
