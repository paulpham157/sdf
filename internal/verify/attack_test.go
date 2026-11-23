package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/task"
)

// Regression tests for the attacks found in review of #193 (each one made
// an unearned PASS before its fix).

// goBase is a Go module whose test, TestAdd, fails unless Add is written.
var goBase = map[string]string{
	"go.mod":              "module example.com/m\n\ngo 1.21\n",
	"a/a.go":              "package a\n\nfunc Add(x, y int) int { return 0 }\n",
	"a/a_test.go":         "package a\n\nimport (\n\t\"os\"\n\t\"strings\"\n\t\"testing\"\n)\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add\")\n\t}\n\tb, _ := os.ReadFile(\"testdata/want.txt\")\n\tif strings.TrimSpace(string(b)) != \"five\" {\n\t\tt.Fatal(\"testdata\")\n\t}\n}\n",
	"a/testdata/want.txt": "five\n",
}

const goodAdd = "package a\n\nfunc Add(x, y int) int { return x + y }\n"

func newGoFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	// No designated glob: Build pins a/a_test.go, a/testdata and go.mod.
	return newFixtureFiles(t, goBase, task.CriterionInput{Name: "unit", Argv: []string{"go", "test", "./a/"}, TimeoutS: 300})
}

// goOpts shares one Go build cache across these tests to keep them fast.
// The verifier's default is a new one per run.
func goOpts(t *testing.T, f *fixture, sha string) Options {
	o := f.opts(sha)
	o.GoCache = goCache
	return o
}

var goCache = func() string {
	d, err := os.MkdirTemp("", "sdf-verify-test-gocache-")
	if err != nil {
		panic(err)
	}
	return d
}()

func TestMain(m *testing.M) {
	code := m.Run()
	_ = removeTree(goCache)
	os.Exit(code)
}

func TestGoHonestPass(t *testing.T) {
	f := newGoFixture(t)
	sha := f.agentCommit(t, map[string]string{"a/a.go": goodAdd})
	o := goOpts(t, f, sha)
	o.ArtifactsDir = t.TempDir()
	res := mustRun(t, o)
	if res.Status != StatusPass {
		ents, _ := os.ReadDir(o.ArtifactsDir)
		for _, e := range ents {
			b, _ := os.ReadFile(filepath.Join(o.ArtifactsDir, e.Name()))
			t.Logf("output %s:\n%s", e.Name(), b)
		}
		t.Logf("isolation %v", res.Verdict.Value["isolation"])
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
	crit := res.Evidence[len(res.Evidence)-1].Value
	if got := crit["designated_tests"].([]string); len(got) != 1 || got[0] != "example.com/m/a TestAdd" {
		t.Fatalf("%v", got)
	}
	if a := strings.Join(crit["run_argv"].([]string), " "); !strings.HasPrefix(a, "go test -count=1 -overlay=") || !strings.HasSuffix(a, " ./a/") {
		t.Fatalf("%v", crit["run_argv"])
	}
}

// B1: a new _test.go with TestMain exiting 0 in a tested package.
func TestB1NewTestMainIsADesignatedFileAdded(t *testing.T) {
	f := newGoFixture(t)
	sha := f.agentCommit(t, map[string]string{
		"a/main_test.go": "package a\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestMain(m *testing.M) {\n\tfmt.Println(\"PASS\")\n\tos.Exit(0)\n}\n",
	})
	res := mustRun(t, goOpts(t, f, sha))
	if res.Status != StatusFail || !hasCode(res, CodeDesignatedFileAdded) || strings.Contains(kinds(res), "criterion:") {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
}

// B1: the same skip from code under test (init exits 0 in the test binary)
// is caught by requiring each designated test to report pass.
func TestB1InitExitIsCaughtByTestJSON(t *testing.T) {
	f := newGoFixture(t)
	sha := f.agentCommit(t, map[string]string{
		"a/a.go": "package a\n\nimport (\n\t\"os\"\n\t\"strings\"\n)\n\nfunc init() {\n\tif strings.HasSuffix(os.Args[0], \".test\") {\n\t\tos.Exit(0)\n\t}\n}\n\nfunc Add(x, y int) int { return 0 }\n",
	})
	res := mustRun(t, goOpts(t, f, sha))
	if res.Status != StatusFail || !hasCode(res, CodeDesignatedTestMissing) {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
}

// B1: go.work and changed testdata are Go test inputs too.
func TestB1GoWorkAndTestdata(t *testing.T) {
	f := newGoFixture(t)
	sha := f.agentCommit(t, map[string]string{"a/a.go": goodAdd, "go.work": "go 1.21\n\nuse .\n"})
	if res := mustRun(t, goOpts(t, f, sha)); res.Status != StatusFail || !hasCode(res, CodeDesignatedFileAdded) {
		t.Fatalf("go.work: %s %+v", res.Status, res.Reasons)
	}
	g(t, f.repo, "checkout", "-q", "-B", "agent2", f.base)
	sha = f.agentCommit(t, map[string]string{"a/a.go": goodAdd, "a/testdata/more.txt": "x\n"})
	if res := mustRun(t, goOpts(t, f, sha)); res.Status != StatusFail || !hasCode(res, CodeDesignatedFileAdded) {
		t.Fatalf("testdata: %s %+v", res.Status, res.Reasons)
	}
}

// B2: one check rewrites the script another runs and hides it from git's
// index with skip-worktree. The disk check before and after each check
// does not read the index.
func TestB2CheckRewritingAnotherChecksScript(t *testing.T) {
	f := newFixture(t,
		task.CriterionInput{Name: "a-prep", Argv: []string{"sh", "-c", "sh src/prep.sh"}, Designated: []string{"tests/*.sh"}},
		task.CriterionInput{Name: "b-accept", Argv: []string{"sh", "tests/run.sh"}, Designated: []string{"tests/*.sh"}},
	)
	// The feature is not done: tests/run.sh would fail.
	sha := f.agentCommit(t, map[string]string{"src/prep.sh": `test -e .git && echo ".git is in the checkout" >&2
printf 'exit 0\n' > tests/run.sh
chmod -R u+w ../git 2>/dev/null
GIT_DIR=../git GIT_WORK_TREE=. git update-index --skip-worktree tests/run.sh 2>/dev/null
exit 0
`})
	res := mustRun(t, f.opts(sha))
	if res.Status != StatusFail || !hasCode(res, CodeCheckoutModified) {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
	b := res.Evidence[len(res.Evidence)-1].Value
	if b["name"] != "b-accept" || b["exit_code"] != nil || !strings.Contains(strings.Join(b["checkout_before"].([]string), " "), "tests/run.sh: content changed") {
		t.Fatalf("b-accept ran on a changed checkout: %v", b)
	}
}

// B2: a check sees no .git in the checkout.
func TestB2ChecksRunWithoutGitDir(t *testing.T) {
	f := newFixture(t, task.CriterionInput{Name: "unit", Argv: []string{"sh", "-c", "test ! -e .git && sh tests/run.sh"}, Designated: []string{"tests/*.sh"}})
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"})
	if res := mustRun(t, f.opts(sha)); res.Status != StatusPass {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
}

// B3: `go env -w GOFLAGS=-exec=fake` in the real HOME, and GOFLAGS in the
// verifier's environment, must not reach a check.
func TestB3RealHomeGoEnvDoesNotReachChecks(t *testing.T) {
	f := newGoFixture(t)
	sha := f.agentCommit(t, map[string]string{"a/a_impl.go": "package a\n"}) // Add still wrong
	home := t.TempDir()
	fake := filepath.Join(home, "fakeexec")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho PASS\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(home, ".config", "go"), filepath.Join(home, "xdg", "go")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "env"), []byte("GOFLAGS=-exec="+fake+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("GOENV", filepath.Join(home, ".config", "go", "env"))
	t.Setenv("GOFLAGS", "-exec="+fake)
	res := mustRun(t, goOpts(t, f, sha))
	if res.Status != StatusFail || !hasCode(res, CodeCheckFailed) {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
}

func TestB3CheckEnvIsPinned(t *testing.T) {
	t.Setenv("HOME", "/real/home")
	t.Setenv("GOFLAGS", "-exec=x")
	t.Setenv("XDG_CONFIG_HOME", "/real/xdg")
	t.Setenv("GOPATH", "/real/gopath")
	d := checkDirs{home: "/run/home", gopath: "/run/gopath", gocache: "/run/gocache", gomodcache: "/run/mod"}
	env, _, _ := checkEnv("/bin", d)
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	want := map[string]string{
		"HOME": "/run/home", "GOFLAGS": "", "GOENV": "off", "GOTOOLCHAIN": "local",
		"GOPATH": "/run/gopath", "GOCACHE": "/run/gocache", "GOMODCACHE": "/run/mod",
		"XDG_CONFIG_HOME": "/run/home/.config", "PATH": "/bin",
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("%s=%q, want %q", k, g, v)
		}
	}
}

// B4: a task found only in the data dir (agent-writable) is never PASS.
func TestB4DataDirAloneIsUntrusted(t *testing.T) {
	f := newFixture(t)
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"})
	o := f.opts(sha)
	o.TaskRef, o.Ledger = "", nil
	res := mustRun(t, o)
	if res.Status != StatusError || !hasCode(res, CodeTaskUntrusted) || !strings.Contains(kinds(res), "criterion:unit") {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}

	o.TaskHash = f.hash
	if res := mustRun(t, o); res.Status != StatusPass {
		t.Fatalf("pinned: %s %+v", res.Status, res.Reasons)
	}
	o.TaskHash = "sha256:" + strings.Repeat("0", 64)
	if res := mustRun(t, o); res.Status != StatusFail || !hasCode(res, CodeTaskHashMismatch) {
		t.Fatalf("wrong pin: %s %+v", res.Status, res.Reasons)
	}
	o.TaskHash = "nope"
	if _, err := Run(context.Background(), o); err == nil {
		t.Fatal("bad --task-hash accepted")
	}
}

func TestNewSymlinkOutsideTheCheckoutFails(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(filepath.Join(f.repo, "src/feature.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(f.repo, "src/feature.txt")); err != nil {
		t.Fatal(err)
	}
	sha := commit(t, f.repo, nil, "link")
	res := mustRun(t, f.opts(sha))
	if res.Status != StatusFail || !hasCode(res, CodeSymlinkOutside) || strings.Contains(kinds(res), "criterion:") {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
}

func TestSecretScanCoversHistoryMessagesAttributesAndBinaries(t *testing.T) {
	key := "ghp_" + strings.Repeat("a1B2", 9)
	cases := []struct {
		name string
		do   func(f *fixture) string
	}{
		{"added then removed", func(f *fixture) string {
			f.agentCommit(t, map[string]string{"k.txt": key + "\n"})
			return f.agentCommit(t, map[string]string{"k.txt": "\x00delete", "src/feature.txt": "done\n"})
		}},
		{"commit message", func(f *fixture) string {
			return commit(t, f.repo, map[string]string{"src/feature.txt": "done\n"}, "leak "+key)
		}},
		{"-diff attribute", func(f *fixture) string {
			return f.agentCommit(t, map[string]string{".gitattributes": "*.txt -diff\n", "k.txt": key + "\n", "src/feature.txt": "done\n"})
		}},
		{"binary", func(f *fixture) string {
			return f.agentCommit(t, map[string]string{"k.bin": "\x00\x01" + key + "\x00", "src/feature.txt": "done\n"})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			res := mustRun(t, f.opts(c.do(f)))
			if res.Status != StatusFail || !hasCode(res, CodeSecretInDiff) {
				t.Fatalf("%s %+v", res.Status, res.Reasons)
			}
			if c.name == "binary" {
				var scan map[string]any
				for _, e := range res.Evidence {
					if e.Value["kind"] == "secret_scan" {
						scan = e.Value
					}
				}
				if b := scan["binary_scanned"].([]string); len(b) != 1 || b[0] != "k.bin" {
					t.Fatalf("%v", b)
				}
			}
		})
	}
}

// atkPlant is Review's round-5 plant payload (plant_payload.go.txt): an
// untracked a/zz_plant.go that finds the hook's send function with
// runtime.FuncForPC and calls it through unsafe to report TestAdd passed.
const atkPlant = "package a\n\nimport (\n\t\"reflect\"\n\t\"regexp\"\n\t\"runtime\"\n\t\"syscall\"\n\t\"testing\"\n\t\"unsafe\"\n)\n\n" +
	"var sdfSend = regexp.MustCompile(`^testing\\.sdf[0-9a-f]+_send$`)\n\n" +
	"func init() {\n\tif !testing.Testing() {\n\t\treturn\n\t}\n\tgo func() {}()\n\tbase := reflect.ValueOf((*testing.T).Run).Pointer()\n\tvar send uintptr\n" +
	"\tfor pc := base - 4<<20; pc < base+4<<20 && send == 0; pc += 4 {\n\t\tif f := runtime.FuncForPC(pc); f != nil && sdfSend.MatchString(f.Name()) {\n\t\t\tsend = f.Entry()\n\t\t}\n\t}\n" +
	"\tif send == 0 {\n\t\treturn\n\t}\n\tfv := &send\n\tcall := *(*func(string))(unsafe.Pointer(&fv))\n\tcall(\"pass example.com/m/a TestAdd\")\n\tsyscall.Exit(0)\n}\n"

// plantFixture is the go fixture plus package b; its criteria are given.
func plantFixture(t *testing.T, criteria ...task.CriterionInput) *fixture {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	base := map[string]string{
		"b/b.go":      "package b\n\nfunc One() int { return 1 }\n",
		"b/b_test.go": "package b\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n\tif One() != 1 {\n\t\tt.Fatal(\"x\")\n\t}\n}\n",
	}
	for k, v := range goBase {
		base[k] = v
	}
	return newFixtureFiles(t, base, criteria...)
}

// plantCommit adds b/plant.go, committed and harmless-looking (no tripwire
// finding), whose init writes ../a/zz_plant.go when b's tests run.
func plantCommit(t *testing.T, f *fixture) string {
	return f.agentCommit(t, map[string]string{"b/plant.go": "package b\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nconst payload = " + strconv.Quote(atkPlant) +
		"\n\nfunc init() {\n\tif !testing.Testing() {\n\t\treturn\n\t}\n\tos.WriteFile(\"../a/zz_plant.go\", []byte(payload), 0o644)\n}\n"})
}

// atk_plant (round 5): criterion a-pre (go test ./b/) writes an untracked
// a/zz_plant.go; a-unit (go test ./a/) would compile it and forge a pass.
// At 48828e3 that was a clean PASS. Now:
//   - isolated: a go test criterion's checkout is read-only, so the plant
//     is never written, and a-unit fails honestly;
//   - isolated with --writable-checkout a-pre, or unisolated: the plant is
//     CHECK_ADDED_CODE and a-unit does not run (and would start from a fresh
//     checkout anyway).
func TestPlantedCodeDoesNotReachALaterCriterion(t *testing.T) {
	for _, mode := range []string{"isolated", "isolated-writable", "unisolated"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "unisolated" {
				old := isolationAvailable
				isolationAvailable = func() (bool, string) { return false, "test" }
				defer func() { isolationAvailable = old }()
			} else {
				requireIsolation(t)
			}
			f := plantFixture(t,
				task.CriterionInput{Name: "a-pre", Argv: []string{"go", "test", "./b/"}, TimeoutS: 300},
				task.CriterionInput{Name: "a-unit", Argv: []string{"go", "test", "./a/"}, TimeoutS: 300},
			)
			sha := plantCommit(t, f)
			o := goOpts(t, f, sha)
			o.AllowUnisolated = mode == "unisolated"
			if mode == "isolated-writable" {
				o.WritableCheckout = []string{"a-pre"}
			}
			res := mustRun(t, o)
			if res.Status != StatusFail || hasCode(res, CodeTamperCapable) {
				t.Fatalf("%s %+v", res.Status, res.Reasons)
			}
			var unitRan bool
			for _, e := range res.Evidence {
				if e.Value["name"] == "a-unit" && e.Value["exit_code"] != nil {
					unitRan = true
				}
			}
			if mode == "isolated" {
				if hasCode(res, CodeCheckAddedCode) || !unitRan || !(hasCode(res, CodeCheckFailed) || hasCode(res, CodeDesignatedTestMissing)) {
					t.Fatalf("read-only checkout: %s %+v", res.Status, res.Reasons)
				}
				return
			}
			if !hasCode(res, CodeCheckAddedCode) || !strings.Contains(strings.Join(reasonDetails(res), " "), "a/zz_plant.go: code added") || unitRan {
				t.Fatalf("%s %+v (a-unit ran: %v)", res.Status, res.Reasons, unitRan)
			}
		})
	}
}

// Within one go test run, a test binary that runs first cannot plant code
// for a package built later: the checkout is read-only for go test
// criteria (isolated). With --writable-checkout the write succeeds and the
// file is CHECK_ADDED_CODE after the run.
func TestPlantWithinOneGoTestRun(t *testing.T) {
	requireIsolation(t)
	f := plantFixture(t, task.CriterionInput{Name: "unit", Argv: []string{"go", "test", "-p", "1", "./b/", "./a/"}, TimeoutS: 300})
	sha := plantCommit(t, f)
	res := mustRun(t, goOpts(t, f, sha))
	if res.Status != StatusFail || hasCode(res, CodeCheckAddedCode) {
		t.Fatalf("read-only checkout: %s %+v", res.Status, res.Reasons)
	}
	crit := res.Evidence[len(res.Evidence)-1].Value
	if crit["checkout_writable"] != false {
		t.Fatalf("checkout_writable %v", crit["checkout_writable"])
	}
	o := goOpts(t, f, sha)
	o.WritableCheckout = []string{"unit"}
	res = mustRun(t, o)
	if res.Status != StatusFail || !hasCode(res, CodeCheckAddedCode) {
		t.Fatalf("--writable-checkout: %s %+v", res.Status, res.Reasons)
	}
}
