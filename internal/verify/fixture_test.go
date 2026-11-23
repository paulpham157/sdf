package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/ledger"
	"github.com/paulpham157/sdf/internal/ledger/ledgertest"
	"github.com/paulpham157/sdf/internal/task"
)

func g(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(cleanGitEnv(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if c == "\x00delete" {
			if err := os.Remove(full); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func commit(t *testing.T, dir string, files map[string]string, msg string) string {
	t.Helper()
	write(t, dir, files)
	g(t, dir, "add", "-A")
	g(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
	return g(t, dir, "rev-parse", "HEAD")
}

// fixture is a repository where main holds the base and, one commit later,
// the declared task file. An agent branches from the base.
type fixture struct {
	repo, data string
	root, base string
	spec       task.Spec
	hash       string
	ledger     *ledger.Store
}

const featureTest = "#!/bin/sh\ngrep -q done src/feature.txt\n"

func newFixture(t *testing.T, criteria ...task.CriterionInput) *fixture {
	t.Helper()
	return newFixtureFiles(t, nil, criteria...)
}

// newFixtureFiles is newFixture with more files in the base.
func newFixtureFiles(t *testing.T, extra map[string]string, criteria ...task.CriterionInput) *fixture {
	t.Helper()
	f := &fixture{repo: t.TempDir(), data: t.TempDir(), ledger: ledgertest.New(t)}
	g(t, f.repo, "init", "-q", "-b", "main")
	f.root = commit(t, f.repo, map[string]string{"README": "r\n"}, "root")
	files := map[string]string{
		"src/feature.txt": "todo\n",
		"tests/run.sh":    featureTest,
	}
	for p, c := range extra {
		files[p] = c
	}
	f.base = commit(t, f.repo, files, "base")
	if len(criteria) == 0 {
		criteria = []task.CriterionInput{{Name: "unit", Argv: []string{"sh", "tests/run.sh"}, Designated: []string{"tests/*.sh"}, TimeoutS: 60}}
	}
	spec, err := task.Build(context.Background(), f.repo, task.Input{ID: "feat", Title: "Feature", Criteria: criteria})
	if err != nil {
		t.Fatal(err)
	}
	d, err := task.Declare(context.Background(), spec, task.Store{RepoDir: f.repo, WriteRepo: true, DataDir: f.data, Ledger: f.ledger})
	if err != nil {
		t.Fatal(err)
	}
	f.spec, f.hash = spec, d.Hash
	g(t, f.repo, "add", "-A")
	g(t, f.repo, "commit", "-q", "-m", "declare task")
	g(t, f.repo, "checkout", "-q", "-b", "agent", f.base)
	return f
}

func (f *fixture) agentCommit(t *testing.T, files map[string]string) string {
	t.Helper()
	return commit(t, f.repo, files, "agent")
}

func (f *fixture) opts(sha string) Options {
	return Options{RepoDir: f.repo, SHA: sha, TaskID: "feat", TaskRef: "main", DataDir: f.data, Ledger: f.ledger}
}

func hasCode(res Result, code string) bool {
	for _, r := range res.Reasons {
		if r.Code == code {
			return true
		}
	}
	return false
}

func kinds(res Result) string {
	var ks []string
	for _, e := range res.Evidence {
		k := e.Value["kind"].(string)
		if n := e.Value["name"].(string); n != "" {
			k += ":" + n
		}
		ks = append(ks, k)
	}
	return strings.Join(ks, " ")
}
