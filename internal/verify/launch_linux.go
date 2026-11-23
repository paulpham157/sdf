//go:build linux

package verify

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// launcherMain is go test's -exec program: args are CONFIG, the test
// binary and its arguments, and the working directory is the package's
// (go sets it). It connects to the verifier, which checks that this
// process is the launcher started by the criterion's go command and binds
// a channel to the package; it receives one end of that channel and
// passes it to the test binary as fd 3, and to nothing else. Isolated, the
// test binary runs under a nested pid 1 (nsInitMain) in new user, pid and
// mount namespaces where every mount is read-only except a fresh TMPDIR
// and HOME (and the checkout with --writable-checkout), so it cannot
// replace another package's test binary in go's work directory, poison the
// build cache, or reach go and this launcher.
func launcherMain(args []string) int {
	die := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "sdf verify launcher: "+format+"\n", a...)
		return 125
	}
	if len(args) < 2 {
		return die("usage: %s CONFIG TEST-BINARY [ARGS...]", launchArg)
	}
	cfg, err := readLaunchConfig(args[0])
	if err != nil {
		return die("config: %v", err)
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: cfg.Socket, Net: "unix"})
	if err != nil {
		return die("connect: %v", err)
	}
	ch, err := recvFD(c)
	c.Close()
	if err != nil {
		return die("the verifier refused this launcher: %v", err)
	}
	bin := args[1]
	var cmd *exec.Cmd
	if cfg.Isolated {
		self, err := os.Executable()
		if err != nil {
			return die("%v", err)
		}
		a := []string{nsInitArg, "--fd3"}
		for _, m := range cfg.Mask {
			a = append(a, "--mask", m)
		}
		for _, k := range cfg.RW {
			a = append(a, "--rw", k)
		}
		a = append(a, "--", bin, bin)
		cmd = exec.Command(self, append(a, args[2:]...)...)
		cmd.SysProcAttr = nsAttr(false)
	} else {
		cmd = exec.Command(bin, args[2:]...)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{ch}
	if err := cmd.Start(); err != nil {
		return die("%v", err)
	}
	ch.Close()
	// The channel has left this process. Its /proc entries are closed to
	// other processes of this user from now on (not before Start: a child
	// in a new user namespace needs its uid_map written by this process,
	// which a non-dumpable parent's child does not allow).
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0); e != 0 {
		_ = cmd.Process.Kill()
		return die("PR_SET_DUMPABLE: %v", e)
	}
	// go signals this process on a test timeout or interrupt.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		for s := range sigs {
			_ = cmd.Process.Signal(s)
		}
	}()
	err = cmd.Wait()
	signal.Stop(sigs)
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return die("%v", err)
}
