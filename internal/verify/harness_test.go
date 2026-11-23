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

// B-1: code under test talks to the verifier's socket itself, or from a
// process it starts. Both are refused, and refusing is a FAIL.
func TestB1ForgedReportsOnTheHarnessSocket(t *testing.T) {
	cases := map[string]string{
		"same process": `c, err := net.Dial("unix", os.Getenv("SDF_VERIFY_HOOK"))
		if err == nil {
			c.Write([]byte("00 1 pass example.com/m/a TestAdd\n"))
			c.Close()
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
			f := newGoFixture(t)
			src := "package a\n\nimport (\n\t\"net\"\n\t\"os\"\n\t\"os/exec\"\n\t\"strings\"\n)\n\nvar _ = exec.Command\n\nfunc init() {\n\tif strings.HasSuffix(os.Args[0], \".test\") {\n\t\t" + body + "\n\t}\n}\n\n" + strings.TrimPrefix(goodAdd, "package a\n\n")
			sha := f.agentCommit(t, map[string]string{"a/a.go": src})
			res := mustRun(t, goOpts(t, f, sha))
			if res.Status != StatusFail || !hasCode(res, CodeTestReportInvalid) {
				t.Fatalf("%s %+v", res.Status, res.Reasons)
			}
		})
	}
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
