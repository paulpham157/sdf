//go:build unix

package live

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// isolateAgentCommand puts the CLI in its own process group and kills that
// group when the run context ends. A CLI started with a shell flag can
// leave a grandchild holding stderr; killing only the direct child leaves
// that grandchild running and Cmd.Wait blocked on the pipe. A child that
// calls setsid leaves this group and the kill does not reach it. WaitDelay
// on the command is the backstop for that case.
func isolateAgentCommand(cmd *exec.Cmd) {
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
