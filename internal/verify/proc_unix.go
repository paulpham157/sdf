//go:build unix

package verify

import (
	"os/exec"
	"syscall"
)

// isolateProcess puts the check in its own process group, so a timeout
// kills everything it started, not only the first process.
func isolateProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
