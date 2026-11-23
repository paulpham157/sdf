package cli_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/ledger"
	"github.com/paulpham157/sdf/internal/task"
)

// ledgerRepo is taskRepo with the task "acc" declared on main by --write
// --no-data-dir plus extra, and an agent commit to verify. It returns the
// repo, the commit, and the declare output.
func ledgerRepo(t *testing.T, extra ...string) (string, string, result) {
	t.Helper()
	repo := taskRepo(t)
	base := gitIn(t, repo, "rev-parse", "HEAD")
	args := append([]string{"task", "declare", "--repo", repo, "--write", "--no-data-dir", "--id", "acc", "--title", "Accept",
		"--check", "accept=sh scripts/accept.sh", "--designated", "accept=scripts/*.sh"}, extra...)
	d := run(t, args...)
	if d.code != 0 {
		t.Fatalf("declare: %d %q", d.code, d.stderr)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "declare")
	gitIn(t, repo, "checkout", "-q", "-b", "agent", base)
	if err := os.WriteFile(filepath.Join(repo, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "work")
	return repo, gitIn(t, repo, "rev-parse", "HEAD"), d
}

// isolate points every default location sdf could use at fresh temporary
// directories and clears $SDF_LEDGER. It returns those directories.
func isolate(t *testing.T) []string {
	t.Helper()
	home, data := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("SDF_LEDGER", "")
	return []string{home, data}
}

func findLedgerFiles(t *testing.T, dirs ...string) []string {
	t.Helper()
	var found []string
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && strings.HasPrefix(d.Name(), "ledger.db") {
				found = append(found, p)
			}
			return nil
		})
	}
	return found
}

func TestTaskLedgerIsOffWithoutFlagOrEnv(t *testing.T) {
	dirs := isolate(t)
	repo := taskRepo(t)
	r := run(t, "task", "declare", "--repo", repo, "--write", "--id", "off", "--title", "Off",
		"--check", "accept=sh scripts/accept.sh", "--designated", "accept=scripts/*.sh")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("%d %q %q", r.code, r.stdout, r.stderr)
	}
	b, err := os.ReadFile(filepath.Join(repo, ".sdf", "tasks", "off.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := task.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	want := []string{
		"task off " + hash,
		"base " + gitIn(t, repo, "rev-parse", "HEAD"),
		"designated files 1",
		"written " + filepath.Join(repo, ".sdf", "tasks", "off.json"),
		"written " + filepath.Join(dirs[1], "sdf", "tasks", "off.json"),
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("stdout\n%s\nwant\n%s", r.stdout, strings.Join(want, "\n"))
	}
	if show := run(t, "task", "show", "off", "--repo", repo); show.code != 0 || strings.Contains(show.stdout, "ledger") {
		t.Fatalf("show: %d %q %q", show.code, show.stdout, show.stderr)
	}

	repo2, sha, _ := ledgerRepo(t)
	v := run(t, "verify", sha, "--task", "acc", "--task-ref", "main", "--repo", repo2, "--no-data-dir")
	if v.code != 0 || strings.Contains(v.stderr, "ledger") {
		t.Fatalf("verify: %d %q", v.code, v.stderr)
	}
	if found := findLedgerFiles(t, append(dirs, repo, repo2)...); len(found) != 0 {
		t.Fatalf("ledger files created without --ledger or $SDF_LEDGER: %v", found)
	}
}

func TestTaskDeclareAndVerifyAppendToTheLedger(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "state", "ledger.db")
	// declare with --ledger (a missing parent directory is created).
	repo, sha, d := ledgerRepo(t, "--ledger", path)
	if !strings.Contains(d.stdout, "written ledger\n") {
		t.Fatalf("declare: %d %q %q", d.code, d.stdout, d.stderr)
	}
	if n := ledgerEntries(t, path); n != 2 { // genesis, task
		t.Fatalf("entries after declare %d", n)
	}
	if show := run(t, "task", "show", "acc", "--repo", repo, "--no-data-dir", "--ledger="+path); show.code != 0 || !strings.Contains(show.stdout, "copy ledger\n") {
		t.Fatalf("show: %d %q %q", show.code, show.stdout, show.stderr)
	}

	// verify with $SDF_LEDGER: the task is read from the ledger and the
	// branch, and the evidence and verdict are appended.
	t.Setenv("SDF_LEDGER", path)
	v := run(t, "verify", sha, "--task", "acc", "--task-ref", "main", "--repo", repo, "--no-data-dir")
	if v.code != 0 {
		t.Fatalf("verify: %d %q", v.code, v.stderr)
	}
	var doc struct {
		Verdict struct {
			Hash string `json:"hash"`
		} `json:"verdict"`
		Evidence []struct {
			Hash string `json:"hash"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(v.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if n := ledgerEntries(t, path); n != 2+len(doc.Evidence)+1 {
		t.Fatalf("entries after verify %d, evidence %d", n, len(doc.Evidence))
	}
	s, err := ledger.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	head, err := s.Head(context.Background())
	if err != nil || head.Kind != task.KindVerdict || head.Hash != doc.Verdict.Hash {
		t.Fatalf("head %+v (%v), verdict %s", head, err, doc.Verdict.Hash)
	}
	if !strings.Contains(v.stdout, `"where": "ledger"`) {
		t.Fatalf("verdict does not list the ledger copy as a task source: %s", v.stdout)
	}

	if a := run(t, "audit", "verify", "--ledger", path, "--require-nonempty"); a.code != 0 {
		t.Fatalf("audit verify: %d %q %q", a.code, a.stdout, a.stderr)
	}
}

func TestTaskLedgerFailuresStopBeforeWriting(t *testing.T) {
	dirs := isolate(t)
	repo, sha, _ := ledgerRepo(t)
	notLedger := filepath.Join(t.TempDir(), "ledger.db")
	junk := []byte("hand-written test file, not a sqlite database\n")
	if err := os.WriteFile(notLedger, junk, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"not a ledger": notLedger,
		"a directory":  t.TempDir(),
	}
	if os.Geteuid() != 0 {
		ro := filepath.Join(t.TempDir(), "ro")
		if err := os.Mkdir(ro, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
		cases["read-only directory"] = filepath.Join(ro, "ledger.db")
	}
	dataFile := filepath.Join(dirs[1], "sdf", "tasks", "new.json")
	for name, path := range cases {
		d := run(t, "task", "declare", "--repo", repo, "--ledger", path, "--id", "new", "--title", "New",
			"--check", "accept=sh scripts/accept.sh", "--designated", "accept=scripts/*.sh")
		if d.code != 1 || d.stdout != "" || !strings.Contains(d.stderr, "sdf task declare: ledger ") {
			t.Errorf("%s: declare %d %q %q", name, d.code, d.stdout, d.stderr)
		}
		if _, err := os.Stat(dataFile); !os.IsNotExist(err) {
			t.Errorf("%s: data dir copy written although the ledger failed (%v)", name, err)
		}
		t.Setenv("SDF_LEDGER", path)
		v := run(t, "verify", sha, "--task", "acc", "--task-ref", "main", "--repo", repo, "--no-data-dir")
		t.Setenv("SDF_LEDGER", "")
		if v.code != 3 || v.stdout != "" || !strings.Contains(v.stderr, "sdf verify: ledger ") {
			t.Errorf("%s: verify %d %q %q", name, v.code, v.stdout, v.stderr)
		}
	}
	if b, _ := os.ReadFile(notLedger); string(b) != string(junk) {
		t.Error("a file that is not a ledger was changed")
	}
	for _, args := range [][]string{
		{"task", "declare", "--repo", repo, "--ledger=", "--id", "x", "--title", "t", "--check", "a=true"},
		{"task", "declare", "--repo", repo, "--ledger"},
		{"task", "show", "acc", "--ledger="},
		{"verify", sha, "--task", "acc", "--ledger="},
		{"verify", sha, "--task", "acc", "--ledger"},
	} {
		if r := run(t, args...); r.code != 2 {
			t.Errorf("%v: %d %q", args, r.code, r.stderr)
		}
	}
}

func TestTaskShowNeverCreatesTheLedger(t *testing.T) {
	isolate(t)
	repo, _, _ := ledgerRepo(t)
	gitIn(t, repo, "checkout", "-q", "main")
	path := filepath.Join(t.TempDir(), "ledger.db")
	// A missing ledger reads as empty: the repo copy is found and the
	// ledger file is not created.
	if r := run(t, "task", "show", "acc", "--repo", repo, "--ledger", path); r.code != 0 || strings.Contains(r.stdout, "copy ledger") {
		t.Fatalf("show: %d %q %q", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("show created the ledger: %v", err)
	}
}
