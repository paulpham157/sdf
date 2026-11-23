package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCheckWithoutCasesRunsTheEmbeddedSet(t *testing.T) {
	// No source tree in the working directory: the default case set must
	// come from the binary, not from a path next to the source.
	t.Chdir(t.TempDir())
	out := run(t, "check")
	if out.code != 0 {
		t.Fatalf("code %d\n%s%s", out.code, out.stdout, out.stderr)
	}
	for _, want := range []string{"priced-session pass\n", "log-count pass\n", "unknown-price not_checked", "0 failed"} {
		if !strings.Contains(out.stdout, want) {
			t.Fatalf("missing %q\n%s", want, out.stdout)
		}
	}
}

func TestVersionCommand(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-v"} {
		out := run(t, arg)
		if out.code != 0 {
			t.Fatalf("%s: code %d\n%s", arg, out.code, out.stderr)
		}
		for _, want := range []string{"sdf ", "commit: ", "built: ", "go: go1.", "platform: "} {
			if !strings.Contains(out.stdout, want) {
				t.Fatalf("%s: missing %q\n%s", arg, want, out.stdout)
			}
		}
	}
	if out := run(t, "version", "--bogus"); out.code != 2 {
		t.Fatalf("code %d", out.code)
	}
}

// sdf demo was removed (#228): it is an unknown command, and the usage line
// no longer lists it.
func TestDemoCommandIsGone(t *testing.T) {
	out := run(t, "demo")
	if out.code != 2 || !strings.Contains(out.stderr, `unrecognized command "demo"`) {
		t.Fatalf("code %d\n%s%s", out.code, out.stdout, out.stderr)
	}
	help := run(t, "--help")
	if help.code != 0 || strings.Contains(help.stdout, "demo") || !strings.Contains(help.stdout, "usage: sdf <serve|summary|check|") {
		t.Fatalf("code %d\n%s", help.code, help.stdout)
	}
}

// TestTrimpathReleaseBinary builds sdf the way `make build` and GoReleaser
// do (-trimpath, stripped, version ldflags) and runs sdf version and sdf
// check from a directory that has no source tree.
func TestTrimpathReleaseBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	root := moduleRoot(t)
	bin := filepath.Join(t.TempDir(), "sdf")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command(goBin, "build", "-trimpath",
		"-ldflags", "-s -w -X github.com/paulpham157/sdf/internal/version.Version=v0.1.0-test -X github.com/paulpham157/sdf/internal/version.Commit=abc1234 -X github.com/paulpham157/sdf/internal/version.Date=2026-11-23T00:00:00Z",
		"-o", bin, "./cmd/sdf")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	work := t.TempDir()
	exe := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = work
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sdf %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	if out := exe("version"); !strings.Contains(out, "sdf v0.1.0-test\n") || !strings.Contains(out, "commit: abc1234\n") || !strings.Contains(out, "built: 2026-11-23T00:00:00Z\n") {
		t.Fatal(out)
	}
	if out := exe("check"); !strings.Contains(out, "priced-session pass\n") || !strings.Contains(out, " 0 failed") {
		t.Fatal(out)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
