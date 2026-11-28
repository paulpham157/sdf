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

// TestPrePushWarnsWhenACommitTouchesADecisionPath installs the hooks, records
// a decision with a path glob, and pushes a commit that changes a matching
// file. The push warns and succeeds. After the decision is retracted, a
// later push of another matching file is silent.
func TestPrePushWarnsWhenACommitTouchesADecisionPath(t *testing.T) {
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
	repo, remote := filepath.Join(dir, "repo"), filepath.Join(dir, "remote.git")
	must(nil, dir, "git", "init", "-q", "--bare", remote)
	must(nil, dir, "git", "init", "-q", "-b", "main", repo)
	must(nil, repo, "git", "config", "user.email", "e2e@example.com")
	must(nil, repo, "git", "config", "user.name", "e2e")
	must(nil, repo, "git", "remote", "add", "origin", remote)
	must(nil, repo, binPath(t), "init", "--git-hooks", "--port", parsed.Port(), "--db", srv.db)
	must(nil, repo, "git", "commit", "-q", "--allow-empty", "-m", "root")
	must(nil, repo, "git", "push", "-q", "origin", "main")
	must(nil, dir, binPath(t), "decision", "add", "--db", srv.db, "--id", "no-cgo", "--status", "decision",
		"--decided-by", "e2e", "--statement", "Do not add cgo to the store", "--path", "**/*.go")

	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		must(nil, repo, "git", "add", "--", name)
		must(nil, repo, "git", "commit", "-q", "-m", "edit "+name)
	}
	write("a.go", "package a\n")
	code, out := run(nil, repo, "git", "push", "origin", "main")
	if code != 0 || !strings.Contains(out, "touches paths scoped by 1 active decision") || !strings.Contains(out, "decision:no-cgo (locked)") || !strings.Contains(out, "matches a.go") || !strings.Contains(out, "The push continues.") {
		t.Fatalf("warn: exit %d\n%s", code, out)
	}
	must(nil, dir, binPath(t), "decision", "retract", "--db", srv.db, "--id", "no-cgo", "--reason", "lifted")
	write("b.go", "package b\n")
	code, out = run(nil, repo, "git", "push", "origin", "main")
	if code != 0 || strings.Contains(out, "sdf:") {
		t.Fatalf("after retract: exit %d\n%s", code, out)
	}
}
