package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"

	"github.com/paulpham157/sdf/internal/task"
)

// Review's out-of-process forgeries against e3a2e25 (round 7). The old peer
// check accepted any process whose executable ended in ".test", whose parent
// was named go and whose grandparent was pid 1, and a report could name any
// test. Two ways to be such a process from code under test:
//
//   - atk_cloneparent: the code under test starts a helper named zz.test
//     with CLONE_PARENT, so the helper is a sibling of the test binary: a
//     child of the real go command.
//   - atk_orphan: the code under test starts a launcher that starts a fake
//     "go" and exits; the fake go is reparented to pid 1 and starts zz.test.
//
// In both the helper finds the harness socket, takes a key and sends an
// authenticated pass for TestAdd, while the real a.test exits 0 in init:
// a forged PASS with isolation pid-namespace. Each case here uses one door:
// the socket directly (as the old hook did; a forged PASS on e3a2e25), or
// the verifier's own launcher binary (the -exec program, with its config).
//
// zzHelper is the helper, built by the test (not committed as source, so
// the tripwire scan of the submitted tree does not see it; an attacker
// could ship it the same way, as data).
const zzHelper = `package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// forge sends an authenticated pass for TestAdd over c, if c gives a key.
func forge(c net.Conn) {
	key := make([]byte, 32)
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, key); err != nil {
		return
	}
	body := "1 pass example.com/m/a TestAdd"
	m := hmac.New(sha256.New, key)
	m.Write([]byte(body))
	io.WriteString(c, hex.EncodeToString(m.Sum(nil))+" "+body+"\n")
	time.Sleep(200 * time.Millisecond)
}

func asTestBinary() {
	// A channel on fd 3, if a launcher passed one.
	if c, err := net.FileConn(os.NewFile(3, "fd3")); err == nil {
		forge(c)
	}
	// The socket itself, as the old hook used it.
	socks, _ := filepath.Glob(SOCKGLOB)
	for _, p := range socks {
		if c, err := net.Dial("unix", p); err == nil {
			forge(c)
		}
	}
}

// start runs argv; with parent, as a sibling of this process (CLONE_PARENT).
func start(parent bool, argv ...string) *exec.Cmd {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if parent {
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_PARENT}
	}
	if err := cmd.Start(); err != nil {
		os.Stderr.WriteString("zz: start " + argv[0] + ": " + err.Error() + "\n")
		return nil
	}
	return cmd
}

// doors starts the helper as a test binary through one door, with or
// without CLONE_PARENT, and waits: "socket" runs it directly (it dials the
// harness socket, as the old hook did); "launcher" runs it through the
// verifier's own launcher binary and config, so a channel would come back
// on fd 3 if the launcher were accepted.
func doors(self string, parent bool) {
	var cmds []*exec.Cmd
	if DOOR == "socket" {
		cmds = append(cmds, start(parent, self, "test"))
	} else {
		socks, _ := filepath.Glob(SOCKGLOB)
		for _, s := range socks {
			cmds = append(cmds, start(parent, LAUNCHER, "__sdf-verify-launch", filepath.Join(filepath.Dir(s), "launch.json"), self, "test"))
		}
	}
	for _, c := range cmds {
		if c != nil {
			c.Wait()
		}
	}
}

func main() {
	self, _ := os.Executable()
	done := func() { os.WriteFile(os.Args[2], []byte("done"), 0o644) }
	switch os.Args[1] {
	case "test":
		asTestBinary()
	case "cloneparent":
		// Started by a.test with CLONE_PARENT: a child of go.
		if DOOR == "socket" {
			asTestBinary()
			done()
			return
		}
		go func() { time.Sleep(3 * time.Second); done() }()
		socks, _ := filepath.Glob(SOCKGLOB)
		for _, s := range socks {
			// Become the launcher, keeping go as the parent.
			syscall.Exec(LAUNCHER, []string{LAUNCHER, "__sdf-verify-launch", filepath.Join(filepath.Dir(s), "launch.json"), self, "test"}, os.Environ())
		}
		done()
	case "fakego":
		// Reparented to pid 1 by now (our parent exited).
		for i := 0; i < 50 && os.Getppid() != 1; i++ {
			time.Sleep(20 * time.Millisecond)
		}
		doors(filepath.Join(filepath.Dir(self), "zz.test"), false)
		done()
	case "orphan":
		// Start the fake go, named go, in its own session, and exit.
		dir := filepath.Dir(self)
		goPath := filepath.Join(dir, "go")
		b, _ := os.ReadFile(self)
		os.WriteFile(goPath, b, 0o755)
		cmd := exec.Command(goPath, "fakego", os.Args[2])
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		cmd.Start()
	}
}
`

// zzAttack builds the helper and returns the submitted a/a.go for mode:
// TestAdd's package, whose init (in a.test) runs the helper and exits 0 so
// no real test runs. The helper binary travels in the commit as data.
func zzAttack(t *testing.T, mode, door string) map[string]string {
	t.Helper()
	launcher, err := launcherPath()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	src := strings.NewReplacer("SOCKGLOB", harnessSocketGlob(t), "LAUNCHER", strconv.Quote(launcher), "DOOR", strconv.Quote(door)).Replace(zzHelper)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module zz\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "zz.test")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=", "GOCACHE="+goCache)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, b)
	}
	bin, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// a.test copies the helper to its TMPDIR as zz.test, runs it, waits for
	// the forgery to be sent, and exits 0 before any test runs.
	a := `package a

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func init() {
	if !strings.HasSuffix(os.Args[0], ".test") {
		return
	}
	b, err := os.ReadFile("../tools/zz.bin")
	if err != nil {
		panic(err)
	}
	zz := filepath.Join(os.TempDir(), "zz.test")
	if err := os.WriteFile(zz, b, 0o755); err != nil {
		panic(err)
	}
	done := filepath.Join(os.TempDir(), "zz.done")
	cmd := exec.Command(zz, "MODE", done)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if "MODE" == "cloneparent" {
		// The helper is a sibling of this process: a child of go.
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_PARENT}
	}
	if err := cmd.Start(); err != nil {
		os.Stderr.WriteString("a.test: " + err.Error() + "\n")
		os.Exit(0)
	}
	for i := 0; i < 250; i++ {
		if _, err := os.Stat(done); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	os.Exit(0)
}

func Add(x, y int) int { return 0 }
`
	return map[string]string{"a/a.go": strings.ReplaceAll(a, "MODE", mode), "tools/zz.bin": string(bin)}
}

// atk_cloneparent and atk_orphan, isolated and unisolated: never a PASS; a
// connection to the socket from anything but the accepted launcher is
// TEST_REPORT_INVALID, and the designated test is not passed.
func TestOutOfProcessForgeryIsRefused(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	for _, mode := range []string{"cloneparent", "orphan"} {
		for _, door := range []string{"socket", "launcher"} {
			for _, iso := range []string{"isolated", "unisolated"} {
				t.Run(mode+"/"+door+"/"+iso, func(t *testing.T) {
					if iso == "unisolated" {
						old := isolationAvailable
						isolationAvailable = func() (bool, string) { return false, "test" }
						defer func() { isolationAvailable = old }()
					} else {
						requireIsolation(t)
					}
					f := newGoFixture(t)
					sha := f.agentCommit(t, zzAttack(t, mode, door))
					o := goOpts(t, f, sha)
					o.AllowUnisolated = iso == "unisolated"
					// Test the harness, not the tripwire.
					o.AllowTamperCapable = true
					res := mustRun(t, o)
					if res.Status == StatusPass {
						t.Fatalf("forged PASS: %+v", res.Reasons)
					}
					if !hasCode(res, CodeDesignatedTestMissing) && !hasCode(res, CodeTestReportInvalid) {
						t.Fatalf("%s %+v", res.Status, res.Reasons)
					}
					t.Logf("%s %+v", res.Status, res.Reasons)
				})
			}
		}
	}
}

// A test binary's channel is bound to its package: a valid, authenticated
// report for another package's test is a violation (a package's binary,
// honest or not, cannot vouch for another package).
func TestReportForAnotherPackageIsRefused(t *testing.T) {
	h := &harness{checkout: "/c", results: map[string][]string{}, expected: []string{"example.com/m/a TestAdd"}}
	a, b := netPipe(t)
	go func() {
		key := make([]byte, 32)
		if _, err := ioReadFull(b, key); err != nil {
			return
		}
		writeReport(b, key, 1, "pass example.com/m/a TestAdd")
		b.Close()
	}()
	h.reports(a, "example.com/m/b")
	if len(h.violations) != 1 || !strings.Contains(h.violations[0], "reported a test of package example.com/m/a") || len(h.results) != 0 {
		t.Fatalf("violations %v results %v", h.violations, h.results)
	}
}

func netPipe(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

func ioReadFull(c net.Conn, b []byte) (int, error) { return io.ReadFull(c, b) }

// writeReport writes report n with body msg as the hook does.
func writeReport(c net.Conn, key []byte, n int, msg string) {
	body := strconv.Itoa(n) + " " + msg
	m := hmac.New(sha256.New, key)
	m.Write([]byte(body))
	io.WriteString(c, hex.EncodeToString(m.Sum(nil))+" "+body+"\n")
}

// Isolated, a test binary cannot write go's work directory (where the other
// packages' test binaries are linked) or the build cache, so it cannot swap
// another package's test binary before go runs it, or poison an object a
// later link uses; its TMPDIR and HOME are writable and fresh.
func TestTestBinaryCannotWriteGoWorkOrCache(t *testing.T) {
	requireIsolation(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	base := map[string]string{}
	for k, v := range goBase {
		base[k] = v
	}
	base["a/a_test.go"] = `package a

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("Add")
	}
	exe, _ := os.Executable()
	for _, dir := range []string{os.Getenv("GOTMPDIR"), os.Getenv("GOCACHE"), filepath.Dir(exe), os.Getenv("GOPATH")} {
		if dir == "" {
			t.Fatal("unset")
		}
		err := os.WriteFile(filepath.Join(dir, "zz-swap"), []byte("x"), 0o755)
		if !errors.Is(err, syscall.EROFS) {
			t.Fatalf("write in %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(exe, []byte("x"), 0o755); err == nil {
		t.Fatal("own binary writable")
	}
	for _, dir := range []string{os.TempDir(), os.Getenv("HOME")} {
		if err := os.WriteFile(filepath.Join(dir, "ok"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write in %s: %v", dir, err)
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
