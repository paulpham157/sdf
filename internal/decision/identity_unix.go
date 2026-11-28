//go:build unix

package decision

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// isolateIdentityCommand puts the lookup in its own process group and
// kills that group when the deadline fires. gh can start a child; killing
// only the leader leaves the child running after sdf decision add returns.
func isolateIdentityCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err != nil && errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
