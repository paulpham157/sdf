package verify

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/task"
)

// atkTW5 is Review's atk_tw5 (round 4): with os, reflect, runtime and
// strings only, it finds testing.(*common).Fail and FailNow with
// runtime.FuncForPC and writes a RET over them through /proc/self/mem, so
// no test can fail.
const atkTW5 = `package a

import (
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func init() {
	if !testing.Testing() {
		return
	}
	base := reflect.ValueOf((*testing.T).Run).Pointer()
	want := map[string]bool{"testing.(*common).Fail": true, "testing.(*common).FailNow": true}
	found := map[string]uintptr{}
	for pc := base - 4<<20; pc < base+4<<20 && len(found) < len(want); pc += 4 {
		f := runtime.FuncForPC(pc)
		if f == nil {
			continue
		}
		if n := f.Name(); want[n] {
			found[n] = f.Entry()
		}
	}
	p := strings.Join([]string{"", "pr" + "oc", "self", "mem"}, "/")
	m, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer m.Close()
	for _, e := range found {
		m.WriteAt([]byte{0xC3}, int64(e))
	}
}
`

// atk_tw5: the tripwire flags it, and with the tripwire overridden it still
// fails: the hook made the test process non-dumpable, so /proc/self/mem
// cannot be opened (at ef1a3af it passed).
func TestTW5ProcSelfMemPatchFails(t *testing.T) {
	f := newGoFixture(t)
	sha := f.agentCommit(t, map[string]string{"a/fix.go": atkTW5})
	res := mustRun(t, goOpts(t, f, sha))
	if res.Status == StatusPass || !hasCode(res, CodeTamperCapable) {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
	o := goOpts(t, f, sha)
	o.AllowTamperCapable = true
	res = mustRun(t, o)
	if res.Status != StatusFail || !(hasCode(res, CodeDesignatedTestMissing) || hasCode(res, CodeCheckFailed)) {
		t.Fatalf("with --allow-tamper-capable: %s %+v", res.Status, res.Reasons)
	}
}

// The hook hardens the test process before any code of the module runs:
// /proc/self/mem cannot be opened, and (amd64, arm64) PR_SET_DUMPABLE is
// refused, so it cannot be undone.
func TestHookHardensTheTestProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	base := map[string]string{}
	for k, v := range goBase {
		base[k] = v
	}
	seccomp := runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"
	base["a/a_test.go"] = `package a

import (
	"os"
	"syscall"
	"testing"
)

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("Add")
	}
	if f, err := os.OpenFile("/proc/self/mem", os.O_RDWR, 0); err == nil {
		f.Close()
		t.Fatal("/proc/self/mem opened for writing")
	}
	if r, _, _ := syscall.RawSyscall(syscall.SYS_PRCTL, 3, 0, 0); r != 0 {
		t.Fatal("dumpable")
	}
	if ` + map[bool]string{true: "true", false: "false"}[seccomp] + ` {
		if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, 4, 1, 0); e != syscall.EPERM {
			t.Fatalf("PR_SET_DUMPABLE 1: %v", e)
		}
	}
}
`
	f := newFixtureFiles(t, base, task.CriterionInput{Name: "unit", Argv: []string{"go", "test", "./a/"}, TimeoutS: 300})
	sha := f.agentCommit(t, map[string]string{"a/a.go": goodAdd})
	if res := mustRun(t, goOpts(t, f, sha)); res.Status != StatusPass {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
}

// The hook compiles on platforms other than this one.
func TestHookSourceBuildsOnOtherPlatforms(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	testingDir := filepath.Join(strings.TrimSpace(string(out)), "src", "testing")
	for _, p := range [][2]string{{"linux", "arm64"}, {"linux", "386"}, {"darwin", "arm64"}} {
		dir := t.TempDir()
		src := filepath.Join(dir, "hook.go")
		if err := os.WriteFile(src, []byte(hookSourceFor("0123456789abcdef", "/x/s", p[0], p[1])), 0o644); err != nil {
			t.Fatal(err)
		}
		ov, _ := json.Marshal(map[string]any{"Replace": map[string]string{filepath.Join(testingDir, "zz_sdfverify_0123456789abcdef.go"): src}})
		ovFile := filepath.Join(dir, "overlay.json")
		if err := os.WriteFile(ovFile, ov, 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("go", "build", "-overlay="+ovFile, "testing")
		cmd.Env = append(os.Environ(), "GOOS="+p[0], "GOARCH="+p[1], "CGO_ENABLED=0", "GOFLAGS=", "GOCACHE="+goCache)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s/%s: %v\n%s", p[0], p[1], err, b)
		}
	}
}

// atk_goroot, atk_pathjack, atk_modcache: an isolated check cannot write the
// toolchain, a PATH directory, a trusted cache or any other host directory;
// writes to a trusted cache land in a per-criterion layer; the hidden .git
// is not visible; IPC and UTS namespaces are the check's own.
func TestIsolatedCheckCannotWriteHostDirectories(t *testing.T) {
	requireIsolation(t)
	wd, _ := os.Getwd()
	pathDir, err := os.MkdirTemp(wd, "pathdir-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(pathDir)
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Skip("no go")
	}
	goroot := strings.TrimSpace(string(out))
	cache := t.TempDir()
	if err := os.WriteFile(filepath.Join(cache, "f"), []byte("orig\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ipc, _ := os.Readlink("/proc/self/ns/ipc")
	uts, _ := os.Readlink("/proc/self/ns/uts")
	probe := "sdf-ro-probe"
	defer func() {
		for _, d := range []string{pathDir, goroot, wd} {
			os.Remove(filepath.Join(d, probe))
		}
	}()
	a := `for d in "` + pathDir + `" "` + goroot + `" "` + wd + `"; do if ( : > "$d/` + probe + `" ) 2>/dev/null; then echo "wrote $d" >&2; exit 1; fi; done
echo changed > "$GOMODCACHE/f" || exit 2
test "$(cat "$GOMODCACHE/f")" = changed || exit 3
test ! -e ../git || { echo "../git is visible" >&2; exit 4; }
test "$(readlink /proc/self/ns/ipc)" != "` + ipc + `" || exit 5
test "$(readlink /proc/self/ns/uts)" != "` + uts + `" || exit 6
: > src/written-by-check || exit 7`
	b := `test "$(cat "$GOMODCACHE/f")" = orig`
	f := newFixture(t,
		task.CriterionInput{Name: "a", Argv: []string{"sh", "-c", a}, Designated: []string{"tests/*.sh"}},
		task.CriterionInput{Name: "b", Argv: []string{"sh", "-c", b}, Designated: []string{"tests/*.sh"}},
	)
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"})
	o := f.opts(sha)
	o.AllowUnisolated = false
	o.Path = pathDir + string(os.PathListSeparator) + os.Getenv("PATH")
	o.GoModCache = cache
	res := mustRun(t, o)
	if res.Status != StatusPass {
		for _, e := range res.Evidence {
			if e.Value["kind"] == "criterion" {
				t.Logf("%s: %v", e.Value["name"], e.Value["error"])
			}
		}
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
	if b, _ := os.ReadFile(filepath.Join(cache, "f")); string(b) != "orig\n" {
		t.Fatalf("trusted cache changed: %q", b)
	}
}

// atk_pathjack: a check drops a fake sh into a PATH directory so later
// script checks pass. Isolated, it cannot write there. Unisolated (or from
// outside), the command pins catch it: COMMAND_CHANGED, and the later
// criterion does not run.
func TestCommandPinsCatchAPathJack(t *testing.T) {
	jack := func(t *testing.T) (*fixture, Options, string) {
		pathDir := t.TempDir()
		f := newFixture(t,
			task.CriterionInput{Name: "a-prep", Argv: []string{"sh", "-c", `printf '#!/bin/true\n' > "$1/sh" && chmod +x "$1/sh"; exit 0`, "x", pathDir}, Designated: []string{"tests/*.sh"}},
			task.CriterionInput{Name: "b-accept", Argv: []string{"sh", "tests/run.sh"}, Designated: []string{"tests/*.sh"}},
		)
		sha := f.agentCommit(t, map[string]string{"README": "lazy\n"})
		o := f.opts(sha)
		o.Path = pathDir + string(os.PathListSeparator) + os.Getenv("PATH")
		return f, o, pathDir
	}
	t.Run("unisolated", func(t *testing.T) {
		old := isolationAvailable
		isolationAvailable = func() (bool, string) { return false, "test" }
		defer func() { isolationAvailable = old }()
		_, o, _ := jack(t)
		o.AllowUnisolated = true
		res := mustRun(t, o)
		if res.Status != StatusFail || !hasCode(res, CodeCommandChanged) {
			t.Fatalf("%s %+v", res.Status, res.Reasons)
		}
		pins := ""
		for _, e := range res.Evidence {
			if e.Value["kind"] == "command_pins" {
				b, _ := json.Marshal(e.Value["commands"])
				pins = string(b)
			}
		}
		if !strings.Contains(pins, `"sha256":"sha256:`) || !strings.Contains(pins, `"criterion":"b-accept"`) {
			t.Fatalf("pins %s", pins)
		}
	})
	t.Run("isolated", func(t *testing.T) {
		requireIsolation(t)
		_, o, pathDir := jack(t)
		o.AllowUnisolated = false
		res := mustRun(t, o)
		if res.Status != StatusFail || hasCode(res, CodeCommandChanged) || !hasCode(res, CodeCheckFailed) {
			t.Fatalf("%s %+v", res.Status, res.Reasons)
		}
		if _, err := os.Stat(filepath.Join(pathDir, "sh")); err == nil {
			t.Fatal("an isolated check wrote a PATH directory")
		}
	})
}

// atk_modcache (per-run cache): nothing a check writes to its HOME, TMPDIR,
// GOPATH, GOCACHE or GOMODCACHE reaches the next criterion.
func TestEachCriterionGetsNewDirectories(t *testing.T) {
	f := newFixture(t,
		task.CriterionInput{Name: "a", Argv: []string{"sh", "-c", `for d in "$HOME" "$TMPDIR" "$GOPATH" "$GOCACHE" "$GOMODCACHE" "$GOTMPDIR"; do [ -z "$d" ] || touch "$d/m" || exit 1; done`}, Designated: []string{"tests/*.sh"}},
		task.CriterionInput{Name: "b", Argv: []string{"sh", "-c", `for d in "$HOME" "$TMPDIR" "$GOPATH" "$GOCACHE" "$GOMODCACHE"; do test ! -e "$d/m" || { echo "$d/m" >&2; exit 1; }; done`}, Designated: []string{"tests/*.sh"}},
	)
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"})
	if res := mustRun(t, f.opts(sha)); res.Status != StatusPass {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
}

// A .git a check creates in the checkout would be read by later git
// commands there (core.fsmonitor, hooks): it is a change.
func TestNewGitDirInTheCheckoutIsAChange(t *testing.T) {
	f := newFixture(t,
		task.CriterionInput{Name: "a", Argv: []string{"sh", "-c", "mkdir .git && echo '[core]' > .git/config"}, Designated: []string{"tests/*.sh"}},
		task.CriterionInput{Name: "b", Argv: []string{"sh", "tests/run.sh"}, Designated: []string{"tests/*.sh"}},
	)
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"})
	res := mustRun(t, f.opts(sha))
	if res.Status != StatusFail || !hasCode(res, CodeCheckoutModified) || !strings.Contains(strings.Join(reasonDetails(res), " "), ".git: added") {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
}

func reasonDetails(res Result) []string {
	var out []string
	for _, r := range res.Reasons {
		out = append(out, r.Detail)
	}
	return out
}
