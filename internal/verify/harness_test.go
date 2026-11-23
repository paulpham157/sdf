package verify

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/task"
)

// Regression tests for Review's #209 attacks (/tmp/r209). Each made an
// unearned PASS at 2c7d398.

// B-1: code under test prints test2json's framing for a pass and exits 0,
// from init (atk_f1) or from inside the function under test (atk_f3).
func TestB1ForgedTestOutputIsNotAResult(t *testing.T) {
	forged := `os.Stdout.WriteString("\x16=== RUN   TestAdd\n\x16--- PASS: TestAdd (0.00s)\n\x16PASS\nok\n")
		syscall.Exit(0)`
	cases := map[string]string{
		"init": "package a\n\nimport (\n\t\"os\"\n\t\"strings\"\n\t\"syscall\"\n)\n\nfunc init() {\n\tif strings.HasSuffix(os.Args[0], \".test\") {\n\t\t" + forged + "\n\t}\n}\n\nfunc Add(x, y int) int { return 0 }\n",
		"Add":  "package a\n\nimport (\n\t\"os\"\n\t\"syscall\"\n)\n\nfunc Add(x, y int) int {\n\t" + forged + "\n\treturn 0\n}\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			f := newGoFixture(t)
			sha := f.agentCommit(t, map[string]string{"a/a.go": src})
			res := mustRun(t, goOpts(t, f, sha))
			if res.Status != StatusFail || !hasCode(res, CodeDesignatedTestMissing) {
				t.Fatalf("%s %+v", res.Status, res.Reasons)
			}
			for _, r := range res.Reasons {
				if r.Code == CodeDesignatedTestMissing && !strings.Contains(r.Detail, "TestAdd (package example.com/m/a)") {
					t.Fatalf("detail %q", r.Detail)
				}
			}
		})
	}
}

// harnessSocketGlob gives this test its own TMPDIR, where newHarness
// creates its socket directory (os.MkdirTemp("", "sdfh")), and returns the
// glob that matches only this run's socket, as a Go string literal for
// attack code: it works whatever $TMPDIR is, and a concurrent verifier's
// socket is not found.
func harnessSocketGlob(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "h")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	t.Setenv("TMPDIR", d)
	return strconv.Quote(filepath.Join(d, "sdfh*", "s"))
}

// B-1: code under test talks to the verifier's socket itself, which is
// refused, and refusing is a FAIL; or it starts its own test binary, which
// gets no channel (fd 3 is close-on-exec and only the launcher passes one),
// so its hook exits and nothing it could report reaches the verifier: the
// honest test passes, with one keyed test binary.
func TestB1ForgedReportsOnTheHarnessSocket(t *testing.T) {
	cases := map[string]string{
		// The socket path is compiled into the hook, but its directory can
		// be found.
		"same process": `socks, _ := filepath.Glob(SOCKGLOB)
		for _, p := range socks {
			if c, err := net.Dial("unix", p); err == nil {
				c.Write([]byte("00 1 pass example.com/m/a TestAdd\n"))
				c.Close()
			}
		}
		if len(socks) == 0 {
			panic("no socket found")
		}`,
		"child process": `if os.Getenv("SDF_ATTACK_CHILD") == "" {
			cmd := exec.Command(os.Args[0], "-test.run=^$")
			cmd.Env = append(os.Environ(), "SDF_ATTACK_CHILD=1")
			cmd.Run()
		}
		_ = net.Dial`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			body := strings.ReplaceAll(body, "SOCKGLOB", harnessSocketGlob(t))
			f := newGoFixture(t)
			src := "package a\n\nimport (\n\t\"net\"\n\t\"os\"\n\t\"os/exec\"\n\t\"path/filepath\"\n\t\"strings\"\n)\n\nvar _ = exec.Command\nvar _ = filepath.Glob\n\nfunc init() {\n\tif strings.HasSuffix(os.Args[0], \".test\") {\n\t\t" + body + "\n\t}\n}\n\n" + strings.TrimPrefix(goodAdd, "package a\n\n")
			sha := f.agentCommit(t, map[string]string{"a/a.go": src})
			res := mustRun(t, goOpts(t, f, sha))
			if name == "child process" {
				if res.Status != StatusPass || keyedBinaries(res) != 1 {
					t.Fatalf("%s %+v (keyed %d)", res.Status, res.Reasons, keyedBinaries(res))
				}
				return
			}
			if res.Status != StatusFail || !hasCode(res, CodeTestReportInvalid) {
				t.Fatalf("%s %+v", res.Status, res.Reasons)
			}
		})
	}
}

// keyedBinaries is how many test binaries got a harness channel in the
// last go test criterion.
func keyedBinaries(res Result) int {
	n := -1
	for _, e := range res.Evidence {
		if v, ok := e.Value["harness_test_binaries"]; ok {
			switch x := v.(type) {
			case int:
				n = x
			case float64:
				n = int(x)
			}
		}
	}
	return n
}

// B-2: a vendored dependency replaced by a fake (atk_vendor), or deleted.
func TestB2VendorMustMatchTheBase(t *testing.T) {
	base := map[string]string{}
	for p, c := range goBase {
		base[p] = c
	}
	base["go.mod"] = "module example.com/m\n\ngo 1.21\n\nrequire example.com/dep v0.0.0\n"
	base["vendor/modules.txt"] = "# example.com/dep v0.0.0\n## explicit\nexample.com/dep\n"
	base["vendor/example.com/dep/dep.go"] = "package dep\n\nfunc Five() int { return 5 }\n"
	for name, change := range map[string]map[string]string{
		"changed": {"vendor/example.com/dep/dep.go": "package dep\n\nfunc Five() int { return 0 }\n"},
		"added":   {"vendor/example.com/dep/extra.go": "package dep\n"},
		"deleted": {"vendor/example.com/dep/dep.go": "\x00delete"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixtureFiles(t, base, task.CriterionInput{Name: "unit", Argv: []string{"go", "test", "./a/"}, TimeoutS: 300})
			sha := f.agentCommit(t, change)
			res := mustRun(t, f.opts(sha))
			if res.Status != StatusFail || !hasCode(res, CodeDesignatedFileAdded) || strings.Contains(kinds(res), "criterion:") {
				t.Fatalf("%s %+v", res.Status, res.Reasons)
			}
		})
	}
}

// B-4: a ledger the agent made (any --ledger / $SDF_LEDGER) is not trusted
// unless pinned with ExpectPrefix.
func TestB4LedgerNeedsAPinnedPrefix(t *testing.T) {
	f := newFixture(t)
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"})
	o := f.opts(sha)
	o.TaskRef, o.DataDir = "", ""
	res := mustRun(t, o)
	if res.Status != StatusError || !hasCode(res, CodeTaskUntrusted) {
		t.Fatalf("unpinned: %s %+v", res.Status, res.Reasons)
	}

	pin := func() string {
		rep, err := f.ledger.Verify(context.Background())
		if err != nil || !rep.OK || rep.Head == nil {
			t.Fatalf("%v %+v", err, rep)
		}
		return strings.Join([]string{itoa(rep.Head.Seq), rep.Head.EntryHash}, ":")
	}
	o.ExpectPrefix = pin()
	if res := mustRun(t, o); res.Status != StatusPass {
		t.Fatalf("pinned: %s %+v", res.Status, res.Reasons)
	}
	// That run appended to the ledger, so the old pin is now behind the
	// head: not checkable, so not trusted.
	if res := mustRun(t, o); res.Status != StatusError || !hasCode(res, CodeTaskUntrusted) {
		t.Fatalf("grown: %s %+v", res.Status, res.Reasons)
	}
	p := pin()
	o.ExpectPrefix = p[:strings.Index(p, ":")+1] + "sha256:" + strings.Repeat("0", 64)
	if res := mustRun(t, o); res.Status != StatusFail || !hasCode(res, CodeTaskHashMismatch) {
		t.Fatalf("wrong pin: %s %+v", res.Status, res.Reasons)
	}
	o.ExpectPrefix = "7:nope"
	if _, err := Run(context.Background(), o); err == nil {
		t.Fatal("bad --expect-prefix accepted")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestLocalTaskRefWarns(t *testing.T) {
	f := newFixture(t)
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"})
	res := mustRun(t, f.opts(sha)) // TaskRef "main" is a local branch
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "local branch") {
		t.Fatalf("%q", res.Warnings)
	}
	o := f.opts(sha)
	o.TaskRef = g(t, f.repo, "rev-parse", "main")
	if res := mustRun(t, o); len(res.Warnings) != 0 && strings.Contains(strings.Join(res.Warnings, " "), "--task-ref") {
		t.Fatalf("%q", res.Warnings)
	}
}

func TestSecretScanCoversCommitHeadersAndPathNames(t *testing.T) {
	key := "ghp_" + strings.Repeat("a1B2", 9)
	cases := map[string]func(f *fixture) string{
		"author": func(f *fixture) string {
			write(t, f.repo, map[string]string{"src/feature.txt": "done\n"})
			g(t, f.repo, "add", "-A")
			g(t, f.repo, "-c", "user.name=x", "commit", "-q", "--author", key+" <a@x>", "-m", "x")
			return g(t, f.repo, "rev-parse", "HEAD")
		},
		"extra header": func(f *fixture) string {
			c := commit(t, f.repo, map[string]string{"src/feature.txt": "done\n"}, "x")
			obj := g(t, f.repo, "cat-file", "commit", c)
			hdr, msg, _ := strings.Cut(obj, "\n\n")
			p := filepath.Join(t.TempDir(), "obj")
			if err := os.WriteFile(p, []byte(hdr+"\nx-note "+key+"\n\n"+msg+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return g(t, f.repo, "hash-object", "-t", "commit", "-w", p)
		},
		"path name": func(f *fixture) string {
			return f.agentCommit(t, map[string]string{"src/feature.txt": "done\n", key + ".txt": "nothing\n"})
		},
	}
	for name, do := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			res := mustRun(t, f.opts(do(f)))
			if res.Status != StatusFail || !hasCode(res, CodeSecretInDiff) {
				t.Fatalf("%s %+v", res.Status, res.Reasons)
			}
			for _, r := range append(res.Evidence, res.Verdict) {
				if strings.Contains(string(r.Canonical), key) {
					t.Fatalf("%s holds the secret", r.ID)
				}
			}
			for _, r := range res.Reasons {
				if strings.Contains(r.Detail, key) {
					t.Fatalf("reason holds the secret: %q", r.Detail)
				}
			}
		})
	}
}

// go test -C on a nested module: the flags go after -C (go requires -C
// first), the module's packages are hooked and named by their own go.mod.
func TestGoTestChdirNestedModule(t *testing.T) {
	base := map[string]string{
		"go.mod":        "module example.com/root\n\ngo 1.21\n",
		"sub/go.mod":    "module example.com/sub\n\ngo 1.21\n",
		"sub/s.go":      "package sub\n\nfunc Add(x, y int) int { return 0 }\n",
		"sub/s_test.go": "package sub\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add\")\n\t}\n}\n",
	}
	f := newFixtureFiles(t, base, task.CriterionInput{Name: "unit", Argv: []string{"go", "test", "-C", "sub", "./..."}, TimeoutS: 300})
	honest := f.agentCommit(t, map[string]string{"sub/s.go": "package sub\n\nfunc Add(x, y int) int { return x + y }\n"})
	res := mustRun(t, goOpts(t, f, honest))
	if res.Status != StatusPass {
		t.Fatalf("honest: %s %+v", res.Status, res.Reasons)
	}
	crit := res.Evidence[len(res.Evidence)-1].Value
	if a := strings.Join(crit["run_argv"].([]string), " "); !strings.HasPrefix(a, "go test -C sub -count=1 -overlay=") {
		t.Fatalf("argv %s", a)
	}
	g(t, f.repo, "checkout", "-q", f.base)
	atk := f.agentCommit(t, map[string]string{"sub/hack.go": "package sub\n\nimport (\n\t\"os\"\n\t\"syscall\"\n)\n\nfunc init() {\n\tos.Stdout.WriteString(\"ok\\n\")\n\tsyscall.Exit(0)\n}\n"})
	if res := mustRun(t, goOpts(t, f, atk)); res.Status != StatusFail || !hasCode(res, CodeDesignatedTestMissing) {
		t.Fatalf("atk: %s %+v", res.Status, res.Reasons)
	}
}

// A replace directory's package is initialized after the hook (it imports
// testing through the overlay), so its init cannot take the socket first:
// its connection is the second from the process, a violation.
func TestReplaceDirInitCannotTakeTheHarnessFirst(t *testing.T) {
	base := map[string]string{}
	for p, c := range goBase {
		base[p] = c
	}
	base["go.mod"] = "module example.com/m\n\ngo 1.21\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => ./lib\n"
	base["lib/go.mod"] = "module example.com/lib\n\ngo 1.21\n"
	base["lib/lib.go"] = "package lib\n\nfunc Two() int { return 2 }\n"
	base["a/a.go"] = "package a\n\nimport \"example.com/lib\"\n\nfunc Add(x, y int) int { return 0 * lib.Two() }\n"
	f := newFixtureFiles(t, base, task.CriterionInput{Name: "unit", Argv: []string{"go", "test", "./a/"}, TimeoutS: 300})
	lib := "package lib\n\nimport (\n\t\"io\"\n\t\"net\"\n\t\"path/filepath\"\n)\n\nvar Conn net.Conn\n\nfunc init() {\n\tsocks, _ := filepath.Glob(" + harnessSocketGlob(t) + ")\n\tfor _, p := range socks {\n\t\tif c, err := net.Dial(\"unix\", p); err == nil {\n\t\t\tio.ReadFull(c, make([]byte, 32))\n\t\t\tConn = c\n\t\t}\n\t}\n}\n\nfunc Two() int { return 2 }\n"
	sha := f.agentCommit(t, map[string]string{"lib/lib.go": lib, "a/a.go": "package a\n\nimport \"example.com/lib\"\n\nfunc Add(x, y int) int { return (x + y) * lib.Two() / 2 }\n"})
	res := mustRun(t, goOpts(t, f, sha))
	if res.Status != StatusFail || !hasCode(res, CodeTestReportInvalid) {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
}
