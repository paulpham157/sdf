//go:build !unix

package verify

import "os/exec"

// isolateProcess has no process group off unix. A timeout kills the first
// process only.
func isolateProcess(cmd *exec.Cmd) {}

// reapGroup is a no-op off unix.
func reapGroup(cmd *exec.Cmd) {}
