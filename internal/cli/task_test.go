package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/cli"
	"github.com/paulpham157/sdf/internal/task"
)

func taskRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":            "module x\n",
		"pkg/a/a_test.go":   "package a\n",
		"scripts/accept.sh": "#!/bin/sh\n",
	}
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "commit.gpgsign=false", "commit", "-q", "-m", "base"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestTaskDeclareAndShow(t *testing.T) {
	repo := taskRepo(t)
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	l := task.NewMemLedger(nil)
	defer cli.SetTaskLedger(l)()

	args := []string{"task", "declare", "--repo", repo, "--write",
		"--id", "add-widget", "--title", "Add the widget",
		"--check", "unit=go test ./...",
		"--designated", "unit=pkg/**/*_test.go",
		"--check", "accept=sh scripts/accept.sh",
		"--timeout", "120"}
	out := run(t, args...)
	if out.code != 0 || out.stderr != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	repoFile := filepath.Join(repo, ".sdf", "tasks", "add-widget.json")
	dataFile := filepath.Join(data, "sdf", "tasks", "add-widget.json")
	b, err := os.ReadFile(repoFile)
	if err != nil {
		t.Fatal(err)
	}
	spec, hash, err := task.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.stdout, "task add-widget "+hash+"\n") ||
		!strings.Contains(out.stdout, "designated files 2\n") ||
		!strings.Contains(out.stdout, "written "+repoFile+"\n") ||
		!strings.Contains(out.stdout, "written "+dataFile+"\n") ||
		!strings.Contains(out.stdout, "written ledger\n") {
		t.Fatalf("stdout %q", out.stdout)
	}
	if spec.Criteria[0].Name != "accept" || spec.Criteria[0].TimeoutS != 120 || len(l.Entries()) != 2 {
		t.Fatalf("%+v", spec)
	}

	again := run(t, args...)
	if again.code != 0 || strings.Contains(again.stdout, "written") {
		t.Fatalf("identical redeclare: %d %q %q", again.code, again.stdout, again.stderr)
	}

	changed := append(append([]string(nil), args...), "--objective", "something else")
	conflict := run(t, changed...)
	if conflict.code != 1 || conflict.stdout != "" || !strings.Contains(conflict.stderr, "already declared with a different hash") {
		t.Fatalf("conflict: %d %q %q", conflict.code, conflict.stdout, conflict.stderr)
	}
	if b2, _ := os.ReadFile(repoFile); string(b2) != string(b) {
		t.Fatal("repo file rewritten")
	}

	sub := run(t, "task", "show", "add-widget", "--repo", filepath.Join(repo, "pkg", "a"))
	if sub.code != 0 || !strings.Contains(sub.stdout, filepath.Join(".sdf", "tasks", "add-widget.json")+"\n") {
		t.Fatalf("show from a subdirectory: %d %q %q", sub.code, sub.stdout, sub.stderr)
	}

	show := run(t, "task", "show", "add-widget", "--repo", repo)
	if show.code != 0 || !strings.HasPrefix(show.stdout, "task add-widget "+hash+"\n") ||
		!strings.Contains(show.stdout, "file pkg/a/a_test.go ") ||
		!strings.Contains(show.stdout, "file scripts/accept.sh ") ||
		!strings.Contains(show.stdout, "copy ledger\n") {
		t.Fatalf("show: %d %q %q", show.code, show.stdout, show.stderr)
	}
	js := run(t, "task", "show", "add-widget", "--repo", repo, "--json")
	if js.code != 0 || !strings.Contains(js.stdout, `"schema": "sdf.task/v1"`) {
		t.Fatalf("show --json: %d %q %q", js.code, js.stdout, js.stderr)
	}
}

func TestTaskDeclareFromFile(t *testing.T) {
	repo := taskRepo(t)
	in := filepath.Join(t.TempDir(), "task.json")
	if err := os.WriteFile(in, []byte(`{"id":"f1","title":"From a file","criteria":[{"name":"unit","argv":["go","test","./..."],"designated":["pkg/a/a_test.go"]}],"secret_scan":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := run(t, "task", "declare", "--repo", repo, "--file", in, "--write", "--no-data-dir")
	if out.code != 0 {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	b, err := os.ReadFile(filepath.Join(repo, ".sdf", "tasks", "f1.json"))
	if err != nil {
		t.Fatal(err)
	}
	spec, _, err := task.Parse(b)
	if err != nil || spec.SecretScan || spec.Title != "From a file" {
		t.Fatalf("%+v %v", spec, err)
	}

	bad := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(bad, []byte(`{"id":"f2","title":"x","criteria":[],"surprise":1}`), 0o644)
	if r := run(t, "task", "declare", "--repo", repo, "--file", bad, "--write"); r.code == 0 {
		t.Fatal("unknown field accepted")
	}
}

func TestTaskShowStripsTerminalControls(t *testing.T) {
	repo := taskRepo(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	title := "Fix \x1b]0;owned\x07it\u202e now"
	if r := run(t, "task", "declare", "--repo", filepath.Join(repo, "pkg"), "--write", "--id", "esc", "--title", title, "--check", "a=true"); r.code != 0 {
		t.Fatalf("%d %q", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(repo, ".sdf", "tasks", "esc.json")); err != nil {
		t.Fatalf("--repo subdir did not write at the root: %v", err)
	}
	r := run(t, "task", "show", "esc", "--repo", repo)
	if r.code != 0 || strings.ContainsAny(r.stdout, "\x1b\x07\u202e") || !strings.Contains(r.stdout, "title Fix ]0;ownedit now\n") {
		t.Fatalf("%q", r.stdout)
	}
}

func TestTaskDeclareUsageErrors(t *testing.T) {
	repo := taskRepo(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, args := range [][]string{
		{"task"},
		{"task", "declare"},
		{"task", "declare", "--repo", repo, "--id", "x", "--title", "t"},
		{"task", "declare", "--repo", repo, "--id", "x", "--title", "t", "--check", "noequals"},
		{"task", "declare", "--repo", repo, "--id", "x", "--title", "t", "--check", "a=b", "--designated", "z=*"},
		{"task", "declare", "--repo", repo, "--id", "x", "--title", "t", "--check", "a=b", "--no-data-dir"},
		{"task", "show"},
	} {
		if r := run(t, args...); r.code != 2 {
			t.Errorf("%v: %d %q", args, r.code, r.stderr)
		}
	}
	for _, id := range []string{"a\nb", "a\x01", string([]byte{'a', 0xff}), "Upper"} {
		r := run(t, "task", "declare", "--repo", repo, "--id", id, "--title", "t", "--check", "a=true", "--write")
		if r.code != 1 || !strings.Contains(r.stderr, "must match") {
			t.Errorf("declare id %q: %d %q", id, r.code, r.stderr)
		}
		if r := run(t, "task", "show", id, "--repo", repo); r.code != 1 {
			t.Errorf("show id %q: %d", id, r.code)
		}
	}
	if r := run(t, "task", "show", "missing", "--repo", repo); r.code != 1 {
		t.Errorf("show missing: %d", r.code)
	}
}
