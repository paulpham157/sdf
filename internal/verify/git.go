package verify

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// git runs git in dir with hooks, external diff, and textconv off, and
// with no user or system config, so the repository under test cannot
// change how the verifier's own git commands behave.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	full := append([]string{"-C", dir, "-c", "core.hooksPath=" + nullDevice(), "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(cleanGitEnv(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+nullDevice(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		return out, fmt.Errorf("git %s: %w: %s", args[0], err, msg)
	}
	return out, nil
}

func gitLine(ctx context.Context, dir string, args ...string) (string, error) {
	b, err := git(ctx, dir, args...)
	return strings.TrimSpace(string(b)), err
}
