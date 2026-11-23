package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/task"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestVerifyPassAndFailExitCodes(t *testing.T) {
	repo := taskRepo(t) // main: go.mod, pkg/a/a_test.go, scripts/accept.sh
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	base := gitIn(t, repo, "rev-parse", "HEAD")
	if r := run(t, "task", "declare", "--repo", repo, "--write", "--id", "acc", "--title", "Accept",
		"--check", "accept=sh scripts/accept.sh", "--designated", "accept=scripts/*.sh"); r.code != 0 {
		t.Fatalf("declare: %d %q", r.code, r.stderr)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "declare")
	gitIn(t, repo, "checkout", "-q", "-b", "agent", base)
	if err := os.WriteFile(filepath.Join(repo, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "work")
	good := gitIn(t, repo, "rev-parse", "HEAD")

	out := filepath.Join(t.TempDir(), "verdict.json")
	r := run(t, "verify", good, "--task", "acc", "--task-ref", "main", "--repo", repo, "--out", out)
	if r.code != 0 || !strings.Contains(r.stderr, "verdict PASS sha256:") {
		t.Fatalf("pass: %d %q %q", r.code, r.stdout, r.stderr)
	}
	if b, _ := os.ReadFile(out); string(b) != r.stdout {
		t.Fatal("--out differs from stdout")
	}
	var doc struct {
		Status  string `json:"status"`
		Verdict struct {
			Kind   string          `json:"kind"`
			Hash   string          `json:"hash"`
			Record json.RawMessage `json:"record"`
		} `json:"verdict"`
		Evidence []json.RawMessage `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Status != "PASS" || doc.Verdict.Kind != "sdf.verdict/v1" || len(doc.Evidence) != 6 {
		t.Fatalf("%+v", doc)
	}
	// Re-canonicalizing the printed record gives back the hashed bytes.
	var rec map[string]any
	dec := json.NewDecoder(strings.NewReader(string(doc.Verdict.Record)))
	dec.UseNumber()
	if err := dec.Decode(&rec); err != nil {
		t.Fatal(err)
	}
	c, err := task.Canonical(fromJSON(rec))
	if err != nil {
		t.Fatal(err)
	}
	if task.PayloadHash(task.KindVerdict, c) != doc.Verdict.Hash {
		t.Fatal("printed verdict does not re-hash")
	}

	if err := os.WriteFile(filepath.Join(repo, "scripts", "accept.sh"), []byte("exit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "commit", "-q", "-am", "weaken the check")
	bad := gitIn(t, repo, "rev-parse", "HEAD")
	r = run(t, "verify", bad, "--task", "acc", "--task-ref", "main", "--repo", repo)
	if r.code != 1 || !strings.Contains(r.stderr, "DESIGNATED_FILE_MODIFIED") || !strings.Contains(r.stdout, `"status": "FAIL"`) {
		t.Fatalf("fail: %d %q", r.code, r.stderr)
	}

	// B4: without --task-ref or --task-hash, the data dir copy (which the
	// agent can write) is not enough for PASS.
	r = run(t, "verify", good, "--task", "acc", "--repo", repo)
	if r.code != 3 || !strings.Contains(r.stderr, "TASK_UNTRUSTED") || !strings.Contains(r.stdout, `"status": "ERROR"`) {
		t.Fatalf("untrusted: %d %q", r.code, r.stderr)
	}
	var pinned struct {
		Verdict struct {
			Record struct {
				TaskHash string `json:"task_hash"`
			} `json:"record"`
		} `json:"verdict"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &pinned); err != nil {
		t.Fatal(err)
	}
	r = run(t, "verify", good, "--task", "acc", "--repo", repo, "--task-hash", pinned.Verdict.Record.TaskHash)
	if r.code != 0 {
		t.Fatalf("pinned hash: %d %q", r.code, r.stderr)
	}

	for _, args := range [][]string{
		{"verify"},
		{"verify", good, "--task", "acc", "--repo", repo, "--task-hash", "abc"},
		{"verify", good},
		{"verify", "abc", "--task", "acc", "--repo", repo},
		{"verify", good, "--task", "acc", "--bogus"},
	} {
		if r := run(t, args...); r.code != 2 {
			t.Errorf("%v: %d %q", args, r.code, r.stderr)
		}
	}
}

// fromJSON turns decoded JSON (with json.Number) into the canonical subset.
func fromJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = fromJSON(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = fromJSON(e)
		}
		return x
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			panic(err)
		}
		return n
	default:
		return v
	}
}
