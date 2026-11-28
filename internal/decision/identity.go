package decision

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
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
// GitHub compares logins without regard to case. sdf stores the bytes it
// was given and does not fold them.
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

// identityLookupTimeout is the deadline for the gh hosts.yml read.
// identityGitReserve is a separate budget for git config github.user,
// started after gh returns or times out. A hung gh does not skip git.
// The command budgets themselves are 3.5 seconds. A killed command may
// also hold its pipes for identityWaitDelay, once for gh and once for
// git, so the worst case is about 4.5 seconds.
const identityLookupTimeout = 3 * time.Second

const identityGitReserve = 500 * time.Millisecond

// identityWaitDelay is how long a command may keep its pipes open after
// the deadline kills it. internal/verify sets the same field, there at
// 5 seconds. Here 500ms is enough for a stuck stdout to be dropped so it
// does not hold sdf decision add.
const identityWaitDelay = 500 * time.Millisecond

// identityStdoutCap is the most stdout one lookup keeps. A longer stream
// is discarded and the source is skipped, so a noisy command cannot fill
// memory or be stored as a login.
const identityStdoutCap = 64 * 1024

// CommandRunner runs name with args and returns stdout. ctx is that
// source's own deadline. A missing binary, a non-zero exit, a timeout, or
// stdout over identityStdoutCap is an error. ResolveDecidedBy treats that
// as "no value" and tries the next source.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecCommand runs name with args under ctx. Stderr is discarded. Stdout
// is capped at identityStdoutCap. WaitDelay bounds how long the pipes may
// stay open after the process is killed. On Unix the deadline kills the
// process group, so a child of gh that stays in that group does not keep
// running. A child that calls setsid, or otherwise leaves the group, is
// not killed. GH_PROMPT_DISABLED
// and GIT_TERMINAL_PROMPT stop a missing credential from waiting on a
// prompt. gh config get reads hosts.yml and does not use the network. git
// config reads the local repository too.
func ExecCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = identityWaitDelay
	isolateIdentityCommand(cmd)
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = nil
	stdout := &capBuffer{limit: identityStdoutCap}
	cmd.Stdout = stdout
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if stdout.over {
		return nil, errors.New("identity command stdout exceeded 64 KiB")
	}
	return stdout.buf.Bytes(), nil
}

// capBuffer keeps at most limit bytes and discards the rest, so the
// process does not block on a full pipe. over is set once a byte is dropped.
type capBuffer struct {
	buf   bytes.Buffer
	n     int
	limit int
	over  bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if c.n < c.limit {
		take := len(p)
		if c.n+take > c.limit {
			take = c.limit - c.n
			c.over = true
		}
		c.buf.Write(p[:take])
		c.n += take
	} else if len(p) > 0 {
		c.over = true
	}
	return len(p), nil
}

// osUserLookup is the OS-username source. Tests replace it so a machine
// account does not become decided_by. Production uses currentOSUser.
var osUserLookup = currentOSUser

// SetOSUserLookup replaces the OS-username source and returns a restore
// function. A nil fn disables the source. The name is local and
// self-asserted; it is not a GitHub authentication.
func SetOSUserLookup(fn func() (string, error)) func() {
	prev := osUserLookup
	if fn == nil {
		fn = func() (string, error) { return "", errors.New("os user disabled") }
	}
	osUserLookup = fn
	return func() { osUserLookup = prev }
}

// ResolveDecidedBy chooses the stored decided_by and where it came from.
// The source is DecidedBySourceFlag, DecidedBySourceGH, DecidedBySourceGit,
// or DecidedBySourceOS. Both results are empty when nothing is stored.
// When explicitSet is true, the trimmed explicit value is returned,
// including empty and including the legacy words, and run is not called.
// A non-empty explicit value has source flag. When the flag is omitted,
// the first configured login that is non-empty, ValidDecidedBy, and not a
// legacy word is used: `gh config get -h github.com user`, then
// `git config --get github.user`, then the OS username. git config
// user.name is not read. A missing command, a failed command, a legacy
// word, a deny-listed machine account, or a value that is not a login is
// skipped. When none is found, the result is empty. sdf does not ask
// GitHub whether the account exists, and this path does not use the
// network. gh has identityLookupTimeout. git then has identityGitReserve
// of its own, so a hung gh still leaves git a turn. The OS username is a
// local read, not another command. Each killed command may then take
// identityWaitDelay to drop its pipes, so the worst case is about 4.5
// seconds. The budgets are not longer than that. The hosts.yml login
// takes precedence over github.user, and either takes precedence over
// the OS username. A nil run skips every source, including the OS
// username. newRecord rejects a case variant of user or agent; this
// function still returns the trimmed explicit bytes so the caller can
// report that error.
func ResolveDecidedBy(explicit string, explicitSet bool, run CommandRunner) (string, string) {
	return resolveDecidedBy(explicit, explicitSet, run, osUserLookup, identityLookupTimeout)
}

func resolveDecidedBy(explicit string, explicitSet bool, run CommandRunner, userName func() (string, error), timeout time.Duration) (string, string) {
	if explicitSet {
		login := strings.TrimSpace(explicit)
		if login == "" {
			return "", ""
		}
		return login, DecidedBySourceFlag
	}
	if run == nil {
		return "", ""
	}
	ghCtx, ghCancel := context.WithTimeout(context.Background(), timeout)
	login, ok := acceptedLogin(configLine(ghCtx, run, "gh", "config", "get", "-h", "github.com", "user"))
	ghCancel()
	if ok {
		return login, DecidedBySourceGH
	}
	gitCtx, gitCancel := context.WithTimeout(context.Background(), identityGitReserve)
	login, ok = acceptedLogin(configLine(gitCtx, run, "git", "config", "--get", "github.user"))
	gitCancel()
	if ok {
		return login, DecidedBySourceGit
	}
	login, ok = osLogin(userName)
	if !ok {
		return "", ""
	}
	return login, DecidedBySourceOS
}

// currentOSUser is the machine account name from os/user.Current. It does
// not use the network or a shell. Tests pass another function to
// resolveDecidedBy.
func currentOSUser() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	if u == nil {
		return "", errors.New("no current user")
	}
	return u.Username, nil
}

// osLogin is the last auto-resolution source. The name is local and
// self-asserted: it is the OS account, not a GitHub authentication.
// A Windows DOMAIN\ prefix is stripped. The remainder must be an accepted
// login and not a common service account. user and agent, in any case,
// are skipped. An error, a deny-listed name, or an invalid name leaves
// decided_by empty. A stored OS name has source os and does not lock.
func osLogin(userName func() (string, error)) (string, bool) {
	if userName == nil {
		return "", false
	}
	name, err := userName()
	if err != nil {
		return "", false
	}
	name = normalizeOSUser(name)
	if osAccountDenied(name) {
		return "", false
	}
	return acceptedLogin(name)
}

// osAccountDeny is the OS-fallback deny list, compared without regard to
// case. These are service and system accounts, not a person's login. A
// match means no OS fallback. gh and git are not checked against this
// list. Windows LOCAL SERVICE and NETWORK SERVICE are included even
// though a space already fails the login pattern.
var osAccountDeny = map[string]struct{}{
	"root": {}, "admin": {}, "administrator": {}, "runner": {}, "ubuntu": {},
	"ec2-user": {}, "node": {}, "vscode": {}, "codespace": {}, "box": {},
	"docker": {}, "jenkins": {}, "github": {}, "gitlab-runner": {},
	"circleci": {}, "build": {}, "buildkite-agent": {}, "nobody": {},
	"daemon": {}, "www-data": {}, "system": {}, "user": {}, "agent": {},
	"local service": {}, "network service": {},
}

// osAccountDenied reports a service or system account, or a Windows
// machine account (a name ending in $). The comparison is case-insensitive.
func osAccountDenied(name string) bool {
	if name == "" {
		return false
	}
	if strings.HasSuffix(name, "$") {
		return true
	}
	_, ok := osAccountDeny[strings.ToLower(name)]
	return ok
}

// normalizeOSUser trims space and drops a Windows DOMAIN\ prefix. The
// part after the last backslash is the account name.
func normalizeOSUser(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, `\`); i >= 0 {
		name = strings.TrimSpace(name[i+1:])
	}
	return name
}

// acceptedLogin reports whether s may be stored by auto-resolution.
// The legacy words stay legal on an explicit --decided-by, which keeps
// the exact case the caller passed. Auto-resolution rejects user and
// agent without regard to case, so User and AGENT are not stored. A
// stored login from flag, gh, or git, and the exact word user, are
// locked brief constraints. A login from the OS username is not. The
// exact word agent is not.
func acceptedLogin(s string) (string, bool) {
	if !ValidDecidedBy(s) || strings.EqualFold(s, DecidedByUser) || strings.EqualFold(s, DecidedByAgent) {
		return "", false
	}
	return s, true
}

func configLine(ctx context.Context, run CommandRunner, name string, args ...string) string {
	out, err := run(ctx, name, args...)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}
