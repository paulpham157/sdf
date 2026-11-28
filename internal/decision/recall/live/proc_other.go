//go:build !unix

package live

import "os/exec"

// isolateAgentCommand has no process group off unix. A timeout kills the
// process the command started.
func isolateAgentCommand(cmd *exec.Cmd) {}
