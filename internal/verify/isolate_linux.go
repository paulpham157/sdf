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
// re-executed copy of this binary that is pid 1 in new user, pid, mount,
// IPC, UTS and (unless cfg.hostNet) network namespaces. When pid 1 exits,
// the kernel kills every process left in the namespace, including ones
// that called setsid or double-forked, before the parent's wait returns;
// so when cmd.Wait returns nothing the check started is still running.
// Killing pid 1 on timeout does the same.
//
// Inside, pid 1 mounts a private /proc, remounts every other mount
// read-only, mounts empty tmpfs over cfg.mask (/tmp, /run, /dev/shm, the
// real home, ...: where sockets to processes outside the namespace live,
// such as a tmux server, docker, ssh-agent or a D-Bus session), binds
// cfg.rw back writable and cfg.ro back read-only, mounts cfg.overlay, brings
// up loopback in the new network namespace, drops its capabilities and
// runs the check.
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
	if c.reportPid {
		a = append(a, "--report-pid")
	}
	for _, m := range c.mask {
		a = append(a, "--mask", m)
	}
	for _, k := range c.rw {
		a = append(a, "--rw", k)
	}
	for _, k := range c.ro {
		a = append(a, "--ro", k)
	}
	for _, o := range c.overlay {
		a = append(a, "--overlay", o.target, o.lower, o.upper, o.work)
	}
	return a
}

func nsAttr(newNet bool) *syscall.SysProcAttr {
	uid, gid := os.Getuid(), os.Getgid()
	flags := uintptr(syscall.CLONE_NEWUSER | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS)
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
		// namespaces only) for the mounts and loopback, and
		// CAP_DAC_OVERRIDE (over files of the one mapped uid) for overlayfs.
		// It clears them before starting the check.
		AmbientCaps: []uintptr{capSysAdmin, capNetAdmin, capDacOverride},
	}
}

const (
	capDacOverride       = 1
	capNetAdmin          = 12
	capSysAdmin          = 21
	prCapAmbient         = 47
	prCapAmbientClearAll = 4
	oPath                = 0x200000
	stRdonly             = 1
)

// probeIsolation starts pid 1 the way a check does (new network namespace,
// every mount read-only, a tmpfs over /tmp, loopback up) with nothing to run.
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
		return false, fmt.Sprintf("cannot create user+pid+mount+ipc+uts+network namespaces and mount in them: %v %s (on Ubuntu 24.04+: sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0)", err, strings.TrimSpace(string(out)))
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
	var masks, rws, ros, command []string
	var overlays []overlayMount
	lo, probe, fd3, reportPid := false, false, false, false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--lo":
			lo = true
		case a == "--probe":
			probe = true
		case a == "--fd3":
			fd3 = true
		case a == "--report-pid":
			reportPid = true
		case (a == "--mask" || a == "--rw" || a == "--ro") && i+1 < len(args):
			switch a {
			case "--mask":
				masks = append(masks, args[i+1])
			case "--rw":
				rws = append(rws, args[i+1])
			default:
				ros = append(ros, args[i+1])
			}
			i++
		case a == "--overlay" && i+4 < len(args):
			overlays = append(overlays, overlayMount{args[i+1], args[i+2], args[i+3], args[i+4]})
			i += 4
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
	if reportPid || fd3 {
		// Inherited without close-on-exec; the check must not get it
		// (with --fd3 it is passed on explicitly).
		syscall.CloseOnExec(3)
	}
	// Every step is required, and the probe does them too: a user namespace
	// without capabilities (Ubuntu's AppArmor restriction on unprivileged
	// user namespaces still lets one be created, confined) cannot, and is
	// reported as no isolation.
	// The working directory was resolved before the mounts below; the check
	// must start in the same path as it is after them (the writable bind).
	wd, err := os.Getwd()
	if err != nil {
		return die("getwd: %v", err)
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return die("make / private: %v", err)
	}
	if err := syscall.Mount("proc", "/proc", "proc", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, ""); err != nil {
		return die("mount /proc: %v", err)
	}
	// Open what to bind back before anything is covered.
	type bind struct {
		path string
		fd   int
		rw   bool
	}
	var binds []bind
	for _, l := range []struct {
		paths []string
		rw    bool
	}{{rws, true}, {ros, false}} {
		for _, k := range l.paths {
			fd, err := syscall.Open(k, oPath|syscall.O_CLOEXEC, 0)
			if err != nil {
				if l.rw {
					return die("open %s: %v", k, err)
				}
				continue
			}
			binds = append(binds, bind{k, fd, l.rw})
		}
	}
	sort.SliceStable(binds, func(i, j int) bool { return len(binds[i].path) < len(binds[j].path) })
	lowerFDs := make([]int, len(overlays))
	for i, o := range overlays {
		fd, err := syscall.Open(o.lower, oPath|syscall.O_CLOEXEC|syscall.O_DIRECTORY, 0)
		if err != nil {
			return die("open %s: %v", o.lower, err)
		}
		lowerFDs[i] = fd
	}
	// Every mount the namespace inherited becomes read-only: the toolchain,
	// PATH directories, caches and any other host directory (a check of
	// criterion A must not change what criterion B, or a later
	// verification, runs).
	if err := remountAllReadOnly(); err != nil {
		return die("%v", err)
	}
	sort.Strings(masks)
	sort.SliceStable(masks, func(i, j int) bool { return len(masks[i]) < len(masks[j]) })
	for _, m := range masks {
		fi, err := os.Lstat(m)
		if err != nil || !fi.IsDir() {
			continue // absent (or under an earlier mask), or a symlink the verifier did not resolve
		}
		mode := "0755"
		if m == "/tmp" || m == "/var/tmp" || m == "/dev/shm" {
			mode = "1777"
		}
		if err := syscall.Mount("tmpfs", m, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode="+mode); err != nil {
			return die("tmpfs over %s: %v", m, err)
		}
	}
	// POSIX message queues of this IPC namespace, not the host's.
	if fi, err := os.Stat("/dev/mqueue"); err == nil && fi.IsDir() {
		if err := syscall.Mount("mqueue", "/dev/mqueue", "mqueue", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, ""); err != nil {
			if err := syscall.Mount("tmpfs", "/dev/mqueue", "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, "mode=0755"); err != nil {
				return die("/dev/mqueue: %v", err)
			}
		}
	}
	for _, b := range binds {
		if !b.rw && !under(b.path, masks) {
			syscall.Close(b.fd)
			continue // already read-only where it is
		}
		var st syscall.Stat_t
		if err := syscall.Fstat(b.fd, &st); err != nil {
			return die("bind %s: %v", b.path, err)
		}
		if st.Mode&syscall.S_IFMT == syscall.S_IFDIR {
			if err := os.MkdirAll(b.path, 0o755); err != nil {
				return die("bind %s: %v", b.path, err)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(b.path), 0o755); err != nil {
				return die("bind %s: %v", b.path, err)
			}
			if f, err := os.OpenFile(b.path, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				f.Close()
			}
		}
		if err := syscall.Mount("/proc/self/fd/"+strconv.Itoa(b.fd), b.path, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
			return die("bind %s: %v", b.path, err)
		}
		syscall.Close(b.fd)
		if err := remountBind(b.path, !b.rw); err != nil {
			return die("%v", err)
		}
	}
	for i, o := range overlays {
		if o.upper == "" {
			if err := syscall.Mount("/proc/self/fd/"+strconv.Itoa(lowerFDs[i]), o.target, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
				return die("bind %s over %s: %v", o.lower, o.target, err)
			}
			syscall.Close(lowerFDs[i])
			if err := remountBind(o.target, true); err != nil {
				return die("%v", err)
			}
			continue
		}
		opts := "lowerdir=/proc/self/fd/" + strconv.Itoa(lowerFDs[i]) + ",upperdir=" + o.upper + ",workdir=" + o.work
		if err := syscall.Mount("overlay", o.target, "overlay", syscall.MS_NOSUID|syscall.MS_NODEV, opts); err != nil {
			return die("overlay %s over %s: %v", o.target, o.lower, err)
		}
		syscall.Close(lowerFDs[i])
	}
	if lo {
		if err := loopbackUp(); err != nil {
			return die("loopback: %v", err)
		}
	}
	if probe {
		// The probe checks that the remount took.
		var fs syscall.Statfs_t
		if err := syscall.Statfs("/", &fs); err != nil || fs.Flags&stRdonly == 0 {
			return die("/ is not read-only after remounting (%v)", err)
		}
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
	if err := os.Chdir(wd); err != nil {
		return die("chdir %s: %v", wd, err)
	}
	cmd := exec.Command(command[0], command[2:]...)
	cmd.Args[0] = command[1]
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, os.Stdout, os.Stderr
	if fd3 {
		// A test binary's harness channel (launch_linux.go): passed on as
		// fd 3, and closed here.
		cmd.Stdin = os.Stdin
		cmd.ExtraFiles = []*os.File{os.NewFile(3, "harness")}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "sdf verify: %v\n", err)
		return 126
	}
	if fd3 {
		cmd.ExtraFiles[0].Close()
	}
	if reportPid {
		// The verifier identifies the criterion's go command by this pid
		// (checkLauncher): written once, then the pipe is closed.
		f := os.NewFile(3, "pid")
		_, err := fmt.Fprintf(f, "%d\n", cmd.Process.Pid)
		f.Close()
		if err != nil {
			_ = cmd.Process.Kill()
			return die("report pid: %v", err)
		}
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

// remountAllReadOnly makes every mount in /proc/self/mountinfo (except
// /proc, which pid 1 just mounted) read-only. A mount whose path cannot be
// reached (covered by another mount, or under a directory this user cannot
// enter) is unreachable for the check too.
func remountAllReadOnly() error {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		mp := unescapeMount(f[4])
		if mp == "/proc" || strings.HasPrefix(mp, "/proc/") {
			continue
		}
		if err := remountBind(mp, true); err != nil {
			var fs syscall.Statfs_t
			switch {
			case errors.Is(err, syscall.ENOENT), errors.Is(err, syscall.ENOTDIR), errors.Is(err, syscall.EACCES):
				continue
			case syscall.Statfs(mp, &fs) == nil && fs.Flags&stRdonly != 0:
				continue
			}
			return err
		}
	}
	return nil
}

// remountBind sets or clears read-only on the mount at p, keeping the
// flags the kernel locks (nosuid, nodev, noexec; atime is kept by not
// naming it).
func remountBind(p string, readOnly bool) error {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(p, &fs); err != nil {
		return fmt.Errorf("remount %s: %w", p, err)
	}
	flags := uintptr(syscall.MS_REMOUNT | syscall.MS_BIND)
	flags |= uintptr(fs.Flags) & (syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC)
	if readOnly {
		flags |= syscall.MS_RDONLY
	}
	if err := syscall.Mount("", p, "", flags, ""); err != nil {
		return fmt.Errorf("remount %s read-only=%v: %w", p, readOnly, err)
	}
	return nil
}

// unescapeMount undoes mountinfo's octal escapes (\040 for a space, ...).
func unescapeMount(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				out = append(out, byte(n))
				i += 3
				continue
			}
		}
		out = append(out, s[i])
	}
	return string(out)
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
