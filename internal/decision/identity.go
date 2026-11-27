package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// loginPattern is a GitHub login's shape: letters and digits, hyphens only
// between those characters. It cannot start or end with a hyphen, and it
// cannot contain two hyphens in a row. The `{0,38}` bound counts groups.
// A group may be two characters (a hyphen plus a letter or digit), so the
// bound is not a 39-character cap. ValidDecidedBy enforces that cap. The
// legacy words "user" and "agent" match, and they stay legal writes.
var loginPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9]){0,38}$`)

// decidedByMaxLen is the GitHub login length limit, in bytes. Logins are
// ASCII, so this is a character cap.
const decidedByMaxLen = 39

// ValidDecidedBy reports whether s is a GitHub login, including the legacy
// words user and agent. Empty is not valid; the caller treats empty as unset.
func ValidDecidedBy(s string) bool {
	if len(s) > decidedByMaxLen {
		return false
	}
	return loginPattern.MatchString(s)
}

// identityLookupTimeout bounds gh and git so a missing or stuck command
// does not hold sdf decision add. A timeout is a miss, not a failure of
// the add.
const identityLookupTimeout = 3 * time.Second

// CommandRunner runs name with args and returns stdout. A missing binary,
// a non-zero exit, or a timeout is an error. ResolveDecidedBy treats that
// as "no value" and tries the next source.
type CommandRunner func(name string, args ...string) ([]byte, error)

// ExecCommand runs name with args. Stderr is discarded. The process is
// killed after identityLookupTimeout. GH_PROMPT_DISABLED and
// GIT_TERMINAL_PROMPT stop a missing credential from waiting on a prompt.
func ExecCommand(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), identityLookupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = nil
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// ResolveDecidedBy chooses the stored decided_by. When explicitSet is
// true, the trimmed explicit value is returned, including empty, and run
// is not called. When the flag is omitted, the first configured login
// that is non-empty and ValidDecidedBy is used: the login field of
// `gh api user`, then `git config --get github.user`, then
// `git config --get user.name`. A missing command, a failed command, or a
// value that is not a login is skipped. When none is found, the result
// is empty. sdf does not ask GitHub whether the account exists.
func ResolveDecidedBy(explicit string, explicitSet bool, run CommandRunner) string {
	if explicitSet {
		return strings.TrimSpace(explicit)
	}
	if run == nil {
		return ""
	}
	if login := githubLogin(run); ValidDecidedBy(login) {
		return login
	}
	if login := gitConfigValue(run, "github.user"); ValidDecidedBy(login) {
		return login
	}
	if login := gitConfigValue(run, "user.name"); ValidDecidedBy(login) {
		return login
	}
	return ""
}

func githubLogin(run CommandRunner) string {
	out, err := run("gh", "api", "user")
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return ""
	}
	var doc struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return ""
	}
	return strings.TrimSpace(doc.Login)
}

func gitConfigValue(run CommandRunner, key string) string {
	out, err := run("git", "config", "--get", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
