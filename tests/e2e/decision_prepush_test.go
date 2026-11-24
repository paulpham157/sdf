//go:build e2e

package e2e

import (
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrePushHookWarnsBlocksAndFailsOpen installs the hooks with the real
// `sdf init --git-hooks --db`, commits inside a Grok CLI session into a
// running sdf serve, and pushes to a local bare remote. The pre-push hook
// warns about a commit with no decision (the push succeeds), accepts an
// SDF-Decision trailer and an implements edge, blocks only when git config
// sdf.prePush is block, and lets the push through once the store is gone.
func TestPrePushHookWarnsBlocksAndFailsOpen(t *testing.T) {
	const session = "grok-prepush-e2e-7d21"
	dir := t.TempDir()
	srv := startServe(t)
	parsed, err := url.Parse(srv.httpBase)
	if err != nil {
		t.Fatal(err)
	}
	env := cleanEnv("HOME="+filepath.Join(dir, "home"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	run := func(extra []string, workdir, name string, args ...string) (int, string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Env = append(append([]string{}, env...), extra...)
		cmd.Dir = workdir
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), string(out)
		}
		if err != nil {
			t.Fatalf("%s %v: %v", name, args, err)
		}
		return 0, string(out)
	}
	must := func(extra []string, workdir, name string, args ...string) string {
		t.Helper()
		code, out := run(extra, workdir, name, args...)
		if code != 0 {
			t.Fatalf("%s %v: exit %d\n%s", name, args, code, out)
		}
		return strings.TrimSpace(out)
	}
	agent := []string{"GROK_SESSION_ID=" + session}
	repo, remote := filepath.Join(dir, "repo"), filepath.Join(dir, "remote.git")
	must(nil, dir, "git", "init", "-q", "--bare", remote)
	must(nil, dir, "git", "init", "-q", "-b", "main", repo)
	must(nil, repo, "git", "config", "user.email", "e2e@example.com")
	must(nil, repo, "git", "config", "user.name", "e2e")
	must(nil, repo, "git", "remote", "add", "origin", remote)
	installed := must(nil, repo, binPath(t), "init", "--git-hooks", "--port", parsed.Port(), "--db", srv.db)
	if !strings.Contains(installed, "pre-push reads the store "+srv.db) {
		t.Fatal(installed)
	}
	commit := func(extra []string, msg string, args ...string) string {
		t.Helper()
		must(extra, repo, "git", append([]string{"commit", "-q", "--allow-empty", "-m", msg}, args...)...)
		return must(nil, repo, "git", "rev-parse", "HEAD")
	}
	c1 := commit(agent, "Store hot attributes in columns")
	c2 := commit(agent, "Add the columns migration", "--trailer", "SDF-Decision: store-columns")
	c3 := commit(agent, "Backfill the columns")
	srv.stop(t)

	code, out := run(nil, repo, "git", "push", "origin", c1+":refs/heads/main")
	if code != 0 || !strings.Contains(out, "1 of 1 pushed commits recorded in SDF implement no decision") || !strings.Contains(out, "commit:"+c1) ||
		!strings.Contains(out, "session: "+session) || !strings.Contains(out, "The push continues (warn mode)") {
		t.Fatalf("warn: exit %d\n%s", code, out)
	}

	must(nil, dir, binPath(t), "decision", "add", "--db", srv.db, "--id", "store-columns", "--status", "decision",
		"--statement", "Store hot attributes in their own columns.")
	must(nil, repo, "git", "config", "sdf.prePush", "block")
	code, out = run(nil, repo, "git", "push", "origin", c2+":refs/heads/main")
	if code != 0 || !strings.Contains(out, "each implements a decision") {
		t.Fatalf("trailer: exit %d\n%s", code, out)
	}
	code, out = run(nil, repo, "git", "push", "origin", c3+":refs/heads/main")
	if code == 0 || !strings.Contains(out, "Push blocked") || !strings.Contains(out, "commit:"+c3) {
		t.Fatalf("block: exit %d\n%s", code, out)
	}
	must(nil, dir, binPath(t), "decision", "link", "--db", srv.db, "--relation", "implements",
		"--from", "commit:"+c3, "--to", "decision:store-columns")
	code, out = run(nil, repo, "git", "push", "origin", "main")
	if code != 0 || strings.Contains(out, "implement no decision") {
		t.Fatalf("after linking: exit %d\n%s", code, out)
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(srv.db + suffix)
	}
	c4 := commit(agent, "Tidy up")
	code, out = run(nil, repo, "git", "push", "origin", "main")
	if code != 0 || !strings.Contains(out, "pre-push decision check skipped: no SDF store at") {
		t.Fatalf("store gone: exit %d\n%s", code, out)
	}
	if head := must(nil, remote, "git", "rev-parse", "main"); head != c4 {
		t.Fatalf("remote main %s, want %s", head, c4)
	}
}
