//go:build !unix

package decision

import "os/exec"

// isolateIdentityCommand has no process group off unix. A timeout kills
// the process the command started.
func isolateIdentityCommand(cmd *exec.Cmd) {}
