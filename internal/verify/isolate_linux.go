//go:build linux

package verify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// isolatedCommand runs command (argv[0] shown as argv0) as the child of a
// re-executed copy of this binary that is pid 1 in new user, pid and mount
// namespaces. When pid 1 exits, the kernel kills every process left in the
// namespace, including ones that called setsid or double-forked, before
// the parent's wait returns; so when cmd.Wait returns nothing the check
// started is still running. Killing pid 1 on timeout does the same.
func isolatedCommand(ctx context.Context, command string, argv []string) (*exec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := append([]string{nsInitArg, command}, argv...)
	cmd := exec.CommandContext(ctx, self, args...)
	cmd.SysProcAttr = nsAttr()
	return cmd, nil
}

func nsAttr() *syscall.SysProcAttr {
	uid, gid := os.Getuid(), os.Getgid()
	return &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
}

// probeIsolation starts pid 1 in the namespaces with nothing to run.
func probeIsolation() (bool, string) {
	self, err := os.Executable()
	if err != nil {
		return false, err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, nsInitArg, "--probe")
	cmd.SysProcAttr = nsAttr()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Sprintf("cannot create user+pid+mount namespaces: %v %s (on Ubuntu 24.04+: sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0)", err, out)
	}
	return true, ""
}

// nsInitMain is pid 1 of a criterion's namespace: it gives the namespace
// its own /proc (so checks cannot see or open the verifier's processes;
// best effort), runs the command, and exits with its status, which takes
// everything else in the namespace with it.
func nsInitMain(args []string) int {
	if os.Getpid() != 1 {
		fmt.Fprintln(os.Stderr, "sdf verify: namespace init is not pid 1")
		return 125
	}
	if len(args) == 1 && args[0] == "--probe" {
		return 0
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "sdf verify: namespace init needs a command")
		return 125
	}
	if syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, "") == nil {
		_ = syscall.Mount("proc", "/proc", "proc", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, "")
	}
	cmd := exec.Command(args[0], args[2:]...)
	cmd.Args[0] = args[1]
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
