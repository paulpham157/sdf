package decision

import (
	"context"
	"errors"
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

	if got := ResolveDecidedBy("", false, script(map[string]struct {
		out []byte
		err error
	}{
		gh:      {out: []byte("user\n")},
		gitUser: {out: []byte("agent\n")},
	})); got != "" {
		t.Fatalf("legacy words from config stored %q", got)
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
	if got := ResolveDecidedBy("", false, script(map[string]struct {
		out []byte
		err error
	}{
		gh:      {err: miss},
		gitUser: {err: miss},
	})); got != "" {
		t.Fatalf("user.name must not be read, got %q", got)
	}

	if got := ResolveDecidedBy("", false, script(map[string]struct {
		out []byte
		err error
	}{
		gh:      {out: []byte("Cursor Agent\n")},
		gitUser: {out: []byte("\n")},
	})); got != "" {
		t.Fatalf("display name stored %q", got)
	}
}

func TestResolveDecidedByHangingCommand(t *testing.T) {
	start := time.Now()
	calls := 0
	got := resolveDecidedBy("", false, func(ctx context.Context, name string, _ ...string) ([]byte, error) {
		calls++
		if name == "gh" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			t.Error("git ran with a live deadline after gh hung")
		}
		return nil, errors.New("live")
	}, 150*time.Millisecond)
	if got != "" {
		t.Fatalf("hung lookup stored %q", got)
	}
	if calls < 1 {
		t.Fatal("no lookup")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout not honored: %s", elapsed)
	}
}
