package decision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestValidDecidedBy(t *testing.T) {
	ok := []string{"a", "user", "agent", "paulpham157", "A", "a-b", "a1-b2", strings.Repeat("a", 39)}
	bad := []string{"", "a--b", "-ab", "ab-", "a-", "has space", "a_b", "a.b", "not a login", strings.Repeat("a", 40), "paulpham157 "}
	for _, s := range ok {
		if !ValidDecidedBy(s) {
			t.Errorf("want valid %q", s)
		}
	}
	for _, s := range bad {
		if ValidDecidedBy(s) {
			t.Errorf("want invalid %q", s)
		}
	}
}

func TestValidDecidedByHyphenLength(t *testing.T) {
	// "a" plus "-b" repeated. Each "-b" is two characters, which the pattern's
	// group bound counts as one, so a 41-character "a-b-b-…" used to match.
	at39 := "a" + strings.Repeat("-b", 19)
	if len(at39) != 39 {
		t.Fatalf("39-char fixture length %d", len(at39))
	}
	at40 := at39 + "c"
	at41 := "a" + strings.Repeat("-b", 20)
	if len(at40) != 40 || len(at41) != 41 {
		t.Fatalf("fixture lengths %d %d", len(at40), len(at41))
	}
	if !ValidDecidedBy(at39) {
		t.Fatalf("want valid 39-char hyphenated login %q", at39)
	}
	for _, s := range []string{at40, at41, "-ab", "ab-", "a-", "-a-b", "a-b-", "a--b"} {
		if ValidDecidedBy(s) {
			t.Errorf("want invalid %q (len %d)", s, len(s))
		}
	}
}

func TestExecCommand(t *testing.T) {
	ctx := context.Background()
	out, err := ExecCommand(ctx, "echo", "hello")
	if err != nil || strings.TrimSpace(string(out)) != "hello" {
		t.Fatalf("echo %q %v", out, err)
	}
	if _, err := ExecCommand(ctx, "sdf-identity-missing-binary"); err == nil {
		t.Fatal("missing binary succeeded")
	}
}

func TestExecCommandHonorsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := ExecCommand(ctx, "sleep", "30"); err == nil {
		t.Fatal("sleep succeeded")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("deadline not honored: %s", elapsed)
	}
}

func TestExecCommandCapsStdout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := ExecCommand(ctx, "sh", "-c", "dd if=/dev/zero bs=1024 count=128 status=none"); err == nil {
		t.Fatal("oversized stdout accepted")
	}
}

func TestResolveDecidedBy(t *testing.T) {
	calls := 0
	boom := CommandRunner(func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("should not run")
	})
	if got := ResolveDecidedBy("  paulpham157  ", true, boom); got != "paulpham157" || calls != 0 {
		t.Fatalf("explicit %q calls %d", got, calls)
	}
	if got := ResolveDecidedBy("  ", true, boom); got != "" || calls != 0 {
		t.Fatalf("explicit empty %q calls %d", got, calls)
	}
	if got := ResolveDecidedBy("user", true, boom); got != "user" || calls != 0 {
		t.Fatalf("explicit legacy %q calls %d", got, calls)
	}
	// An explicit value keeps its case. Auto-resolution is what folds the
	// legacy-word check; the flag itself does not.
	if got := ResolveDecidedBy("User", true, boom); got != "User" || calls != 0 {
		t.Fatalf("explicit exact case %q calls %d", got, calls)
	}
	if got := ResolveDecidedBy("", false, nil); got != "" {
		t.Fatalf("nil runner %q", got)
	}

	script := func(responses map[string]struct {
		out []byte
		err error
	}) CommandRunner {
		return func(_ context.Context, name string, args ...string) ([]byte, error) {
			key := name + " " + strings.Join(args, " ")
			r, ok := responses[key]
			if !ok {
				t.Errorf("unexpected command %s", key)
				return nil, errors.New("unexpected")
			}
			return r.out, r.err
		}
	}
	miss := errors.New("missing")
	gh := "gh config get -h github.com user"
	gitUser := "git config --get github.user"
	// Commands that do not yield a login must not pick up this machine's
	// account. The OS-username cases pass their own function.
	noUser := func() (string, error) { return "", errors.New("no os user") }
	resolveCommands := func(run CommandRunner) string {
		return resolveDecidedBy("", false, run, noUser, identityLookupTimeout)
	}

	if got := ResolveDecidedBy("", false, script(map[string]struct {
		out []byte
		err error
	}{
		gh: {out: []byte("paulpham157\n")},
	})); got != "paulpham157" {
		t.Fatalf("gh config %q", got)
	}

	if got := ResolveDecidedBy("", false, script(map[string]struct {
		out []byte
		err error
	}{
		gh:      {err: miss},
		gitUser: {out: []byte("octocat\n")},
	})); got != "octocat" {
		t.Fatalf("non-zero gh should fall through to git, got %q", got)
	}

	if got := resolveCommands(script(map[string]struct {
		out []byte
		err error
	}{
		gh:      {out: []byte("user\n")},
		gitUser: {out: []byte("agent\n")},
	})); got != "" {
		t.Fatalf("legacy words from config stored %q", got)
	}

	if got := resolveCommands(script(map[string]struct {
		out []byte
		err error
	}{
		gh:      {out: []byte("User\n")},
		gitUser: {out: []byte("AGENT\n")},
	})); got != "" {
		t.Fatalf("case-variant legacy words from config stored %q", got)
	}

	if got := ResolveDecidedBy("", false, script(map[string]struct {
		out []byte
		err error
	}{
		gh:      {out: []byte("user\n")},
		gitUser: {out: []byte("paulpham157\n")},
	})); got != "paulpham157" {
		t.Fatalf("legacy gh should fall through, got %q", got)
	}

	// git config user.name is not a source, even when it is a login and
	// even when it is the legacy word that would lock the brief.
	if got := resolveCommands(script(map[string]struct {
		out []byte
		err error
	}{
		gh:      {err: miss},
		gitUser: {err: miss},
	})); got != "" {
		t.Fatalf("user.name must not be read, got %q", got)
	}

	if got := resolveCommands(script(map[string]struct {
		out []byte
		err error
	}{
		gh:      {out: []byte("Cursor Agent\n")},
		gitUser: {out: []byte("\n")},
	})); got != "" {
		t.Fatalf("display name stored %q", got)
	}
}

func TestResolveDecidedByHungGHStillReadsGit(t *testing.T) {
	start := time.Now()
	got := resolveDecidedBy("", false, func(ctx context.Context, name string, _ ...string) ([]byte, error) {
		if name == "gh" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		select {
		case <-ctx.Done():
			t.Error("git budget was already spent by the hung gh")
			return nil, ctx.Err()
		default:
		}
		return []byte("octocat\n"), nil
	}, func() (string, error) {
		t.Error("os user consulted after git returned a login")
		return "", errors.New("unused")
	}, 150*time.Millisecond)
	if got != "octocat" {
		t.Fatalf("hung gh stored %q, want octocat from git", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("gh deadline not honored: %s", elapsed)
	}
}

func TestResolveDecidedByHungGitAfterFailedGH(t *testing.T) {
	start := time.Now()
	got := resolveDecidedBy("", false, func(ctx context.Context, name string, _ ...string) ([]byte, error) {
		if name == "gh" {
			return nil, errors.New("missing")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}, func() (string, error) {
		return "", errors.New("no os user")
	}, identityLookupTimeout)
	if got != "" {
		t.Fatalf("hung git stored %q", got)
	}
	elapsed := time.Since(start)
	if elapsed < 400*time.Millisecond || elapsed > 900*time.Millisecond {
		t.Fatalf("git budget %s, want about 500ms", elapsed)
	}
}

func TestLockedDecidedBy(t *testing.T) {
	for _, s := range []string{"paulpham157", "user", "A", "a-b", "User"} {
		if !LockedDecidedBy(s) {
			t.Errorf("want locked %q", s)
		}
	}
	for _, s := range []string{"", "agent", "not a login", "a_b", strings.Repeat("a", 40)} {
		if LockedDecidedBy(s) {
			t.Errorf("want unlocked %q", s)
		}
	}
}

func TestResolveDecidedByOSUser(t *testing.T) {
	miss := errors.New("missing")
	gh := "gh config get -h github.com user"
	gitUser := "git config --get github.user"
	bothMiss := func(_ context.Context, name string, args ...string) ([]byte, error) {
		key := name + " " + strings.Join(args, " ")
		if key != gh && key != gitUser {
			t.Errorf("unexpected command %s", key)
		}
		return nil, miss
	}
	calls := 0
	user := func() (string, error) {
		calls++
		return `CORP\paulpham157`, nil
	}
	if got := resolveDecidedBy("", false, bothMiss, user, time.Second); got != "paulpham157" || calls != 1 {
		t.Fatalf("windows user %q calls %d", got, calls)
	}

	calls = 0
	if got := resolveDecidedBy("", false, func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "gh" {
			t.Errorf("git ran after a hosts.yml login")
		}
		return []byte("octocat\n"), nil
	}, func() (string, error) {
		calls++
		return "paulpham157", nil
	}, time.Second); got != "octocat" || calls != 0 {
		t.Fatalf("gh should win, got %q calls %d", got, calls)
	}

	if got := resolveDecidedBy("", false, nil, func() (string, error) {
		t.Fatal("nil runner consulted the os user")
		return "paulpham157", nil
	}, time.Second); got != "" {
		t.Fatalf("nil runner %q", got)
	}

	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: `CORP\octocat`, want: "octocat"},
		{name: "  CORP\\paulpham157  ", want: "paulpham157"},
		{name: "ubuntu", want: "ubuntu"},
		{name: `CORP\user`},
		{name: `CORP\agent`},
		{name: "User"},
		{name: "AGENT"},
		{name: "not a login"},
		{name: "a_b"},
		{name: strings.Repeat("a", 40)},
		{name: `CORP\`},
		{err: errors.New("lookup failed")},
	}
	for _, tc := range cases {
		got := resolveDecidedBy("", false, bothMiss, func() (string, error) {
			return tc.name, tc.err
		}, time.Second)
		if got != tc.want {
			t.Errorf("os user %q err %v = %q, want %q", tc.name, tc.err, got, tc.want)
		}
	}
}

func TestExecCommandKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups")
	}
	marker := filepath.Join(t.TempDir(), "late")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	script := "(sleep 2; touch " + strconv.Quote(marker) + ") & sleep 30"
	start := time.Now()
	if _, err := ExecCommand(ctx, "sh", "-c", script); err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("deadline not honored: %s", time.Since(start))
	}
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a child outlived the timed-out command")
	}
}
