package cli_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/store"
)

// A missing database reads as an empty store and exits 0, as
// docs/outcomes.md says; stderr names the absolute path that was looked at
// and how to point at the right one. Nothing is created.
func TestSummaryOfAMissingDatabaseSaysWhereItLooked(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	out := run(t, "summary")
	want := filepath.Join(dir, "sdf-telemetry.sqlite")
	if out.code != 0 || out.stdout != "No telemetry sessions in the store.\n" || !strings.Contains(out.stderr, "no database at "+want+" yet") || !strings.Contains(out.stderr, "--db PATH") || !strings.Contains(out.stderr, "db=") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("sdf summary created %s", want)
	}

	explicit := filepath.Join(dir, "other.sqlite")
	out = run(t, "summary", "--json", "--db", "other.sqlite")
	if out.code != 0 || strings.Contains(out.stdout, "no database") || !strings.Contains(out.stderr, "no database at "+explicit+" yet (from --db)") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	if _, err := os.Stat(explicit); !os.IsNotExist(err) {
		t.Fatalf("sdf summary created %s", explicit)
	}
}

// An existing database gets no hint.
func TestSummaryOfAnExistingDatabaseHasNoHint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "here.sqlite")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	out := run(t, "summary", "--db", path)
	if out.code != 0 || out.stderr != "" {
		t.Fatalf("%d %q", out.code, out.stderr)
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
