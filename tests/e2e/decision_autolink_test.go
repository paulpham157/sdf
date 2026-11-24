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

// TestGitCommitAutoLinksFromSessionAndTrailer commits through the real
// hooks into a running sdf serve. A later commit in a session that already
// implements one decision gets an implements edge (Option A). A commit that
// carries SDF-Decision: store-columns is linked even without a session
// (Option B). The earlier session commit is not linked retroactively.
func TestGitCommitAutoLinksFromSessionAndTrailer(t *testing.T) {
	const session = "grok-autolink-e2e-9a14"
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
	sh(gitEnv, dir, "git", "init", "-q", "-b", "main", repo)
	sh(gitEnv, repo, "git", "config", "user.email", "e2e@example.com")
	sh(gitEnv, repo, "git", "config", "user.name", "e2e")
	sh(gitEnv, repo, binPath(t), "init", "--git-hooks", "--port", parsed.Port())

	agent := append(append([]string{}, gitEnv...), "GROK_SESSION_ID="+session)
	sh(agent, repo, "git", "commit", "-q", "--allow-empty", "-m", "Record the session")
	first := sh(gitEnv, repo, "git", "rev-parse", "HEAD")

	sh(gitEnv, dir, binPath(t), "decision", "add", "--db", srv.db, "--id", "store-columns", "--status", "decision",
		"--statement", "Store hot attributes in their own columns.")
	sh(gitEnv, dir, binPath(t), "decision", "link", "--db", srv.db, "--relation", "implements",
		"--from", "session:"+session, "--to", "decision:store-columns")

	sh(agent, repo, "git", "commit", "-q", "--allow-empty", "-m", "Implement the columns")
	second := sh(gitEnv, repo, "git", "rev-parse", "HEAD")

	sh(gitEnv, repo, "git", "commit", "-q", "--allow-empty", "-m", "Name the decision in a trailer",
		"--trailer", "SDF-Decision: store-columns")
	third := sh(gitEnv, repo, "git", "rev-parse", "HEAD")

	srv.stop(t)

	cmd := exec.Command(binPath(t), "decision", "audit", "--db", srv.db, "--json")
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	totals := doc["totals"].(map[string]any)
	// Audit only counts a commit as linked when it also has a session, so
	// the session-less trailer commit is in commits_total but not
	// commits_linked. Its implements edge still shows in decision list.
	if totals["commits_total"] != 3.0 || totals["commits_with_session"] != 2.0 || totals["commits_linked"] != 1.0 || totals["edges_checked"] != 3.0 {
		t.Fatalf("totals %v", totals)
	}
	bySHA := map[string]map[string]any{}
	for _, row := range doc["commits"].([]any) {
		c := row.(map[string]any)
		bySHA[c["sha"].(string)] = c
	}
	if c := bySHA[first]; c == nil || c["linked"] != false || c["session_has_decision"] != true {
		t.Fatalf("first commit should stay unlinked (not retroactive): %v", bySHA[first])
	}
	if c := bySHA[second]; c == nil || c["linked"] != true {
		t.Fatalf("Option A did not link the later session commit: %v", bySHA[second])
	}
	if _, ok := bySHA[third]; ok {
		t.Fatalf("session-less trailer commit should not appear in audit commits: %v", bySHA[third])
	}

	listOut, err := exec.Command(binPath(t), "decision", "list", "--db", srv.db, "--json").Output()
	if err != nil {
		t.Fatal(err)
	}
	var graph struct {
		Edges []struct {
			Relation string `json:"relation"`
			From     struct {
				Kind string `json:"kind"`
				Ref  string `json:"ref"`
			} `json:"from"`
			To struct {
				Kind string `json:"kind"`
				Ref  string `json:"ref"`
			} `json:"to"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(listOut, &graph); err != nil {
		t.Fatalf("list not JSON: %v\n%s", err, listOut)
	}
	has := func(sha string) bool {
		for _, e := range graph.Edges {
			if e.Relation == "implements" && e.From.Kind == "commit" && e.From.Ref == sha && e.To.Kind == "decision" && e.To.Ref == "store-columns" {
				return true
			}
		}
		return false
	}
	if has(first) {
		t.Fatalf("first commit was linked retroactively: %s", first)
	}
	if !has(second) {
		t.Fatalf("Option A missing implements edge for %s in %s", second, listOut)
	}
	if !has(third) {
		t.Fatalf("Option B missing implements edge for %s in %s", third, listOut)
	}
}
