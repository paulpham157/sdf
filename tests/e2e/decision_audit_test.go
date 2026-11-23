//go:build e2e

package e2e

import (
	"encoding/json"
	"errors"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDecisionAuditFindsAHookCommitUntilItsSessionIsLinked commits through
// the real git hooks from `sdf init --git-hooks` into a running sdf serve,
// once inside a Grok CLI session and once outside any, then checks that
// sdf decision audit reports the session's commit (exit 1) until the session
// is linked to a decision (exit 0), and never reports the commit with no
// session.
func TestDecisionAuditFindsAHookCommitUntilItsSessionIsLinked(t *testing.T) {
	const session = "grok-audit-e2e-41c7"
	dir := t.TempDir()
	srv := startServe(t)
	parsed, err := url.Parse(srv.httpBase)
	if err != nil {
		t.Fatal(err)
	}
	gitEnv := cleanEnv("HOME="+filepath.Join(dir, "home"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	sh := func(env []string, workdir, name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Env = env
		cmd.Dir = workdir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	repo := filepath.Join(dir, "repo")
	sh(gitEnv, dir, "git", "init", "-q", repo)
	sh(gitEnv, repo, "git", "config", "user.email", "e2e@example.com")
	sh(gitEnv, repo, "git", "config", "user.name", "e2e")
	sh(gitEnv, repo, binPath(t), "init", "--git-hooks", "--port", parsed.Port())
	sh(append(append([]string{}, gitEnv...), "GROK_SESSION_ID="+session), repo, "git", "commit", "-q", "--allow-empty", "-m", "Store hot attributes in columns")
	inSession := sh(gitEnv, repo, "git", "rev-parse", "HEAD")
	sh(gitEnv, repo, "git", "commit", "-q", "--allow-empty", "-m", "Bump the README")
	srv.stop(t)
	db := srv.db

	sh(gitEnv, dir, binPath(t), "decision", "add", "--db", db, "--id", "store-columns", "--status", "decision",
		"--statement", "Store hot attributes in their own columns.")

	audit := func() (int, map[string]any) {
		t.Helper()
		cmd := exec.Command(binPath(t), "decision", "audit", "--db", db, "--json")
		out, err := cmd.Output()
		code := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out)
		}
		return code, doc
	}
	code, doc := audit()
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%v", code, doc)
	}
	totals := doc["totals"].(map[string]any)
	if totals["commits_total"] != 1.0 || totals["commits_with_session"] != 1.0 || totals["commits_linked"] != 0.0 {
		t.Fatalf("totals %v", totals)
	}
	commits := doc["findings"].(map[string]any)["unlinked_session_commits"].([]any)
	if len(commits) != 1 {
		t.Fatalf("unlinked_session_commits %v", commits)
	}
	c := commits[0].(map[string]any)
	if c["sha"] != inSession || c["session_id"] != session || c["source"] != "event" || c["event"] != "sdf.git.commit" || c["repo"] != repo {
		t.Fatalf("finding %v (want sha %s)", c, inSession)
	}

	sh(gitEnv, dir, binPath(t), "decision", "link", "--db", db, "--relation", "implements",
		"--from", "session:"+session, "--to", "decision:store-columns")
	code, doc = audit()
	if code != 0 || doc["finding_count"] != 0.0 {
		t.Fatalf("after linking: exit %d\n%v", code, doc)
	}
	if totals := doc["totals"].(map[string]any); totals["commits_linked"] != 1.0 || totals["decisions_active_with_implements"] != 1.0 {
		t.Fatalf("totals after linking %v", totals)
	}
}
