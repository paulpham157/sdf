package cli_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSummaryDoesNotCreateAMissingDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	out := run(t, "summary")
	want := filepath.Join(dir, "sdf-telemetry.sqlite")
	if out.code != 1 || !strings.Contains(out.stderr, "no database at "+want) || !strings.Contains(out.stderr, "--db PATH") || !strings.Contains(out.stderr, "sdf serve") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("sdf summary created %s", want)
	}

	explicit := filepath.Join(dir, "elsewhere", "x.sqlite")
	out = run(t, "summary", "--json", "--db", explicit)
	if out.code != 1 || out.stdout != "" || !strings.Contains(out.stderr, "no database at "+explicit+" (from --db)") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	if _, err := os.Stat(filepath.Dir(explicit)); !os.IsNotExist(err) {
		t.Fatal("sdf summary created a directory")
	}
}

func TestStoreErrorsNameTheAbsolutePath(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the test user cannot write")
	}
	dir := t.TempDir()
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	t.Chdir(ro)
	out := run(t, "serve", "--port", "0", "--grpc-port", "0", "--no-ui")
	want := filepath.Join(ro, "sdf-telemetry.sqlite")
	if out.code != 1 || !strings.Contains(out.stderr, "database "+want) || !strings.Contains(out.stderr, "writable") {
		t.Fatalf("%d %q", out.code, out.stderr)
	}
	out = run(t, "decision", "add", "--id", "d1", "--statement", "s", "--status", "hypothesis")
	if out.code != 1 || !strings.Contains(out.stderr, "database "+want) {
		t.Fatalf("%d %q", out.code, out.stderr)
	}
}
