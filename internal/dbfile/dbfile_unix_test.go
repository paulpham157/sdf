//go:build unix

package dbfile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// noUmask clears the process umask for the test; it is process-wide, so
// these tests must not run in parallel.
func noUmask(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })
}

func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := Warnings
	Warnings = &buf
	t.Cleanup(func() { Warnings = old })
	return &buf
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestCreateNewFileAndDirectory(t *testing.T) {
	noUmask(t)
	warn := captureWarnings(t)
	dir := filepath.Join(t.TempDir(), "a", "b")
	path := filepath.Join(dir, "x.sqlite")
	if err := Create(path); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, path); m != 0o600 {
		t.Errorf("file mode %#o", m)
	}
	if m := mode(t, dir); m != 0o700 {
		t.Errorf("dir mode %#o", m)
	}
	if warn.Len() != 0 {
		t.Errorf("unexpected warning %q", warn)
	}
}

func TestCreateKeepsExistingContentAndTightens(t *testing.T) {
	noUmask(t)
	warn := captureWarnings(t)
	path := filepath.Join(t.TempDir(), "x.sqlite")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Create(path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "data" {
		t.Fatalf("content changed: %q", b)
	}
	if m := mode(t, path); m != 0o600 {
		t.Errorf("mode %#o", m)
	}
	if !strings.Contains(warn.String(), "was mode 0644; changed it to 0600") {
		t.Errorf("warning %q", warn)
	}
}

func TestTightenKeepsOwnerBitsAndSidecars(t *testing.T) {
	noUmask(t)
	captureWarnings(t)
	path := filepath.Join(t.TempDir(), "x.sqlite")
	for p, m := range map[string]os.FileMode{path: 0o444, path + "-wal": 0o666, path + "-shm": 0o640, path + "-journal": 0o604} {
		if err := os.WriteFile(p, nil, m); err != nil {
			t.Fatal(err)
		}
	}
	Tighten(path)
	for p, want := range map[string]os.FileMode{path: 0o400, path + "-wal": 0o600, path + "-shm": 0o600, path + "-journal": 0o600} {
		if m := mode(t, p); m != want {
			t.Errorf("%s: %#o, want %#o", filepath.Base(p), m, want)
		}
	}
}

// TestTightenFailureOnlyWarns: a file that cannot be changed (a read-only
// mount, another user's file) is left as it is, with a warning.
func TestTightenFailureOnlyWarns(t *testing.T) {
	noUmask(t)
	warn := captureWarnings(t)
	old := chmod
	chmod = func(string, os.FileMode) error { return syscall.EROFS }
	t.Cleanup(func() { chmod = old })
	path := filepath.Join(t.TempDir(), "x.sqlite")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Create(path); err != nil {
		t.Fatalf("Create on an unchangeable file: %v", err)
	}
	if m := mode(t, path); m != 0o644 {
		t.Errorf("mode %#o", m)
	}
	if !strings.Contains(warn.String(), "could not be changed to 0600") {
		t.Errorf("warning %q", warn)
	}
}

// TestCreateLeavesExistingDirectory: an existing directory is not changed;
// a group- or other-writable one is warned about unless it is sticky.
func TestCreateLeavesExistingDirectory(t *testing.T) {
	noUmask(t)
	for _, tc := range []struct {
		mode os.FileMode
		warn bool
	}{{0o755, false}, {0o775, true}, {0o777 | os.ModeSticky, false}} {
		warn := captureWarnings(t)
		dir := filepath.Join(t.TempDir(), "d")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, tc.mode); err != nil {
			t.Fatal(err)
		}
		if err := Create(filepath.Join(dir, "x.sqlite")); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode() & (os.ModePerm | os.ModeSticky); got != tc.mode {
			t.Errorf("dir %v changed to %v", tc.mode, got)
		}
		if got := strings.Contains(warn.String(), "group or others can write it"); got != tc.warn {
			t.Errorf("dir %v: warned %v, want %v (%q)", tc.mode, got, tc.warn, warn)
		}
	}
}
