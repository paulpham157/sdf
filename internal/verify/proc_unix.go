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

// reapGroup kills whatever the check left running in its process group
// once it has exited, so a background process cannot outlive the check and
// change the checkout or the verifier's output afterwards. A process that
// left the group (setsid) is not reached; see docs/verify.md.
func reapGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
