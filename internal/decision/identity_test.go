package decision

import (
	"errors"
	"strings"
	"testing"
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
	out, err := ExecCommand("echo", "hello")
	if err != nil || strings.TrimSpace(string(out)) != "hello" {
		t.Fatalf("echo %q %v", out, err)
	}
	if _, err := ExecCommand("sdf-identity-missing-binary"); err == nil {
		t.Fatal("missing binary succeeded")
	}
}

func TestResolveDecidedBy(t *testing.T) {
	calls := 0
	boom := CommandRunner(func(string, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("should not run")
	})
	if got := ResolveDecidedBy("  paulpham157  ", true, boom); got != "paulpham157" || calls != 0 {
		t.Fatalf("explicit %q calls %d", got, calls)
	}
	if got := ResolveDecidedBy("  ", true, boom); got != "" || calls != 0 {
		t.Fatalf("explicit empty %q calls %d", got, calls)
	}
	if got := ResolveDecidedBy("", false, nil); got != "" {
		t.Fatalf("nil runner %q", got)
	}

	script := func(responses map[string]struct {
		out []byte
		err error
	}) CommandRunner {
		return func(name string, args ...string) ([]byte, error) {
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

	if got := ResolveDecidedBy("", false, script(map[string]struct {
		out []byte
		err error
	}{
		"gh api user": {out: []byte("{\"login\":\"paulpham157\",\"id\":1}\n")},
	})); got != "paulpham157" {
		t.Fatalf("gh %q", got)
	}

	if got := ResolveDecidedBy("", false, script(map[string]struct {
		out []byte
		err error
	}{
		"gh api user":                  {err: miss},
		"git config --get github.user": {out: []byte("octocat\n")},
	})); got != "octocat" {
		t.Fatalf("github.user %q", got)
	}

	if got := ResolveDecidedBy("", false, script(map[string]struct {
		out []byte
		err error
	}{
		"gh api user":                  {out: []byte("not json")},
		"git config --get github.user": {out: []byte("Cursor Agent\n")},
		"git config --get user.name":   {out: []byte("paulpham157\n")},
	})); got != "paulpham157" {
		t.Fatalf("user.name fallback %q", got)
	}

	if got := ResolveDecidedBy("", false, script(map[string]struct {
		out []byte
		err error
	}{
		"gh api user":                  {err: miss},
		"git config --get github.user": {err: miss},
		"git config --get user.name":   {out: []byte("Cursor Agent\n")},
	})); got != "" {
		t.Fatalf("display name stored %q", got)
	}

	if got := ResolveDecidedBy("", false, script(map[string]struct {
		out []byte
		err error
	}{
		"gh api user":                  {out: []byte("{\"login\":\"\"}")},
		"git config --get github.user": {out: []byte("\n")},
		"git config --get user.name":   {err: miss},
	})); got != "" {
		t.Fatalf("all empty %q", got)
	}
}
