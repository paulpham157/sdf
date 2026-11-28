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
	if got, src := ResolveDecidedBy("  paulpham157  ", true, boom); got != "paulpham157" || src != DecidedBySourceFlag || calls != 0 {
		t.Fatalf("explicit %q %q calls %d", got, src, calls)
	}
	if got, src := ResolveDecidedBy("  ", true, boom); got != "" || src != "" || calls != 0 {
		t.Fatalf("explicit empty %q %q calls %d", got, src, calls)
	}
	if got, src := ResolveDecidedBy("user", true, boom); got != "user" || src != DecidedBySourceFlag || calls != 0 {
		t.Fatalf("explicit legacy %q %q calls %d", got, src, calls)
	}
	// The resolver returns the trimmed flag so newRecord can reject a case
	// variant. It does not fold User into user.
	if got, src := ResolveDecidedBy("User", true, boom); got != "User" || src != DecidedBySourceFlag || calls != 0 {
		t.Fatalf("explicit exact case %q %q calls %d", got, src, calls)
	}
	if got, src := ResolveDecidedBy("", false, nil); got != "" || src != "" {
		t.Fatalf("nil runner %q %q", got, src)
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
	resolveCommands := func(run CommandRunner) (string, string) {
		return resolveDecidedBy("", false, run, noUser, identityLookupTimeout)
	}

	if got, src := ResolveDecidedBy("", false, script(map[string]struct {
		out []byte
		err error
	}{
		gh: {out: []byte("paulpham157\n")},
	})); got != "paulpham157" || src != DecidedBySourceGH {
		t.Fatalf("gh config %q %q", got, src)
	}

	if got, src := ResolveDecidedBy("", false, script(map[string]struct {
		out []byte
		err error
	}{
		gh:      {err: miss},
		gitUser: {out: []byte("octocat\n")},
	})); got != "octocat" || src != DecidedBySourceGit {
		t.Fatalf("non-zero gh should fall through to git, got %q %q", got, src)
	}

	if got, src := resolveCommands(script(map[string]struct {
		out []byte
		err error
	}{
		gh:      {out: []byte("user\n")},
		gitUser: {out: []byte("agent\n")},
	})); got != "" || src != "" {
		t.Fatalf("legacy words from config stored %q %q", got, src)
	}

	if got, src := resolveCommands(script(map[string]struct {
		out []byte
		err error
	}{
		gh:      {out: []byte("User\n")},
		gitUser: {out: []byte("AGENT\n")},
	})); got != "" || src != "" {
		t.Fatalf("case-variant legacy words from config stored %q %q", got, src)
	}

	if got, src := ResolveDecidedBy("", false, script(map[string]struct {
		out []byte
		err error
	}{
		gh:      {out: []byte("user\n")},
		gitUser: {out: []byte("paulpham157\n")},
	})); got != "paulpham157" || src != DecidedBySourceGit {
		t.Fatalf("legacy gh should fall through, got %q %q", got, src)
	}

	// git config user.name is not a source, even when it is a login and
	// even when it is the legacy word that would lock the brief.
	if got, src := resolveCommands(script(map[string]struct {
		out []byte
		err error
	}{
		gh:      {err: miss},
		gitUser: {err: miss},
	})); got != "" || src != "" {
		t.Fatalf("user.name must not be read, got %q %q", got, src)
	}

	if got, src := resolveCommands(script(map[string]struct {
		out []byte
		err error
	}{
		gh:      {out: []byte("Cursor Agent\n")},
		gitUser: {out: []byte("\n")},
	})); got != "" || src != "" {
		t.Fatalf("display name stored %q %q", got, src)
	}

	// The OS deny list does not apply to gh. A hosts.yml login of ubuntu
	// is stored and can lock. The same name from the OS username is not.
	if got, src := resolveCommands(script(map[string]struct {
		out []byte
		err error
	}{
		gh: {out: []byte("ubuntu\n")},
	})); got != "ubuntu" || src != DecidedBySourceGH {
		t.Fatalf("gh ubuntu %q %q", got, src)
	}
}

func TestResolveDecidedByHungGHStillReadsGit(t *testing.T) {
	start := time.Now()
	got, src := resolveDecidedBy("", false, func(ctx context.Context, name string, _ ...string) ([]byte, error) {
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
	if got != "octocat" || src != DecidedBySourceGit {
		t.Fatalf("hung gh stored %q %q, want octocat from git", got, src)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("gh deadline not honored: %s", elapsed)
	}
}

func TestResolveDecidedByHungGitAfterFailedGH(t *testing.T) {
	start := time.Now()
	got, src := resolveDecidedBy("", false, func(ctx context.Context, name string, _ ...string) ([]byte, error) {
		if name == "gh" {
			return nil, errors.New("missing")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}, func() (string, error) {
		return "", errors.New("no os user")
	}, identityLookupTimeout)
	if got != "" || src != "" {
		t.Fatalf("hung git stored %q %q", got, src)
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
	if LocksBrief("paulpham157", DecidedBySourceOS) || LocksBrief("user", DecidedBySourceOS) {
		t.Fatal("os source locked")
	}
	for _, src := range []string{"", DecidedBySourceFlag, DecidedBySourceGH, DecidedBySourceGit} {
		if !LocksBrief("paulpham157", src) || !LocksBrief("user", src) {
			t.Errorf("source %q did not lock a login", src)
		}
		if LocksBrief("agent", src) {
			t.Errorf("source %q locked agent", src)
		}
	}
}

func TestLegacyCaseVariantRejected(t *testing.T) {
	for _, by := range []string{"User", "USER", "Agent", "AGENT"} {
		_, err := newRecord(RecordInput{
			ID: "dont-push", Statement: "Do not push.", Status: StatusDecision, DecidedBy: by,
		}, "1970-01-01T00:00:00.000000+00:00")
		if err == nil || !strings.Contains(err.Error(), "exact legacy word") {
			t.Fatalf("%q: %v", by, err)
		}
	}
	for _, by := range []string{"user", "agent", "paulpham157"} {
		rec, err := newRecord(RecordInput{
			ID: "dont-push", Statement: "Do not push.", Status: StatusDecision, DecidedBy: by, DecidedBySource: DecidedBySourceFlag,
		}, "1970-01-01T00:00:00.000000+00:00")
		if err != nil || rec.DecidedBy != by || rec.DecidedBySource != DecidedBySourceFlag {
			t.Fatalf("%q: %+v %v", by, rec, err)
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
	if got, src := resolveDecidedBy("", false, bothMiss, user, time.Second); got != "paulpham157" || src != DecidedBySourceOS || calls != 1 {
		t.Fatalf("windows user %q %q calls %d", got, src, calls)
	}

	calls = 0
	if got, src := resolveDecidedBy("", false, func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "gh" {
			t.Errorf("git ran after a hosts.yml login")
		}
		return []byte("octocat\n"), nil
	}, func() (string, error) {
		calls++
		return "paulpham157", nil
	}, time.Second); got != "octocat" || src != DecidedBySourceGH || calls != 0 {
		t.Fatalf("gh should win, got %q %q calls %d", got, src, calls)
	}

	if got, src := resolveDecidedBy("", false, nil, func() (string, error) {
		t.Fatal("nil runner consulted the os user")
		return "paulpham157", nil
	}, time.Second); got != "" || src != "" {
		t.Fatalf("nil runner %q %q", got, src)
	}

	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: `CORP\octocat`, want: "octocat"},
		{name: "  CORP\\paulpham157  ", want: "paulpham157"},
		{name: "ada", want: "ada"},
		{name: "ubuntu"},
		{name: "Ubuntu"},
		{name: "ROOT"},
		{name: "box"},
		{name: "runner"},
		{name: "ec2-user"},
		{name: "gitlab-runner"},
		{name: "www-data"},
		{name: "buildkite-agent"},
		{name: `NT AUTHORITY\SYSTEM`},
		{name: `NT AUTHORITY\LOCAL SERVICE`},
		{name: `NT AUTHORITY\NETWORK SERVICE`},
		{name: `CORP\SERVER$`},
		{name: "machine$"},
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
		got, src := resolveDecidedBy("", false, bothMiss, func() (string, error) {
			return tc.name, tc.err
		}, time.Second)
		wantSrc := ""
		if tc.want != "" {
			wantSrc = DecidedBySourceOS
		}
		if got != tc.want || src != wantSrc {
			t.Errorf("os user %q err %v = %q %q, want %q %q", tc.name, tc.err, got, src, tc.want, wantSrc)
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
