package toolproxy

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestConcurrentMultiProcessWritersUnsupported records the operator rule in
// docs/sdf-orch.md: one process per fence file. OpenSQLiteStore does not
// flock the file and does not set locking_mode=EXCLUSIVE, so a second open
// of a live file succeeds. That success is not support for two writers.
// This test must not be changed into an exactly-once assertion across two
// stores; that would claim a multi-writer mode this package does not offer.
// Reopen after Close remains the supported restart path.
func TestConcurrentMultiProcessWritersUnsupported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fence.db")
	first, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })

	var mode string
	if err := first.db.QueryRow(`PRAGMA locking_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "normal") {
		t.Fatalf("locking_mode=%q; single-process-per-file is the operator rule, not an exclusive SQLite lock", mode)
	}
	if got := first.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections=%d; one connection serialises claims inside one process and is not a cross-process lock", got)
	}

	opened := make(chan error, 1)
	go func() {
		second, err := OpenSQLiteStore(path)
		if err != nil {
			opened <- err
			return
		}
		opened <- second.Close()
	}()
	select {
	case err := <-opened:
		if err != nil {
			t.Fatalf("second open: %v; rejecting a second open would be a lock this package does not take", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second open blocked; the fence must not take an exclusive cross-process lock")
	}

	matches, err := filepath.Glob(path + "*.lock")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("lock files %v; do not flock the fence file", matches)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen after close (supported restart): %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
